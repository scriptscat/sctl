package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestDownloadsList(t *testing.T) {
	result := `{"contentTrust":"untrusted-page-content","hasMore":true,"items":[
		{"id":7,"url":"https://files.example/a.zip","filename":"/home/u/Downloads/a.zip","state":"complete","bytesReceived":2048,"totalBytes":2048,"startTime":1788251400000,"exists":true},
		{"id":8,"url":"https://files.example/b\u001b[31m.zip","filename":"/home/u/Downloads/b.zip","state":"in_progress","bytesReceived":10,"totalBytes":-1,"startTime":1788251300000,"exists":false}]}`

	Convey("sctl downloads list 列出下载", t, func() {
		Convey("不带参数只下发默认 limit;表格含 ID、状态、进度、开始时间、是否在磁盘上,URL 经 terminalSafe,hasMore 提示走 stderr", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out, errOut := runCLICapture("downloads", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads.list")
			So(string(req.Input), ShouldEqual, `{"limit":100}`)
			for _, want := range []string{"ID", "STATE", "RECEIVED", "TOTAL", "STARTED", "EXISTS", "https://files.example/a.zip", "/home/u/Downloads/a.zip", "complete", "2048", "in_progress"} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "\x1b")
			So(errOut, ShouldContainSubstring, "--limit")
		})

		Convey("--state、--query、--limit 下发为 state、query、limit", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("downloads", "list", "--state", "interrupted", "--query", "report", "--limit", "5")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"limit":5,"query":"report","state":"interrupted"}`)
		})

		Convey("--state 取值不合法或 --limit 越界时退出码 3,且不发起调用", func() {
			for _, args := range [][]string{{"--state", "paused"}, {"--limit", "0"}, {"--limit", "1001"}} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
				code, _ := runCLI(append([]string{"downloads", "list"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("downloads", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust"`)
		})
	})
}

func TestDownloadsStart(t *testing.T) {
	Convey("sctl downloads start <url> 开始下载并输出下载 ID", t, func() {
		Convey("只给 URL 时不带 filename", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":42}`)})
			code, out := runCLI("downloads", "start", "https://files.example/a.zip")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads.start")
			So(string(req.Input), ShouldEqual, `{"url":"https://files.example/a.zip"}`)
			So(out, ShouldContainSubstring, "42")
		})

		Convey("--filename 原样下发,合法性由扩展检查", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":42}`)})
			code, _ := runCLI("downloads", "start", "https://files.example/a.zip", "--filename", "sub/a.zip")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"filename":"sub/a.zip","url":"https://files.example/a.zip"}`)
		})

		Convey("扩展以 INVALID_REQUEST 拒绝绝对路径时退出码为 3", func() {
			stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: "filename must be a relative path"}})
			code, _ := runCLI("downloads", "start", "https://files.example/a.zip", "--filename", "/etc/x")
			So(code, ShouldEqual, exitError)
		})

		Convey("缺少 URL 是参数错误,不发起调用", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":1}`)})
			code, _ := runCLI("downloads", "start")
			So(code, ShouldNotEqual, exitOK)
			So(req.Action, ShouldBeEmpty)
		})
	})
}

func TestDownloadsPauseResumeShow(t *testing.T) {
	Convey("pause、resume、show 各下发对应方法和数字 ID", t, func() {
		for _, verb := range []string{"pause", "resume", "show"} {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":5}`)})
			code, out := runCLI("downloads", verb, "5")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads."+verb)
			So(string(req.Input), ShouldEqual, `{"id":5}`)
			So(out, ShouldContainSubstring, "5")
		}
	})

	Convey("ID 不是非负整数时退出码 3,且不发起调用", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":5}`)})
		for _, id := range []string{"abc", "1.5"} {
			code, _ := runCLI("downloads", "pause", id)
			So(code, ShouldEqual, exitError)
		}
		code, _ := runCLI("downloads", "pause", "--", "-1")
		So(code, ShouldEqual, exitError)
		So(req.Action, ShouldBeEmpty)
	})

	Convey("未知 ID 的 NOT_FOUND 映射为退出码 3", t, func() {
		stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no download 9"}})
		code, _ := runCLI("downloads", "show", "9")
		So(code, ShouldEqual, exitError)
	})
}

func TestDownloadsL1Commands(t *testing.T) {
	Convey("cancel、erase、delete-file 是 L1 命令,需要 --yes", t, func() {
		Convey("cancel 加 --yes 下发 confirm: true 与 id", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":5}`)})
			code, _ := runCLI("downloads", "cancel", "5", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads.cancel")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"id":5}`)
		})

		Convey("erase 加 --yes 下发全部 ID", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"ids":[1,2]}`)})
			code, out := runCLI("downloads", "erase", "1", "2", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads.erase")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"ids":[1,2]}`)
			So(out, ShouldContainSubstring, "2")
		})

		Convey("delete-file 加 --yes 下发 downloads.deleteFile", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":5}`)})
			code, _ := runCLI("downloads", "delete-file", "5", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "downloads.deleteFile")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"id":5}`)
		})

		Convey("不加 --yes 时不带 confirm,daemon 的 CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			for _, args := range [][]string{{"cancel", "5"}, {"erase", "5"}, {"delete-file", "5"}} {
				req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
					Code: "CONFIRMATION_REQUIRED", Message: "requires explicit confirmation: pass --yes on the command line or confirm: true in the input",
				}})
				code, _, _, err := runCLIResult(strings.NewReader(""), append([]string{"downloads"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(string(req.Input), ShouldNotContainSubstring, "confirm")
				So(err.Error(), ShouldContainSubstring, "--yes")
			}
		})
	})
}
