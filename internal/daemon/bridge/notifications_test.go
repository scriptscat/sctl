package bridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

type notification struct {
	instanceID string
	method     string
	params     string
}

// recordingListener 记录收到的通知与断开;onNotification/onGone 用于在回调里做重入探测。
type recordingListener struct {
	notifications chan notification
	mu            sync.Mutex
	gone          []string
	goneCh        chan string
	onNotify      func()
	onGone        func(instanceID string)
}

func newRecordingListener() *recordingListener {
	return &recordingListener{notifications: make(chan notification, 16), goneCh: make(chan string, 16)}
}

func (l *recordingListener) OnNotification(instanceID, method string, params json.RawMessage) {
	if l.onNotify != nil {
		l.onNotify()
	}
	l.notifications <- notification{instanceID, method, string(params)}
}

func (l *recordingListener) OnInstanceGone(instanceID string) {
	if l.onGone != nil {
		l.onGone(instanceID)
	}
	l.mu.Lock()
	l.gone = append(l.gone, instanceID)
	l.mu.Unlock()
	l.goneCh <- instanceID
}

func (l *recordingListener) goneCount() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.gone)
}

func (l *recordingListener) nextNotification() notification {
	select {
	case n := <-l.notifications:
		return n
	case <-time.After(3 * time.Second):
		So("no notification delivered", ShouldBeEmpty)
		return notification{}
	}
}

func (l *recordingListener) awaitGone() string {
	select {
	case id := <-l.goneCh:
		return id
	case <-time.After(3 * time.Second):
		So("OnInstanceGone not called", ShouldBeEmpty)
		return ""
	}
}

// settle 给可能的多余回调留出时间,用来断言"恰好一次"。
func settle() { time.Sleep(300 * time.Millisecond) }

func (e *extClient) writeNotification(method string, params any) {
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	message, err := newNotification(method, params)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, message), ShouldBeNil)
}

const (
	debuggerEventParams    = `{"tabId":7,"method":"Page.loadEventFired","params":{"timestamp":1}}`
	debuggerDetachedParams = `{"tabId":7,"reason":"canceled_by_user"}`
)

func TestBrowserNotificationsReachTheListener(t *testing.T) {
	Convey("已认证浏览器实例发来的调试器通知送达监听者,并带上实例 ID", t, func() {
		h := startTestServer(t)
		l := newRecordingListener()
		h.srv.SetBrowserListener(l)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")

		a.writeNotification(string(generated.NotificationDebuggerEvent), json.RawMessage(debuggerEventParams))
		So(l.nextNotification(), ShouldResemble, notification{instanceA, "debugger.event", debuggerEventParams})

		b.writeNotification(string(generated.NotificationDebuggerDetached), json.RawMessage(debuggerDetachedParams))
		So(l.nextNotification(), ShouldResemble, notification{instanceB, "debugger.detached", debuggerDetachedParams})
		a.alive()
	})
}

func TestScriptCatNotificationsAreDroppedWithoutClosingTheConnection(t *testing.T) {
	Convey("ScriptCat 发来的同名通知被丢弃,连接保持", t, func() {
		h := startTestServer(t)
		l := newRecordingListener()
		h.srv.SetBrowserListener(l)
		sc := h.connectScriptCat()
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")

		sc.writeNotification("debugger.event", json.RawMessage(debuggerEventParams))
		sc.writeNotification("debugger.detached", json.RawMessage(debuggerDetachedParams))
		sc.alive()
		h.scriptsListWorks(sc)

		// 浏览器的通知是监听者收到的第一条,说明 ScriptCat 的两条没有漏进去。
		a.writeNotification("debugger.event", json.RawMessage(debuggerEventParams))
		So(l.nextNotification().instanceID, ShouldEqual, instanceA)
		settle()
		So(l.notifications, ShouldHaveLength, 0)
	})
}

func TestNotificationsWithoutListenerAreIgnored(t *testing.T) {
	Convey("没有注册监听者时通知被忽略,连接保持", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		a.writeNotification("debugger.event", json.RawMessage(debuggerEventParams))
		a.alive()
	})
}

func TestInstanceGoneFiresOncePerLostConnection(t *testing.T) {
	Convey("浏览器连接每丢失一次,OnInstanceGone 恰好触发一次", t, func() {
		h := startTestServer(t)
		l := newRecordingListener()
		h.srv.SetBrowserListener(l)
		keyA := h.registerBrowser(instanceA, "chrome-0123")

		Convey("断开连接", func() {
			a := h.connectBrowser(instanceA, keyA, "chrome-0123")
			So(a.ws.Close(1000, ""), ShouldBeNil)
			So(l.awaitGone(), ShouldEqual, instanceA)
			settle()
			So(l.goneCount(), ShouldEqual, 1)
		})

		Convey("同一实例重连替换旧连接:旧连接丢失触发一次,新连接不受影响,旧连接随后退出不再重复触发", func() {
			old := h.connectBrowser(instanceA, keyA, "chrome-0123")
			successor := h.connectBrowser(instanceA, keyA, "chrome-0123")
			So(l.awaitGone(), ShouldEqual, instanceA)
			old.closedByDaemon()
			settle()
			So(l.goneCount(), ShouldEqual, 1)
			successor.alive()
			successor.writeNotification("debugger.event", json.RawMessage(debuggerEventParams))
			So(l.nextNotification().instanceID, ShouldEqual, instanceA)
		})

		Convey("忘记在线实例", func() {
			a := h.connectBrowser(instanceA, keyA, "chrome-0123")
			So(h.srv.ForgetInstance("chrome-0123"), ShouldBeNil)
			So(l.awaitGone(), ShouldEqual, instanceA)
			a.closedByDaemon()
			settle()
			So(l.goneCount(), ShouldEqual, 1)
		})

		Convey("忘记离线实例与 ScriptCat 断开都不触发", func() {
			sc := h.connectScriptCat()
			So(sc.ws.Close(1000, ""), ShouldBeNil)
			So(h.srv.ForgetInstance("chrome-0123"), ShouldBeNil)
			settle()
			So(l.goneCount(), ShouldEqual, 0)
		})

		Convey("未完成登记就断开的连接不触发", func() {
			e := h.authenticateBrowser(instanceA, keyA)
			So(e.ws.Close(1000, ""), ShouldBeNil)
			settle()
			So(l.goneCount(), ShouldEqual, 0)
		})
	})
}

func TestListenerRunsOutsideServerLocks(t *testing.T) {
	Convey("监听者回调里可以再调用服务(不持有服务锁),不会死锁", t, func() {
		h := startTestServer(t)
		l := newRecordingListener()
		reentered := make(chan struct{}, 4)
		probe := func() {
			h.srv.Instances()
			h.srv.ExtConnected()
			_, _ = h.srv.ResolveBrowser("")
			reentered <- struct{}{}
		}
		l.onNotify = probe
		l.onGone = func(string) { probe() }
		h.srv.SetBrowserListener(l)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")

		a.writeNotification("debugger.event", json.RawMessage(debuggerEventParams))
		l.nextNotification()
		So(a.ws.Close(1000, ""), ShouldBeNil)
		l.awaitGone()
		So(reentered, ShouldHaveLength, 2)

		Convey("替换与忘记的回调同样不持有 regMu", func() {
			old := h.connectBrowser(instanceA, keyA, "chrome-0123")
			l.onGone = func(string) { _ = h.srv.ForgetInstance("nonexistent-instance") }
			successor := h.connectBrowser(instanceA, keyA, "chrome-0123")
			l.awaitGone()
			old.closedByDaemon()
			successor.alive()
		})
	})
}

func TestResolveBrowser(t *testing.T) {
	Convey("ResolveBrowser 复用调用路径的名称优先、ID 前缀匹配与错误码", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "0123")
		h.registerBrowser("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "offline-one")
		h.registerBrowser("aaaaaaaabbbbbbbbbbbbbbbbbbbbbbbb", "offline-two")
		h.connectBrowser(instanceA, keyA, "chrome-0123")
		h.connectBrowser(instanceB, keyB, "0123")

		Convey("名称精确匹配优先于 ID 前缀", func() {
			info, err := h.srv.ResolveBrowser("0123")
			So(err, ShouldBeNil)
			So(info.ID, ShouldEqual, instanceB)
			So(info.Name, ShouldEqual, "0123")
			So(info.Online, ShouldBeTrue)
		})

		Convey("按唯一 ID 前缀匹配", func() {
			info, err := h.srv.ResolveBrowser("fedc")
			So(err, ShouldBeNil)
			So(info.ID, ShouldEqual, instanceB)
		})

		Convey("不匹配返回 BROWSER_NOT_FOUND", func() {
			_, err := h.srv.ResolveBrowser("nope")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserNotFound)
		})

		Convey("前缀匹配多个返回 BROWSER_AMBIGUOUS", func() {
			_, err := h.srv.ResolveBrowser("aaaaaaaa")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserAmbiguous)
		})

		Convey("匹配到离线实例返回 BROWSER_OFFLINE", func() {
			_, err := h.srv.ResolveBrowser("offline-one")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserOffline)
		})

		Convey("目标为空且多个在线时返回 BROWSER_AMBIGUOUS", func() {
			_, err := h.srv.ResolveBrowser("")
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserAmbiguous)
		})
	})
}

func TestResolveBrowserWithoutTargetPicksTheOnlyOnlineInstance(t *testing.T) {
	Convey("目标为空且恰好一个在线时选中它;没有在线时 NO_BROWSER_CONNECTED", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		_, err := h.srv.ResolveBrowser("")
		So(errorCode(err), ShouldEqual, generated.ErrorCodeNoBrowserConnected)
		h.connectBrowser(instanceA, keyA, "chrome-0123")
		info, err := h.srv.ResolveBrowser("")
		So(err, ShouldBeNil)
		So(info.ID, ShouldEqual, instanceA)
	})
}

func TestCallInstanceTargetsTheExactInstance(t *testing.T) {
	Convey("CallInstance 只发给精确 ID 的在线实例", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		h.registerBrowser("cccccccccccccccccccccccccccccccc", "offline")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()

		Convey("在线实例收到请求并返回其结果,另一实例不受影响", func() {
			out := make(chan callOutcome, 1)
			go func() {
				resp, err := h.srv.CallInstance(ctx, instanceB, Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
				out <- callOutcome{resp, err}
			}()
			req := b.read()
			So(req.Method, ShouldEqual, "tabs.list")
			b.writeResult(req.ID, json.RawMessage(tabsListResult))
			got := <-out
			So(got.err, ShouldBeNil)
			So(got.resp.OK, ShouldBeTrue)
			So(string(got.resp.Result), ShouldEqual, tabsListResult)
			a.alive()
		})

		Convey("已配对但离线返回 BROWSER_OFFLINE", func() {
			_, err := h.srv.CallInstance(ctx, "cccccccccccccccccccccccccccccccc", Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserOffline)
		})

		Convey("ID 前缀不算精确 ID", func() {
			_, err := h.srv.CallInstance(ctx, instanceA[:8], Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserOffline)
		})

		Convey("忘记后的实例返回 BROWSER_OFFLINE", func() {
			So(h.srv.ForgetInstance("chrome-0123"), ShouldBeNil)
			_, err := h.srv.CallInstance(ctx, instanceA, Request{Action: "tabs.list", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserOffline)
		})

		Convey("非浏览器方法被拒为 INVALID_REQUEST", func() {
			_, err := h.srv.CallInstance(ctx, instanceA, Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, CodeInvalidRequest)
		})
	})
}
