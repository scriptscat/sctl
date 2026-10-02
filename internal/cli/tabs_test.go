package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// TestTabsList 覆盖 sctl tabs list 的表格与 -o json 输出(docs/specs 第 1 期命令表)。
func TestTabsList(t *testing.T) {
	Convey("sctl tabs list 列出标签页", t, func() {
		result := `{"contentTrust":"untrusted-page-content","tabs":[
			{"tabId":1,"windowId":10,"active":true,"pinned":false,"title":"Example","url":"https://example.com"}
		]}`

		Convey("默认表格含标签 ID、窗口 ID、是否激活、是否固定、标题与 URL", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("tabs", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "TAB ID")
			So(out, ShouldContainSubstring, "WINDOW ID")
			So(out, ShouldContainSubstring, "ACTIVE")
			So(out, ShouldContainSubstring, "PINNED")
			So(out, ShouldContainSubstring, "TITLE")
			So(out, ShouldContainSubstring, "URL")
			So(out, ShouldContainSubstring, "Example")
			So(out, ShouldContainSubstring, "https://example.com")
			// 单实例结果不该多出 BROWSER 列。
			So(out, ShouldNotContainSubstring, "BROWSER")
		})

		Convey("结果项带 groupId 时表格与没有它时逐字节相同,-o json 保留 groupId", func() {
			withGroup := `{"contentTrust":"untrusted-page-content","tabs":[
				{"tabId":1,"windowId":10,"active":true,"pinned":false,"groupId":5,"title":"Example","url":"https://example.com"},
				{"tabId":2,"windowId":10,"active":false,"pinned":false,"groupId":-1,"title":"Other","url":"https://other.example"}
			]}`
			withoutGroup := `{"contentTrust":"untrusted-page-content","tabs":[
				{"tabId":1,"windowId":10,"active":true,"pinned":false,"title":"Example","url":"https://example.com"},
				{"tabId":2,"windowId":10,"active":false,"pinned":false,"title":"Other","url":"https://other.example"}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(withoutGroup)})
			_, want := runCLI("tabs", "list")
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(withGroup)})
			code, got := runCLI("tabs", "list")
			So(code, ShouldEqual, exitOK)
			So(got, ShouldEqual, want)
			So(got, ShouldNotContainSubstring, "GROUP")

			stubDaemon(t, control.CallResult{OK: true, Result: []byte(withGroup)})
			_, out := runCLI("tabs", "list", "-o", "json")
			So(out, ShouldContainSubstring, `"groupId": 5`)
			So(out, ShouldContainSubstring, `"groupId": -1`)
		})

		Convey("网页控制的标题与 URL 里的控制字符以转义形式打印,不会把终端控制序列或换行写进表格", func() {
			hostile := `{"contentTrust":"untrusted-page-content","tabs":[
				{"tabId":1,"windowId":10,"active":true,"pinned":false,"title":"\u001b]0;pwned\u0007Evil\nFAKE ROW\u202e","url":"https://example.com/\u001b[2J"}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(hostile)})
			code, out := runCLI("tabs", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(out, ShouldNotContainSubstring, "\x07")
			So(out, ShouldNotContainSubstring, "\u202e")
			So(strings.Split(strings.TrimRight(out, "\n"), "\n"), ShouldHaveLength, 2)
			So(out, ShouldContainSubstring, `\x1b]0;pwned\aEvil\nFAKE ROW\u202e`)
			So(out, ShouldContainSubstring, `https://example.com/\x1b[2J`)
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("tabs", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"tabId": 1`)
			So(out, ShouldContainSubstring, "untrusted-page-content")
		})

		Convey("--window 传下去的请求带 windowId", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","tabs":[]}`)})
			code, _ := runCLI("tabs", "list", "--window", "7")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabs.list")
			So(string(req.Input), ShouldContainSubstring, `"windowId":7`)
		})

		Convey("不带 --window 时不下发 windowId 字段", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","tabs":[]}`)})
			code, _ := runCLI("tabs", "list")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldNotContainSubstring, "windowId")
		})

		Convey("多实例汇总结果给表格加 BROWSER 列,JSON 每项带浏览器名称和 ID", func() {
			merged := `{"contentTrust":"untrusted-page-content","tabs":[
				{"tabId":1,"windowId":10,"active":true,"pinned":false,"title":"A","url":"https://a.example","browser":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"chrome-a"}},
				{"tabId":2,"windowId":20,"active":false,"pinned":true,"title":"B","url":"https://b.example","browser":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"chrome-b"}}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})

			code, out := runCLI("tabs", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "chrome-a")
			So(out, ShouldContainSubstring, "chrome-b")

			code, out = runCLI("tabs", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"name": "chrome-a"`)
			So(out, ShouldContainSubstring, `"id": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"`)
		})
	})
}

// TestTabsOpen 覆盖 sctl tabs open:打印新标签 ID,--window/--background 透传。
func TestTabsOpen(t *testing.T) {
	Convey("sctl tabs open 打开一个标签并打印它的标签 ID", t, func() {
		Convey("默认表格打印标签 ID", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(`{"tabId":42}`)})
			code, out := runCLI("tabs", "open", "https://example.com")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "TAB ID")
			So(out, ShouldContainSubstring, "42")
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(`{"tabId":42}`)})
			code, out := runCLI("tabs", "open", "https://example.com", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"tabId": 42`)
		})

		Convey("--window 与 --background 透传给请求", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabId":1}`)})
			code, _ := runCLI("tabs", "open", "https://example.com", "--window", "9", "--background")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "tabs.open")
			So(string(req.Input), ShouldContainSubstring, `"url":"https://example.com"`)
			So(string(req.Input), ShouldContainSubstring, `"windowId":9`)
			So(string(req.Input), ShouldContainSubstring, `"background":true`)
		})

		Convey("不带 --window/--background 时只下发 url", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabId":1}`)})
			code, _ := runCLI("tabs", "open", "https://example.com")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldNotContainSubstring, "windowId")
			So(string(req.Input), ShouldNotContainSubstring, "background")
		})
	})
}

// TestTabsClose 覆盖 sctl tabs close <tabId>...。
func TestTabsClose(t *testing.T) {
	Convey("sctl tabs close 关闭一个或多个标签页", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabIds":[1,2]}`)})
		code, out := runCLI("tabs", "close", "1", "2")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "tabs.close")
		So(string(req.Input), ShouldEqual, `{"tabIds":[1,2]}`)
		So(out, ShouldContainSubstring, "TAB ID")
		So(out, ShouldContainSubstring, "1")
		So(out, ShouldContainSubstring, "2")
	})
}

// TestTabsActivate 覆盖 sctl tabs activate <tabId>。
func TestTabsActivate(t *testing.T) {
	Convey("sctl tabs activate 激活标签并切到它所在窗口", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"tabId":5,"windowId":10}`)})
		code, out := runCLI("tabs", "activate", "5")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "tabs.activate")
		So(string(req.Input), ShouldEqual, `{"tabId":5}`)
		So(out, ShouldContainSubstring, "WINDOW ID")
		So(out, ShouldContainSubstring, "10")
	})
}

// TestWindowsList 覆盖 sctl windows list。
func TestWindowsList(t *testing.T) {
	Convey("sctl windows list 列出窗口", t, func() {
		result := `{"windows":[{"windowId":1,"focused":true,"state":"normal","tabCount":3}]}`

		Convey("默认表格含窗口 ID、是否有焦点、状态与标签数", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("windows", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "WINDOW ID")
			So(out, ShouldContainSubstring, "FOCUSED")
			So(out, ShouldContainSubstring, "STATE")
			So(out, ShouldContainSubstring, "TABS")
			So(out, ShouldContainSubstring, "normal")
			So(out, ShouldNotContainSubstring, "BROWSER")
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("windows", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"windowId": 1`)
		})

		Convey("多实例汇总结果给表格加 BROWSER 列", func() {
			merged := `{"windows":[
				{"windowId":1,"focused":true,"state":"normal","tabCount":1,"browser":{"id":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","name":"chrome-a"}},
				{"windowId":2,"focused":false,"state":"minimized","tabCount":2,"browser":{"id":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","name":"chrome-b"}}
			]}`
			stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})
			code, out := runCLI("windows", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "BROWSER")
			So(out, ShouldContainSubstring, "chrome-a")
			So(out, ShouldContainSubstring, "chrome-b")
		})
	})
}

// TestBrowserTargetFlag 覆盖「显式 --browser 优先于 SCTL_BROWSER」(docs/specs 路由与目标选择)。
func TestBrowserTargetFlag(t *testing.T) {
	Convey("--browser 与 SCTL_BROWSER 决定下发的目标浏览器", t, func() {
		Convey("只设置 SCTL_BROWSER 时下发它的值", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windows":[]}`)})
			t.Setenv("SCTL_BROWSER", "env-browser")
			code, _ := runCLI("windows", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "env-browser")
		})

		Convey("显式 --browser 优先于 SCTL_BROWSER", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windows":[]}`)})
			t.Setenv("SCTL_BROWSER", "env-browser")
			code, _ := runCLI("windows", "list", "--browser", "flag-browser")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "flag-browser")
		})

		Convey("都不设置时下发空目标,交给 daemon 按在线实例解析", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"windows":[]}`)})
			t.Setenv("SCTL_BROWSER", "")
			code, _ := runCLI("windows", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "")
		})

		Convey("--browser 对 tabs 子命令同样生效", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"contentTrust":"untrusted-page-content","tabs":[]}`)})
			code, _ := runCLI("tabs", "list", "--browser", "chrome-b")
			So(code, ShouldEqual, exitOK)
			So(req.Browser, ShouldEqual, "chrome-b")
		})
	})
}

// TestBrowserTargetErrorExitCodes 覆盖目标选择表的错误退出码(docs/specs 路由与目标选择;
// docs/protocol.md §3.1/§4):离线/未知/歧义退出码 3,调用中途断开(OPERATION_EXPIRED)退出码 2,
// 未知标签/窗口 ID(NOT_FOUND)退出码 3。
func TestBrowserTargetErrorExitCodes(t *testing.T) {
	Convey("浏览器目标与调用错误按错误码映射退出码", t, func() {
		Convey("没有浏览器在线 → NO_BROWSER_CONNECTED,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NO_BROWSER_CONNECTED", Message: "no browser connected"}})
			code, _ := runCLI("tabs", "list")
			So(code, ShouldEqual, exitError)
		})

		Convey("目标已配对但离线 → BROWSER_OFFLINE,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "BROWSER_OFFLINE", Message: "browser \"chrome-a\" is offline"}})
			code, _ := runCLI("tabs", "list", "--browser", "chrome-a")
			So(code, ShouldEqual, exitError)
		})

		Convey("目标不匹配任何已配对实例 → BROWSER_NOT_FOUND,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "BROWSER_NOT_FOUND", Message: "no such browser"}})
			code, _ := runCLI("tabs", "list", "--browser", "nope")
			So(code, ShouldEqual, exitError)
		})

		Convey("多实例在线且方法不可合并、未指定目标 → BROWSER_AMBIGUOUS,退出码 3(候选列表由 daemon 拼入 message,见 controlapi 层测试)", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "BROWSER_AMBIGUOUS", Message: "several browsers are online, choose a target browser: chrome-a (id-a), chrome-b (id-b)"}})
			code, _ := runCLI("tabs", "open", "https://example.com")
			So(code, ShouldEqual, exitError)
		})

		Convey("多实例在线、操作类命令未指定目标 → 错误信息列出候选浏览器并要求加 --browser", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "BROWSER_AMBIGUOUS", Message: "several browsers are online, choose a target browser: chrome-a (id-a), edge-b (id-b)"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "tabs", "close", "5")
			So(code, ShouldEqual, exitError)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "chrome-a (id-a), edge-b (id-b)")
			So(err.Error(), ShouldContainSubstring, "--browser")
		})

		Convey("调用进行时目标浏览器断开 → OPERATION_EXPIRED,退出码 2", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "OPERATION_EXPIRED"}})
			code, _ := runCLI("tabs", "activate", "1")
			So(code, ShouldEqual, exitVoided)
		})

		Convey("标签 ID 不存在 → NOT_FOUND,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no such tab"}})
			code, _ := runCLI("tabs", "activate", "999")
			So(code, ShouldEqual, exitError)
		})

		Convey("窗口 ID 不存在 → NOT_FOUND,退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no such window"}})
			code, _ := runCLI("tabs", "list", "--window", "999")
			So(code, ShouldEqual, exitError)
		})
	})
}

// stubDaemonHolding 起一个收到 /control/call 后一直不回复的假 daemon,返回请求抵达时关闭的通道。
func stubDaemonHolding(t *testing.T) <-chan struct{} {
	t.Helper()
	arrived := make(chan struct{})
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	hold := func(_ http.ResponseWriter, r *http.Request) {
		// 读完请求体服务端才会察觉客户端断开并取消 r.Context()。
		_, _ = io.Copy(io.Discard, r.Body)
		close(arrived)
		<-r.Context().Done()
	}
	mux.HandleFunc(control.PathCall, hold)
	mux.HandleFunc(control.PathPage, hold)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return arrived
}

func TestCancelingABrowserAction(t *testing.T) {
	Convey("Ctrl-C 取消浏览器操作:退出码 2,但不声称操作已作废,因为浏览器操作即时生效、无法撤回", t, func() {
		arrived := stubDaemonHolding(t)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go func() {
			<-arrived
			cancel()
		}()
		code, _, _, err := runCLIContext(ctx, strings.NewReader(""), "tabs", "close", "5")
		So(code, ShouldEqual, exitVoided)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldNotContainSubstring, "voided")
		So(err.Error(), ShouldContainSubstring, "may already")
	})
}
