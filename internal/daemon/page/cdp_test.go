package page

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func cdpSend(m *Manager, tab int, input string) (json.RawMessage, error) {
	return m.Do(context.Background(), Request{Action: "cdp.send", TabID: tabRef(tab), Input: json.RawMessage(input)})
}

func TestCdpSend(t *testing.T) {
	Convey("cdp.send 把原始 CDP 命令发给标签页的顶层会话", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})

		Convey("原样转发方法与参数,结果是 Chrome 的结果对象加 tabId 与 contentTrust", func() {
			cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Browser.getVersion" {
					return json.RawMessage(`{"product":"Chrome/125","jsVersion":"12.5"}`), nil
				}
				return json.RawMessage(`{}`), nil
			})
			res, err := cdpSend(m, 3, `{"method":"Browser.getVersion","params":{"a":[1,2]}}`)
			So(err, ShouldBeNil)
			So(string(res), ShouldEqualJSON, `{"product":"Chrome/125","jsVersion":"12.5","tabId":3,"contentTrust":"untrusted-page-content"}`)
			cdp.mu.Lock()
			defer cdp.mu.Unlock()
			last := cdp.sent[len(cdp.sent)-1]
			So(last.Method, ShouldEqual, "Browser.getVersion")
			So(last.TabID, ShouldEqual, 3)
			So(last.SessionID, ShouldEqual, "")
			So(string(last.Params), ShouldEqualJSON, `{"a":[1,2]}`)
		})

		Convey("省略 params 时不带参数", func() {
			_, err := cdpSend(m, 3, `{"method":"Page.getNavigationHistory"}`)
			So(err, ShouldBeNil)
			cdp.mu.Lock()
			defer cdp.mu.Unlock()
			So(cdp.sent[len(cdp.sent)-1].Params, ShouldBeEmpty)
		})

		Convey("method 不是 Domain.method、params 不是对象时返回 INVALID_REQUEST,不发给 Chrome", func() {
			for _, input := range []string{
				`{}`, `{"method":""}`, `{"method":"Page"}`, `{"method":"Page."}`, `{"method":".enable"}`,
				`{"method":"Page.a.b"}`, `{"method":"Page enable"}`,
				`{"method":"Page.enable","params":[]}`, `{"method":"Page.enable","params":"x"}`, `{"method":"Page.enable","params":null}`, `{"method":"Page.enable","params":1}`,
			} {
				_, err := cdpSend(m, 3, input)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
			So(cdp.methods(3), ShouldBeEmpty)
		})

		Convey("拒绝列表里的命令返回 INVALID_REQUEST 并说明原因,不发给 Chrome", func() {
			for _, method := range []string{
				"Page.disable", "Runtime.disable", "Network.disable", "Log.disable",
				"Emulation.setFocusEmulationEnabled", "Target.setAutoAttach", "Target.detachFromTarget",
			} {
				_, err := cdpSend(m, 3, `{"method":"`+method+`","params":{}}`)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
				So(err.Error(), ShouldContainSubstring, method)
			}
			So(cdp.methods(3), ShouldBeEmpty)
		})

		Convey("拒绝列表只拦列出的命令:对应的 enable 照常发送", func() {
			_, err := cdpSend(m, 3, `{"method":"Page.enable"}`)
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldContain, "Page.enable")
		})

		Convey("JS 弹框打开时照常发送,可用来处理弹框", func() {
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			openDialog(m, 3, "alert", "hi")
			res, err := cdpSend(m, 3, `{"method":"Page.handleJavaScriptDialog","params":{"accept":true}}`)
			So(err, ShouldBeNil)
			So(string(res), ShouldContainSubstring, `"tabId":3`)
			So(cdp.methods(3), ShouldContain, "Page.handleJavaScriptDialog")
		})

		Convey("Chrome 的拒绝成为 INVALID_REQUEST,保留 Chrome 自己的错误信息", func() {
			chrome := `{"code":-32601,"message":"'Foo.bar' wasn't found"}`
			cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Foo.bar" {
					return nil, &Error{Code: generated.ErrorCodeInvalidRequest, Message: chrome}
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := cdpSend(m, 3, `{"method":"Foo.bar"}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(err.Error(), ShouldContainSubstring, chrome)
		})

		Convey("结果超过单帧上限时返回 PAYLOAD_TOO_LARGE", func() {
			cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "DOM.getDocument" {
					return nil, &Error{Code: generated.ErrorCodePayloadTooLarge, Message: "result exceeds the frame limit"}
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := cdpSend(m, 3, `{"method":"DOM.getDocument"}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePayloadTooLarge)
		})

		Convey("Chrome 一直不回答时按请求的时限返回 TIMEOUT", func() {
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Debugger.pause" {
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := m.Do(context.Background(), Request{Action: "cdp.send", TabID: tabRef(3), Timeout: 50 * time.Millisecond, Input: json.RawMessage(`{"method":"Debugger.pause"}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeTimeout)
		})
	})
}
