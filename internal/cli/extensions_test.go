package cli

import (
	"context"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestExtensionsList(t *testing.T) {
	result := `{"contentTrust":"untrusted-page-content","items":[
		{"id":"aaaabbbbccccddddeeeeffffgggghhhh","name":"Tab Tidy","version":"0.9.3","enabled":true,"type":"extension","installType":"development","mayDisable":true},
		{"id":"mhoplkcgjabnfdieanpgkcbjlhmoedfa","name":"Helper\u001b[31m","version":"3.1.0","enabled":false,"type":"extension","installType":"admin","mayDisable":false,"browser":{"id":"b1","name":"edge-b"}}]}`

	Convey("sctl extensions list 列出已安装的扩展", t, func() {
		Convey("不带参数下发空输入;表格含 ID、名称、版本、启用、类型、安装方式与能否禁用,名称经 terminalSafe,汇总时多一列 BROWSER", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("extensions", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "extensions.list")
			So(string(req.Input), ShouldEqual, `{}`)
			for _, want := range []string{"ID", "NAME", "VERSION", "ENABLED", "TYPE", "INSTALL", "MAY DISABLE", "BROWSER",
				"aaaabbbbccccddddeeeeffffgggghhhh", "Tab Tidy", "0.9.3", "development", "admin", "edge-b"} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "\x1b")
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("extensions", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust"`)
		})
	})
}

func TestExtensionsEnableDisable(t *testing.T) {
	Convey("sctl extensions enable/disable", t, func() {
		Convey("enable 是 L0:下发 ID,不带 confirm", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"abc","enabled":true}`)})
			code, out := runCLI("extensions", "enable", "abc")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "extensions.enable")
			So(string(req.Input), ShouldEqual, `{"id":"abc"}`)
			So(out, ShouldContainSubstring, "enabled extension abc")
		})

		Convey("disable 是 L1:加 --yes 下发 confirm: true", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"abc","enabled":false}`)})
			code, out := runCLI("extensions", "disable", "abc", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "extensions.disable")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"id":"abc"}`)
			So(out, ShouldContainSubstring, "disabled extension abc")
		})

		Convey("disable 不加 --yes 时不带 confirm,daemon 的 CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code: "CONFIRMATION_REQUIRED", Message: "requires explicit confirmation: pass --yes on the command line or confirm: true in the input",
			}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "extensions", "disable", "abc")
			So(code, ShouldEqual, exitError)
			So(string(req.Input), ShouldEqual, `{"id":"abc"}`)
			So(err.Error(), ShouldContainSubstring, "--yes")
		})

		Convey("disable 的帮助提醒:禁用 ScriptCat 会断开它与 daemon 的连接", func() {
			_, out := runCLI("extensions", "disable", "--help")
			So(out, ShouldContainSubstring, "ScriptCat")
			So(out, ShouldContainSubstring, "disconnect")
		})

		Convey("NOT_FOUND 与 INVALID_REQUEST(sctl Browser 自己、策略安装)映射为退出码 3", func() {
			for _, code := range []string{"NOT_FOUND", "INVALID_REQUEST"} {
				stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: code}})
				got, _ := runCLI("extensions", "disable", "abc", "--yes")
				So(got, ShouldEqual, exitError)
			}
		})

		Convey("缺 ID 时不发起调用", func() {
			for _, verb := range []string{"enable", "disable"} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{}`)})
				code, _ := runCLI("extensions", verb)
				So(code, ShouldNotEqual, exitOK)
				So(req.Action, ShouldBeEmpty)
			}
		})
	})
}

func TestExtensionsUninstall(t *testing.T) {
	Convey("sctl extensions uninstall <id> 是 L2:阻塞到浏览器里批准并在 Chrome 的确认框里确认", t, func() {
		online := []control.BrowserInfo{{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "chrome-a", Online: true}}
		uninstalled := control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","id":"abc","name":"Tab Tidy\u001b[2J"}`)}

		Convey("下发 ID,不带 confirm;等待提示写到 stderr 并点名浏览器;成功时输出被卸载扩展的名称和 ID", func() {
			req := stubDaemonApproval(t, online, uninstalled)
			code, out, errOut := runCLICapture("extensions", "uninstall", "abc")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "extensions.uninstall")
			So(string(req.Input), ShouldEqual, `{"id":"abc"}`)
			So(errOut, ShouldContainSubstring, "waiting for approval in browser chrome-a")
			So(out, ShouldNotContainSubstring, "waiting")
			So(out, ShouldContainSubstring, "uninstalled extension abc")
			So(out, ShouldContainSubstring, "Tab Tidy")
			So(out, ShouldNotContainSubstring, "\x1b")
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonApproval(t, online, uninstalled)
			code, out, _ := runCLICapture("extensions", "uninstall", "abc", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"id": "abc"`)
		})

		Convey("拒绝或在 Chrome 里取消退出码 1,超时或作废退出码 2,NOT_FOUND 与 INVALID_REQUEST 退出码 3", func() {
			for code, want := range map[string]int{
				"USER_REJECTED":     exitRejected,
				"OPERATION_EXPIRED": exitVoided,
				"NOT_FOUND":         exitError,
				"INVALID_REQUEST":   exitError,
			} {
				stubDaemonApproval(t, online, control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: code}})
				got, _ := runCLI("extensions", "uninstall", "abc")
				So(got, ShouldEqual, want)
			}
		})

		Convey("一次只卸载一个扩展:缺 ID 或多于一个 ID 时不发起调用", func() {
			for _, args := range [][]string{{}, {"a", "b"}} {
				rec := stubDaemonRecording(t, uninstalled)
				code, _ := runCLI(append([]string{"extensions", "uninstall"}, args...)...)
				So(code, ShouldNotEqual, exitOK)
				So(rec.snapshot(), ShouldBeEmpty)
			}
		})
	})

	Convey("Ctrl-C 取消等待中的卸载:退出码 2,并说明操作已作废", t, func() {
		arrived := stubDaemonHolding(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-arrived
			cancel()
		}()
		code, _, _, err := runCLIContext(ctx, strings.NewReader(""), "extensions", "uninstall", "abc", "--browser", "work")
		So(code, ShouldEqual, exitVoided)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "voided")
	})
}
