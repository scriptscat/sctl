package controlapi

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

const (
	instanceA = "0123456789abcdef0123456789abcdef"
	instanceB = "fedcba9876543210fedcba9876543210"
	// instanceC 与 instanceA 共享前缀 "0123",用于前缀歧义。
	instanceC = "0123aaaaaaaaaaaaaaaaaaaaaaaaaaaa"
)

// callTimeout 限定一次控制调用的等待:路由错时请求落到不会应答的对端,测试应失败而不是挂起。
const callTimeout = 3 * time.Second

func (h *testHarness) goCall(req control.CallRequest) <-chan control.CallResult {
	out := make(chan control.CallResult, 1)
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		resp, err := postControl(ctx, h.httpBase(), control.PathCall, testControlToken, "", req)
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

func (h *testHarness) callControl(req control.CallRequest) control.CallResult {
	return <-h.goCall(req)
}

func tabsResult(tabID int) json.RawMessage {
	return json.RawMessage(fmt.Sprintf(`{"tabs":[{"tabId":%d,"windowId":1,"active":true,"pinned":false,"title":"t","url":"https://example.com/"}],"contentTrust":"untrusted-page-content"}`, tabID))
}

// answer 读取发到 e 的下一条业务请求,断言方法后以 result 应答。
func (e *extClient) answer(method string, result json.RawMessage) {
	req := e.read()
	So(req.Method, ShouldEqual, method)
	e.writeResult(req.ID, result)
}

// idle 证明 e 没有收到任何业务请求:对端自发 ping 的应答是它读到的下一条消息。
func (e *extClient) idle() {
	id := "idle-probe"
	e.writeRequest("$session.ping", id, struct{}{})
	So(e.read().ID, ShouldEqual, id)
}

func errCode(res control.CallResult) string {
	if res.Error == nil {
		return ""
	}
	return res.Error.Code
}

type mergedTabs struct {
	Tabs []struct {
		TabID   int `json:"tabId"`
		Browser struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		} `json:"browser"`
	} `json:"tabs"`
	ContentTrust string `json:"contentTrust"`
}

func TestBrowserTargetSelection(t *testing.T) {
	Convey("浏览器方法按在线实例数与目标选择路由", t, func() {
		h := startTestServer(t)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		// ScriptCat 声明了协议全部方法,浏览器方法也不会发给它。
		sc := h.doSessionHandshake(key)
		h.pairedBrowser(instanceC, "work")

		Convey("没有浏览器在线:列表类与操作类都返回 NO_BROWSER_CONNECTED", func() {
			So(errCode(h.callControl(control.CallRequest{Action: "tabs.list", Input: json.RawMessage(`{}`)})), ShouldEqual, generated.ErrorCodeNoBrowserConnected)
			So(errCode(h.callControl(control.CallRequest{Action: "tabs.open", Input: json.RawMessage(`{"url":"https://example.com/"}`)})), ShouldEqual, generated.ErrorCodeNoBrowserConnected)
			sc.idle()
		})

		Convey("恰好一个在线:列表类与操作类都使用它,结果原样返回", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")

			ch := h.goCall(control.CallRequest{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			a.answer("tabs.list", tabsResult(1))
			res := <-ch
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, string(tabsResult(1)))

			ch = h.goCall(control.CallRequest{Action: "tabs.open", Input: json.RawMessage(`{"url":"https://example.com/"}`)})
			a.answer("tabs.open", json.RawMessage(`{"tabId":7}`))
			res = <-ch
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"tabId":7}`)
			sc.idle()
		})

		Convey("多个在线", func() {
			a := h.connectBrowser(instanceA, "chrome-0123")
			b := h.connectBrowser(instanceB, "edge-fedc")

			Convey("未指定目标的列表类调用汇总所有在线浏览器,每项带浏览器名称与 ID", func() {
				ch := h.goCall(control.CallRequest{Action: "tabs.list", Input: json.RawMessage(`{}`)})
				b.answer("tabs.list", tabsResult(2))
				a.answer("tabs.list", tabsResult(1))
				res := <-ch
				So(res.OK, ShouldBeTrue)
				var merged mergedTabs
				So(json.Unmarshal(res.Result, &merged), ShouldBeNil)
				So(merged.ContentTrust, ShouldEqual, "untrusted-page-content")
				So(merged.Tabs, ShouldHaveLength, 2)
				So(merged.Tabs[0].TabID, ShouldEqual, 1)
				So(merged.Tabs[0].Browser.Name, ShouldEqual, "chrome-0123")
				So(merged.Tabs[0].Browser.ID, ShouldEqual, instanceA)
				So(merged.Tabs[1].TabID, ShouldEqual, 2)
				So(merged.Tabs[1].Browser.Name, ShouldEqual, "edge-fedc")
				So(merged.Tabs[1].Browser.ID, ShouldEqual, instanceB)
			})

			Convey("汇总按方法声明的合并字段进行,windows.list 同样汇总", func() {
				ch := h.goCall(control.CallRequest{Action: "windows.list", Input: json.RawMessage(`{}`)})
				a.answer("windows.list", json.RawMessage(`{"windows":[{"windowId":1,"focused":true,"state":"normal","tabCount":3}]}`))
				b.answer("windows.list", json.RawMessage(`{"windows":[{"windowId":9,"focused":false,"state":"minimized","tabCount":1},{"windowId":10,"focused":false,"state":"normal","tabCount":2}]}`))
				res := <-ch
				So(res.OK, ShouldBeTrue)
				var merged struct {
					Windows []struct {
						WindowID int `json:"windowId"`
						Browser  struct {
							Name string `json:"name"`
						} `json:"browser"`
					} `json:"windows"`
				}
				So(json.Unmarshal(res.Result, &merged), ShouldBeNil)
				So(merged.Windows, ShouldHaveLength, 3)
				So(merged.Windows[0].Browser.Name, ShouldEqual, "chrome-0123")
				So(merged.Windows[2].WindowID, ShouldEqual, 10)
				So(merged.Windows[2].Browser.Name, ShouldEqual, "edge-fedc")
			})

			Convey("readingList.list 按 entries 汇总,任一浏览器还有未返回的条目时 hasMore 为 true", func() {
				ch := h.goCall(control.CallRequest{Action: "readingList.list", Input: json.RawMessage(`{"limit":1}`)})
				a.answer("readingList.list", json.RawMessage(`{"contentTrust":"untrusted-page-content","hasMore":false,"entries":[{"url":"https://a.example/","title":"A","read":false,"createdAt":1,"updatedAt":2}]}`))
				b.answer("readingList.list", json.RawMessage(`{"contentTrust":"untrusted-page-content","hasMore":true,"entries":[{"url":"https://b.example/","title":"B","read":true,"createdAt":3,"updatedAt":4}]}`))
				res := <-ch
				So(res.OK, ShouldBeTrue)
				var merged struct {
					HasMore bool `json:"hasMore"`
					Entries []struct {
						URL     string `json:"url"`
						Browser struct {
							Name string `json:"name"`
						} `json:"browser"`
					} `json:"entries"`
				}
				So(json.Unmarshal(res.Result, &merged), ShouldBeNil)
				So(merged.HasMore, ShouldBeTrue)
				So(merged.Entries, ShouldHaveLength, 2)
				So(merged.Entries[0].URL, ShouldEqual, "https://a.example/")
				So(merged.Entries[0].Browser.Name, ShouldEqual, "chrome-0123")
				So(merged.Entries[1].Browser.Name, ShouldEqual, "edge-fedc")
			})

			Convey("未指定目标的操作类调用返回 BROWSER_AMBIGUOUS 并列出在线候选,不转发", func() {
				res := h.callControl(control.CallRequest{Action: "tabs.open", Input: json.RawMessage(`{"url":"https://example.com/"}`)})
				So(errCode(res), ShouldEqual, generated.ErrorCodeBrowserAmbiguous)
				So(res.Error.Message, ShouldContainSubstring, "chrome-0123")
				So(res.Error.Message, ShouldContainSubstring, instanceA)
				So(res.Error.Message, ShouldContainSubstring, "edge-fedc")
				So(res.Error.Message, ShouldContainSubstring, instanceB)
				So(res.Error.Message, ShouldNotContainSubstring, "work")
				a.idle()
				b.idle()
			})

			Convey("按名称指定目标的列表类调用只发给目标,结果原样返回", func() {
				ch := h.goCall(control.CallRequest{Action: "tabs.list", Browser: "edge-fedc", Input: json.RawMessage(`{}`)})
				b.answer("tabs.list", tabsResult(2))
				res := <-ch
				So(res.OK, ShouldBeTrue)
				So(string(res.Result), ShouldEqual, string(tabsResult(2)))
				a.idle()
			})

			Convey("按实例 ID 前缀指定目标的操作类调用发给该实例", func() {
				ch := h.goCall(control.CallRequest{Action: "tabs.open", Browser: "fed", Input: json.RawMessage(`{"url":"https://example.com/"}`)})
				b.answer("tabs.open", json.RawMessage(`{"tabId":3}`))
				So((<-ch).OK, ShouldBeTrue)
				a.idle()
			})

			Convey("完整实例 ID 指定目标", func() {
				ch := h.goCall(control.CallRequest{Action: "tabs.open", Browser: instanceA, Input: json.RawMessage(`{"url":"https://example.com/"}`)})
				a.answer("tabs.open", json.RawMessage(`{"tabId":4}`))
				So((<-ch).OK, ShouldBeTrue)
				b.idle()
			})

			Convey("目标是已配对但离线的实例:BROWSER_OFFLINE", func() {
				res := h.callControl(control.CallRequest{Action: "tabs.list", Browser: "work", Input: json.RawMessage(`{}`)})
				So(errCode(res), ShouldEqual, generated.ErrorCodeBrowserOffline)
				So(res.Error.Message, ShouldContainSubstring, "work")
			})

			Convey("目标不对应任何已配对实例:BROWSER_NOT_FOUND", func() {
				So(errCode(h.callControl(control.CallRequest{Action: "tabs.open", Browser: "nope", Input: json.RawMessage(`{"url":"https://example.com/"}`)})), ShouldEqual, generated.ErrorCodeBrowserNotFound)
				a.idle()
				b.idle()
			})

			Convey("实例 ID 前缀匹配多个已配对实例:BROWSER_AMBIGUOUS 并列出匹配的实例", func() {
				res := h.callControl(control.CallRequest{Action: "tabs.list", Browser: "0123", Input: json.RawMessage(`{}`)})
				So(errCode(res), ShouldEqual, generated.ErrorCodeBrowserAmbiguous)
				So(res.Error.Message, ShouldContainSubstring, instanceA)
				So(res.Error.Message, ShouldContainSubstring, instanceC)
				So(res.Error.Message, ShouldNotContainSubstring, instanceB)
				a.idle()
			})
		})
	})
}

func TestBrowserDisconnectVoidsCall(t *testing.T) {
	Convey("调用进行中目标浏览器断开 → OPERATION_EXPIRED", t, func() {
		h := startTestServer(t)
		a := h.connectBrowser(instanceA, "chrome-0123")

		Convey("单个目标", func() {
			ch := h.goCall(control.CallRequest{Action: "tabs.open", Input: json.RawMessage(`{"url":"https://example.com/"}`)})
			So(a.read().Method, ShouldEqual, "tabs.open")
			a.ws.CloseNow()
			So(errCode(<-ch), ShouldEqual, generated.ErrorCodeOperationExpired)
		})

		Convey("汇总中的一个实例断开时整次调用作废", func() {
			b := h.connectBrowser(instanceB, "edge-fedc")
			ch := h.goCall(control.CallRequest{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			a.answer("tabs.list", tabsResult(1))
			So(b.read().Method, ShouldEqual, "tabs.list")
			b.ws.CloseNow()
			So(errCode(<-ch), ShouldEqual, generated.ErrorCodeOperationExpired)
		})
	})
}

func TestScriptsRoutingUnchangedWithBrowsers(t *testing.T) {
	Convey("scripts.* 路由与既有错误不受浏览器实例影响", t, func() {
		h := startTestServer(t)
		a := h.connectBrowser(instanceA, "chrome-0123")

		Convey("ScriptCat 未连接时 scripts.* 仍返回 INTERNAL_ERROR,不发给浏览器", func() {
			res := h.callControl(control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			So(errCode(res), ShouldEqual, generated.ErrorCodeInternalError)
			a.idle()
		})

		Convey("scripts.* 指定目标浏览器被拒为 INVALID_REQUEST", func() {
			key, err := newKeyAndSave(h)
			So(err, ShouldBeNil)
			sc := h.doSessionHandshake(key)
			res := h.callControl(control.CallRequest{Action: "scripts.list", Browser: "chrome-0123", Input: json.RawMessage(`{}`)})
			So(errCode(res), ShouldEqual, generated.ErrorCodeInvalidRequest)
			sc.idle()
			a.idle()
		})
	})
}
