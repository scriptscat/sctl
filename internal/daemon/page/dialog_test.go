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

func dialogEvent(m *Manager, tab int, method, params string) {
	raw, err := json.Marshal(generated.DebuggerEventNotification{Method: method, Params: json.RawMessage(params), TabId: tab})
	So(err, ShouldBeNil)
	m.OnNotification(testInstance, string(generated.NotificationDebuggerEvent), raw)
}

func openDialog(m *Manager, tab int, kind, message string) {
	params, err := json.Marshal(map[string]any{"type": kind, "message": message, "url": "http://x/", "hasBrowserHandler": false})
	So(err, ShouldBeNil)
	dialogEvent(m, tab, "Page.javascriptDialogOpening", string(params))
}

func closeDialog(m *Manager, tab int) {
	dialogEvent(m, tab, "Page.javascriptDialogClosed", `{"result":true,"userInput":""}`)
}

func dialog(m *Manager, tab int, input string) (json.RawMessage, error) {
	return m.Do(context.Background(), Request{Action: "dialog", TabID: &tab, Input: json.RawMessage(input)})
}

func TestDialogState(t *testing.T) {
	Convey("JS 弹框打开期间", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)

		Convey("没有弹框时 page dialog 返回 NOT_FOUND,不发出 handleJavaScriptDialog", func() {
			_, err := dialog(m, 3, `{"action":"accept"}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
			So(cdp.methods(3), ShouldNotContain, "Page.handleJavaScriptDialog")
		})

		Convey("页面命令返回 DIALOG_OPEN,带弹框类型与文字,并且不向页面发命令", func() {
			openDialog(m, 3, "confirm", "Delete everything?")
			before := len(cdp.methods(3))
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "confirm")
			So(err.Error(), ShouldContainSubstring, "Delete everything?")
			So(cdp.methods(3), ShouldHaveLength, before)
		})

		Convey("page screenshot 与其他页面命令一样立即返回 DIALOG_OPEN,不发出截图;page detach 不受影响", func() {
			openDialog(m, 3, "alert", "hi there")
			before := len(cdp.methods(3))
			_, err := m.Do(context.Background(), Request{Action: "screenshot", TabID: tabRef(3), Input: json.RawMessage(`{}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "alert")
			So(err.Error(), ShouldContainSubstring, "hi there")
			So(cdp.methods(3), ShouldHaveLength, before)
			_, err = m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3), Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
		})

		Convey("accept 带 --text 发出 Page.handleJavaScriptDialog,弹框关闭后页面命令恢复", func() {
			openDialog(m, 3, "prompt", "name?")
			_, err := dialog(m, 3, `{"action":"accept","text":"Ada"}`)
			So(err, ShouldBeNil)
			cdp.mu.Lock()
			var params string
			for _, c := range cdp.sent {
				if c.Method == "Page.handleJavaScriptDialog" {
					params = string(c.Params)
				}
			}
			cdp.mu.Unlock()
			So(params, ShouldEqual, `{"accept":true,"promptText":"Ada"}`)
			closeDialog(m, 3)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
		})

		Convey("dismiss 发出 accept:false", func() {
			openDialog(m, 3, "confirm", "sure?")
			_, err := dialog(m, 3, `{"action":"dismiss"}`)
			So(err, ShouldBeNil)
			cdp.mu.Lock()
			var params string
			for _, c := range cdp.sent {
				if c.Method == "Page.handleJavaScriptDialog" {
					params = string(c.Params)
				}
			}
			cdp.mu.Unlock()
			So(params, ShouldEqual, `{"accept":false}`)
		})

		Convey("处理完弹框后页面立刻打开的下一个弹框不被误清除", func() {
			openDialog(m, 3, "confirm", "first")
			cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Page.handleJavaScriptDialog" {
					closeDialog(m, 3)
					openDialog(m, 3, "alert", "second")
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := dialog(m, 3, `{"action":"accept"}`)
			So(err, ShouldBeNil)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "second")
		})

		Convey("dismiss 与 accept 之外的动作或 accept 以外带 text 都是 INVALID_REQUEST", func() {
			openDialog(m, 3, "prompt", "x")
			for _, in := range []string{`{}`, `{"action":"ignore"}`, `{"action":"dismiss","text":"a"}`} {
				_, err := dialog(m, 3, in)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})

		Convey("弹框文字作为带引号的不可信文本,超长时截断", func() {
			openDialog(m, 3, "alert", strings.Repeat("x", 5000))
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(len(err.Error()), ShouldBeLessThan, 1500)
			So(err.Error(), ShouldContainSubstring, "untrusted")
		})
	})
}

func TestDialogOpeningInterruptsRunningScreenshot(t *testing.T) {
	Convey("截图执行中弹框打开时立即返回 DIALOG_OPEN,不等 screenshotTimeout", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		entered := make(chan struct{})
		cdp.setSend(captureFake(func(ctx context.Context, _ Command) (json.RawMessage, error) {
			close(entered)
			<-ctx.Done() // 弹框卡住渲染进程:截图不会返回
			return nil, ctx.Err()
		}))
		_, err := probe(m, Request{TabID: tabRef(7)})
		So(err, ShouldBeNil)
		done := make(chan error, 1)
		go func() {
			_, err := shoot(m, `{}`)
			done <- err
		}()
		<-entered
		openDialog(m, 7, "alert", "mid-shot")
		select {
		case err := <-done:
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "mid-shot")
		case <-time.After(5 * time.Second):
			So("the screenshot did not return", ShouldBeEmpty)
		}
	})
}

func TestDialogOpeningInterruptsRunningAction(t *testing.T) {
	Convey("动作执行中弹框打开(点击触发 alert)时立即返回 DIALOG_OPEN,不等超时,弹框保持打开", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})
		entered := make(chan struct{})
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			if cmd.Method == probeMethod {
				close(entered)
				<-ctx.Done() // 渲染进程被弹框卡住,命令不会返回
				return nil, ctx.Err()
			}
			return json.RawMessage(`{}`), nil
		})
		done := make(chan error, 1)
		go func() {
			_, err := probe(m, Request{TabID: tabRef(3), Timeout: time.Minute})
			done <- err
		}()
		<-entered
		openDialog(m, 3, "alert", "clicked")
		select {
		case err := <-done:
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "clicked")
		case <-time.After(5 * time.Second):
			So("the action did not return", ShouldBeEmpty)
		}
		So(slices.Contains(cdp.methods(3), "Page.handleJavaScriptDialog"), ShouldBeFalse)
		_, err := dialog(m, 3, `{"action":"dismiss"}`)
		So(err, ShouldBeNil)
	})
}

// dismissal 是假 CDP 收到的一条 Page.handleJavaScriptDialog:发往哪个标签页、参数,以及当时已经有过几次断开。
type dismissal struct {
	tab            int
	params         string
	detachesBefore int
}

// recordDismissals 让假 CDP 记下每条 Page.handleJavaScriptDialog,handle 非 nil 时由它决定这条命令的结果。
func recordDismissals(cdp *fakeCDP, handle func() error) func() []dismissal {
	var mu sync.Mutex
	var got []dismissal
	cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
		if cmd.Method == "Page.handleJavaScriptDialog" {
			d := dismissal{tab: cmd.TabID, params: string(cmd.Params), detachesBefore: len(cdp.detachCalls())}
			mu.Lock()
			got = append(got, d)
			mu.Unlock()
			if handle != nil {
				if err := handle(); err != nil {
					return nil, err
				}
			}
		}
		return json.RawMessage(`{}`), nil
	})
	return func() []dismissal {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(got)
	}
}

func TestSctlDismissesKnownDialogBeforeDetaching(t *testing.T) {
	Convey("sctl 自己断开调试器前先关闭(dismiss)它知道的弹框,不留下之后谁都处理不了的孤儿弹框", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)
		_, err = probe(m, Request{TabID: tabRef(4)})
		So(err, ShouldBeNil)
		dismissals := recordDismissals(cdp, nil)

		Convey("page detach:先 dismiss 再断开", func() {
			openDialog(m, 3, "confirm", "leave?")
			_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(dismissals(), ShouldResemble, []dismissal{{tab: 3, params: `{"accept":false}`, detachesBefore: 0}})
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
		})

		Convey("page detach --all:只关闭有弹框的标签页上的弹框,都在断开之前", func() {
			openDialog(m, 4, "alert", "hi")
			_, err := m.Do(context.Background(), Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
			So(err, ShouldBeNil)
			So(dismissals(), ShouldResemble, []dismissal{{tab: 4, params: `{"accept":false}`, detachesBefore: 0}})
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
		})

		Convey("5 分钟空闲断开:先 dismiss 再断开", func() {
			openDialog(m, 3, "prompt", "name?")
			clock.Advance(idleTimeout)
			So(dismissals(), ShouldResemble, []dismissal{{tab: 3, params: `{"accept":false}`, detachesBefore: 0}})
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}, {4}})
		})

		Convey("没有弹框或弹框已经关闭时断开不发出 handleJavaScriptDialog", func() {
			openDialog(m, 3, "alert", "hi")
			closeDialog(m, 3)
			_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			clock.Advance(idleTimeout)
			So(dismissals(), ShouldBeEmpty)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}, {4}})
		})

		Convey("关闭弹框失败时照常断开", func() {
			recordDismissals(cdp, func() error { return &Error{Code: generated.ErrorCodeInvalidRequest, Message: "No dialog is showing"} })
			openDialog(m, 3, "alert", "hi")
			res, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":3,"tabIds":[3]}`)
		})

		Convey("附加途中弹框打开、放弃这次附加时,先关闭它再断开;命令照第 3 期返回 DIALOG_OPEN", func() {
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				switch cmd.Method {
				case "Runtime.enable":
					openDialog(m, 5, "alert", "during attach")
					<-ctx.Done() // 弹框卡住渲染进程,之后的附加命令不再回应
					return nil, ctx.Err()
				case "Page.handleJavaScriptDialog":
					So(cdp.detachCalls(), ShouldBeEmpty)
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := probe(m, Request{TabID: tabRef(5)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(cdp.methods(5), ShouldContain, "Page.handleJavaScriptDialog")
			So(cdp.detachCalls(), ShouldResemble, [][]int{{5}})
		})

		Convey("附加途中调试器被动分离(用户关掉提示条)时不关闭附加途中打开的弹框:分离之后再发命令会让扩展重新附加", func() {
			entered := make(chan struct{})
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Runtime.enable" {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return json.RawMessage(`{}`), nil
			})
			done := make(chan error, 1)
			go func() {
				// debug 命令在弹框打开时照常执行,弹框不会中断这次附加。
				_, err := m.Do(context.Background(), Request{Action: "debug.console", TabID: tabRef(5)})
				done <- err
			}()
			<-entered
			openDialog(m, 5, "alert", "during attach")
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":5,"reason":"canceled_by_user"}`))
			So(errorCode(<-done), ShouldEqual, generated.ErrorCodeDebuggerDetached)
			So(cdp.methods(5), ShouldNotContain, "Page.handleJavaScriptDialog")
		})

		Convey("其余时候仍不自动处理弹框:debug stop、调试器被动分离都不发出 handleJavaScriptDialog", func() {
			openDialog(m, 3, "alert", "hi")
			_, err := m.Do(context.Background(), Request{Action: "debug.stop", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"canceled_by_user"}`))
			So(dismissals(), ShouldBeEmpty)
		})
	})
}
