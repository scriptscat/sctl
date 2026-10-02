package page

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// captureFake 是只回应截图相关命令的假页面;capture 决定 Page.captureScreenshot 的行为。
func captureFake(capture func(ctx context.Context, cmd Command) (json.RawMessage, error)) func(context.Context, Command) (json.RawMessage, error) {
	base := (&renderingPage{rendering: true}).send
	return func(ctx context.Context, cmd Command) (json.RawMessage, error) {
		if cmd.Method == "Page.captureScreenshot" {
			return capture(ctx, cmd)
		}
		return base(ctx, cmd)
	}
}

func shoot(m *Manager, input string) (json.RawMessage, error) {
	return doAction(m, "screenshot", input, time.Minute)
}

func TestScreenshotInput(t *testing.T) {
	Convey("screenshot 在边界上校验输入,非法输入不触发截图", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		for _, input := range []string{
			`{"quality":50}`,
			`{"format":"png","quality":50}`,
			`{"format":"jpeg","quality":101}`,
			`{"format":"jpeg","quality":-1}`,
			`{"format":"gif"}`,
			`{"full":true,"selector":"#a"}`,
			`{"full":true,"ref":"e1"}`,
			`{"ref":"e1","selector":"#a"}`,
			`{"ref":"#a"}`,
		} {
			_, err := shoot(m, input)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		}
		So(slices.Contains(cdp.methods(7), "Page.captureScreenshot"), ShouldBeFalse)
	})
}

func TestScreenshotFailures(t *testing.T) {
	Convey("扩展中转报告结果超过单帧上限时", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		cdp.setSend(captureFake(func(context.Context, Command) (json.RawMessage, error) {
			return nil, &Error{Code: generated.ErrorCodePayloadTooLarge, Message: "result exceeds the 4194304 byte frame limit"}
		}))
		_, err := shoot(m, `{"full":true}`)
		So(errorCode(err), ShouldEqual, generated.ErrorCodePayloadTooLarge)
		So(err.Error(), ShouldContainSubstring, "jpeg")
		So(err.Error(), ShouldContainSubstring, "viewport")
	})

	Convey("截图在期限内不返回时", t, func() {
		old := screenshotTimeout
		screenshotTimeout = 50 * time.Millisecond
		defer func() { screenshotTimeout = old }()

		Convey("返回 PAGE_HIDDEN 并提示 --activate,而不是等到动作超时", func() {
			m, cdp := newActionManager(&renderingPage{rendering: true})
			cdp.setSend(captureFake(func(ctx context.Context, _ Command) (json.RawMessage, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			}))
			started := time.Now()
			_, err := shoot(m, `{}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
			So(err.Error(), ShouldContainSubstring, "--activate")
			So(time.Since(started), ShouldBeLessThan, 10*time.Second)
		})

		Convey("返回空数据同样是 PAGE_HIDDEN,不交出空白图", func() {
			m, cdp := newActionManager(&renderingPage{rendering: true})
			cdp.setSend(captureFake(func(context.Context, Command) (json.RawMessage, error) {
				return json.RawMessage(`{"data":""}`), nil
			}))
			_, err := shoot(m, `{}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
		})
	})

	Convey("截图成功时结果带动作字段、图片数据与 MIME 类型", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		var params map[string]any
		cdp.setSend(captureFake(func(_ context.Context, cmd Command) (json.RawMessage, error) {
			So(json.Unmarshal(cmd.Params, &params), ShouldBeNil)
			return json.RawMessage(`{"data":"aGVsbG8="}`), nil
		}))
		raw, err := shoot(m, `{"format":"jpeg","quality":40}`)
		So(err, ShouldBeNil)
		So(string(raw), ShouldEqualJSON, `{"contentTrust":"untrusted-page-content","tabId":7,"url":"https://example.test/","title":"Example","navigated":false,"data":"aGVsbG8=","mimeType":"image/jpeg"}`)
		So(params["format"], ShouldEqual, "jpeg")
		So(params["quality"], ShouldEqual, float64(40))
		So(strings.Contains(string(raw), "PAGE_HIDDEN"), ShouldBeFalse)
	})
}

// unrenderedFramePage 在 oopifPage 上回应元素截图用到的命令:子会话 S1 的 frame 在前 idleFrames 轮里不产生
// 动画帧,就像刚被滚入视口、还没开始渲染的跨域 iframe。
type unrenderedFramePage struct {
	*oopifPage
	mu         sync.Mutex
	idleFrames int
	// log 按顺序记录 S1 上的帧检查结果与顶层的截图。
	log []string
}

func (p *unrenderedFramePage) send(ctx context.Context, cmd Command) (json.RawMessage, error) {
	switch cmd.Method {
	case "Runtime.callFunctionOn":
		var params struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			return nil, err
		}
		switch params.FunctionDeclaration {
		case stateFunction:
			return json.RawMessage(`{"result":{"type":"object","value":{"state":"ok"}}}`), nil
		case stableFunction:
			p.mu.Lock()
			defer p.mu.Unlock()
			if cmd.SessionID == "S1" && p.idleFrames > 0 {
				p.idleFrames--
				p.log = append(p.log, "noFrame")
				return json.RawMessage(`{"result":{"type":"object","value":{"state":"noFrame"}}}`), nil
			}
			p.log = append(p.log, "frames")
			return json.RawMessage(`{"result":{"type":"object","value":{"state":"ok","box":[20,30,120,60]}}}`), nil
		}
	case "DOM.getContentQuads":
		return json.RawMessage(`{"quads":[[20,30,140,30,140,90,20,90]]}`), nil
	case "DOM.getFrameOwner":
		return json.RawMessage(`{"backendNodeId":11}`), nil
	case "DOM.getBoxModel":
		return json.RawMessage(`{"model":{"content":[300,100,600,100,600,300,300,300],"width":300,"height":200}}`), nil
	case "Page.getLayoutMetrics":
		return json.RawMessage(`{"cssContentSize":{"width":800,"height":1500},"cssVisualViewport":{"pageX":0,"pageY":1000,"clientWidth":800,"clientHeight":600}}`), nil
	case "Page.getFrameTree":
		return json.RawMessage(`{"frameTree":{"frame":{"id":"main"}}}`), nil
	case "Page.captureScreenshot":
		p.mu.Lock()
		p.log = append(p.log, "capture")
		p.mu.Unlock()
		return json.RawMessage(`{"data":"aGVsbG8="}`), nil
	}
	return p.oopifPage.send(ctx, cmd)
}

func (p *unrenderedFramePage) events() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.log)
}

func TestScreenshotOfElementInFrameThatIsNotRenderingYet(t *testing.T) {
	Convey("截取跨进程 iframe 里的元素时,iframe 还没开始渲染就截图只会得到空白", t, func() {
		cdp := newFakeCDP()
		pg := &unrenderedFramePage{oopifPage: newOOPIFPage(3)}
		cdp.setSend(pg.send)
		m := newSnapshotManager(cdp)
		pg.m = m
		_, err := takeSnapshot(m, 3)
		So(err, ShouldBeNil)
		shootInner := func() (json.RawMessage, error) {
			tab := 3
			return m.Do(context.Background(), Request{Action: "screenshot", TabID: &tab, Timeout: time.Minute, Input: json.RawMessage(`{"ref":"e3"}`)})
		}

		Convey("等到 iframe 产生动画帧之后才截图", func() {
			pg.mu.Lock()
			pg.idleFrames = 2
			pg.mu.Unlock()
			_, err := shootInner()
			So(err, ShouldBeNil)
			So(pg.events(), ShouldResemble, []string{"noFrame", "noFrame", "frames", "capture"})
		})

		Convey("iframe 一直不渲染时返回 PAGE_HIDDEN,不交出空白图", func() {
			pg.mu.Lock()
			pg.idleFrames = 1 << 30
			pg.mu.Unlock()
			_, err := shootInner()
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
			So(err.Error(), ShouldContainSubstring, "captured blank")
			So(pg.events(), ShouldNotContain, "capture")
		})
	})
}

func TestElementScreenshotClip(t *testing.T) {
	Convey("元素截图的范围与 getBoundingClientRect 一致:以换行开头的行内元素在上一行留下的宽为零的片段不算", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		var clip map[string]any
		base := captureFake(func(_ context.Context, cmd Command) (json.RawMessage, error) {
			var params struct {
				Clip map[string]any `json:"clip"`
			}
			So(json.Unmarshal(cmd.Params, &params), ShouldBeNil)
			clip = params.Clip
			return json.RawMessage(`{"data":"aGVsbG8="}`), nil
		})
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			switch cmd.Method {
			case "DOM.getContentQuads":
				return json.RawMessage(`{"quads":[[152,10,152,10,152,29,152,29],[8,34,162,34,162,53,8,53]]}`), nil
			case "Page.getLayoutMetrics":
				return json.RawMessage(`{"cssContentSize":{"width":800,"height":1500},"cssVisualViewport":{"pageX":0,"pageY":100,"clientWidth":800,"clientHeight":600}}`), nil
			}
			return base(ctx, cmd)
		})
		_, err := shoot(m, `{"selector":"#target"}`)
		So(err, ShouldBeNil)
		So(clip, ShouldResemble, map[string]any{"x": float64(8), "y": float64(134), "width": float64(154), "height": float64(19), "scale": float64(1)})
	})
}
