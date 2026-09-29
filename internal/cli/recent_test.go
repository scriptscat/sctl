package cli

import (
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestRecentList(t *testing.T) {
	result := `{"contentTrust":"untrusted-page-content","hasMore":false,"items":[
		{"sessionId":"tab-1","type":"tab","closedTime":1788251400000,"title":"Example\u001b[31m","url":"https://example.com/"},
		{"sessionId":"window-1","type":"window","closedTime":1788251300000,"title":"Another","url":"https://another.example/","tabCount":3}]}`

	Convey("sctl recent list 列出最近关闭的标签页和窗口", t, func() {
		Convey("表格给出 recent restore 要用的会话 ID、类型、关闭时间、窗口的标签页数、标题和 URL;不给 --limit 时不下发 limit", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("recent", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "recent.list")
			So(string(req.Input), ShouldEqual, `{}`)
			for _, want := range []string{"SESSION", "TYPE", "CLOSED", "TABS", "TITLE", "URL", "tab-1", "window-1", "https://another.example/",
				time.UnixMilli(1788251400000).Local().Format(time.DateTime)} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "\x1b")
		})

		Convey("--limit 下发为 limit;还有未返回的项时在 stderr 提示可调大到 25", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","hasMore":true,"items":[
				{"sessionId":"tab-1","type":"tab","closedTime":1788251400000,"title":"Example","url":"https://example.com/"}]}`)})
			code, _, errOut := runCLICapture("recent", "list", "--limit", "1")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"limit":1}`)
			So(errOut, ShouldContainSubstring, "--limit (at most 25)")
		})

		Convey("--limit 越界(1-25 之外)时退出码 3,且不发起调用", func() {
			for _, limit := range []string{"0", "26"} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
				code, _ := runCLI("recent", "list", "--limit", limit)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("没有最近关闭的项时给出说明", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","hasMore":false,"items":[]}`)})
			code, out := runCLI("recent", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "(no recently closed items)")
		})
	})
}

func TestRecentRestore(t *testing.T) {
	Convey("sctl recent restore 恢复一项并输出恢复出的标签页或窗口 ID", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabId":42}`)})
		code, out := runCLI("recent", "restore", "tab-1")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "recent.restore")
		So(string(req.Input), ShouldEqual, `{"sessionId":"tab-1"}`)
		So(out, ShouldContainSubstring, "restored tab 42")
	})
}
