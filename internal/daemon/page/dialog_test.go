package page

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
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
