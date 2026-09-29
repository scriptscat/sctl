package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// screenshotTimeout 是等 Page.captureScreenshot 返回的上限。开启焦点模拟后后台标签页照常出图;真机探针
// 显示不开时 Chrome 125 上截图永不返回,焦点模拟也无效的页面(最小化、冻结)同样如此。超过它就返回
// PAGE_HIDDEN,绝不交出空白图。15 秒远大于正常截图(整页大图也在数秒内),又小于 screenshotActionTimeout,
// 所以先于动作超时触发。
var screenshotTimeout = 15 * time.Second

// screenshotActionTimeout 是 screenshot 动作的默认超时:排队、元素等待与截图各自的耗时之和。
const screenshotActionTimeout = 30 * time.Second

var screenshotMIME = map[string]string{"png": "image/png", "jpeg": "image/jpeg"}

type screenshotInput struct {
	TargetSpec
	Full    bool   `json:"full"`
	Format  string `json:"format"`
	Quality *int   `json:"quality"`
}

func (in screenshotInput) hasTarget() bool { return in.Ref != "" || in.Selector != "" }

// screenshotResult 是动作结果加上图片:Data 是 base64 编码的图像。
type screenshotResult struct {
	ActionResult
	Data     string `json:"data"`
	MimeType string `json:"mimeType"`
}

func (in *screenshotInput) validate() error {
	if in.Format == "" {
		in.Format = "png"
	}
	if _, ok := screenshotMIME[in.Format]; !ok {
		return invalidRequest(fmt.Sprintf("unknown screenshot format %q: use png or jpeg", in.Format))
	}
	if in.Quality != nil {
		if in.Format != "jpeg" {
			return invalidRequest("--quality applies to jpeg only; add --format jpeg")
		}
		if *in.Quality < 0 || *in.Quality > 100 {
			return invalidRequest(fmt.Sprintf("screenshot quality must be between 0 and 100, not %d", *in.Quality))
		}
	}
	if in.hasTarget() {
		if in.Full {
			return invalidRequest("page screenshot takes --full or a target, not both")
		}
		return in.TargetSpec.validate("screenshot")
	}
	return nil
}

// runScreenshot 在弹框打开期间也会尝试截图(spec 允许),但渲染进程被弹框卡住时 Chrome 连最初的
// Page.getFrameTree 都不会回应,所以这时把整个动作限制在 screenshotTimeout 内,超时报告 DIALOG_OPEN。
func runScreenshot(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	d := t.dialog.current()
	if d == nil {
		return takeScreenshot(ctx, t, input)
	}
	bounded, cancel := context.WithTimeout(ctx, screenshotTimeout)
	defer cancel()
	res, err := takeScreenshot(bounded, t, input)
	if err != nil && ctx.Err() == nil && bounded.Err() != nil {
		return nil, noImageDialogError(t.id, *d)
	}
	return res, err
}

func noImageDialogError(tabID int, d dialogInfo) *Error {
	e := dialogOpenError(tabID, d)
	e.Message = fmt.Sprintf("tab %d produced no image because a JS dialog blocks the page: ", tabID) + e.Message
	return e
}

func takeScreenshot(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in screenshotInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()

	params := map[string]any{"format": in.Format, "fromSurface": true}
	if in.Quality != nil {
		params["quality"] = *in.Quality
	}
	switch {
	case in.hasTarget():
		clip, oversized, err := elementClip(ctx, t, in.TargetSpec)
		if err != nil {
			return nil, err
		}
		// 元素滚入视口后整个在视口里时不能开 captureBeyondViewport:开了之后跨进程 iframe 的内容在
		// 截图里是空白(真机观察)。只有比视口还大的元素才需要它。
		params["clip"], params["captureBeyondViewport"] = clip, oversized
	case in.Full:
		clip, err := pageClip(ctx, t)
		if err != nil {
			return nil, err
		}
		params["clip"], params["captureBeyondViewport"] = clip, true
	}
	data, err := capture(ctx, t, params)
	if err != nil {
		return nil, err
	}
	res, err := run.finish(ctx, false)
	if err != nil {
		return nil, err
	}
	return screenshotResult{ActionResult: res, Data: data, MimeType: screenshotMIME[in.Format]}, nil
}

type clip struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
	Scale  float64 `json:"scale"`
}

// layoutMetrics 是 Page.getLayoutMetrics 中截图用到的字段。
type layoutMetrics struct {
	Content struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"cssContentSize"`
	Viewport struct {
		PageX  float64 `json:"pageX"`
		PageY  float64 `json:"pageY"`
		Width  float64 `json:"clientWidth"`
		Height float64 `json:"clientHeight"`
	} `json:"cssVisualViewport"`
}

func metricsOf(ctx context.Context, t *Tab) (layoutMetrics, error) {
	var m layoutMetrics
	err := t.send(ctx, "Page.getLayoutMetrics", nil, &m)
	return m, err
}

// pageClip 是整个文档的范围;配合 captureBeyondViewport 截到视口以外。
func pageClip(ctx context.Context, t *Tab) (clip, error) {
	m, err := metricsOf(ctx, t)
	if err != nil {
		return clip{}, err
	}
	return clip{Width: m.Content.Width, Height: m.Content.Height, Scale: 1}, nil
}

// elementClip 等目标已挂载且可见(同时滚入视口),返回它的边界框在顶层页面坐标里的范围。子会话不能截图
// (真机探针:只有顶层目标可以),所以跨进程 iframe 里的元素要把 frame 内的坐标逐层换算到顶层。
func elementClip(ctx context.Context, t *Tab, spec TargetSpec) (c clip, oversized bool, err error) {
	var wait backoff
	for {
		el, _, err := actionElement(ctx, t, spec, checks{visible: true}, nil)
		if err != nil {
			return clip{}, false, err
		}
		var res struct {
			Quads [][]float64 `json:"quads"`
		}
		if err := t.sendTo(ctx, el.sessionID, "DOM.getContentQuads", map[string]int{"backendNodeId": el.backendNodeID}, &res); err != nil {
			if !isCDPError(err) {
				return clip{}, false, err
			}
		}
		minX, minY, maxX, maxY, ok := quadBounds(res.Quads)
		if ok && maxX > minX && maxY > minY {
			dx, dy, err := t.frameOffset(ctx, el.sessionID)
			if err != nil {
				return clip{}, false, err
			}
			m, err := metricsOf(ctx, t)
			if err != nil {
				return clip{}, false, err
			}
			c := clip{
				X: minX + dx + m.Viewport.PageX, Y: minY + dy + m.Viewport.PageY,
				Width: maxX - minX, Height: maxY - minY, Scale: 1,
			}
			return c, c.Width > m.Viewport.Width || c.Height > m.Viewport.Height, nil
		}
		// 元素在两次检查之间失去了布局框(例如被框架重新渲染),重新等它可见。
		if err := wait.sleep(ctx); err != nil {
			return clip{}, false, waitFailed(ctx, err, "the element has no visible box to capture")
		}
	}
}

// frameOffset 返回 sessionID 会话的视口原点在顶层视口里的位置:沿子会话链逐层加上 iframe 元素内容框
// 的左上角(iframe 的边框与内边距不属于子文档的视口)。
func (t *Tab) frameOffset(ctx context.Context, sessionID string) (x, y float64, err error) {
	for sessionID != "" {
		frameID, parent, ok := t.frames.owner(sessionID)
		if !ok {
			return 0, 0, staleFrame(t.id)
		}
		var owner struct {
			BackendNodeID int `json:"backendNodeId"`
		}
		if err := t.sendTo(ctx, parent, "DOM.getFrameOwner", map[string]string{"frameId": frameID}, &owner); err != nil {
			return 0, 0, err
		}
		var box struct {
			Model struct {
				Content []float64 `json:"content"`
			} `json:"model"`
		}
		if err := t.sendTo(ctx, parent, "DOM.getBoxModel", map[string]int{"backendNodeId": owner.BackendNodeID}, &box); err != nil {
			return 0, 0, err
		}
		if len(box.Model.Content) != 8 {
			return 0, 0, fmt.Errorf("unexpected box model for the iframe owning frame %s", frameID)
		}
		x += box.Model.Content[0]
		y += box.Model.Content[1]
		sessionID = parent
	}
	return x, y, nil
}

func staleFrame(tabID int) *Error {
	return &Error{
		Code:    generated.ErrorCodeStaleRef,
		Message: fmt.Sprintf("the iframe containing the element was replaced while capturing tab %d; take a new snapshot", tabID),
	}
}

// capture 发出截图并在 screenshotTimeout 内等结果。页面不出图(超时或空数据)是 PAGE_HIDDEN;扩展中转
// 因结果超过单帧上限拒绝时换成给出办法的 PAYLOAD_TOO_LARGE。
func capture(ctx context.Context, t *Tab, params map[string]any) (string, error) {
	wait, cancel := context.WithTimeout(ctx, screenshotTimeout)
	defer cancel()
	var res struct {
		Data string `json:"data"`
	}
	err := t.send(wait, "Page.captureScreenshot", params, &res)
	switch {
	case err == nil && res.Data != "":
		return res.Data, nil
	case err == nil, ctx.Err() == nil && wait.Err() != nil:
		// 弹框卡住渲染进程时截图不会返回;这不是标签页被隐藏,--activate 帮不上忙。
		if d := t.dialog.current(); d != nil {
			return "", noImageDialogError(t.id, *d)
		}
		return "", &Error{
			Code: generated.ErrorCodePageHidden,
			Message: fmt.Sprintf("tab %d produced no image (no screenshot within %s), so a blank picture is not returned; retry with --activate",
				t.id, screenshotTimeout),
		}
	}
	var pe *Error
	if errors.As(err, &pe) && pe.Code == generated.ErrorCodePayloadTooLarge {
		return "", &Error{
			Code:    generated.ErrorCodePayloadTooLarge,
			Message: "the screenshot is larger than one protocol frame: use --format jpeg (lower --quality shrinks it further) or capture only the viewport instead of --full",
		}
	}
	return "", err
}
