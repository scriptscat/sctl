package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// TestTabsManage 覆盖 sctl tabs move/pin/unpin/mute/unmute/reload/duplicate。
func TestTabsManage(t *testing.T) {
	Convey("sctl tabs 整理子命令", t, func() {
		Convey("move 下发全部标签页 ID,--window 与 --index 只在给出时下发,-1 表示末尾", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1,2]}`)})
			code, out, _ := runCLICapture("tabs", "move", "1", "2", "--window", "20", "--index", "-1")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabs.move")
			So(string(req.Input), ShouldEqual, `{"index":-1,"tabIds":[1,2],"windowId":20}`)
			So(out, ShouldContainSubstring, "TAB ID")

			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1]}`)})
			code, _ = runCLI("tabs", "move", "1")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"tabIds":[1]}`)
		})

		Convey("move 拒绝小于 -1 的 --index,且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "tabs", "move", "1", "--index", "-2")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "--index")
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("pin/unpin/mute/unmute 各自路由到对应的方法", func() {
			for verb, action := range map[string]string{"pin": "tabs.pin", "unpin": "tabs.unpin", "mute": "tabs.mute", "unmute": "tabs.unmute"} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[4,5]}`)})
				code, _ := runCLI("tabs", verb, "4", "5")
				So(code, ShouldEqual, exitOK)
				So(req.Action, ShouldEqual, action)
				So(string(req.Input), ShouldEqual, `{"tabIds":[4,5]}`)
			}
		})

		Convey("非整数的标签页 ID 退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "tabs", "pin", "1", "x")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, `"x"`)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("reload 只在给出 --bypass-cache 时下发 bypassCache", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1]}`)})
			code, _ := runCLI("tabs", "reload", "1", "--bypass-cache")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabs.reload")
			So(string(req.Input), ShouldEqual, `{"bypassCache":true,"tabIds":[1]}`)

			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1]}`)})
			code, _ = runCLI("tabs", "reload", "1")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"tabIds":[1]}`)
		})

		Convey("duplicate 输出新标签页 ID;-o json 原样输出", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabId":99}`)})
			code, out, _ := runCLICapture("tabs", "duplicate", "7")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabs.duplicate")
			So(string(req.Input), ShouldEqual, `{"tabId":7}`)
			So(out, ShouldContainSubstring, "99")

			stubDaemon(t, control.CallResult{OK: true, Result: []byte(`{"tabId":99}`)})
			code, out, _ = runCLICapture("tabs", "duplicate", "7", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"tabId": 99`)
		})

		Convey("NOT_FOUND 退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no tab 404"}})
			code, _ := runCLI("tabs", "pin", "404")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestWindowsManage 覆盖 sctl windows open/close/focus/state。
func TestWindowsManage(t *testing.T) {
	Convey("sctl windows 整理子命令", t, func() {
		Convey("open 下发全部 URL 与 --state,输出新窗口 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windowId":77}`)})
			code, out, _ := runCLICapture("windows", "open", "https://a.example/", "https://b.example/", "--state", "maximized")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "windows.open")
			So(string(req.Input), ShouldEqual, `{"state":"maximized","urls":["https://a.example/","https://b.example/"]}`)
			So(out, ShouldContainSubstring, "WINDOW ID")
			So(out, ShouldContainSubstring, "77")
		})

		Convey("open 不带参数时下发空对象", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windowId":77}`)})
			code, _ := runCLI("windows", "open")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{}`)
		})

		Convey("无效的 --state 退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "windows", "open", "--state", "huge")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "huge")
			code, _, _, _ = runCLIResult(strings.NewReader(""), "windows", "state", "10", "huge")
			So(code, ShouldEqual, exitError)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("close 下发全部窗口 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windowIds":[10,20]}`)})
			code, out, _ := runCLICapture("windows", "close", "10", "20")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "windows.close")
			So(string(req.Input), ShouldEqual, `{"windowIds":[10,20]}`)
			So(out, ShouldContainSubstring, "WINDOW ID")
		})

		Convey("focus 下发单个窗口 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windowId":20}`)})
			code, _ := runCLI("windows", "focus", "20")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "windows.focus")
			So(string(req.Input), ShouldEqual, `{"windowId":20}`)
		})

		Convey("state 下发窗口 ID 与状态", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windowId":10,"state":"minimized"}`)})
			code, out, _ := runCLICapture("windows", "state", "10", "minimized")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "windows.state")
			So(string(req.Input), ShouldEqual, `{"state":"minimized","windowId":10}`)
			So(out, ShouldContainSubstring, "minimized")
		})

		Convey("窗口 ID 不是整数时退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _ := runCLI("windows", "close", "x")
			So(code, ShouldEqual, exitError)
			code, _ = runCLI("windows", "focus", "x")
			So(code, ShouldEqual, exitError)
			So(rec.snapshot(), ShouldBeEmpty)
		})
	})
}
