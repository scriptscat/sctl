package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
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
			b.writeResult(reqB.ID, json.RawMessage(`{"tabs":[{"tabId":3,"windowId":7,"active":true,"pinned":false,"groupId":-1,"title":"t","url":"https://example.com/"}],"contentTrust":"untrusted-page-content"}`))

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

// instanceC 与 instanceA 共享 ID 前缀 "0123",用于前缀歧义。
const instanceC = "0123aaaaaaaaaaaaaaaaaaaaaaaaaaaa"

// scriptsListResult 是一个 ScriptCat 实例对 scripts.list 的应答,列出给定 uuid 的脚本。
func scriptsListResult(uuids ...string) json.RawMessage {
	items := make([]string, len(uuids))
	for i, u := range uuids {
		items[i] = `{"uuid":"` + u + `","name":"s","namespace":"ns","type":"normal","enabled":true,"updatedAt":"2026-11-02T00:00:00Z","hasUpdateUrl":false}`
	}
	return json.RawMessage(`{"scripts":[` + strings.Join(items, ",") + `],"contentTrust":"untrusted-user-script-metadata"}`)
}

// answer 读取发到 e 的下一条业务请求,断言方法后以 result 应答。
func (e *extClient) answer(method string, result json.RawMessage) {
	req := e.read()
	So(req.Method, ShouldEqual, method)
	e.writeResult(req.ID, result)
}

// answerNotFound 读取发到 e 的下一条业务请求,断言方法后以 NOT_FOUND 应答。
func (e *extClient) answerNotFound(method string) {
	req := e.read()
	So(req.Method, ShouldEqual, method)
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	So(wsjson.Write(ctx, e.ws, newError(req.ID, generated.ErrorCodeNotFound, "no such script")), ShouldBeNil)
}

// sourceTag 是汇总列表里每一项附带的来源实例;ID 用指针区分「没有 id 字段」与空串。
type sourceTag struct {
	ID   *string `json:"id"`
	Name string  `json:"name"`
}

type mergedScripts struct {
	Scripts []struct {
		UUID      string    `json:"uuid"`
		ScriptCat sourceTag `json:"scriptcat"`
	} `json:"scripts"`
	ContentTrust string `json:"contentTrust"`
}

const metadataInput = `{"uuid":"00000000-0000-4000-8000-000000000000"}`

func TestScriptCatTargetSelection(t *testing.T) {
	Convey("scripts.* 调用按在线 ScriptCat 实例数与 ScriptCat 目标路由", t, func() {
		h := startTestServer(t)
		keyA := h.registerScriptCat(instanceA, "work")
		keyB := h.registerScriptCat(instanceB, "home")
		h.registerScriptCat(instanceC, "spare")

		Convey("没有实例在线时,指定已配对实例为目标:SCRIPTCAT_OFFLINE", func() {
			_, err := h.call(Request{Action: "scripts.list", ScriptCat: "work", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatOffline)
		})

		Convey("恰好一个在线:未指定目标的操作类调用发给它", func() {
			a := h.connectScriptCatInstance(instanceA, keyA, "work")
			out := h.goCall(Request{Action: "scripts.metadata.get", Input: json.RawMessage(metadataInput)})
			a.answerNotFound("scripts.metadata.get")
			got := <-out
			So(got.err, ShouldBeNil)
			So(got.resp.Error.Code, ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("多个在线", func() {
			a := h.connectScriptCatInstance(instanceA, keyA, "work")
			b := h.connectScriptCatInstance(instanceB, keyB, "home")

			Convey("未指定目标的 scripts.list 汇总全部在线实例:按实例名称排序,每项标上来源实例", func() {
				out := h.goCall(Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
				a.answer("scripts.list", scriptsListResult("from-work"))
				b.answer("scripts.list", scriptsListResult("from-home-1", "from-home-2"))
				got := <-out
				So(got.err, ShouldBeNil)
				So(got.resp.OK, ShouldBeTrue)
				var merged mergedScripts
				So(json.Unmarshal(got.resp.Result, &merged), ShouldBeNil)
				So(merged.ContentTrust, ShouldEqual, "untrusted-user-script-metadata")
				So(merged.Scripts, ShouldHaveLength, 3)
				So(merged.Scripts[0].UUID, ShouldEqual, "from-home-1")
				So(merged.Scripts[1].UUID, ShouldEqual, "from-home-2")
				So(merged.Scripts[2].UUID, ShouldEqual, "from-work")
				So(merged.Scripts[0].ScriptCat.Name, ShouldEqual, "home")
				So(*merged.Scripts[0].ScriptCat.ID, ShouldEqual, instanceB)
				So(merged.Scripts[2].ScriptCat.Name, ShouldEqual, "work")
				So(*merged.Scripts[2].ScriptCat.ID, ShouldEqual, instanceA)
			})

			Convey("未指定目标的其他 scripts.* 调用:SCRIPTCAT_AMBIGUOUS 并只列出在线实例,不转发", func() {
				_, err := h.call(Request{Action: "scripts.metadata.get", Input: json.RawMessage(metadataInput)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatAmbiguous)
				So(err.Error(), ShouldContainSubstring, "work ("+instanceA+")")
				So(err.Error(), ShouldContainSubstring, "home ("+instanceB+")")
				So(err.Error(), ShouldNotContainSubstring, "spare")
				a.alive()
				b.alive()
			})

			Convey("按名称指定目标:只发给该实例,结果原样返回", func() {
				out := h.goCall(Request{Action: "scripts.list", ScriptCat: "home", Input: json.RawMessage(`{}`)})
				b.answer("scripts.list", scriptsListResult("from-home"))
				got := <-out
				So(got.err, ShouldBeNil)
				So(string(got.resp.Result), ShouldEqual, string(scriptsListResult("from-home")))
				a.alive()
			})

			Convey("按实例 ID 前缀指定目标的操作类调用发给该实例", func() {
				out := h.goCall(Request{Action: "scripts.metadata.get", ScriptCat: "fed", Input: json.RawMessage(metadataInput)})
				b.answerNotFound("scripts.metadata.get")
				So((<-out).resp.Error.Code, ShouldEqual, generated.ErrorCodeNotFound)
				a.alive()
			})

			Convey("汇总调用中一个实例断开:整次调用作废", func() {
				out := h.goCall(Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
				a.answer("scripts.list", scriptsListResult("from-work"))
				So(b.read().Method, ShouldEqual, "scripts.list")
				b.ws.CloseNow()
				So((<-out).err, ShouldEqual, ErrDisconnected)
			})

			Convey("目标不匹配任何已配对实例:SCRIPTCAT_NOT_FOUND", func() {
				_, err := h.call(Request{Action: "scripts.list", ScriptCat: "nope", Input: json.RawMessage(`{}`)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatNotFound)
				a.alive()
				b.alive()
			})

			Convey("实例 ID 前缀匹配多个已配对实例:SCRIPTCAT_AMBIGUOUS 并列出匹配的实例", func() {
				_, err := h.call(Request{Action: "scripts.list", ScriptCat: "0123", Input: json.RawMessage(`{}`)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatAmbiguous)
				So(err.Error(), ShouldContainSubstring, instanceA)
				So(err.Error(), ShouldContainSubstring, instanceC)
				So(err.Error(), ShouldNotContainSubstring, instanceB)
			})

			Convey("目标是已配对但离线的实例:SCRIPTCAT_OFFLINE", func() {
				_, err := h.call(Request{Action: "scripts.list", ScriptCat: "spare", Input: json.RawMessage(`{}`)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatOffline)
				So(err.Error(), ShouldContainSubstring, "spare")
			})

			Convey("名称精确匹配优先于 ID 前缀", func() {
				keyNamed := h.registerScriptCat("abcdef0123456789abcdef0123456789", "fed")
				named := h.connectScriptCatInstance("abcdef0123456789abcdef0123456789", keyNamed, "fed")
				out := h.goCall(Request{Action: "scripts.list", ScriptCat: "fed", Input: json.RawMessage(`{}`)})
				named.answer("scripts.list", scriptsListResult())
				So((<-out).err, ShouldBeNil)
				b.alive()
			})
		})
	})
}

func TestLegacyScriptCatTargetSelection(t *testing.T) {
	Convey("老版 ScriptCat 只以保留名 scriptcat 被选中,不参与 ID 前缀匹配", t, func() {
		h := startTestServer(t)
		keyA := h.registerScriptCat(instanceA, "work")

		Convey("没有 pairing.key 时目标 scriptcat 不对应任何实例:SCRIPTCAT_NOT_FOUND", func() {
			_, err := h.call(Request{Action: "scripts.list", ScriptCat: "scriptcat", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatNotFound)
		})

		Convey("pairing.key 存在而老版未连接时目标 scriptcat:SCRIPTCAT_OFFLINE", func() {
			key, _ := auth.NewLongTermKey()
			So(h.keys.Save(key), ShouldBeNil)
			_, err := h.call(Request{Action: "scripts.list", ScriptCat: "scriptcat", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatOffline)
		})

		Convey("老版与新版实例同时在线", func() {
			legacy := h.connectScriptCat()
			a := h.connectScriptCatInstance(instanceA, keyA, "work")

			Convey("目标 scriptcat 发给老版连接", func() {
				out := h.goCall(Request{Action: "scripts.list", ScriptCat: "scriptcat", Input: json.RawMessage(`{}`)})
				legacy.answer("scripts.list", scriptsListResult("legacy"))
				So((<-out).err, ShouldBeNil)
				a.alive()
			})

			Convey("任何 ID 前缀都不会选中老版连接", func() {
				_, err := h.call(Request{Action: "scripts.list", ScriptCat: "s", Input: json.RawMessage(`{}`)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatNotFound)
				legacy.alive()
			})

			Convey("未指定目标的操作类调用:SCRIPTCAT_AMBIGUOUS 列出老版与新版实例", func() {
				_, err := h.call(Request{Action: "scripts.metadata.get", Input: json.RawMessage(metadataInput)})
				So(errorCode(err), ShouldEqual, generated.ErrorCodeScriptcatAmbiguous)
				So(err.Error(), ShouldContainSubstring, "scriptcat")
				So(err.Error(), ShouldContainSubstring, "work ("+instanceA+")")
			})

			Convey("未指定目标的 scripts.list 汇总两者,老版条目的来源只有名称没有 id", func() {
				out := h.goCall(Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
				legacy.answer("scripts.list", scriptsListResult("legacy"))
				a.answer("scripts.list", scriptsListResult("from-work"))
				got := <-out
				So(got.err, ShouldBeNil)
				var merged mergedScripts
				So(json.Unmarshal(got.resp.Result, &merged), ShouldBeNil)
				So(merged.Scripts, ShouldHaveLength, 2)
				So(merged.Scripts[0].UUID, ShouldEqual, "legacy")
				So(merged.Scripts[0].ScriptCat.Name, ShouldEqual, "scriptcat")
				So(merged.Scripts[0].ScriptCat.ID, ShouldBeNil)
				So(merged.Scripts[1].ScriptCat.Name, ShouldEqual, "work")
			})
		})
	})
}

func TestTargetsOfTheOtherPeerKindAreRejected(t *testing.T) {
	Convey("浏览器方法带 ScriptCat 目标被拒为 INVALID_REQUEST,不转发", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		keyS := h.registerScriptCat(instanceB, "work")
		sc := h.connectScriptCatInstance(instanceB, keyS, "work")
		_, err := h.call(Request{Action: "tabs.list", ScriptCat: "work", Input: json.RawMessage(`{}`)})
		So(errorCode(err), ShouldEqual, CodeInvalidRequest)
		a.alive()
		sc.alive()
	})
}
