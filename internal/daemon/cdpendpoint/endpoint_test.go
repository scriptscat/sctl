package cdpendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func secretOf(info Info) string {
	rest := strings.TrimPrefix(info.HTTPURL, "http://")
	return rest[strings.Index(rest, "/cdp/")+len("/cdp/"):]
}

func TestCreateEndpoint(t *testing.T) {
	Convey("创建端点", t, func() {
		h := newHarness(t)

		Convey("重复创建返回同一地址;密钥至少 128 位;两种地址指向同一密钥", func() {
			first := h.create(t)
			again := h.create(t)
			So(again.HTTPURL, ShouldEqual, first.HTTPURL)
			So(again.WSURL, ShouldEqual, first.WSURL)
			secret := secretOf(first)
			So(len(secret), ShouldBeGreaterThanOrEqualTo, 32)
			So(first.HTTPURL, ShouldEqual, "http://"+h.host+"/cdp/"+secret)
			So(first.WSURL, ShouldStartWith, "ws://"+h.host+"/cdp/"+secret+"/devtools/browser/")
			So(len(strings.TrimPrefix(first.WSURL, "ws://"+h.host+"/cdp/"+secret+"/devtools/browser/")), ShouldBeGreaterThan, 0)
			So(first.ClientConnected, ShouldBeFalse)
			So(first.ExpiresAt, ShouldEqual, h.clock.Now().Add(time.Hour))
		})

		Convey("不同浏览器的端点密钥不同", func() {
			h.bridge.instances["home"] = &fakeInstance{id: "inst-home", online: true}
			work := h.create(t)
			home, err := h.m.Create("home", h.host)
			So(err, ShouldBeNil)
			So(secretOf(*home.Endpoint), ShouldNotEqual, secretOf(work))
			So(home.Browser, ShouldResemble, BrowserRef{ID: "inst-home", Name: "home"})
		})

		Convey("浏览器离线或没有浏览器连着时返回第 1 期的错误码,不创建端点", func() {
			h.bridge.setOnline("work", false)
			_, err := h.m.Create("work", h.host)
			var be *bridge.Error
			So(errors.As(err, &be), ShouldBeTrue)
			So(be.Code, ShouldEqual, generated.ErrorCodeBrowserOffline)
			_, err = h.m.Create("", h.host)
			So(errors.As(err, &be), ShouldBeTrue)
			So(be.Code, ShouldEqual, generated.ErrorCodeNoBrowserConnected)
		})

		Convey("调用方经主机名连到 daemon 时不给出必被 Host 检查拒绝的地址:INVALID_REQUEST,不创建端点", func() {
			_, err := h.m.Create("work", "devbox.lan:8643")
			var be *bridge.Error
			So(errors.As(err, &be), ShouldBeTrue)
			So(be.Code, ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(be.Message, ShouldContainSubstring, "devbox.lan:8643")
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint, ShouldBeNil)

			h.create(t)
			_, err = h.m.Status("work", "devbox.lan:8643")
			So(errors.As(err, &be), ShouldBeTrue)
			So(be.Code, ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("status 在没有端点时如实说明,有端点时给出同一地址", func() {
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint, ShouldBeNil)
			So(snap.Browser, ShouldResemble, BrowserRef{ID: "inst-work", Name: "work"})
			info := h.create(t)
			snap, err = h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint.HTTPURL, ShouldEqual, info.HTTPURL)
		})
	})
}

func TestVersionEndpoint(t *testing.T) {
	Convey("/json/version", t, func() {
		h := newHarness(t)
		info := h.create(t)

		Convey("有无结尾斜杠都返回 Chrome 形状的 JSON,UA 取自扩展,WS 地址用请求的 Host", func() {
			for _, path := range []string{"/json/version", "/json/version/"} {
				status, body := h.get(t, info.HTTPURL+path, "", "")
				So(status, ShouldEqual, http.StatusOK)
				var v map[string]string
				So(json.Unmarshal([]byte(body), &v), ShouldBeNil)
				So(v["Browser"], ShouldEqual, "Chrome/125.0.6422.141")
				So(v["Protocol-Version"], ShouldEqual, "1.3")
				So(v["User-Agent"], ShouldEqual, testUserAgent)
				So(v["webSocketDebuggerUrl"], ShouldEqual, info.WSURL)
			}
			status, body := h.get(t, info.HTTPURL+"/json/version", "localhost:9222", "")
			So(status, ShouldEqual, http.StatusOK)
			So(body, ShouldContainSubstring, `"webSocketDebuggerUrl":"ws://localhost:9222/cdp/`+secretOf(info)+`/devtools/browser/`)
		})

		Convey("浏览器离线时说明原因而不是给出假 UA", func() {
			h.bridge.setOnline("work", false)
			status, body := h.get(t, info.HTTPURL+"/json/version", "", "")
			So(status, ShouldEqual, http.StatusServiceUnavailable)
			So(body, ShouldContainSubstring, "not connected")
		})
	})
}

func TestEndpointRejects(t *testing.T) {
	Convey("端点拒绝", t, func() {
		h := newHarness(t)
		info := h.create(t)
		secret := secretOf(info)
		base := "http://" + h.host

		Convey("密钥错误与从未存在的路径得到同样的 404,不透露是否有端点", func() {
			wrong := strings.Repeat("0", len(secret))
			s1, b1 := h.get(t, base+"/cdp/"+wrong+"/json/version", "", "")
			s2, b2 := h.get(t, base+"/cdp/"+secret[:len(secret)-1]+"/json/version", "", "")
			s3, b3 := h.get(t, base+"/cdp/nothing", "", "")
			So(s1, ShouldEqual, http.StatusNotFound)
			So([]any{s2, b2}, ShouldResemble, []any{s1, b1})
			So([]any{s3, b3}, ShouldResemble, []any{s1, b1})
			_, status, _ := dial(t, strings.Replace(info.WSURL, secret, wrong, 1), nil)
			So(status, ShouldEqual, http.StatusNotFound)
			_, status, _ = dial(t, info.WSURL+"x", nil)
			So(status, ShouldEqual, http.StatusNotFound)
			So(h.log.count("handover inst-work"), ShouldEqual, 0)
		})

		Convey("Host 不是回环名或 IP 字面量时拒绝(防 DNS 重绑定);回环名与 IP 字面量放行", func() {
			for _, host := range []string{"evil.example:8643", "evil.example", "127.0.0.1.evil.example:8643"} {
				status, _ := h.get(t, info.HTTPURL+"/json/version", host, "")
				So(status, ShouldEqual, http.StatusForbidden)
				_, status, _ = dial(t, info.WSURL, &websocket.DialOptions{Host: host})
				So(status, ShouldEqual, http.StatusForbidden)
			}
			for _, host := range []string{"localhost:8643", "LOCALHOST", "127.0.0.1:1", "[::1]:8643", "192.168.1.5:8643"} {
				status, _ := h.get(t, info.HTTPURL+"/json/version", host, "")
				So(status, ShouldEqual, http.StatusOK)
			}
			So(h.log.count("handover inst-work"), ShouldEqual, 0)
		})

		Convey("带任何 Origin 的请求都被拒绝:网页 Origin 与扩展、DevTools Origin 一样", func() {
			for _, origin := range []string{"https://evil.example", "http://127.0.0.1:8643", "null", "chrome-extension://abcdef", "devtools://devtools"} {
				_, status, body := dial(t, info.WSURL, &websocket.DialOptions{HTTPHeader: http.Header{"Origin": {origin}}})
				So(status, ShouldEqual, http.StatusForbidden)
				So(body, ShouldContainSubstring, "Origin")
				status, _ = h.get(t, info.HTTPURL+"/json/version", "", origin)
				So(status, ShouldEqual, http.StatusForbidden)
			}
			So(h.log.count("handover inst-work"), ShouldEqual, 0)
		})

		Convey("WS 地址上的普通 HTTP 请求被拒绝,不交出 sctl 的标签页", func() {
			status, body := h.get(t, strings.Replace(info.WSURL, "ws://", "http://", 1), "", "")
			So(status, ShouldEqual, http.StatusBadRequest)
			So(body, ShouldContainSubstring, "WebSocket")
			So(h.log.count("handover inst-work"), ShouldEqual, 0)
		})

		Convey("已有客户端连着时第二个连接被拒绝,响应说明已有客户端在用", func() {
			h.connect(t, info)
			_, status, body := dial(t, info.WSURL, nil)
			So(status, ShouldEqual, http.StatusConflict)
			So(body, ShouldContainSubstring, "already connected")
			So(h.log.count("handover inst-work"), ShouldEqual, 1)
		})

		Convey("交出标签页失败时拒绝升级并说明原因,不运行会话", func() {
			h.pages.handOverErr = errors.New("detach failed")
			_, status, body := dial(t, info.WSURL, nil)
			So(status, ShouldEqual, http.StatusServiceUnavailable)
			So(body, ShouldContainSubstring, "detach failed")
			So(h.started, ShouldBeEmpty)
			// 失败后端点仍空闲:下一次连接可以成功。
			h.pages.mu.Lock()
			h.pages.handOverErr = nil
			h.pages.mu.Unlock()
			h.connect(t, info)
		})
	})
}

func TestClientSession(t *testing.T) {
	Convey("客户端会话", t, func() {
		h := newHarness(t)
		info := h.create(t)

		Convey("连上时先交出 sctl 的标签页,再运行会话;status 显示客户端连着与连上时间,连着时不失效", func() {
			h.connect(t, info)
			So(h.log.all(), ShouldResemble, []string{"handover inst-work"})
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint.ClientConnected, ShouldBeTrue)
			So(snap.Endpoint.ConnectedAt, ShouldEqual, h.clock.Now())
			So(snap.Endpoint.ExpiresAt.IsZero(), ShouldBeTrue)
		})

		Convey("会话第一次向标签页发命令前把它标为端点所有,之后不再重复标记", func() {
			_, b := h.connect(t, info)
			ctx := context.Background()
			res, err := b.Send(ctx, 5, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"ok":true}`)
			_, err = b.Send(ctx, 5, "S1", "Runtime.enable", json.RawMessage(`{}`))
			So(err, ShouldBeNil)
			So(h.log.all(), ShouldResemble, []string{
				"handover inst-work",
				"debugger.own 5 true",
				"debugger.send 5 Page.enable",
				"debugger.send 5 S1 Runtime.enable {}",
			})
		})

		Convey("扩展的错误原样交给会话", func() {
			_, b := h.connect(t, info)
			h.bridge.fail[generated.MethodDebuggerSend] = bridge.Error{Code: "INVALID_REQUEST", Message: `{"code":-32601,"message":"'Foo.bar' wasn't found"}`}
			_, err := b.Send(context.Background(), 5, "", "Foo.bar", nil)
			var be *bridge.Error
			So(errors.As(err, &be), ShouldBeTrue)
			So(be.Message, ShouldContainSubstring, "wasn't found")
		})

		Convey("会话能列出目标、读 UA、为客户端打开与关闭标签页", func() {
			_, b := h.connect(t, info)
			ctx := context.Background()
			targets, err := b.Targets(ctx)
			So(err, ShouldBeNil)
			So(targets, ShouldResemble, []Target{{TabID: 5, TargetID: "T5", Title: "Five", URL: "https://five.test/"}})
			ua, err := b.UserAgent(ctx)
			So(err, ShouldBeNil)
			So(ua, ShouldEqual, testUserAgent)
			opened, err := b.Open(ctx, "https://new.test/", true)
			So(err, ShouldBeNil)
			So(opened, ShouldResemble, OpenedTab{TabID: 100, TargetID: "T100"})
			So(b.CloseTab(ctx, 100), ShouldBeNil)
		})

		Convey("会话自己分离或关闭的标签页,断开客户端时不再处理", func() {
			conn, b := h.connect(t, info)
			ctx := context.Background()
			_, err := b.Send(ctx, 5, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			So(b.DetachTab(ctx, 5), ShouldBeNil)
			opened, err := b.Open(ctx, "https://new.test/", true)
			So(err, ShouldBeNil)
			So(b.CloseTab(ctx, opened.TabID), ShouldBeNil)
			So(conn.Close(websocket.StatusNormalClosure, ""), ShouldBeNil)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			So(h.log.count("debugger.detach 5"), ShouldEqual, 1)
			So(h.log.count("debugger.own 5 false"), ShouldEqual, 0)
			So(h.log.count("debugger.own 100 false"), ShouldEqual, 0)
		})

		Convey("只把这个浏览器的通知转给会话", func() {
			_, b := h.connect(t, info)
			h.m.OnNotification("inst-other", string(generated.NotificationDebuggerTabRemoved), json.RawMessage(`{"tabId":1}`))
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerTabCreated), json.RawMessage(`{"tabId":7,"targetId":"T7","title":"","url":"about:blank"}`))
			select {
			case n := <-b.Notifications():
				So(n.Method, ShouldEqual, string(generated.NotificationDebuggerTabCreated))
				So(string(n.Params), ShouldContainSubstring, `"tabId":7`)
			case <-time.After(5 * time.Second):
				So("no notification", ShouldBeEmpty)
			}
			So(b.Notifications(), ShouldBeEmpty)
		})

		Convey("客户端断开:先关端点标签页上已知的弹框,再断开端点附加的标签页、清除所有标记,不关标签页,最后收回;地址可再次连接", func() {
			conn, b := h.connect(t, info)
			ctx := context.Background()
			_, err := b.Send(ctx, 5, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			_, err = b.Send(ctx, 6, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			_, err = b.Open(ctx, "https://new.test/", false)
			So(err, ShouldBeNil)
			// 弹框在标签页 5 的子会话里打开,之后标签页 6 的弹框又关上了。
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerEvent), json.RawMessage(`{"tabId":5,"sessionId":"S9","method":"Page.javascriptDialogOpening","params":{"type":"alert","message":"hi"}}`))
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerEvent), json.RawMessage(`{"tabId":6,"method":"Page.javascriptDialogOpening","params":{"type":"alert","message":"x"}}`))
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerEvent), json.RawMessage(`{"tabId":6,"method":"Page.javascriptDialogClosed","params":{"result":true}}`))
			So(conn.Close(websocket.StatusNormalClosure, "bye"), ShouldBeNil)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)

			entries := h.log.all()
			tail := entries[len(entries)-7:]
			So(tail[0], ShouldEqual, `debugger.send 5 S9 Page.handleJavaScriptDialog {"accept":false}`)
			So(tail[1:3], ShouldResemble, []string{"debugger.detach 5", "debugger.detach 6"})
			So(tail[3:6], ShouldResemble, []string{"debugger.own 5 false", "debugger.own 6 false", "debugger.own 100 false"})
			So(tail[6], ShouldEqual, "reclaim inst-work")
			for _, e := range entries {
				So(e, ShouldNotStartWith, "debugger.close")
			}

			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint.ClientConnected, ShouldBeFalse)
			So(snap.Endpoint.ExpiresAt, ShouldEqual, h.clock.Now().Add(time.Hour))

			_, err = b.Send(ctx, 5, "", "Page.enable", nil)
			So(err, ShouldNotBeNil)

			h.connect(t, info)
			So(h.log.count("handover inst-work"), ShouldEqual, 2)
		})

		Convey("Chrome 已断开或已关闭的标签页在断开客户端时不再断开", func() {
			conn, b := h.connect(t, info)
			ctx := context.Background()
			for _, tab := range []int{5, 6} {
				_, err := b.Send(ctx, tab, "", "Page.enable", nil)
				So(err, ShouldBeNil)
			}
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerDetached), json.RawMessage(`{"tabId":5,"reason":"canceled_by_user"}`))
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerTabRemoved), json.RawMessage(`{"tabId":6}`))
			So(conn.Close(websocket.StatusNormalClosure, ""), ShouldBeNil)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			So(h.log.count("debugger.detach 5"), ShouldEqual, 0)
			So(h.log.count("debugger.detach 6"), ShouldEqual, 0)
			So(h.log.count("debugger.own 6 false"), ShouldEqual, 0)
		})

		Convey("网络断开(没有关闭帧)同样清理并收回", func() {
			conn, b := h.connect(t, info)
			_, err := b.Send(context.Background(), 5, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			So(conn.CloseNow(), ShouldBeNil)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			So(h.log.count("debugger.detach 5"), ShouldEqual, 1)
		})

		Convey("会话自己返回(Browser.close)时关闭客户端连接并同样清理", func() {
			done := make(chan struct{})
			h2 := newHarnessWithHook(t, hookFunc(func(ctx context.Context, conn *websocket.Conn, b *Browser) error {
				if _, err := b.Send(ctx, 5, "", "Page.enable", nil); err != nil {
					return err
				}
				<-done
				return nil
			}))
			info2 := h2.create(t)
			conn, status, _ := dial(t, info2.WSURL, nil)
			So(status, ShouldEqual, http.StatusSwitchingProtocols)
			So(h2.log.waitFor("debugger.send 5 Page.enable", 1), ShouldBeTrue)
			close(done)
			So(waitClosed(conn), ShouldEqual, websocket.StatusNormalClosure)
			So(h2.waitClientGone("inst-work"), ShouldBeTrue)
			So(h2.log.count("debugger.detach 5"), ShouldEqual, 1)
		})
	})
}

func TestEndpointExpiry(t *testing.T) {
	Convey("端点失效", t, func() {
		h := newHarness(t)
		info := h.create(t)
		versionURL := info.HTTPURL + "/json/version"

		Convey("连续 60 分钟没有客户端连着时失效,之后地址被拒绝", func() {
			h.clock.Advance(time.Hour - time.Second)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusOK)
			h.clock.Advance(time.Second)
			status, _ = h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
			_, status, _ = dial(t, info.WSURL, nil)
			So(status, ShouldEqual, http.StatusNotFound)
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint, ShouldBeNil)
			So(secretOf(h.create(t)), ShouldNotEqual, secretOf(info))
		})

		Convey("客户端连着时不计时;断开后重新计满 60 分钟", func() {
			conn, _ := h.connect(t, info)
			h.clock.Advance(3 * time.Hour)
			So(conn.Close(websocket.StatusNormalClosure, ""), ShouldBeNil)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			h.clock.Advance(time.Hour - time.Second)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusOK)
			h.clock.Advance(time.Second)
			status, _ = h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
		})

		Convey("close 让地址立即失效,断开连着的客户端并在清理完之后返回;没有端点时也成功", func() {
			conn, b := h.connect(t, info)
			_, err := b.Send(context.Background(), 5, "", "Page.enable", nil)
			So(err, ShouldBeNil)
			// 客户端一直在读,才能及时回应关闭帧。
			closedWith := make(chan websocket.StatusCode, 1)
			go func() { closedWith <- waitClosed(conn) }()
			ref, closed, err := h.m.Close(context.Background(), "work")
			So(err, ShouldBeNil)
			So(closed, ShouldBeTrue)
			So(ref, ShouldResemble, BrowserRef{ID: "inst-work", Name: "work"})
			So(h.log.count("reclaim inst-work"), ShouldEqual, 1)
			So(h.log.count("debugger.detach 5"), ShouldEqual, 1)
			So(<-closedWith, ShouldEqual, websocket.StatusGoingAway)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)

			_, closed, err = h.m.Close(context.Background(), "work")
			So(err, ShouldBeNil)
			So(closed, ShouldBeFalse)
		})

		Convey("浏览器离线时 status 仍给出端点,close 仍让地址立即失效:浏览器重新连上后地址被拒绝", func() {
			h.bridge.setOnline("work", false)
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Browser, ShouldResemble, BrowserRef{ID: "inst-work", Name: "work"})
			So(snap.Endpoint.HTTPURL, ShouldEqual, info.HTTPURL)
			ref, closed, err := h.m.Close(context.Background(), "work")
			So(err, ShouldBeNil)
			So(closed, ShouldBeTrue)
			So(ref, ShouldResemble, BrowserRef{ID: "inst-work", Name: "work"})
			h.bridge.setOnline("work", true)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
		})

		Convey("浏览器被忘记(离线时也一样)时失效", func() {
			h.bridge.setOnline("work", false)
			h.m.OnInstanceForgotten("inst-work")
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
		})

		Convey("在线时被忘记:先断开客户端并收回,再失效", func() {
			conn, _ := h.connect(t, info)
			h.m.OnInstanceGone("inst-work")
			h.m.OnInstanceForgotten("inst-work")
			So(waitClosed(conn), ShouldEqual, websocket.StatusGoingAway)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
		})

		Convey("浏览器实例断开时关闭客户端连接并收回,端点本身保留到失效", func() {
			conn, _ := h.connect(t, info)
			h.m.OnInstanceGone("inst-work")
			So(waitClosed(conn), ShouldEqual, websocket.StatusGoingAway)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			snap, err := h.m.Status("work", h.host)
			So(err, ShouldBeNil)
			So(snap.Endpoint.HTTPURL, ShouldEqual, info.HTTPURL)
			So(snap.Endpoint.ClientConnected, ShouldBeFalse)
			h.connect(t, info)
		})

		Convey("没有客户端时浏览器断开不影响端点", func() {
			h.m.OnInstanceGone("inst-work")
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusOK)
		})

		Convey("daemon 退出时关闭客户端连接、收回,地址失效", func() {
			conn, _ := h.connect(t, info)
			h.cancel()
			So(waitClosed(conn), ShouldEqual, websocket.StatusGoingAway)
			So(h.waitClientGone("inst-work"), ShouldBeTrue)
			status, _ := h.get(t, versionURL, "", "")
			So(status, ShouldEqual, http.StatusNotFound)
		})
	})
}

func TestNotificationOverflowEndsSession(t *testing.T) {
	Convey("会话跟不上通知时结束它而不是静默丢事件", t, func() {
		old := notificationBuffer
		notificationBuffer = 2
		defer func() { notificationBuffer = old }()
		h := newHarness(t)
		info := h.create(t)
		conn, _ := h.connect(t, info)
		for range 3 {
			h.m.OnNotification("inst-work", string(generated.NotificationDebuggerTabRemoved), json.RawMessage(`{"tabId":1}`))
		}
		So(waitClosed(conn), ShouldEqual, websocket.StatusTryAgainLater)
		So(h.waitClientGone("inst-work"), ShouldBeTrue)
	})
}

func TestConcurrentClients(t *testing.T) {
	Convey("同时到达的几个客户端只有一个连上,其余都被告知已有客户端", t, func() {
		h := newHarness(t)
		info := h.create(t)
		const n = 5
		statuses := make(chan int, n)
		for range n {
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cancel()
				conn, resp, err := websocket.Dial(ctx, info.WSURL, nil)
				if err == nil {
					t.Cleanup(func() { _ = conn.CloseNow() })
					statuses <- http.StatusSwitchingProtocols
					return
				}
				if resp == nil {
					statuses <- 0
					return
				}
				statuses <- resp.StatusCode
			}()
		}
		counts := map[int]int{}
		for range n {
			counts[<-statuses]++
		}
		So(counts, ShouldResemble, map[int]int{http.StatusSwitchingProtocols: 1, http.StatusConflict: n - 1})
		So(h.log.count("handover inst-work"), ShouldEqual, 1)
	})
}

func TestCloseWhileHandingOver(t *testing.T) {
	Convey("交出标签页期间端点被关闭:连接被拒绝,不运行会话,端点失效", t, func() {
		log := newEventLog()
		h := &harness{log: log, clock: newFakeClock(), started: make(chan *Browser, 1)}
		h.bridge = newFakeBridge(log)
		blocking := &blockingPages{fakePages: fakePages{log: log}, entered: make(chan struct{})}
		h.pages = &blocking.fakePages
		h.startWithPages(t, blocking, hookFunc(func(ctx context.Context, conn *websocket.Conn, b *Browser) error {
			h.started <- b
			return nil
		}))
		info := h.create(t)
		result := make(chan int, 1)
		go func() {
			_, status, _ := dial(t, info.WSURL, nil)
			result <- status
		}()
		<-blocking.entered
		_, closed, err := h.m.Close(context.Background(), "work")
		So(err, ShouldBeNil)
		So(closed, ShouldBeTrue)
		So(<-result, ShouldEqual, http.StatusServiceUnavailable)
		So(h.started, ShouldBeEmpty)
		snap, err := h.m.Status("work", h.host)
		So(err, ShouldBeNil)
		So(snap.Endpoint, ShouldBeNil)
	})
}
