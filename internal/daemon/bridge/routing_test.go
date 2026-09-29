package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

const tabsListResult = `{"tabs":[],"contentTrust":"untrusted-page-content"}`

type callOutcome struct {
	resp Response
	err  error
}

// callTimeout 限定测试调用的等待:路由错时请求发给了不会应答的对端,测试应失败而不是挂起。
const callTimeout = 3 * time.Second

func (h *testHarness) call(req Request) (Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	return h.srv.Call(ctx, req)
}

func (h *testHarness) goCall(req Request) <-chan callOutcome {
	out := make(chan callOutcome, 1)
	go func() {
		resp, err := h.call(req)
		out <- callOutcome{resp, err}
	}()
	return out
}

func errorCode(err error) string {
	var be *Error
	if !errors.As(err, &be) {
		return ""
	}
	return be.Code
}

func TestCallsRouteByMethodOwnership(t *testing.T) {
	Convey("调用按方法归属路由,与对端声明了哪些方法无关", t, func() {
		h := startTestServer(t)
		// ScriptCat 在测试里声明了协议全部方法(含浏览器方法),路由仍只看方法归属。
		sc := h.connectScriptCat()

		Convey("没有浏览器在线时,浏览器方法不会发给 ScriptCat,而是返回 NO_BROWSER_CONNECTED", func() {
			_, err := h.call(Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNoBrowserConnected)
			sc.alive()
		})

		Convey("浏览器在线时,浏览器方法发给浏览器实例,scripts.* 仍发给 ScriptCat", func() {
			keyA := h.registerBrowser(instanceA, "chrome-0123")
			a := h.connectBrowser(instanceA, keyA, "chrome-0123")

			out := h.goCall(Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			req := a.read()
			So(req.Method, ShouldEqual, "tabs.list")
			a.writeResult(req.ID, json.RawMessage(tabsListResult))
			got := <-out
			So(got.err, ShouldBeNil)
			So(string(got.resp.Result), ShouldEqual, tabsListResult)

			h.scriptsListWorks(sc)
		})

		Convey("scripts.* 调用指定目标浏览器时被拒为 INVALID_REQUEST,不转发", func() {
			keyA := h.registerBrowser(instanceA, "chrome-0123")
			a := h.connectBrowser(instanceA, keyA, "chrome-0123")
			_, err := h.call(Request{Action: "scripts.list", Browser: "chrome-0123", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, CodeInvalidRequest)
			sc.alive()
			a.alive()
		})
	})
}

func TestBrowserTargetNamePrecedesIDPrefix(t *testing.T) {
	Convey("目标先按名称精确匹配:形如 ID 前缀的名称指向该名称的实例", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "0123")

		out := h.goCall(Request{Action: "tabs.open", Browser: "0123", Input: json.RawMessage(`{"url":"https://example.com/"}`)})
		req := b.read()
		So(req.Method, ShouldEqual, "tabs.open")
		b.writeResult(req.ID, json.RawMessage(`{"tabId":1}`))
		So((<-out).err, ShouldBeNil)
		a.alive()
	})
}

func TestMergedCallFailsWhenAnyInstanceFails(t *testing.T) {
	Convey("汇总调用中任一实例回错误时整次调用返回该错误,不返回部分结果", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")

		out := h.goCall(Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
		reqA := a.read()
		reqB := b.read()
		a.writeResult(reqA.ID, json.RawMessage(tabsListResult))
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		So(wsjson.Write(ctx, b.ws, newError(reqB.ID, CodeInternal, "tabs unavailable")), ShouldBeNil)

		got := <-out
		So(got.err, ShouldBeNil)
		So(got.resp.OK, ShouldBeFalse)
		So(got.resp.Error.Code, ShouldEqual, CodeInternal)
		So(got.resp.Result, ShouldBeNil)
	})
}

func TestMergedListSkipsInstancesWithoutTheRequestedWindow(t *testing.T) {
	Convey("未指定目标的列表类调用按窗口过滤时,没有该窗口的实例不贡献条目,整次调用仍汇总其余实例", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()

		Convey("只有一个实例有该窗口:返回它的标签页,并标上来源浏览器", func() {
			out := h.goCall(Request{Action: "tabs.list", Input: json.RawMessage(`{"windowId":7}`)})
			reqA := a.read()
			reqB := b.read()
			So(wsjson.Write(ctx, a.ws, newError(reqA.ID, generated.ErrorCodeNotFound, "no window 7")), ShouldBeNil)
			b.writeResult(reqB.ID, json.RawMessage(`{"tabs":[{"tabId":3,"windowId":7,"active":true,"pinned":false,"title":"t","url":"https://example.com/"}],"contentTrust":"untrusted-page-content"}`))

			got := <-out
			So(got.err, ShouldBeNil)
			So(got.resp.OK, ShouldBeTrue)
			var merged struct {
				Tabs []struct {
					TabID   int `json:"tabId"`
					Browser struct {
						Name string `json:"name"`
					} `json:"browser"`
				} `json:"tabs"`
			}
			So(json.Unmarshal(got.resp.Result, &merged), ShouldBeNil)
			So(merged.Tabs, ShouldHaveLength, 1)
			So(merged.Tabs[0].TabID, ShouldEqual, 3)
			So(merged.Tabs[0].Browser.Name, ShouldEqual, "edge-fedc")
		})

		Convey("没有任何实例有该窗口:返回 NOT_FOUND", func() {
			out := h.goCall(Request{Action: "tabs.list", Input: json.RawMessage(`{"windowId":7}`)})
			reqA := a.read()
			reqB := b.read()
			So(wsjson.Write(ctx, a.ws, newError(reqA.ID, generated.ErrorCodeNotFound, "no window 7")), ShouldBeNil)
			So(wsjson.Write(ctx, b.ws, newError(reqB.ID, generated.ErrorCodeNotFound, "no window 7")), ShouldBeNil)

			got := <-out
			So(got.err, ShouldBeNil)
			So(got.resp.OK, ShouldBeFalse)
			So(got.resp.Error.Code, ShouldEqual, generated.ErrorCodeNotFound)
		})
	})
}
