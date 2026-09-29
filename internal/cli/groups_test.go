package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// TestGroupsList 覆盖 sctl groups list 的表格、JSON 与 --window。
func TestGroupsList(t *testing.T) {
	Convey("sctl groups list 列出标签组", t, func() {
		result := `{"contentTrust":"untrusted-page-content","groups":[
			{"groupId":5,"windowId":10,"title":"Work","color":"blue","collapsed":false,"tabCount":3}
		]}`

		Convey("默认表格含组 ID、窗口 ID、标题、颜色、是否折叠与标签页数量,单实例没有 BROWSER 列", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("groups", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabGroups.list")
			So(string(req.Input), ShouldEqual, `{}`)
			for _, want := range []string{"GROUP ID", "WINDOW ID", "TITLE", "COLOR", "COLLAPSED", "TABS", "Work", "blue"} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "BROWSER")
		})

		Convey("--window 下发 windowId", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("groups", "list", "--window", "10")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"windowId":10}`)
		})

		Convey("网页控制的标题以转义形式打印", func() {
			hostile := `{"contentTrust":"untrusted-page-content","groups":[
				{"groupId":5,"windowId":10,"title":"\u001b[2JEvil\nFAKE","color":"red","collapsed":true,"tabCount":1}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(hostile)})
			code, out := runCLI("groups", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(strings.Split(strings.TrimRight(out, "\n"), "\n"), ShouldHaveLength, 2)
		})

		Convey("多实例汇总结果多一列 BROWSER;没有标签组时提示为空", func() {
			merged := `{"contentTrust":"untrusted-page-content","groups":[
				{"groupId":5,"windowId":10,"title":"Work","color":"blue","collapsed":false,"tabCount":3,"browser":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"chrome-a"}}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})
			code, out := runCLI("groups", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "chrome-a")

			stubDaemon(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","groups":[]}`)})
			code, out = runCLI("groups", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "(no groups)")
		})

		Convey("-o json 原样输出", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("groups", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"groupId": 5`)
		})
	})
}

// TestGroupsWrite 覆盖 sctl groups create/add/edit/ungroup。
func TestGroupsWrite(t *testing.T) {
	Convey("sctl groups 写子命令", t, func() {
		Convey("create 下发全部标签页 ID、--title 与 --color,输出组 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":12}`)})
			code, out, _ := runCLICapture("groups", "create", "1", "2", "--title", "Work", "--color", "blue")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabGroups.create")
			So(string(req.Input), ShouldEqual, `{"color":"blue","tabIds":[1,2],"title":"Work"}`)
			So(out, ShouldContainSubstring, "GROUP ID")
			So(out, ShouldContainSubstring, "12")

			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":12}`)})
			code, _ = runCLI("groups", "create", "1")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"tabIds":[1]}`)
		})

		Convey("无效颜色与非整数 ID 退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "groups", "create", "1", "--color", "magenta")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "magenta")
			code, _, _, _ = runCLIResult(strings.NewReader(""), "groups", "edit", "5", "--color", "magenta")
			So(code, ShouldEqual, exitError)
			code, _, _, _ = runCLIResult(strings.NewReader(""), "groups", "create", "x")
			So(code, ShouldEqual, exitError)
			code, _, _, _ = runCLIResult(strings.NewReader(""), "groups", "add", "x", "1")
			So(code, ShouldEqual, exitError)
			code, _, _, _ = runCLIResult(strings.NewReader(""), "groups", "ungroup", "1", "x")
			So(code, ShouldEqual, exitError)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("add 下发组 ID 与全部标签页 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":5,"tabIds":[3,4]}`)})
			code, out, _ := runCLICapture("groups", "add", "5", "3", "4")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabGroups.add")
			So(string(req.Input), ShouldEqual, `{"groupId":5,"tabIds":[3,4]}`)
			So(out, ShouldContainSubstring, "TAB ID")
		})

		Convey("edit 只下发给出的字段,--collapse/--expand 映射到 collapsed", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":5}`)})
			code, _ := runCLI("groups", "edit", "5", "--title", "New", "--color", "red", "--collapse")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabGroups.edit")
			So(string(req.Input), ShouldEqual, `{"collapsed":true,"color":"red","groupId":5,"title":"New"}`)

			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":5}`)})
			code, _ = runCLI("groups", "edit", "5", "--expand")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"collapsed":false,"groupId":5}`)

			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"groupId":5}`)})
			code, _ = runCLI("groups", "edit", "5", "--title", "")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"groupId":5,"title":""}`)
		})

		Convey("edit 没有任何修改、或同时给 --collapse 与 --expand 时退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "groups", "edit", "5")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "nothing to change")
			code, _, _, _ = runCLIResult(strings.NewReader(""), "groups", "edit", "5", "--collapse", "--expand")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("ungroup 下发全部标签页 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1,2]}`)})
			code, out, _ := runCLICapture("groups", "ungroup", "1", "2")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabGroups.ungroup")
			So(string(req.Input), ShouldEqual, `{"tabIds":[1,2]}`)
			So(out, ShouldContainSubstring, "TAB ID")
		})

		Convey("NOT_FOUND 与 INVALID_REQUEST 退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no tab group 404"}})
			code, _ := runCLI("groups", "add", "404", "1")
			So(code, ShouldEqual, exitError)
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: "tabs are in different windows"}})
			code, _ = runCLI("groups", "create", "1", "3")
			So(code, ShouldEqual, exitError)
		})
	})
}
