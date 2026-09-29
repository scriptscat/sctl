package page

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// renderingPage 是一个选择器 #target 恰好匹配一个元素的假页面。rendering 为 false 时页面不产生动画帧,
// 就像焦点模拟也救不回来的被冻结或最小化的标签页。
type renderingPage struct {
	rendering bool
	// obscuredBy 非空时点击点被这个描述的元素挡住。
	obscuredBy string
}

func (p *renderingPage) send(_ context.Context, cmd Command) (json.RawMessage, error) {
	switch cmd.Method {
	case "DOM.getDocument":
		return json.RawMessage(`{"root":{"nodeId":1}}`), nil
	case "DOM.querySelectorAll":
		return json.RawMessage(`{"nodeIds":[2]}`), nil
	case "DOM.describeNode":
		return json.RawMessage(`{"node":{"backendNodeId":20}}`), nil
	case "DOM.resolveNode":
		return json.RawMessage(`{"object":{"type":"object","objectId":"obj-20"}}`), nil
	case "Page.getFrameTree":
		return json.RawMessage(`{"frameTree":{"frame":{"id":"main"}}}`), nil
	case "Page.getNavigationHistory":
		return json.RawMessage(`{"currentIndex":0,"entries":[{"url":"https://example.test/","title":"Example"}]}`), nil
	case "DOM.getContentQuads":
		return json.RawMessage(`{"quads":[[10,10,110,10,110,40,10,40]]}`), nil
	case "Runtime.callFunctionOn":
		var params struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			return nil, err
		}
		if strings.Contains(params.FunctionDeclaration, "requestAnimationFrame") {
			if !p.rendering {
				return json.RawMessage(`{"result":{"type":"object","value":{"state":"noFrame"}}}`), nil
			}
			if p.obscuredBy != "" {
				by, err := json.Marshal(p.obscuredBy)
				if err != nil {
					return nil, err
				}
				return json.RawMessage(`{"result":{"type":"object","value":{"state":"obscured","by":` + string(by) + `}}}`), nil
			}
			return json.RawMessage(`{"result":{"type":"object","value":{"state":"ok","fx":0.5,"fy":0.5}}}`), nil
		}
		return json.RawMessage(`{"result":{"type":"object","value":{"state":"ok"}}}`), nil
	}
	return json.RawMessage(`{}`), nil
}

func newActionManager(p *renderingPage) (*Manager, *fakeCDP) {
	cdp := newFakeCDP()
	cdp.setSend(p.send)
	return newTestManager(cdp, &fakeClock{}), cdp
}

func doAction(m *Manager, action, input string, timeout time.Duration) (json.RawMessage, error) {
	return m.Do(context.Background(), Request{Action: action, TabID: tabRef(7), Timeout: timeout, Input: json.RawMessage(input)})
}

func TestActionInputValidation(t *testing.T) {
	Convey("click/hover 在边界上校验输入,非法输入不发出任何鼠标事件", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		cases := map[string][]string{
			"click": {
				`{}`,
				`{"ref":"e5","selector":"#a"}`,
				`{"ref":"#a"}`,
				`{"selector":"#a","button":"back"}`,
				`{"selector":"#a","count":0}`,
				`{"selector":"#a","modifiers":["Hyper"]}`,
				`{"selector":"#a","modifiers":["Alt","Alt"]}`,
			},
			"hover": {
				`{}`,
				`{"ref":"e1","selector":"#a"}`,
				`{"selector":"#a","button":"left"}`,
			},
		}
		for action, inputs := range cases {
			for _, input := range inputs {
				_, err := doAction(m, action, input, time.Second)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		}
		So(slices.Contains(cdp.methods(7), "Input.dispatchMouseEvent"), ShouldBeFalse)
	})
}

func TestActionOnPageThatDoesNotRender(t *testing.T) {
	Convey("页面不产生动画帧、位置稳定无法确认时", t, func() {
		Convey("click 返回 PAGE_HIDDEN 并提示 --activate,不点击也不等到超时", func() {
			m, cdp := newActionManager(&renderingPage{rendering: false})
			started := time.Now()
			_, err := doAction(m, "click", `{"selector":"#target"}`, time.Minute)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageHidden)
			So(err.Error(), ShouldContainSubstring, "--activate")
			So(time.Since(started), ShouldBeLessThan, 30*time.Second)
			So(slices.Contains(cdp.methods(7), "Input.dispatchMouseEvent"), ShouldBeFalse)
		})

		Convey("页面正常渲染时 hover 移动鼠标并返回动作结果", func() {
			m, cdp := newActionManager(&renderingPage{rendering: true})
			raw, err := doAction(m, "hover", `{"selector":"#target"}`, time.Minute)
			So(err, ShouldBeNil)
			So(string(raw), ShouldEqualJSON, `{"contentTrust":"untrusted-page-content","tabId":7,"url":"https://example.test/","title":"Example","navigated":false}`)
			So(slices.Contains(cdp.methods(7), "Input.dispatchMouseEvent"), ShouldBeTrue)
		})
	})
}

func TestObscuredClickTimeout(t *testing.T) {
	Convey("点击点一直被挡住时,TIMEOUT 写明遮挡元素;页面给的超长描述按字符截断,不切开多字节字符", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true, obscuredBy: "div#a" + strings.Repeat("遮", 200)})
		_, err := doAction(m, "click", `{"selector":"#target"}`, 300*time.Millisecond)
		So(errorCode(err), ShouldEqual, generated.ErrorCodeTimeout)
		So(err.Error(), ShouldContainSubstring, "obscured by div#a遮")
		So(utf8.ValidString(err.Error()), ShouldBeTrue)
		So(slices.Contains(cdp.methods(7), "Input.dispatchMouseEvent"), ShouldBeFalse)
	})
}
