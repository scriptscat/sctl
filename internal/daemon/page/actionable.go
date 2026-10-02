package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	// rendered 要求跨进程 iframe 里的元素所在 frame 已在渲染(产生动画帧)。iframe 被滚入视口时,父页面的
	// 滚动经浏览器进程异步完成,视口外的跨域 iframe 不渲染;此刻截图,它在图里是空白(真机复现)。
	// 同进程的元素由截图时强制绘制的那个渲染进程画出,不需要等。
	rendered bool
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

// stableFunction 在元素滚入视口后隔一个动画帧测两次边界框,判断位置稳定,返回稳定的边界框
// (元素所在 frame 的视口坐标)。目标是文本节点时测量文本自身。
const stableFunction = `async function (frameWait) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (!this.isConnected || !el) return { state: "detached" };
  const frame = () => new Promise((resolve) => {
    let done = false;
    requestAnimationFrame(() => { if (!done) { done = true; resolve(true); } });
    setTimeout(() => { if (!done) { done = true; resolve(false); } }, frameWait);
  });
  const range = this.nodeType === 1 ? null : document.createRange();
  if (range) range.selectNodeContents(this);
  const box = () => { const r = (range || el).getBoundingClientRect(); return [r.left, r.top, r.width, r.height]; };
  if (!(await frame())) return { state: "noFrame" };
  const first = box();
  if (!(await frame())) return { state: "noFrame" };
  const second = box();
  if (first.some((v, i) => v !== second[i])) return { state: "unstable" };
  return { state: "ok", box: second };
}`

// hitPointFunction 从 DOM.getContentQuads 给出的四边形(会话视口坐标;折行的行内元素每行一个)里
// 依次取在视口内的那部分的中心,在元素所在 frame 里做命中测试(穿过 shadow root),返回第一个命中
// 元素自身或其后代的点,坐标仍是会话视口坐标。四边形与 frame 坐标之间按四边形的外接矩形与
// stableFunction 测得的边界框 box 对应换算:元素在同进程 iframe 里时两者相差 iframe 的位置。外接矩形
// 与 getBoundingClientRect 一样只算宽高都不为零的四边形:以换行开头的行内元素在上一行留下宽为零的
// 片段,算进去会让换算错位,命中测试的点与点击的点不再是同一个点(真机复现:点到了遮挡元素上)。
// 边界框与 box 不同说明元素又动了。全部被挡住时报告第一个可见区域上的遮挡元素。
const hitPointFunction = `function (quads, box, hitTarget) {
  const el = this.nodeType === 1 ? this : this.parentElement;
  if (!this.isConnected || !el) return { state: "detached" };
  const range = this.nodeType === 1 ? null : document.createRange();
  if (range) range.selectNodeContents(this);
  const r = (range || el).getBoundingClientRect();
  if (r.left !== box[0] || r.top !== box[1] || r.width !== box[2] || r.height !== box[3]) return { state: "unstable" };
  const xsOf = (q) => [q[0], q[2], q[4], q[6]], ysOf = (q) => [q[1], q[3], q[5], q[7]];
  const sized = quads.filter((q) => Math.max(...xsOf(q)) > Math.min(...xsOf(q)) && Math.max(...ysOf(q)) > Math.min(...ysOf(q)));
  const bounding = sized.length > 0 ? sized : quads;
  const xs = bounding.flatMap(xsOf), ys = bounding.flatMap(ysOf);
  const qx = Math.min(...xs), qy = Math.min(...ys), qw = Math.max(...xs) - qx, qh = Math.max(...ys) - qy;
  const sx = qw > 0 ? box[2] / qw : 1, sy = qh > 0 ? box[3] / qh : 1;
  const toFrame = (x, y) => [box[0] + (x - qx) * sx, box[1] + (y - qy) * sy];
  const toSession = (x, y) => ({ x: qx + (x - box[0]) / sx, y: qy + (y - box[1]) / sy });
  let by;
  for (const q of quads) {
    const [x0, y0] = toFrame(Math.min(q[0], q[2], q[4], q[6]), Math.min(q[1], q[3], q[5], q[7]));
    const [x1, y1] = toFrame(Math.max(q[0], q[2], q[4], q[6]), Math.max(q[1], q[3], q[5], q[7]));
    const left = Math.max(x0, 0), top = Math.max(y0, 0), right = Math.min(x1, innerWidth), bottom = Math.min(y1, innerHeight);
    if (right - left < 1 || bottom - top < 1) continue;
    const x = (left + right) / 2, y = (top + bottom) / 2;
    if (!hitTarget) return { state: "ok", ...toSession(x, y) };
    let hit = document.elementFromPoint(x, y);
    while (hit && hit.shadowRoot) {
      const inner = hit.shadowRoot.elementFromPoint(x, y);
      if (!inner || inner === hit) break;
      hit = inner;
    }
    for (let n = hit; n; n = n.parentNode || n.host) {
      if (n === el) return { state: "ok", ...toSession(x, y) };
    }
    if (by === undefined) {
      by = "nothing";
      if (hit) {
        by = hit.localName + (hit.id ? "#" + hit.id : "");
        for (const c of hit.classList) by += "." + c;
      }
    }
  }
  return by === undefined ? { state: "outside" } : { state: "obscured", by };
}`

// maxObscurerLength 限制遮挡元素描述的长度:它来自页面的 id 与 class,可以任意长。
const maxObscurerLength = 120

type probeResult struct {
	State string `json:"state"`
	By    string `json:"by"`
	// Box 是 stableFunction 测得的边界框 [left, top, width, height]。
	Box []float64 `json:"box"`
	// X、Y 是 hitPointFunction 选出的点。
	X float64 `json:"x"`
	Y float64 `json:"y"`
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
				consequence := "the element's position cannot be confirmed stable"
				if !c.needsPoint() {
					consequence = "the iframe containing the element would be captured blank"
				}
				return actionPoint{}, &Error{
					Code: generated.ErrorCodePageHidden,
					Message: fmt.Sprintf("tab %d is not rendering (no animation frame within %s), so %s; retry with --activate",
						t.id, frameWait*hiddenAfterFrames, consequence),
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
	waitFrames := c.rendered && el.sessionID != ""
	if !c.needsPoint() && !waitFrames {
		return actionPoint{}, "", nil
	}
	var stable probeResult
	if err := callOn(ctx, t, el.sessionID, node.Object.ObjectID, stableFunction, &stable, frameWait.Milliseconds()); err != nil {
		return actionPoint{}, "", err
	}
	if !c.needsPoint() {
		// 只等渲染:两个动画帧都来了即可,元素是否还在动不是这里的条件。
		if stable.State == "unstable" {
			stable.State = "ok"
		}
		why, err := positionReason(stable)
		return actionPoint{}, why, err
	}
	if why, err := positionReason(stable); why != "" || err != nil {
		return actionPoint{}, why, err
	}
	var res struct {
		Quads [][]float64 `json:"quads"`
	}
	err = t.sendTo(ctx, el.sessionID, "DOM.getContentQuads", map[string]int{"backendNodeId": el.backendNodeID}, &res)
	if err != nil {
		if isCDPError(err) {
			return actionPoint{}, "the element is not visible", nil
		}
		return actionPoint{}, "", err
	}
	quads := slices.DeleteFunc(res.Quads, func(q []float64) bool { return len(q) != 8 })
	if len(quads) == 0 {
		return actionPoint{}, "the element is not visible", nil
	}
	var pos probeResult
	if err := callOn(ctx, t, el.sessionID, node.Object.ObjectID, hitPointFunction, &pos, quads, stable.Box, c.hitTarget); err != nil {
		return actionPoint{}, "", err
	}
	if why, err := positionReason(pos); why != "" || err != nil {
		return actionPoint{}, why, err
	}
	return actionPoint{sessionID: el.sessionID, x: pos.X, y: pos.Y}, "", nil
}

// positionReason 把 stableFunction 与 hitPointFunction 的结果转换为不满足的条件;why 与 err 都为空表示满足。
func positionReason(pos probeResult) (string, error) {
	switch pos.State {
	case "ok":
		return "", nil
	case "noFrame":
		return whyNoFrame, nil
	case "unstable":
		return "the element is not stable (it is still moving)", nil
	case "outside":
		return "the element is outside the viewport", nil
	case "obscured":
		return "the element does not receive pointer events at its click point: obscured by " + truncateRunes(pos.By, maxObscurerLength, "…"), nil
	case "detached":
		return "", errDetached
	}
	return "", fmt.Errorf("unexpected actionability state %q", pos.State)
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

// quadBounds 返回四边形的外接矩形;一个元素折行时有多个四边形。与 getBoundingClientRect(及 hitPointFunction)
// 一样只算宽高都不为零的四边形,全部为零时才用全部:以换行开头的行内元素在上一行留下宽为零的片段,它不属于
// 元素看得见的范围。
func quadBounds(quads [][]float64) (minX, minY, maxX, maxY float64, ok bool) {
	var sized [][]float64
	for _, q := range quads {
		if len(q) != 8 {
			continue
		}
		if x0, y0, x1, y1 := pointBounds(q); x1 > x0 && y1 > y0 {
			sized = append(sized, q)
		}
	}
	if len(sized) == 0 {
		sized = quads
	}
	for _, q := range sized {
		if len(q) != 8 {
			continue
		}
		x0, y0, x1, y1 := pointBounds(q)
		if !ok {
			minX, minY, maxX, maxY, ok = x0, y0, x1, y1, true
			continue
		}
		minX, maxX = min(minX, x0), max(maxX, x1)
		minY, maxY = min(minY, y0), max(maxY, y1)
	}
	return minX, minY, maxX, maxY, ok
}

// pointBounds 返回一个四边形四个顶点的外接矩形。
func pointBounds(q []float64) (minX, minY, maxX, maxY float64) {
	minX, maxX, minY, maxY = q[0], q[0], q[1], q[1]
	for i := 2; i < 8; i += 2 {
		minX, maxX = min(minX, q[i]), max(maxX, q[i])
		minY, maxY = min(minY, q[i+1]), max(maxY, q[i+1])
	}
	return minX, minY, maxX, maxY
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
