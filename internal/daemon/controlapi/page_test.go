package controlapi

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func (h *testHarness) goPage(req control.PageRequest) <-chan control.CallResult {
	out := make(chan control.CallResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		resp, err := postControl(ctx, h.httpBase(), control.PathPage, testControlToken, "", req)
		if err != nil {
			out <- control.CallResult{Error: &control.CallError{Code: "TEST_TRANSPORT", Message: err.Error()}}
			return
		}
		defer resp.Body.Close()
		var res control.CallResult
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			res = control.CallResult{Error: &control.CallError{Code: "TEST_DECODE", Message: err.Error()}}
		}
		out <- res
	}()
	return out
}

// debuggerSend 读取下一条 debugger.send,断言目标标签页与 CDP 方法,返回请求以便应答。
func (e *extClient) debuggerSend(tabID int, cdpMethod string) bridge.Message {
	req := e.read()
	So(req.Method, ShouldEqual, "debugger.send")
	var params struct {
		Input generated.DebuggerSendParams `json:"input"`
	}
	So(json.Unmarshal(req.Params, &params), ShouldBeNil)
	So(params.Input.TabId, ShouldEqual, tabID)
	So(params.Input.Method, ShouldEqual, cdpMethod)
	return req
}

func (e *extClient) writeDomainError(id, code, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	So(wsjson.Write(ctx, e.ws, bridge.Message{JSONRPC: "2.0", ID: id, Error: &bridge.RPCError{Code: -32000, Message: message, Data: &bridge.ErrorData{Code: code}}}), ShouldBeNil)
}

func (e *extClient) writeNotification(method string, params any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := json.Marshal(params)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, bridge.Message{JSONRPC: "2.0", Method: method, Params: raw}), ShouldBeNil)
}

// answerEvalStart 应答 page eval 求值之前的准备:查询主 frame,并列出已有的标签页(tabs),据此认出求值打开的新标签页。
func (e *extClient) answerEvalStart(tabID int, tabs string) {
	req := e.debuggerSend(tabID, "Page.getFrameTree")
	e.writeResult(req.ID, json.RawMessage(`{"result":{"frameTree":{"frame":{"id":"main"}}}}`))
	e.answer("tabs.list", json.RawMessage(`{"contentTrust":"untrusted-page-content","tabs":`+tabs+`}`))
}

// answerEvalEnd 应答求值之后的命令:求值得到数字 3,释放对象组,再读取动作结束时的页面 URL 与标题。
func (e *extClient) answerEvalEnd(tabID int) {
	req := e.debuggerSend(tabID, "Runtime.evaluate")
	e.writeResult(req.ID, json.RawMessage(`{"result":{"result":{"type":"number","value":3}}}`))
	req = e.debuggerSend(tabID, "Runtime.releaseObjectGroup")
	e.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
	req = e.debuggerSend(tabID, "Page.getNavigationHistory")
	e.writeResult(req.ID, json.RawMessage(`{"result":{"currentIndex":0,"entries":[{"id":1,"url":"https://example.test/","title":"Example"}]}}`))
}

// answerEval 应答一次 page eval 在已附加标签页上发出的全部命令。
func (e *extClient) answerEval(tabID int) {
	e.answerEvalStart(tabID, tabList(tabID))
	e.answerEvalEnd(tabID)
}

// tabList 是只含给定标签页的 tabs.list 结果。
func tabList(ids ...int) string {
	tabs := make([]map[string]any, len(ids))
	for i, id := range ids {
		tabs[i] = map[string]any{"tabId": id, "windowId": 1, "active": i == 0, "pinned": false, "groupId": -1, "title": "t", "url": "https://example.test/"}
	}
	raw, err := json.Marshal(tabs)
	So(err, ShouldBeNil)
	return string(raw)
}

// answerAttach 应答附加后的准备命令,顺序与附加钩子一致:焦点模拟,开启 iframe 自动附加(扁平会话,新 iframe
// 启动时暂停),开启 Page 域(JS 弹框与文档替换事件),读取主文档 URL,然后开启控制台与浏览器消息,最后开启网络记录。
func (e *extClient) answerAttach(tabID int) {
	req := e.debuggerSend(tabID, "Emulation.setFocusEmulationEnabled")
	e.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
	req = e.debuggerSend(tabID, "Target.setAutoAttach")
	var sent struct {
		Input generated.DebuggerSendParams `json:"input"`
	}
	So(json.Unmarshal(req.Params, &sent), ShouldBeNil)
	So(string(sent.Input.Params), ShouldEqual, `{"autoAttach":true,"flatten":true,"waitForDebuggerOnStart":true}`)
	e.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
	req = e.debuggerSend(tabID, "Page.enable")
	e.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
	req = e.debuggerSend(tabID, "Page.getFrameTree")
	e.writeResult(req.ID, json.RawMessage(`{"result":{"frameTree":{"frame":{"id":"main","url":"https://example.test/"}}}}`))
	for _, method := range []string{"Runtime.enable", "Log.enable", "Network.enable"} {
		req = e.debuggerSend(tabID, method)
		e.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
	}
}

var evalInput = json.RawMessage(`{"expression":"1 + 2"}`)

func TestPageTargetSelection(t *testing.T) {
	Convey("/control/page 的目标选择", t, func() {
		h := startTestServer(t)

		Convey("没有浏览器在线:NO_BROWSER_CONNECTED", func() {
			res := <-h.goPage(control.PageRequest{Action: "eval", Input: evalInput})
			So(errCode(res), ShouldEqual, generated.ErrorCodeNoBrowserConnected)
		})

		Convey("未指定标签页时用浏览器报告的默认标签页,附加后开启焦点模拟,结果带 tabId", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			ch := h.goPage(control.PageRequest{Action: "eval", Input: evalInput})
			a.answer("tabs.current", json.RawMessage(`{"tabId":5,"windowId":1}`))
			a.answerAttach(5)
			a.answerEval(5)
			res := <-ch
			So(res.Error, ShouldBeNil)
			So(string(res.Result), ShouldEqual, `{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.test/","title":"Example","navigated":false,"value":3}`)
		})

		Convey("浏览器没有可用的默认标签页:NOT_FOUND", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			ch := h.goPage(control.PageRequest{Action: "eval", Input: evalInput})
			req := a.read()
			So(req.Method, ShouldEqual, "tabs.current")
			a.writeDomainError(req.ID, generated.ErrorCodeNotFound, "no normal browser window is open")
			So(errCode(<-ch), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("指定的标签页不存在:NOT_FOUND", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			ch := h.goPage(control.PageRequest{Action: "eval", TabID: new(9), Input: evalInput})
			req := a.debuggerSend(9, "Emulation.setFocusEmulationEnabled")
			a.writeDomainError(req.ID, generated.ErrorCodeNotFound, "no tab 9")
			// 附加准备失败后 daemon 断开这个标签页,扩展里可能已经附加。
			a.answer("debugger.detach", json.RawMessage(`{"tabIds":[]}`))
			So(errCode(<-ch), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("--activate 只让标签页成为窗口内的激活标签页(tabs.select),不聚焦窗口", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			ch := h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Activate: true, Input: evalInput})
			a.answer("tabs.select", json.RawMessage(`{"tabId":5,"windowId":1}`))
			a.answerAttach(5)
			a.answerEval(5)
			So((<-ch).Error, ShouldBeNil)
		})

		Convey("多个浏览器在线且未指定目标:BROWSER_AMBIGUOUS;按名称指定时发给该实例", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			b := h.connectBrowser(instanceB, "edge-fedc")
			So(errCode(<-h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Input: evalInput})), ShouldEqual, generated.ErrorCodeBrowserAmbiguous)

			ch := h.goPage(control.PageRequest{Action: "eval", Browser: "edge-fedc", TabID: new(5), Input: evalInput})
			b.answerAttach(5)
			b.answerEval(5)
			So((<-ch).Error, ShouldBeNil)
			a.idle()
		})
	})
}

func TestPageDebuggerLifecycle(t *testing.T) {
	Convey("/control/page 与调试器生命周期", t, func() {
		h := startTestServer(t)
		a := h.connectBrowser(instanceA, "chrome-0123")

		Convey("命令执行中收到该标签页的 debugger.detached:DEBUGGER_DETACHED", func() {
			ch := h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Input: evalInput})
			a.answerAttach(5)
			a.answerEvalStart(5, tabList(5))
			a.debuggerSend(5, "Runtime.evaluate")
			a.writeNotification("debugger.detached", map[string]any{"tabId": 5, "reason": "canceled_by_user"})
			res := <-ch
			So(errCode(res), ShouldEqual, generated.ErrorCodeDebuggerDetached)
			So(a.read().Method, ShouldEqual, methodCancel)

			// 状态已清空:下一条命令重新开启焦点模拟。
			ch = h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Input: evalInput})
			a.answerAttach(5)
			a.answerEval(5)
			So((<-ch).Error, ShouldBeNil)
		})

		Convey("动作打开的新标签页经扩展的 Page.windowOpen 事件与 tabs.list 认出,结果带它的 tabId", func() {
			ch := h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Input: evalInput})
			a.answerAttach(5)
			a.answerEvalStart(5, tabList(5, 6))
			req := a.debuggerSend(5, "Runtime.evaluate")
			a.writeNotification("debugger.event", map[string]any{"tabId": 5, "method": "Page.windowOpen", "params": map[string]any{"url": "https://example.test/new"}})
			a.writeResult(req.ID, json.RawMessage(`{"result":{"result":{"type":"number","value":3}}}`))
			req = a.debuggerSend(5, "Runtime.releaseObjectGroup")
			a.writeResult(req.ID, json.RawMessage(`{"result":{}}`))
			a.answer("tabs.list", json.RawMessage(`{"contentTrust":"untrusted-page-content","tabs":`+tabList(5, 6, 8)+`}`))
			req = a.debuggerSend(5, "Page.getNavigationHistory")
			a.writeResult(req.ID, json.RawMessage(`{"result":{"currentIndex":0,"entries":[{"id":1,"url":"https://example.test/","title":"Example"}]}}`))
			res := <-ch
			So(res.Error, ShouldBeNil)
			So(string(res.Result), ShouldEqual, `{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.test/","title":"Example","navigated":false,"newTabId":8,"value":3}`)
		})

		Convey("命令执行中浏览器断开:DEBUGGER_DETACHED", func() {
			ch := h.goPage(control.PageRequest{Action: "eval", TabID: new(5), Input: evalInput})
			a.debuggerSend(5, "Emulation.setFocusEmulationEnabled")
			So(a.ws.CloseNow(), ShouldBeNil)
			So(errCode(<-ch), ShouldEqual, generated.ErrorCodeDebuggerDetached)
		})

		Convey("page detach 发 debugger.detach 并返回断开的标签页", func() {
			ch := h.goPage(control.PageRequest{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
			req := a.read()
			So(req.Method, ShouldEqual, "debugger.detach")
			So(string(req.Params), ShouldContainSubstring, `"input":{}`)
			a.writeResult(req.ID, json.RawMessage(`{"tabIds":[5,6]}`))
			res := <-ch
			So(res.Error, ShouldBeNil)
			So(string(res.Result), ShouldEqual, `{"tabIds":[5,6]}`)
		})
	})
}

func TestPageRequestValidation(t *testing.T) {
	Convey("/control/page 在边界上校验请求", t, func() {
		h := startTestServer(t)

		Convey("没有控制令牌:401", func() {
			resp, err := postControl(context.Background(), h.httpBase(), control.PathPage, "", "", control.PageRequest{Action: "eval"})
			So(err, ShouldBeNil)
			resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})

		Convey("未知动作、负的标签页 ID、负的超时、换算成时长会溢出的超时:INVALID_REQUEST", func() {
			So(errCode(<-h.goPage(control.PageRequest{Action: "eval", TimeoutMs: math.MaxInt64/int(time.Millisecond) + 1, Input: evalInput})), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(errCode(<-h.goPage(control.PageRequest{Action: "teleport"})), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(errCode(<-h.goPage(control.PageRequest{Action: "eval", TabID: new(-1), Input: evalInput})), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(errCode(<-h.goPage(control.PageRequest{Action: "eval", TimeoutMs: -1, Input: evalInput})), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("内部方法不能经 /control/call 调用", func() {
			for _, action := range []string{"tabs.current", "tabs.select", "debugger.send", "debugger.detach"} {
				So(errCode(h.callControl(control.CallRequest{Action: action, Input: json.RawMessage(`{}`)})), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})
	})
}
