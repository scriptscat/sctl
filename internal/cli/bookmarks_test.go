package cli

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

const bookmarkListResult = `{"contentTrust":"untrusted-page-content","hasMore":false,"nodes":[
	{"id":"10","type":"folder","title":"Dev","parentId":"1","index":0,"addedAt":1000,"childCount":2},
	{"id":"14","type":"bookmark","title":"News","url":"https://news.example/","parentId":"1","index":1,"addedAt":2000}
]}`

// TestBookmarksList 覆盖 sctl bookmarks list [--folder ID] [--recursive]。
func TestBookmarksList(t *testing.T) {
	Convey("sctl bookmarks list 列出书签节点", t, func() {
		Convey("默认下发 limit:100、不带 folder 与 recursive,表格含类型、标题、URL、父文件夹、位置与子项数", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(bookmarkListResult)})
			code, out, errOut := runCLICapture("bookmarks", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.list")
			So(string(req.Input), ShouldEqual, `{"limit":100}`)
			for _, want := range []string{"ID", "TYPE", "TITLE", "URL", "PARENT", "INDEX", "ADDED", "CHILDREN", "folder", "bookmark", "Dev", "https://news.example/"} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "BROWSER")
			So(errOut, ShouldBeEmpty)
		})

		Convey("--folder 与 --recursive 透传;递归列表不带 limit,且不能与 --limit 同用", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(bookmarkListResult)})
			code, _ := runCLI("bookmarks", "list", "--folder", "1", "--recursive")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"folder":"1","recursive":true}`)

			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(bookmarkListResult)})
			code, _ = runCLI("bookmarks", "list", "--recursive", "--limit", "5")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("--limit 超出 1..1000 时退出码 3 且不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(bookmarkListResult)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "bookmarks", "list", "--limit", "1001")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "--limit")
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("hasMore 为 true 时在 stderr 提示;-o json 原样输出", func() {
			more := strings.Replace(bookmarkListResult, `"hasMore":false`, `"hasMore":true`, 1)
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(more)})
			code, out, errOut := runCLICapture("bookmarks", "list")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "--limit")
			So(out, ShouldNotContainSubstring, "--limit")

			code, out, errOut = runCLICapture("bookmarks", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust": "untrusted-page-content"`)
			So(errOut, ShouldNotContainSubstring, "--limit")
		})

		Convey("网页控制的标题与 URL 里的控制字符以转义形式打印", func() {
			hostile := `{"contentTrust":"untrusted-page-content","hasMore":false,"nodes":[
				{"id":"14","type":"bookmark","title":"\u001b]0;pwned\u0007Evil\nFAKE ROW","url":"https://a.example/\u001b[2J","parentId":"1","index":0}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(hostile)})
			code, out := runCLI("bookmarks", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(strings.Split(strings.TrimRight(out, "\n"), "\n"), ShouldHaveLength, 2)
		})

		Convey("多实例汇总结果给表格加 BROWSER 列", func() {
			merged := `{"contentTrust":"untrusted-page-content","hasMore":false,"nodes":[
				{"id":"14","type":"bookmark","title":"A","url":"https://a.example/","parentId":"1","index":0,"browser":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"chrome-a"}}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})
			code, out := runCLI("bookmarks", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "chrome-a")
		})

		Convey("文件夹不存在 → NOT_FOUND,文件夹 ID 是书签 → INVALID_REQUEST,退出码都是 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no bookmark 999"}})
			code, _ := runCLI("bookmarks", "list", "--folder", "999")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestBookmarksSearch 覆盖 sctl bookmarks search <关键词> [--limit N]。
func TestBookmarksSearch(t *testing.T) {
	Convey("sctl bookmarks search 按关键词搜索并显示文件夹路径", t, func() {
		result := `{"contentTrust":"untrusted-page-content","hasMore":false,"nodes":[
			{"id":"13","type":"bookmark","title":"Go Blog","url":"https://go.dev/blog/","parentId":"12","index":0,"path":["Bookmarks bar","Dev","Go"]}
		]}`
		Convey("下发 query 与 limit,表格有 PATH 列,路径层级用 / 连接", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("bookmarks", "search", "go")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.search")
			So(string(req.Input), ShouldEqual, `{"limit":100,"query":"go"}`)
			So(out, ShouldContainSubstring, "PATH")
			So(out, ShouldContainSubstring, "Bookmarks bar / Dev / Go")
			So(out, ShouldContainSubstring, "https://go.dev/blog/")
		})

		Convey("必须恰好一个关键词;--limit 越界时不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("bookmarks", "search")
			So(code, ShouldNotEqual, exitOK)
			code, _ = runCLI("bookmarks", "search", "go", "--limit", "0")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("路径里的控制字符以转义形式打印", func() {
			hostile := strings.Replace(result, `"Dev"`, `"\u001b[2JDev"`, 1)
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(hostile)})
			code, out := runCLI("bookmarks", "search", "go")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
		})
	})
}

// TestBookmarksAdd 覆盖 add 与 mkdir。
func TestBookmarksAdd(t *testing.T) {
	Convey("sctl bookmarks add / mkdir 新建书签与文件夹并输出新 ID", t, func() {
		Convey("add 只给 URL 时只下发 url", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"100"}`)})
			code, out := runCLI("bookmarks", "add", "https://a.example/")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.add")
			So(string(req.Input), ShouldEqual, `{"url":"https://a.example/"}`)
			So(out, ShouldContainSubstring, "100")
		})

		Convey("add 的 --title、--folder、--index 透传,--index 0 也照常下发", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"100"}`)})
			code, _ := runCLI("bookmarks", "add", "https://a.example/", "--title", "A", "--folder", "10", "--index", "0")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"folder":"10","index":0,"title":"A","url":"https://a.example/"}`)
		})

		Convey("mkdir 下发标题、folder 与 index", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"101"}`)})
			code, out := runCLI("bookmarks", "mkdir", "Reading", "--folder", "1", "--index", "2")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.mkdir")
			So(string(req.Input), ShouldEqual, `{"folder":"1","index":2,"title":"Reading"}`)
			So(out, ShouldContainSubstring, "101")
		})

		Convey("负数 --index 在发起调用前被拒绝", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{"id":"1"}`)})
			code, _, _, err := runCLIResult(strings.NewReader(""), "bookmarks", "add", "https://a.example/", "--index", "-1")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "--index")
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("目标文件夹不存在 → NOT_FOUND,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no bookmark 999"}})
			code, _ := runCLI("bookmarks", "add", "https://a.example/", "--folder", "999")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestBookmarksMove 覆盖 sctl bookmarks move <ID>... --folder ID [--index I]。
func TestBookmarksMove(t *testing.T) {
	Convey("sctl bookmarks move 移动多个节点", t, func() {
		Convey("下发全部 ID、目标文件夹与 index", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"ids":["14","20"]}`)})
			code, out := runCLI("bookmarks", "move", "14", "20", "--folder", "10", "--index", "0")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.move")
			So(string(req.Input), ShouldEqual, `{"folder":"10","ids":["14","20"],"index":0}`)
			So(out, ShouldContainSubstring, "20")
		})

		Convey("缺 --folder 或缺 ID 时不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{"ids":[]}`)})
			code, _ := runCLI("bookmarks", "move", "14")
			So(code, ShouldNotEqual, exitOK)
			code, _ = runCLI("bookmarks", "move", "--folder", "10")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("受保护节点 → INVALID_REQUEST,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: "bookmark 1 is the root or a built-in top-level folder"}})
			code, _ := runCLI("bookmarks", "move", "1", "--folder", "10")
			So(code, ShouldEqual, exitError)
		})
	})
}

// TestBookmarksEdit 覆盖 sctl bookmarks edit <ID> [--title T] [--url U]。
func TestBookmarksEdit(t *testing.T) {
	Convey("sctl bookmarks edit 修改标题或 URL", t, func() {
		Convey("只下发给了的字段", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"id":"14"}`)})
			code, out := runCLI("bookmarks", "edit", "14", "--title", "Renamed")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.edit")
			So(string(req.Input), ShouldEqual, `{"id":"14","title":"Renamed"}`)
			So(out, ShouldContainSubstring, "14")

			code, _ = runCLI("bookmarks", "edit", "14", "--url", "https://x.example/", "--title", "")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"id":"14","title":"","url":"https://x.example/"}`)
		})

		Convey("两个字段都没给时不发起调用", func() {
			rec := stubDaemonRecording(t, control.CallResult{OK: true, Result: []byte(`{"id":"14"}`)})
			code, _ := runCLI("bookmarks", "edit", "14")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})

		Convey("给文件夹设 URL → INVALID_REQUEST,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: "bookmark 10 is a folder and cannot have a URL"}})
			code, _ := runCLI("bookmarks", "edit", "10", "--url", "https://x.example/")
			So(code, ShouldEqual, exitError)
		})
	})
}

// stubDaemonApproval 起一个假 daemon:/control/browsers 返回给定实例列表,/control/call 记录请求,
// 请求进入审批(请求方要了 pending 行时先写出它)后返回给定结果。
func stubDaemonApproval(t *testing.T, list []control.BrowserInfo, result control.CallResult) *control.CallRequest {
	t.Helper()
	return stubDaemonL2(t, list, result, true)
}

// stubDaemonL2 与 stubDaemonApproval 相同;entersApproval 为 false 时模拟审批前的校验失败:不写 pending 行,直接给出结果。
func stubDaemonL2(t *testing.T, list []control.BrowserInfo, result control.CallResult, entersApproval bool) *control.CallRequest {
	t.Helper()
	captured := &control.CallRequest{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathBrowsers, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(control.BrowsersResult{Browsers: list})
	})
	mux.HandleFunc(control.PathCall, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(captured)
		if entersApproval && captured.ReportPending {
			_ = json.NewEncoder(w).Encode(control.CallResult{Pending: true})
		}
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	t.Setenv("SCTL_BROWSER", "")
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return captured
}

// TestBookmarksRemove 覆盖 sctl bookmarks rm <ID>...:L2,阻塞等待浏览器里的审批。
func TestBookmarksRemove(t *testing.T) {
	Convey("sctl bookmarks rm 在浏览器里批准后删除书签", t, func() {
		online := []control.BrowserInfo{
			{ID: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", Name: "chrome-a", Online: true},
			{ID: "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", Name: "edge-b", Online: false},
		}
		removed := control.CallResult{OK: true, Result: []byte(`{"ids":["14","10"],"bookmarks":3,"folders":2}`)}

		Convey("下发全部 ID,不带 confirm;等待提示写到 stderr 并点名唯一在线的浏览器;成功时输出删除的书签数与文件夹数", func() {
			req := stubDaemonApproval(t, online, removed)
			code, out, errOut := runCLICapture("bookmarks", "rm", "14", "10")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "bookmarks.remove")
			So(req.Browser, ShouldEqual, "")
			So(string(req.Input), ShouldEqual, `{"ids":["14","10"]}`)
			So(errOut, ShouldContainSubstring, "waiting for approval in browser chrome-a")
			So(errOut, ShouldContainSubstring, "Ctrl-C")
			So(out, ShouldNotContainSubstring, "waiting")
			for _, want := range []string{"BOOKMARKS", "FOLDERS", "3", "2"} {
				So(out, ShouldContainSubstring, want)
			}
		})

		Convey("给了 --browser 时提示点名这个目标并照常下发", func() {
			req := stubDaemonApproval(t, online, removed)
			code, _, errOut := runCLICapture("bookmarks", "rm", "14", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "work")
			So(errOut, ShouldContainSubstring, "waiting for approval in browser work")
		})

		Convey("--browser 给的是实例 ID 前缀时,提示点名它对应的浏览器名称", func() {
			req := stubDaemonApproval(t, online, removed)
			code, _, errOut := runCLICapture("bookmarks", "rm", "14", "--browser", "aaaa")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "aaaa")
			So(errOut, ShouldContainSubstring, "waiting for approval in browser chrome-a")
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonApproval(t, online, removed)
			code, out, _ := runCLICapture("bookmarks", "rm", "14", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"bookmarks": 3`)
		})

		Convey("拒绝退出码 1,超时或作废退出码 2,内容已变化(CONFLICT)与 NOT_FOUND 退出码 3", func() {
			for code, want := range map[string]int{
				"USER_REJECTED":     exitRejected,
				"OPERATION_EXPIRED": exitVoided,
				"CONFLICT":          exitError,
				"NOT_FOUND":         exitError,
				"INVALID_REQUEST":   exitError,
			} {
				stubDaemonApproval(t, online, control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: code}})
				got, _ := runCLI("bookmarks", "rm", "14")
				So(got, ShouldEqual, want)
			}
		})

		Convey("缺 ID 时不发起调用", func() {
			rec := stubDaemonRecording(t, removed)
			code, _ := runCLI("bookmarks", "rm")
			So(code, ShouldNotEqual, exitOK)
			So(rec.snapshot(), ShouldBeEmpty)
		})
	})
}

func TestCancelingABookmarkRemoval(t *testing.T) {
	Convey("Ctrl-C 取消等待审批的书签删除:退出码 2,并说明操作已作废", t, func() {
		arrived := stubDaemonHolding(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-arrived
			cancel()
		}()
		code, _, _, err := runCLIContext(ctx, strings.NewReader(""), "bookmarks", "rm", "14", "--browser", "work")
		So(code, ShouldEqual, exitVoided)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "voided")
	})
}
