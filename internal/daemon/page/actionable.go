package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// checks 是动作执行前要等元素满足的条件(spec §动作的自动等待表)。已挂载总是检查。
type checks struct {
	visible  bool
	stable   bool
	enabled  bool
	editable bool
	// hitTarget 要求点击点落在元素自身或其后代上。
	hitTarget bool
}

var (
	clickChecks = checks{visible: true, stable: true, enabled: true, hitTarget: true}
	hoverChecks = checks{visible: true, stable: true, hitTarget: true}
)

// needsPoint 表示条件里含有只能在元素的实际位置上判断的项,等待结束时得到可以发出鼠标事件的点。
func (c checks) needsPoint() bool { return c.stable || c.hitTarget }

// actionPoint 是鼠标事件的目标点,坐标是 sessionID 会话的视口坐标:顶层会话为主 frame 视口,跨进程
// iframe 的子会话为该 iframe 自己的视口(真机探针:子会话上的 Input 命令使用 frame 内坐标)。
type actionPoint struct {
	sessionID string
	x, y      float64
}

// actionObjectGroup 收拢自动等待期间解析出的元素对象,等待结束时整组释放。
const actionObjectGroup = "sctl-action"

// frameWait 是等一个动画帧的上限。开启焦点模拟的后台标签页照常产生动画帧(真机探针);连续
// hiddenAfterFrames 次等不到,说明焦点模拟也无效(最小化的窗口、被冻结的标签页),位置稳定无法确认。
const (
	frameWait         = time.Second
	hiddenAfterFrames = 3
)

// stateFunction 检查不需要布局稳定的条件,返回第一个不满足的:detached、hidden、disabled、readonly,
// 全部满足时为 ok。目标是文本节点时按它的父元素判断。
const stateFunction = `function (c) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (!this.isConnected || !el) return { state: "detached" };
  if (c.visible) {
    const r = el.getBoundingClientRect();
    if (!(r.width > 0 && r.height > 0) || !el.checkVisibility({ visibilityProperty: true })) return { state: "hidden" };
  }
  if (c.enabled && (el.matches(":disabled") || el.closest('[aria-disabled="true"]'))) return { state: "disabled" };
  if (c.editable) {
    const control = el.matches("input, textarea, select");
    if (!control && !el.isContentEditable) return { state: "noneditable" };
    if ((control && el.readOnly) || el.closest('[aria-readonly="true"]')) return { state: "readonly" };
  }
  return { state: "ok" };
}`

// positionFunction 在元素滚入视口后,隔一个动画帧测两次边界框判断位置稳定,再在元素可见部分的中心
// 做命中测试(穿过 shadow root)。点以相对边界框的比例 fx/fy 返回:元素所在 frame 的坐标与会话视口
// 坐标之间的换算由 DOM.getContentQuads 完成。
const positionFunction = `async function (frameWait, hitTarget) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (!this.isConnected || !el) return { state: "detached" };
  const frame = () => new Promise((resolve) => {
    let done = false;
    requestAnimationFrame(() => { if (!done) { done = true; resolve(true); } });
    setTimeout(() => { if (!done) { done = true; resolve(false); } }, frameWait);
  });
  const box = () => { const r = el.getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; };
  if (!(await frame())) return { state: "noFrame" };
  const first = box();
  if (!(await frame())) return { state: "noFrame" };
  const [left, top, width, height] = box();
  if (first[0] !== left || first[1] !== top || first[2] !== width || first[3] !== height) return { state: "unstable" };
  const x0 = Math.max(left, 0), y0 = Math.max(top, 0);
  const x1 = Math.min(left + width, innerWidth), y1 = Math.min(top + height, innerHeight);
  if (x1 <= x0 || y1 <= y0) return { state: "outside" };
  const x = (x0 + x1) / 2, y = (y0 + y1) / 2;
  const at = { fx: (x - left) / width, fy: (y - top) / height };
  if (!hitTarget) return { state: "ok", ...at };
  let hit = document.elementFromPoint(x, y);
  while (hit && hit.shadowRoot) {
    const inner = hit.shadowRoot.elementFromPoint(x, y);
    if (!inner || inner === hit) break;
    hit = inner;
  }
  for (let n = hit; n; n = n.parentNode || n.host) {
    if (n === el) return { state: "ok", ...at };
  }
  let by = "nothing";
  if (hit) {
    by = hit.localName + (hit.id ? "#" + hit.id : "");
    for (const c of hit.classList) by += "." + c;
  }
  return { state: "obscured", by };
}`

// maxObscurerLength 限制遮挡元素描述的长度:它来自页面的 id 与 class,可以任意长。
const maxObscurerLength = 120

type probeResult struct {
	State string  `json:"state"`
	By    string  `json:"by"`
	FX    float64 `json:"fx"`
	FY    float64 `json:"fy"`
}

// errDetached 表示元素在等待期间离开了文档:引用就此失效,选择器可以重新查询。
var errDetached = errors.New("the element left the document")

// waitActionable 等元素满足 c 中的条件,先把它滚动到可视区域内。超时返回 TIMEOUT,消息写明最后一个
// 未满足的条件;元素离开文档返回 errDetached;页面不产生动画帧时返回 PAGE_HIDDEN。
// c.needsPoint() 时返回鼠标事件的目标点。
func waitActionable(ctx context.Context, t *Tab, el element, c checks) (actionPoint, error) {
	defer func() {
		if err := t.sendTo(ctx, el.sessionID, "Runtime.releaseObjectGroup", map[string]string{"objectGroup": actionObjectGroup}, nil); err != nil {
			t.m.log.Debug("failed to release action objects", zap.Int("tabId", t.id), zap.Error(err))
		}
	}()
	var (
		wait     backoff
		reason   = "the element is not ready"
		noFrames int
	)
	for {
		point, why, err := probeOnce(ctx, t, el, c)
		switch {
		case err != nil:
			return actionPoint{}, waitFailed(ctx, err, reason)
		case why == "":
			return point, nil
		case why == whyNoFrame:
			noFrames++
			if noFrames >= hiddenAfterFrames {
				return actionPoint{}, &Error{
					Code: generated.ErrorCodePageHidden,
					Message: fmt.Sprintf("tab %d is not rendering (no animation frame within %s), so the element's position cannot be confirmed stable; retry with --activate",
						t.id, frameWait*hiddenAfterFrames),
				}
			}
		default:
			noFrames = 0
			reason = why
		}
		if err := wait.sleep(ctx); err != nil {
			return actionPoint{}, waitFailed(ctx, err, reason)
		}
	}
}

// whyNoFrame 标记页面这一轮没有产生动画帧;它不作为超时原因写给调用方。
const whyNoFrame = "no animation frame"

// probeOnce 检查一轮条件。why 为空表示全部满足;否则是不满足的条件,写进超时消息。
func probeOnce(ctx context.Context, t *Tab, el element, c checks) (actionPoint, string, error) {
	var node struct {
		Object remoteObject `json:"object"`
	}
	err := t.sendTo(ctx, el.sessionID, "DOM.resolveNode", map[string]any{"backendNodeId": el.backendNodeID, "objectGroup": actionObjectGroup}, &node)
	if err != nil {
		if isCDPError(err) {
			return actionPoint{}, "", errDetached
		}
		return actionPoint{}, "", err
	}
	var state probeResult
	if err := callOn(ctx, t, el.sessionID, node.Object.ObjectID, stateFunction, &state, map[string]bool{
		"visible": c.visible, "enabled": c.enabled, "editable": c.editable,
	}); err != nil {
		return actionPoint{}, "", err
	}
	if why, err := stateReason(state); why != "" || err != nil {
		return actionPoint{}, why, err
	}
	err = t.sendTo(ctx, el.sessionID, "DOM.scrollIntoViewIfNeeded", map[string]int{"backendNodeId": el.backendNodeID}, nil)
	if err != nil {
		if !isCDPError(err) {
			return actionPoint{}, "", err
		}
		// 没有布局框的元素滚动不了;需要可见的动作下一轮会报告它不可见,其余动作(如隐藏的文件输入框)照常执行。
		if c.visible {
			return actionPoint{}, "the element is not visible", nil
		}
	}
	if !c.needsPoint() {
		return actionPoint{}, "", nil
	}
	var pos probeResult
	if err := callOn(ctx, t, el.sessionID, node.Object.ObjectID, positionFunction, &pos, frameWait.Milliseconds(), c.hitTarget); err != nil {
		return actionPoint{}, "", err
	}
	switch pos.State {
	case "ok":
	case "noFrame":
		return actionPoint{}, whyNoFrame, nil
	case "unstable":
		return actionPoint{}, "the element is not stable (it is still moving)", nil
	case "outside":
		return actionPoint{}, "the element is outside the viewport", nil
	case "obscured":
		by := pos.By
		if len(by) > maxObscurerLength {
			by = by[:maxObscurerLength] + "…"
		}
		return actionPoint{}, "the element does not receive pointer events at its click point: obscured by " + by, nil
	case "detached":
		return actionPoint{}, "", errDetached
	default:
		return actionPoint{}, "", fmt.Errorf("unexpected actionability state %q", pos.State)
	}
	return pointFromQuads(ctx, t, el, pos)
}

func stateReason(state probeResult) (string, error) {
	switch state.State {
	case "ok":
		return "", nil
	case "detached":
		return "", errDetached
	case "hidden":
		return "the element is not visible", nil
	case "disabled":
		return "the element is disabled", nil
	case "noneditable":
		return "the element is not editable (not an input, textarea, select or contenteditable element)", nil
	case "readonly":
		return "the element is read-only", nil
	}
	return "", fmt.Errorf("unexpected actionability state %q", state.State)
}

// pointFromQuads 把元素边界框内的比例位置换算为会话视口坐标。元素在同进程 iframe 里时,它的
// getBoundingClientRect 是 iframe 内的坐标,而 DOM.getContentQuads 给出会话视口里的坐标。
func pointFromQuads(ctx context.Context, t *Tab, el element, pos probeResult) (actionPoint, string, error) {
	var res struct {
		Quads [][]float64 `json:"quads"`
	}
	err := t.sendTo(ctx, el.sessionID, "DOM.getContentQuads", map[string]int{"backendNodeId": el.backendNodeID}, &res)
	if err != nil {
		if isCDPError(err) {
			return actionPoint{}, "the element is not visible", nil
		}
		return actionPoint{}, "", err
	}
	minX, minY, maxX, maxY, ok := quadBounds(res.Quads)
	if !ok {
		return actionPoint{}, "the element is not visible", nil
	}
	return actionPoint{
		sessionID: el.sessionID,
		x:         minX + pos.FX*(maxX-minX),
		y:         minY + pos.FY*(maxY-minY),
	}, "", nil
}

// quadBounds 返回全部四边形的外接矩形;一个元素折行时有多个四边形,外接矩形与 getBoundingClientRect 对应。
func quadBounds(quads [][]float64) (minX, minY, maxX, maxY float64, ok bool) {
	for _, q := range quads {
		if len(q) != 8 {
			continue
		}
		for i := 0; i < 8; i += 2 {
			x, y := q[i], q[i+1]
			if !ok {
				minX, maxX, minY, maxY, ok = x, x, y, y, true
				continue
			}
			minX, maxX = min(minX, x), max(maxX, x)
			minY, maxY = min(minY, y), max(maxY, y)
		}
	}
	return minX, minY, maxX, maxY, ok
}

// callOn 以 args 为参数在对象上调用函数,把按值返回的结果解到 result。页面在函数里抛出的异常是内部错误:
// 这些函数只读 DOM,抛异常说明页面环境被改坏了。
func callOn(ctx context.Context, t *Tab, sessionID, objectID, function string, result any, args ...any) error {
	arguments := make([]map[string]any, len(args))
	for i, a := range args {
		arguments[i] = map[string]any{"value": a}
	}
	var res struct {
		Result           remoteObject      `json:"result"`
		ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
	}
	err := t.sendTo(ctx, sessionID, "Runtime.callFunctionOn", map[string]any{
		"objectId":            objectID,
		"functionDeclaration": function,
		"arguments":           arguments,
		"returnByValue":       true,
		"awaitPromise":        true,
	}, &res)
	if err != nil {
		if isCDPError(err) {
			// 对象所在的文档已被替换(执行上下文销毁):元素不在了。
			return errDetached
		}
		return err
	}
	if res.ExceptionDetails != nil {
		return &Error{Code: generated.ErrorCodeInternalError, Message: "checking the element failed in the page: " + exceptionMessage(res.ExceptionDetails)}
	}
	if err := json.Unmarshal(res.Result.Value, result); err != nil {
		return fmt.Errorf("decode the actionability result: %w", err)
	}
	return nil
}

// backoff 是自动等待的重试间隔,先密后疏,与 Playwright 的节奏相近。
type backoff struct{ n int }

var backoffSteps = []time.Duration{20 * time.Millisecond, 50 * time.Millisecond, 100 * time.Millisecond, 100 * time.Millisecond, 250 * time.Millisecond}

// sleep 等下一次重试;ctx 结束时返回 ctx 的错误。
func (b *backoff) sleep(ctx context.Context) error {
	d := backoffSteps[min(b.n, len(backoffSteps)-1)]
	b.n++
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// waitFailed 把自动等待期间的失败转换为结果:动作超时时返回写明 reason(最后一个未满足的条件)的
// TIMEOUT,其余失败原样返回。进行中的 CDP 命令也会因超时返回 ctx 的错误,同样按超时处理。
func waitFailed(ctx context.Context, err error, reason string) error {
	var timeout *Error
	if ctx.Err() != nil && errors.As(context.Cause(ctx), &timeout) && timeout.Code == generated.ErrorCodeTimeout {
		return &Error{Code: generated.ErrorCodeTimeout, Message: timeout.Message + ": " + reason}
	}
	return err
}
