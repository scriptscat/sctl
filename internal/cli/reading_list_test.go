package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// TestReadingListList 覆盖 sctl reading-list list:表格、筛选、--limit 与 hasMore 提示。
func TestReadingListList(t *testing.T) {
	Convey("sctl reading-list list 列出阅读列表", t, func() {
		created := time.Date(2026, 9, 1, 8, 30, 0, 0, time.UTC)
		updated := time.Date(2026, 9, 2, 9, 45, 0, 0, time.UTC)
		result := `{"contentTrust":"untrusted-page-content","hasMore":false,"entries":[
			{"url":"https://a.example/","title":"Article A","read":false,"createdAt":` + strconv.FormatInt(created.UnixMilli(), 10) + `,"updatedAt":` + strconv.FormatInt(updated.UnixMilli(), 10) + `}
		]}`

		Convey("默认表格含 URL、标题、是否已读、添加与更新时间,默认只取 100 条且不带筛选", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out, errOut := runCLICapture("reading-list", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "readingList.list")
			So(string(req.Input), ShouldEqual, `{"limit":100}`)
			for _, want := range []string{"URL", "TITLE", "READ", "ADDED", "UPDATED", "https://a.example/", "Article A", "false",
				created.Local().Format(time.DateTime), updated.Local().Format(time.DateTime)} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "BROWSER")
			So(errOut, ShouldNotContainSubstring, "--limit")
		})

		Convey("--unread 与 --read 分别下发 read:false 与 read:true,二者不能同时给", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("reading-list", "list", "--unread")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"limit":100,"read":false}`)

			code, _ = runCLI("reading-list", "list", "--read")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"limit":100,"read":true}`)

			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ = runCLI("reading-list", "list", "--read", "--unread")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("--limit 在 1..1000 内原样下发,超出范围时退出码 3 且不发起调用", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			for _, limit := range []string{"1", "1000"} {
				code, _ := runCLI("reading-list", "list", "--limit", limit)
				So(code, ShouldEqual, exitOK)
				So(string(req.Input), ShouldEqual, `{"limit":`+limit+`}`)
			}

			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(result)})
			for _, limit := range []string{"0", "1001", "-1"} {
				code, _, _, err := runCLIResult(strings.NewReader(""), "reading-list", "list", "--limit", limit)
				So(code, ShouldEqual, exitError)
				So(err.Error(), ShouldContainSubstring, "--limit")
			}
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("hasMore 为 true 时表格之后在 stderr 提示还有更多条目,stdout 只有表格", func() {
			more := strings.Replace(result, `"hasMore":false`, `"hasMore":true`, 1)
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(more)})
			code, out, errOut := runCLICapture("reading-list", "list")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "--limit")
			So(out, ShouldNotContainSubstring, "--limit")
		})

		Convey("-o json 原样输出结果,不另加提示", func() {
			more := strings.Replace(result, `"hasMore":false`, `"hasMore":true`, 1)
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(more)})
			code, out, errOut := runCLICapture("reading-list", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"hasMore": true`)
			So(out, ShouldContainSubstring, "untrusted-page-content")
			So(errOut, ShouldNotContainSubstring, "--limit")
		})

		Convey("网页控制的标题里的控制字符以转义形式打印", func() {
			hostile := `{"contentTrust":"untrusted-page-content","hasMore":false,"entries":[
				{"url":"https://a.example/","title":"\u001b]0;pwned\u0007Evil\nFAKE ROW","read":true,"createdAt":0,"updatedAt":0}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(hostile)})
			code, out := runCLI("reading-list", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(strings.Split(strings.TrimRight(out, "\n"), "\n"), ShouldHaveLength, 2)
			So(out, ShouldContainSubstring, `\x1b]0;pwned\aEvil\nFAKE ROW`)
		})

		Convey("多实例汇总结果给表格加 BROWSER 列", func() {
			merged := `{"contentTrust":"untrusted-page-content","hasMore":false,"entries":[
				{"url":"https://a.example/","title":"A","read":false,"createdAt":0,"updatedAt":0,"browser":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"chrome-a"}},
				{"url":"https://b.example/","title":"B","read":true,"createdAt":0,"updatedAt":0,"browser":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"chrome-b"}}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})
			code, out := runCLI("reading-list", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "chrome-a")
			So(out, ShouldContainSubstring, "chrome-b")
		})

		Convey("浏览器不提供阅读列表 API → UNSUPPORTED,退出码 3 且消息写明缺少的 API", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "UNSUPPORTED", Message: "this browser does not provide chrome.readingList"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "reading-list", "list")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "chrome.readingList")
		})
	})
}

// TestReadingListAdd 覆盖 sctl reading-list add <url> [--title T]。
func TestReadingListAdd(t *testing.T) {
	Convey("sctl reading-list add 添加一条阅读列表条目", t, func() {
		Convey("只给 URL 时只下发 url,标题的默认值由扩展决定", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"url":"https://a.example/","title":"https://a.example/"}`)})
			code, out := runCLI("reading-list", "add", "https://a.example/")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "readingList.add")
			So(string(req.Input), ShouldEqual, `{"url":"https://a.example/"}`)
			So(out, ShouldContainSubstring, "URL")
			So(out, ShouldContainSubstring, "https://a.example/")
		})

		Convey("--title 透传", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"url":"https://a.example/","title":"A"}`)})
			code, _ := runCLI("reading-list", "add", "https://a.example/", "--title", "A")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"title":"A","url":"https://a.example/"}`)
		})

		Convey("URL 已在列表中 → CONFLICT,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "CONFLICT", Message: "https://a.example/ is already in the reading list"}})
			code, _ := runCLI("reading-list", "add", "https://a.example/")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestReadingListMarkRead 覆盖 sctl reading-list mark-read <url>... [--unread]。
func TestReadingListMarkRead(t *testing.T) {
	Convey("sctl reading-list mark-read 标记已读或未读", t, func() {
		Convey("默认标记为已读", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"urls":["https://a.example/","https://b.example/"],"read":true}`)})
			code, out := runCLI("reading-list", "mark-read", "https://a.example/", "https://b.example/")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "readingList.markRead")
			So(string(req.Input), ShouldEqual, `{"read":true,"urls":["https://a.example/","https://b.example/"]}`)
			So(out, ShouldContainSubstring, "https://b.example/")
		})

		Convey("--unread 标记为未读", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"urls":["https://a.example/"],"read":false}`)})
			code, _ := runCLI("reading-list", "mark-read", "https://a.example/", "--unread")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"read":false,"urls":["https://a.example/"]}`)
		})

		Convey("有 URL 不在列表中 → NOT_FOUND,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "https://x.example/ is not in the reading list"}})
			code, _ := runCLI("reading-list", "mark-read", "https://x.example/")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestReadingListRemove 覆盖 L1 命令 sctl reading-list rm <url>... 的 --yes 语义。
func TestReadingListRemove(t *testing.T) {
	Convey("sctl reading-list rm 移出阅读列表,需要 --yes 显式确认", t, func() {
		Convey("加 --yes 时下发 confirm: true", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"urls":["https://a.example/"]}`)})
			code, out := runCLI("reading-list", "rm", "https://a.example/", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "readingList.remove")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"urls":["https://a.example/"]}`)
			So(out, ShouldContainSubstring, "https://a.example/")
		})

		Convey("不加 --yes 时不带 confirm,daemon 的 CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code:    "CONFIRMATION_REQUIRED",
				Message: "readingList.remove requires explicit confirmation: pass --yes on the command line or confirm: true in the input",
			}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "reading-list", "rm", "https://a.example/")
			So(code, ShouldEqual, exitError)
			So(string(req.Input), ShouldEqual, `{"urls":["https://a.example/"]}`)
			So(err.Error(), ShouldContainSubstring, "--yes")
		})

		Convey("--browser 对 reading-list 子命令同样生效", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"urls":["https://a.example/"]}`)})
			code, _ := runCLI("reading-list", "rm", "https://a.example/", "--yes", "--browser", "chrome-b")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "chrome-b")
		})
	})
}
