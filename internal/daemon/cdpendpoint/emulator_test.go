package cdpendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// 以下请求序列取自探针里真实客户端的握手(.dev-kit/artifacts/2026-10-08-raw-cdp/probe/out/*-focus-mux/relay.log),
// 参数里过长的部分换成了同样形状的短值。

// playwrightPageInit 是 Playwright 1.6x 附加一个页面后在它的会话上连发的初始化命令。
var playwrightPageInit = [][2]string{
	{"Page.enable", `{}`},
	{"Page.getFrameTree", `{}`},
	{"Log.enable", `{}`},
	{"Page.setLifecycleEventsEnabled", `{"enabled":true}`},
	{"Runtime.enable", `{}`},
	{"Page.addScriptToEvaluateOnNewDocument", `{"source":"","worldName":"__playwright_utility_world_page@f67ce691"}`},
	{"Network.enable", `{}`},
	{"Target.setAutoAttach", `{"autoAttach":true,"waitForDebuggerOnStart":true,"flatten":true}`},
	{"Emulation.setFocusEmulationEnabled", `{"enabled":true}`},
	{"Page.setFontFamilies", `{"fontFamilies":{"standard":"Times","fixed":"Courier"}}`},
	{"Emulation.setEmulatedMedia", `{"media":"","features":[{"name":"prefers-color-scheme","value":"light"}]}`},
	{"Inspector.enable", `{}`},
	{"Runtime.runIfWaitingForDebugger", `{}`},
}

// puppeteerPageInit 是 Puppeteer 25 附加一个页面后在它的会话上连发的初始化命令。
var puppeteerPageInit = [][2]string{
	{"Target.setAutoAttach", `{"waitForDebuggerOnStart":true,"flatten":true,"autoAttach":true,"filter":[{}]}`},
	{"Runtime.runIfWaitingForDebugger", `{}`},
	{"Network.enable", `{}`},
	{"Page.enable", `{}`},
	{"Page.getFrameTree", `{}`},
	{"Page.setLifecycleEventsEnabled", `{"enabled":true}`},
	{"Runtime.enable", `{}`},
	{"Audits.enable", `{}`},
	{"Performance.enable", `{}`},
	{"Log.enable", `{}`},
	{"WebMCP.enable", `{}`},
}

// sendAll 在会话上连发 cmds,不等应答,返回各自的 id。
func (c *cdpTestClient) sendAll(sessionID string, cmds [][2]string) []int {
	ids := make([]int, len(cmds))
	for i, cmd := range cmds {
		ids[i] = c.send(sessionID, cmd[0], cmd[1])
	}
	return ids
}

// expectForwardedInOrder 断言 cmds 按发出的顺序到达了标签页 tab 的会话,而且之前已经开了焦点模拟。
func expectForwardedInOrder(h *harness, tab int, sessionID string, cmds [][2]string) {
	prefix := fmt.Sprintf("debugger.send %d ", tab)
	if sessionID != "" {
		prefix += sessionID + " "
	}
	last := cmds[len(cmds)-1]
	So(h.log.waitFor(prefix+last[0]+" "+last[1], 1), ShouldBeTrue)
	var want []string
	for _, cmd := range cmds {
		want = append(want, prefix+cmd[0]+" "+cmd[1])
	}
	// 从客户端的第一条命令起看:之前那条是端点自己开的焦点模拟。
	sent := sentOrder(h.log, prefix)
	var got []string
	for _, e := range sent[max(indexOf(sent, want[0]), 0):] {
		if indexOf(want, e) >= 0 {
			got = append(got, e)
		}
	}
	So(got, ShouldResemble, want)
}

// expectFocusFirst 断言端点在客户端的第一条命令之前为标签页开了焦点模拟。
func expectFocusFirst(h *harness, tab int, firstClientCommand string) {
	sent := sentOrder(h.log, fmt.Sprintf("debugger.send %d ", tab))
	focus := indexOf(sent, fmt.Sprintf(`debugger.send %d Emulation.setFocusEmulationEnabled {"enabled":true}`, tab))
	So(focus, ShouldBeGreaterThanOrEqualTo, 0)
	So(focus, ShouldBeLessThan, indexOf(sent, fmt.Sprintf("debugger.send %d %s", tab, firstClientCommand)))
}

func TestPlaywrightHandshake(t *testing.T) {
	Convey("回放 Playwright connectOverCDP 的握手:看到两个已打开的页面、会话命令按序转发、事件带对的会话", t, func() {
		h, _ := newEmulatorHarness(t, tabFive, tabSix)
		info := h.create(t)

		status, body := h.get(t, info.HTTPURL+"/json/version/", "", "")
		So(status, ShouldEqual, 200)
		var version struct {
			WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
		}
		So(json.Unmarshal([]byte(body), &version), ShouldBeNil)
		c := dialCDP(t, version.WebSocketDebuggerURL)

		getVersion := c.send("", "Browser.getVersion", `{}`)
		autoAttach := c.send("", "Target.setAutoAttach", `{"autoAttach":true,"waitForDebuggerOnStart":true,"flatten":true}`)
		download := c.send("", "Browser.setDownloadBehavior", `{"behavior":"allowAndName","downloadPath":"/tmp/playwright-artifacts","eventsEnabled":true}`)

		v, _ := c.response(getVersion)
		So(v.Error, ShouldBeNil)
		So(string(v.Result), ShouldEqualJSON, `{"protocolVersion":"1.3","product":"Chrome/125.0.6422.141","revision":"","userAgent":"`+testUserAgent+`","jsVersion":""}`)

		aa, aaAt := c.response(autoAttach)
		So(aa.Error, ShouldBeNil)
		So(string(aa.Result), ShouldEqualJSON, `{}`)
		// Chrome 在应答之前报告已有的目标;Playwright 据此在 connectOverCDP 返回前就列出这些页面。
		attached := c.events("Target.attachedToTarget", aaAt)
		So(len(attached), ShouldEqual, 2)
		sessions := attachedSessions(attached)
		So(sessions, ShouldContainKey, "T5")
		So(sessions, ShouldContainKey, "T6")
		So(sessions["T5"], ShouldNotEqual, sessions["T6"])
		for _, ev := range attached {
			So(ev.SessionID, ShouldBeEmpty)
			So(paramOf(ev, "waitingForDebugger"), ShouldEqual, false)
			ti := targetInfoOf(ev)
			So(ti["type"], ShouldEqual, "page")
			So(ti["attached"], ShouldEqual, true)
			So(ti["browserContextId"], ShouldEqual, "CTX1")
		}

		dl, _ := c.response(download)
		So(dl.Error, ShouldBeNil)
		So(string(dl.Result), ShouldEqualJSON, `{}`)

		s5, s6 := sessions["T5"], sessions["T6"]
		ids5 := c.sendAll(s5, playwrightPageInit)
		ids6 := c.sendAll(s6, playwrightPageInit)
		browserInfo := c.send("", "Target.getTargetInfo", `{}`)
		for _, id := range append(ids5, ids6...) {
			r, _ := c.response(id)
			So(r.Error, ShouldBeNil)
		}
		r, _ := c.response(ids5[0])
		So(r.SessionID, ShouldEqual, s5)
		expectForwardedInOrder(h, 5, "", playwrightPageInit)
		expectForwardedInOrder(h, 6, "", playwrightPageInit)
		expectFocusFirst(h, 5, "Page.enable {}")
		expectFocusFirst(h, 6, "Page.enable {}")
		So(h.log.count("debugger.own 5 true"), ShouldEqual, 1)

		bi, _ := c.response(browserInfo)
		So(bi.Error, ShouldBeNil)
		So(string(bi.Result), ShouldEqualJSON, `{"targetInfo":{"targetId":"`+info.WSURL[strings.LastIndex(info.WSURL, "/")+1:]+`","type":"browser","title":"","url":"","attached":true,"canAccessOpener":false}}`)
		So(h.log.count("debugger.send 5 Browser.setDownloadBehavior"), ShouldEqual, 0)

		// 页面事件转回附加它的会话;跨进程 iframe 的子会话 ID 原样透传,命令带着它发给同一个标签页。
		h.chromeEvent(6, "", "Page.frameNavigated", `{"frame":{"id":"T6","url":"http://127.0.0.1:8711/other.html"}}`)
		ev, _ := c.event("Page.frameNavigated", nil)
		So(ev.SessionID, ShouldEqual, s6)
		So(string(ev.Params), ShouldEqualJSON, `{"frame":{"id":"T6","url":"http://127.0.0.1:8711/other.html"}}`)

		child := "231B374CA23B1DBFF99983733DB2D90F"
		h.chromeEvent(5, "", "Target.attachedToTarget", `{"sessionId":"`+child+`","targetInfo":{"targetId":"AA7F","type":"iframe","title":"","url":"","attached":true,"canAccessOpener":false},"waitingForDebugger":true}`)
		childAttached, _ := c.event("Target.attachedToTarget", func(m cdpMessage) bool { return m.SessionID == s5 })
		So(paramOf(childAttached, "sessionId"), ShouldEqual, child)
		So(paramOf(childAttached, "waitingForDebugger"), ShouldEqual, true)
		childIDs := c.sendAll(child, playwrightPageInit[:5])
		for _, id := range childIDs {
			r, _ := c.response(id)
			So(r.Error, ShouldBeNil)
			So(r.SessionID, ShouldEqual, child)
		}
		expectForwardedInOrder(h, 5, child, playwrightPageInit[:5])
		h.chromeEvent(5, child, "Runtime.executionContextCreated", `{"context":{"id":3,"origin":"http://localhost:8711"}}`)
		childEvent, _ := c.event("Runtime.executionContextCreated", nil)
		So(childEvent.SessionID, ShouldEqual, child)
		So(string(childEvent.Params), ShouldEqualJSON, `{"context":{"id":3,"origin":"http://localhost:8711"}}`)
	})
}

func TestPuppeteerHandshake(t *testing.T) {
	Convey("回放 Puppeteer connect 的握手:先发现后自动附加,页面会话命令转发,Chrome 不认识的命令带 Chrome 的错误", t, func() {
		h, _ := newEmulatorHarness(t, tabFive, tabSix)
		info := h.create(t)
		c := dialCDP(t, info.WSURL)

		contexts := c.call("", "Target.getBrowserContexts", `{}`)
		So(contexts.Error, ShouldBeNil)
		So(string(contexts.Result), ShouldEqualJSON, `{"browserContextIds":[]}`)

		discover, discoverAt := c.response(c.send("", "Target.setDiscoverTargets", `{"discover":true,"filter":[{}]}`))
		So(discover.Error, ShouldBeNil)
		created := c.events("Target.targetCreated", discoverAt)
		So(len(created), ShouldEqual, 2)
		So(targetInfoOf(created[0])["type"], ShouldEqual, "page")
		So(targetInfoOf(created[0])["attached"], ShouldEqual, false)
		So(c.events("Target.attachedToTarget", -1), ShouldBeEmpty)

		aa, aaAt := c.response(c.send("", "Target.setAutoAttach", `{"waitForDebuggerOnStart":true,"flatten":true,"autoAttach":true,"filter":[{"type":"page","exclude":true},{}]}`))
		So(aa.Error, ShouldBeNil)
		attached := c.events("Target.attachedToTarget", aaAt)
		So(len(attached), ShouldEqual, 2)
		sessions := attachedSessions(attached)
		// 已经报告过的目标不再报告一次。
		So(len(c.events("Target.targetCreated", -1)), ShouldEqual, 2)

		for _, tab := range []struct {
			id      int
			session string
		}{{5, sessions["T5"]}, {6, sessions["T6"]}} {
			ids := c.sendAll(tab.session, puppeteerPageInit)
			for i, id := range ids {
				r, _ := c.response(id)
				if puppeteerPageInit[i][0] == "WebMCP.enable" {
					So(r.Error, ShouldResemble, &cdpError{Code: -32601, Message: "'WebMCP.enable' wasn't found"})
					continue
				}
				So(r.Error, ShouldBeNil)
			}
			expectForwardedInOrder(h, tab.id, "", puppeteerPageInit)
			expectFocusFirst(h, tab.id, puppeteerPageInit[0][0]+" "+puppeteerPageInit[0][1])
		}

		v := c.call("", "Browser.getVersion", `{}`)
		So(v.Error, ShouldBeNil)
		var version map[string]any
		So(json.Unmarshal(v.Result, &version), ShouldBeNil)
		So(version["product"], ShouldEqual, "Chrome/125.0.6422.141")
	})
}

// connectAutoAttached 像 Playwright 与 Puppeteer 一样连上:先发现、再自动附加,返回客户端与 targetId → sessionId。
func connectAutoAttached(t *testing.T, h *harness) (*cdpTestClient, map[string]string) {
	t.Helper()
	info := h.create(t)
	c := dialCDP(t, info.WSURL)
	if r := c.call("", "Target.setDiscoverTargets", `{"discover":true}`); r.Error != nil {
		t.Fatalf("setDiscoverTargets: %+v", r.Error)
	}
	_, at := c.response(c.send("", "Target.setAutoAttach", `{"autoAttach":true,"waitForDebuggerOnStart":true,"flatten":true}`))
	return c, attachedSessions(c.events("Target.attachedToTarget", at))
}

func TestBrowserCommands(t *testing.T) {
	Convey("Browser.close 只断开客户端:先回答,再以 1000 关闭连接,标签页都保留", t, func() {
		h, _ := newEmulatorHarness(t, tabFive, tabSix)
		c, _ := connectAutoAttached(t, h)
		r := c.call("", "Browser.close", `{}`)
		So(r.Error, ShouldBeNil)
		<-c.done
		So(websocket.CloseStatus(c.readErr), ShouldEqual, websocket.StatusNormalClosure)
		So(h.log.waitFor("reclaim inst-work", 1), ShouldBeTrue)
		So(h.log.count("debugger.close 5"), ShouldEqual, 0)
		So(h.log.count("debugger.close 6"), ShouldEqual, 0)
		So(h.log.count("debugger.detach 5"), ShouldEqual, 1)
		So(h.log.count("debugger.detach 6"), ShouldEqual, 1)
	})

	Convey("浏览器读不出 User-Agent 时 Browser.getVersion 返回 CDP 错误,连接照常", t, func() {
		h, _ := newEmulatorHarness(t, tabFive)
		h.bridge.fail[generated.MethodDebuggerUserAgent] = bridge.Error{Code: generated.ErrorCodeInternalError, Message: "boom"}
		info := h.create(t)
		c := dialCDP(t, info.WSURL)
		r := c.call("", "Browser.getVersion", `{}`)
		So(r.Error, ShouldResemble, &cdpError{Code: -32000, Message: "INTERNAL_ERROR: boom"})
		So(c.call("", "Target.getBrowserContexts", `{}`).Error, ShouldBeNil)
	})

	Convey("product 取自 User-Agent:headless 时是 HeadlessChrome,认不出时是 Chrome", t, func() {
		So(chromeProduct("Mozilla/5.0 (Macintosh) AppleWebKit/537.36 (KHTML, like Gecko) HeadlessChrome/125.0.0.0 Safari/537.36"), ShouldEqual, "HeadlessChrome/125.0.0.0")
		So(chromeProduct("Mozilla/5.0 Unknown"), ShouldEqual, "Chrome")
	})
}

func TestTargetCommands(t *testing.T) {
	Convey("根会话上的目标命令", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive, tabSix)
		c, sessions := connectAutoAttached(t, h)

		Convey("createTarget 在这个浏览器里打开标签页,应答之前报告它被创建并已附加;随后扩展的 tabCreated 不重复报告", func() {
			chrome.addTab(generated.DebuggerTabNotification{TabId: 100, TargetId: "T100", Title: "", URL: "about:blank"})
			r, at := c.response(c.send("", "Target.createTarget", `{"url":"about:blank"}`))
			So(r.Error, ShouldBeNil)
			So(string(r.Result), ShouldEqualJSON, `{"targetId":"T100"}`)
			So(h.log.count("debugger.open about:blank"), ShouldEqual, 1)
			created := c.events("Target.targetCreated", at)
			So(targetInfoOf(created[len(created)-1])["targetId"], ShouldEqual, "T100")
			So(attachedSessions(c.events("Target.attachedToTarget", at)), ShouldContainKey, "T100")
			expectFocusFirst(h, 100, `Target.getTargetInfo`)

			h.notify(generated.NotificationDebuggerTabCreated, generated.DebuggerTabNotification{TabId: 100, TargetId: "T100", URL: "about:blank"})
			So(c.call("", "Target.getBrowserContexts", `{}`).Error, ShouldBeNil)
			So(len(c.events("Target.targetCreated", -1)), ShouldEqual, 3)
			So(len(c.events("Target.attachedToTarget", -1)), ShouldEqual, 3)
		})

		Convey("createTarget 的 background 在后台打开;指定浏览器上下文时不支持,不打开标签页", func() {
			chrome.addTab(generated.DebuggerTabNotification{TabId: 100, TargetId: "T100", URL: "https://new.test/"})
			So(c.call("", "Target.createTarget", `{"url":"https://new.test/","background":true}`).Error, ShouldBeNil)
			So(h.log.count("debugger.open https://new.test/ background"), ShouldEqual, 1)
			r := c.call("", "Target.createTarget", `{"url":"https://new.test/","browserContextId":"CTX9"}`)
			So(r.Error, ShouldNotBeNil)
			So(r.Error.Message, ShouldContainSubstring, "sctl's CDP endpoint does not support new browser contexts")
			So(h.log.count("debugger.open https://new.test/ background")+h.log.count("debugger.open https://new.test/"), ShouldEqual, 1)
		})

		Convey("closeTarget 关闭那个标签页;标签页关闭后报告分离与销毁", func() {
			r := c.call("", "Target.closeTarget", `{"targetId":"T6"}`)
			So(r.Error, ShouldBeNil)
			So(string(r.Result), ShouldEqualJSON, `{"success":true}`)
			So(h.log.count("debugger.close 6"), ShouldEqual, 1)
			h.notify(generated.NotificationDebuggerTabRemoved, generated.DebuggerTabRemovedNotification{TabId: 6})
			detached, _ := c.event("Target.detachedFromTarget", nil)
			So(string(detached.Params), ShouldEqualJSON, `{"sessionId":"`+sessions["T6"]+`","targetId":"T6"}`)
			destroyed, _ := c.event("Target.targetDestroyed", nil)
			So(string(destroyed.Params), ShouldEqualJSON, `{"targetId":"T6"}`)
			gone := c.call(sessions["T6"], "Page.enable", `{}`)
			So(gone.Error, ShouldResemble, &cdpError{Code: -32001, Message: "Session with given id not found: " + sessions["T6"]})
		})

		Convey("activateTarget 激活那个标签页;未知的目标返回 Chrome 的错误", func() {
			So(c.call("", "Target.activateTarget", `{"targetId":"T6"}`).Error, ShouldBeNil)
			So(h.log.count("tabs.activate 6"), ShouldEqual, 1)
			r := c.call("", "Target.activateTarget", `{"targetId":"NOPE"}`)
			So(r.Error, ShouldResemble, &cdpError{Code: -32602, Message: "No target with given id found"})
			r = c.call("", "Target.attachToTarget", `{"targetId":"NOPE","flatten":true}`)
			So(r.Error, ShouldResemble, &cdpError{Code: -32602, Message: "No target with given id found"})
		})

		Convey("getTargets 与 getTargetInfo 报告页面目标", func() {
			r := c.call("", "Target.getTargets", `{}`)
			So(r.Error, ShouldBeNil)
			var targets struct {
				TargetInfos []map[string]any `json:"targetInfos"`
			}
			So(json.Unmarshal(r.Result, &targets), ShouldBeNil)
			So(len(targets.TargetInfos), ShouldEqual, 2)
			for _, ti := range targets.TargetInfos {
				So(ti["type"], ShouldEqual, "page")
				So(ti["attached"], ShouldEqual, true)
			}
			r = c.call("", "Target.getTargetInfo", `{"targetId":"T6"}`)
			So(r.Error, ShouldBeNil)
			So(string(r.Result), ShouldEqualJSON, `{"targetInfo":{"targetId":"T6","type":"page","title":"other user tab","url":"http://127.0.0.1:8711/other.html","attached":true,"canAccessOpener":false,"browserContextId":"CTX1"}}`)
		})

		Convey("页面会话上的 getTargetInfo 转发给 Chrome,回答这个页面自己的信息", func() {
			r := c.call(sessions["T5"], "Target.getTargetInfo", `{}`)
			So(r.Error, ShouldBeNil)
			So(targetInfoOf(cdpMessage{Params: r.Result})["targetId"], ShouldEqual, "T5")
		})

		Convey("attachToTarget 再开一个会话:应答之前报告附加,焦点模拟不重复开,事件两个会话都收到;分离到最后一个会话时断开调试器", func() {
			r, at := c.response(c.send("", "Target.attachToTarget", `{"targetId":"T5","flatten":true}`))
			So(r.Error, ShouldBeNil)
			var res struct {
				SessionID string `json:"sessionId"`
			}
			So(json.Unmarshal(r.Result, &res), ShouldBeNil)
			So(res.SessionID, ShouldNotEqual, sessions["T5"])
			ev := c.events("Target.attachedToTarget", at)
			So(paramOf(ev[len(ev)-1], "sessionId"), ShouldEqual, res.SessionID)
			So(h.log.count(`debugger.send 5 Emulation.setFocusEmulationEnabled {"enabled":true}`), ShouldEqual, 1)

			So(c.call(res.SessionID, "Runtime.evaluate", `{"expression":"document.title"}`).Error, ShouldBeNil)
			So(h.log.count(`debugger.send 5 Runtime.evaluate {"expression":"document.title"}`), ShouldEqual, 1)
			h.chromeEvent(5, "", "Page.loadEventFired", `{"timestamp":1}`)
			c.event("Page.loadEventFired", func(m cdpMessage) bool { return m.SessionID == res.SessionID })
			c.event("Page.loadEventFired", func(m cdpMessage) bool { return m.SessionID == sessions["T5"] })

			d, dAt := c.response(c.send("", "Target.detachFromTarget", `{"sessionId":"`+res.SessionID+`"}`))
			So(d.Error, ShouldBeNil)
			So(len(c.events("Target.detachedFromTarget", dAt+1)), ShouldEqual, 1)
			So(h.log.count("debugger.detach 5"), ShouldEqual, 0)
			So(c.call("", "Target.detachFromTarget", `{"sessionId":"`+sessions["T5"]+`"}`).Error, ShouldBeNil)
			So(h.log.waitFor("debugger.detach 5", 1), ShouldBeTrue)

			// 再附加时重新开焦点模拟:Chrome 断开调试器时清掉了它。
			So(c.call("", "Target.attachToTarget", `{"targetId":"T5","flatten":true}`).Error, ShouldBeNil)
			So(h.log.count(`debugger.send 5 Emulation.setFocusEmulationEnabled {"enabled":true}`), ShouldEqual, 2)
		})

		Convey("根会话上分离子会话:发给子会话所在的父会话", func() {
			child := "CHILDSESSION"
			h.chromeEvent(6, "", "Target.attachedToTarget", `{"sessionId":"`+child+`","targetInfo":{"targetId":"F1","type":"iframe"},"waitingForDebugger":false}`)
			c.event("Target.attachedToTarget", func(m cdpMessage) bool { return m.SessionID == sessions["T6"] })
			So(c.call("", "Target.detachFromTarget", `{"sessionId":"`+child+`"}`).Error, ShouldBeNil)
			So(h.log.count(`debugger.send 6 Target.detachFromTarget {"sessionId":"`+child+`"}`), ShouldEqual, 1)
			r := c.call("", "Target.detachFromTarget", `{"sessionId":"UNKNOWN"}`)
			So(r.Error, ShouldNotBeNil)
		})
	})
}

func TestAttachWithoutAutoAttach(t *testing.T) {
	Convey("只发现不自动附加的客户端用 attachToTarget 附加:附加前开焦点模拟", t, func() {
		h, _ := newEmulatorHarness(t, tabFive)
		info := h.create(t)
		c := dialCDP(t, info.WSURL)
		So(c.call("", "Target.setDiscoverTargets", `{"discover":true}`).Error, ShouldBeNil)
		So(h.log.count(`debugger.send 5 Emulation.setFocusEmulationEnabled {"enabled":true}`), ShouldEqual, 0)
		r := c.call("", "Target.attachToTarget", `{"targetId":"T5","flatten":true}`)
		So(r.Error, ShouldBeNil)
		var res struct {
			SessionID string `json:"sessionId"`
		}
		So(json.Unmarshal(r.Result, &res), ShouldBeNil)
		So(c.call(res.SessionID, "Page.enable", `{}`).Error, ShouldBeNil)
		expectFocusFirst(h, 5, "Page.enable {}")

		r = c.call("", "Target.attachToTarget", `{"targetId":"T5"}`)
		So(r.Error, ShouldNotBeNil)
		So(r.Error.Message, ShouldContainSubstring, "flatten")
	})
}

func TestAttachInterruptedByDetach(t *testing.T) {
	Convey("附加准备期间 Chrome 断开了调试器:attachToTarget 返回错误,不给出一个从未报告附加、命令都找不到的会话", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		info := h.create(t)
		c := dialCDP(t, info.WSURL)
		So(c.call("", "Target.setDiscoverTargets", `{"discover":true}`).Error, ShouldBeNil)
		targetInfo := make(chan struct{})
		chrome.setBlock("Target.getTargetInfo", targetInfo)
		attach := c.send("", "Target.attachToTarget", `{"targetId":"T5","flatten":true}`)
		So(h.log.waitFor("debugger.send 5 Target.getTargetInfo", 1), ShouldBeTrue)
		h.notify(generated.NotificationDebuggerDetached, generated.DebuggerDetachedNotification{TabId: 5, Reason: "canceled_by_user"})
		// 写协程写出这条应答之前已处理完上面的断开通知。
		So(c.call("", "Target.getBrowserContexts", `{}`).Error, ShouldBeNil)
		close(targetInfo)

		r, _ := c.response(attach)
		So(r.Error, ShouldNotBeNil)
		So(r.Error.Message, ShouldContainSubstring, "detached")
		So(c.events("Target.attachedToTarget", -1), ShouldBeEmpty)
	})
}

func TestTabLifecycle(t *testing.T) {
	Convey("客户端连着时标签页的打开、变化与关闭", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		c, _ := connectAutoAttached(t, h)
		seven := generated.DebuggerTabNotification{TabId: 7, TargetId: "T7", Title: "", URL: "http://127.0.0.1:8711/other.html?popup=1"}
		chrome.addTab(seven)

		Convey("用户或页面新开的标签页:报告创建,开焦点模拟后自动附加", func() {
			h.notify(generated.NotificationDebuggerTabCreated, seven)
			created, createdAt := c.event("Target.targetCreated", func(m cdpMessage) bool { return targetInfoOf(m)["targetId"] == "T7" })
			So(targetInfoOf(created)["url"], ShouldEqual, seven.URL)
			attached, attachedAt := c.event("Target.attachedToTarget", func(m cdpMessage) bool { return targetInfoOf(m)["targetId"] == "T7" })
			So(attachedAt, ShouldBeGreaterThan, createdAt)
			So(targetInfoOf(attached)["browserContextId"], ShouldEqual, "CTX1")
			So(h.log.count(`debugger.send 7 Emulation.setFocusEmulationEnabled {"enabled":true}`), ShouldEqual, 1)
			session := paramOf(attached, "sessionId").(string)

			Convey("标题或地址变化时报告 targetInfoChanged,只有状态变化时不报告", func() {
				h.notify(generated.NotificationDebuggerTabUpdated, generated.DebuggerTabNotification{TabId: 7, TargetId: "T7", Title: "other user tab", URL: seven.URL})
				changed, _ := c.event("Target.targetInfoChanged", nil)
				So(targetInfoOf(changed)["title"], ShouldEqual, "other user tab")
				So(targetInfoOf(changed)["attached"], ShouldEqual, true)
				h.notify(generated.NotificationDebuggerTabUpdated, generated.DebuggerTabNotification{TabId: 7, TargetId: "T7", Title: "other user tab", URL: seven.URL})
				So(c.call("", "Target.getBrowserContexts", `{}`).Error, ShouldBeNil)
				So(len(c.events("Target.targetInfoChanged", -1)), ShouldEqual, 1)
			})

			Convey("用户关掉调试提示条:报告分离但目标还在,之后这个会话的命令找不到会话", func() {
				h.notify(generated.NotificationDebuggerDetached, generated.DebuggerDetachedNotification{TabId: 7, Reason: "canceled_by_user"})
				detached, _ := c.event("Target.detachedFromTarget", nil)
				So(string(detached.Params), ShouldEqualJSON, `{"sessionId":"`+session+`","targetId":"T7"}`)
				r := c.call(session, "Page.enable", `{}`)
				So(r.Error.Code, ShouldEqual, -32001)
				So(c.events("Target.targetDestroyed", -1), ShouldBeEmpty)
			})

			Convey("Chrome 因标签页关闭而断开:报告分离与销毁", func() {
				h.notify(generated.NotificationDebuggerDetached, generated.DebuggerDetachedNotification{TabId: 7, Reason: "target_closed"})
				h.notify(generated.NotificationDebuggerTabRemoved, generated.DebuggerTabRemovedNotification{TabId: 7})
				c.event("Target.targetDestroyed", nil)
				So(c.call("", "Target.getBrowserContexts", `{}`).Error, ShouldBeNil)
				So(len(c.events("Target.detachedFromTarget", -1)), ShouldEqual, 1)
				So(len(c.events("Target.targetDestroyed", -1)), ShouldEqual, 1)
			})
		})

		Convey("还没报告过的标签页先以变化通知出现时同样报告创建并附加", func() {
			h.notify(generated.NotificationDebuggerTabUpdated, seven)
			c.event("Target.targetCreated", func(m cdpMessage) bool { return targetInfoOf(m)["targetId"] == "T7" })
			c.event("Target.attachedToTarget", func(m cdpMessage) bool { return targetInfoOf(m)["targetId"] == "T7" })
		})
	})
}

func TestUnsupportedFeatures(t *testing.T) {
	Convey("不支持的功能返回说明 sctl 端点不支持的 CDP 错误,不发给浏览器", t, func() {
		h, _ := newEmulatorHarness(t, tabFive)
		c, sessions := connectAutoAttached(t, h)
		cases := []struct{ session, method, feature string }{
			{"", "Target.createBrowserContext", "new browser contexts"},
			{"", "Target.disposeBrowserContext", "new browser contexts"},
			{"", "Browser.grantPermissions", "permissions"},
			{"", "Browser.resetPermissions", "permissions"},
			{"", "Browser.setWindowBounds", "window size and position"},
			{"", "Browser.getWindowForTarget", "window size and position"},
			{sessions["T5"], "Browser.getWindowForTarget", "window size and position"},
			{"", "Security.setIgnoreCertificateErrors", "ignoring certificate errors"},
			{sessions["T5"], "Security.setIgnoreCertificateErrors", "ignoring certificate errors"},
			{sessions["T5"], "ServiceWorker.enable", "Service Worker targets"},
		}
		for _, tc := range cases {
			r := c.call(tc.session, tc.method, `{}`)
			So(r.Error, ShouldNotBeNil)
			So(r.Error.Message, ShouldEqual, tc.method+": sctl's CDP endpoint does not support "+tc.feature)
			So(h.log.count("debugger.send 5 "+tc.method+" {}"), ShouldEqual, 0)
		}
		r := c.call("", "Browser.crash", `{}`)
		So(r.Error.Message, ShouldEqual, "Browser.crash: sctl's CDP endpoint does not support this command")
	})

	Convey("根会话上的其他命令经已附加的标签页发出,Chrome 125 拒绝读 Cookie 的错误原样返回", t, func() {
		h, _ := newEmulatorHarness(t, tabFive)
		c, _ := connectAutoAttached(t, h)
		So(c.call("", "Storage.setCookies", `{"cookies":[{"name":"pwk","value":"v","url":"http://127.0.0.1:8711"}]}`).Error, ShouldBeNil)
		So(h.log.count(`debugger.send 5 Storage.setCookies {"cookies":[{"name":"pwk","value":"v","url":"http://127.0.0.1:8711"}]}`), ShouldEqual, 1)
		r := c.call("", "Storage.getCookies", `{}`)
		So(r.Error, ShouldResemble, &cdpError{Code: -32000, Message: "Permission denied"})
	})

	Convey("没有已附加的标签页时根会话上的其他命令返回错误", t, func() {
		h, _ := newEmulatorHarness(t)
		info := h.create(t)
		c := dialCDP(t, info.WSURL)
		r := c.call("", "Storage.getCookies", `{}`)
		So(r.Error, ShouldNotBeNil)
		So(r.Error.Message, ShouldContainSubstring, "no tab is attached")
	})
}

func TestCDPErrorMapping(t *testing.T) {
	Convey("扩展的错误映射回 CDP 错误", t, func() {
		cases := []struct {
			name string
			err  error
			want cdpError
		}{
			{"Chrome 压扁的错误还原成 Chrome 的 code 与 message", &bridge.Error{Code: generated.ErrorCodeInvalidRequest, Message: `{"code":-32601,"message":"'Foo.bar' wasn't found"}`}, cdpError{Code: -32601, Message: "'Foo.bar' wasn't found"}},
			{"带 data 的 Chrome 错误保留 data", &bridge.Error{Code: generated.ErrorCodeInvalidRequest, Message: `{"code":-32602,"data":"Failed to deserialize params.partitionKey","message":"Invalid parameters"}`}, cdpError{Code: -32602, Message: "Invalid parameters", Data: json.RawMessage(`"Failed to deserialize params.partitionKey"`)}},
			{"不是 JSON 的 Chrome 错误原样作为 message", &bridge.Error{Code: generated.ErrorCodeInvalidRequest, Message: "Detached while handling command."}, cdpError{Code: -32000, Message: "Detached while handling command."}},
			{"结果超过单帧上限", &bridge.Error{Code: generated.ErrorCodePayloadTooLarge, Message: "result exceeds 4194304 bytes"}, cdpError{Code: -32000, Message: "the result is too large for sctl to relay: result exceeds 4194304 bytes"}},
			{"命令执行中调试器断开", &bridge.Error{Code: generated.ErrorCodeDebuggerDetached, Message: "the debugger detached while the command was running"}, cdpError{Code: -32000, Message: "the debugger detached from the tab: the debugger detached while the command was running"}},
			{"标签页不存在", &bridge.Error{Code: generated.ErrorCodeNotFound, Message: "no tab with id 9"}, cdpError{Code: -32602, Message: "no tab with id 9"}},
			{"Chrome 不允许附加", &bridge.Error{Code: generated.ErrorCodePageNotAutomatable, Message: "Cannot access a chrome:// URL"}, cdpError{Code: -32000, Message: "Chrome does not let sctl debug this tab: Cannot access a chrome:// URL"}},
			{"浏览器离线", &bridge.Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser x is not connected"}, cdpError{Code: -32000, Message: "the browser disconnected from sctl"}},
			{"扩展连接断开", bridge.ErrDisconnected, cdpError{Code: -32000, Message: "the browser disconnected from sctl"}},
			{"扩展不支持的方法", &bridge.Error{Code: generated.ErrorCodeMethodNotFound, Message: "extension does not support debugger.targets"}, cdpError{Code: -32601, Message: "extension does not support debugger.targets"}},
			{"其他扩展错误带上错误码", &bridge.Error{Code: generated.ErrorCodeOperationExpired, Message: "operation expired"}, cdpError{Code: -32000, Message: "OPERATION_EXPIRED: operation expired"}},
			{"其他错误", errors.New("boom"), cdpError{Code: -32000, Message: "boom"}},
		}
		for _, tc := range cases {
			Convey(tc.name, func() {
				So(toCDPError(tc.err), ShouldResemble, tc.want)
			})
		}
	})
}

func TestEmulatorConcurrency(t *testing.T) {
	Convey("同一会话上被页面挡住的命令不挡后面的命令:弹框打开时照样能关掉它", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		c, sessions := connectAutoAttached(t, h)
		dialog := make(chan struct{})
		chrome.setBlock("Runtime.evaluate", dialog)
		chrome.setBefore("Page.handleJavaScriptDialog", func(int) { close(dialog) })
		eval := c.send(sessions["T5"], "Runtime.evaluate", `{"expression":"alert(1)"}`)
		handle := c.send(sessions["T5"], "Page.handleJavaScriptDialog", `{"accept":true}`)
		r, _ := c.response(handle)
		So(r.Error, ShouldBeNil)
		r, _ = c.response(eval)
		So(r.Error, ShouldBeNil)
	})

	Convey("Chrome 在应答之前发出的事件先于应答送到客户端", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		c, sessions := connectAutoAttached(t, h)
		// 事件多到写协程还在逐条写出时应答就已到达。
		const n = 300
		chrome.setBefore("Runtime.enable", func(tab int) {
			for i := range n {
				h.chromeEvent(tab, "", "Runtime.executionContextCreated", fmt.Sprintf(`{"context":{"id":%d}}`, i))
			}
		})
		_, respAt := c.response(c.send(sessions["T5"], "Runtime.enable", `{}`))
		So(len(c.events("Runtime.executionContextCreated", respAt)), ShouldEqual, n)
	})

	Convey("命令进行中客户端断开:进行中的调用被取消,会话结束,标签页交还 sctl", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		c, sessions := connectAutoAttached(t, h)
		chrome.setBlock("Runtime.evaluate", make(chan struct{}))
		c.send(sessions["T5"], "Runtime.evaluate", `{"expression":"new Promise(function () {})","awaitPromise":true}`)
		So(h.log.waitFor(`debugger.send 5 Runtime.evaluate {"expression":"new Promise(function () {})","awaitPromise":true}`, 1), ShouldBeTrue)
		So(c.conn.Close(websocket.StatusNormalClosure, ""), ShouldBeNil)
		So(h.log.waitFor("canceled Runtime.evaluate", 1), ShouldBeTrue)
		So(h.log.waitFor("reclaim inst-work", 1), ShouldBeTrue)
	})

	Convey("sctl cdp close 时进行中的命令被取消,会话结束", t, func() {
		h, chrome := newEmulatorHarness(t, tabFive)
		c, sessions := connectAutoAttached(t, h)
		chrome.setBlock("Page.navigate", make(chan struct{}))
		c.send(sessions["T5"], "Page.navigate", `{"url":"https://slow.test/"}`)
		So(h.log.waitFor(`debugger.send 5 Page.navigate {"url":"https://slow.test/"}`, 1), ShouldBeTrue)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, closed, err := h.m.Close(ctx, "work")
		So(err, ShouldBeNil)
		So(closed, ShouldBeTrue)
		So(h.log.count("canceled Page.navigate"), ShouldEqual, 1)
		<-c.done
		So(websocket.CloseStatus(c.readErr), ShouldEqual, websocket.StatusGoingAway)
	})

	Convey("不是 CDP 请求的消息让端点以 1003 断开客户端", t, func() {
		h, _ := newEmulatorHarness(t, tabFive)
		info := h.create(t)
		c := dialCDP(t, info.WSURL)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		So(c.conn.Write(ctx, websocket.MessageText, []byte(`not json`)), ShouldBeNil)
		<-c.done
		So(websocket.CloseStatus(c.readErr), ShouldEqual, websocket.StatusUnsupportedData)
		So(h.log.waitFor("reclaim inst-work", 1), ShouldBeTrue)
	})
}
