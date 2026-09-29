package cli

import (
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

// stubPageDaemon 起一个假 daemon:/control/page 恒返回 result,并记录收到的请求数与最近一次请求。
type pageStub struct {
	calls int
	last  control.PageRequest
}

func stubPageDaemon(t *testing.T, result control.CallResult) *pageStub {
	t.Helper()
	stub := &pageStub{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathPage, func(w http.ResponseWriter, r *http.Request) {
		stub.calls++
		stub.last = control.PageRequest{}
		_ = json.NewDecoder(r.Body).Decode(&stub.last)
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return stub
}

func pageError(code, message string) control.CallResult {
	return control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: message}}
}

func TestPageEval(t *testing.T) {
	Convey("sctl page eval", t, func() {
		// t.Setenv 在同一测试的各个 Convey 分支之间保留,每个分支都从没有 SCTL_BROWSER 开始。
		t.Setenv("SCTL_BROWSER", "")
		ok := control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"value":{"title":"Example","n":[1,2]}}`)}

		Convey("默认输出一行摘要:tabId 与紧凑的 JSON 值;请求只带表达式,标签页交给 daemon 选择", func() {
			stub := stubPageDaemon(t, ok)
			code, out := runCLI("page", "eval", "document.title")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5: {\"title\":\"Example\",\"n\":[1,2]}\n")
			So(stub.last.Action, ShouldEqual, "eval")
			So(string(stub.last.Input), ShouldEqual, `{"expression":"document.title"}`)
			So(stub.last.TabID, ShouldBeNil)
			So(stub.last.Activate, ShouldBeFalse)
			So(stub.last.TimeoutMs, ShouldEqual, 0)
			So(stub.last.Browser, ShouldEqual, "")
		})

		Convey("--tab、--activate、--timeout、--browser 原样转发", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "eval", "1", "--tab", "9", "--activate", "--timeout", "30s", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(stub.last.TabID, ShouldNotBeNil)
			So(*stub.last.TabID, ShouldEqual, 9)
			So(stub.last.Activate, ShouldBeTrue)
			So(stub.last.TimeoutMs, ShouldEqual, 30000)
			So(stub.last.Browser, ShouldEqual, "work")
		})

		Convey("未给 --browser 时取 SCTL_BROWSER", func() {
			t.Setenv("SCTL_BROWSER", "home")
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "eval", "1")
			So(code, ShouldEqual, exitOK)
			So(stub.last.Browser, ShouldEqual, "home")
		})

		Convey("-o json 输出完整的结构化结果", func() {
			stubPageDaemon(t, ok)
			code, out := runCLI("page", "eval", "1", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust": "untrusted-page-content"`)
			So(out, ShouldContainSubstring, `"tabId": 5`)
		})

		Convey("页面给出的值里的控制字符以转义形式打印", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage("{\"contentTrust\":\"untrusted-page-content\",\"tabId\":5,\"value\":\"a\\u001b[2Jb\u202e\"}")})
			code, out := runCLI("page", "eval", "x")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(out, ShouldNotContainSubstring, "\u202e")
		})

		Convey("第二个位置参数作为元素引用以 ref 转发", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "eval", "el => el.textContent", "e5")
			So(code, ShouldEqual, exitOK)
			So(stub.calls, ShouldEqual, 1)
			So(string(stub.last.Input), ShouldEqualJSON, `{"expression":"el => el.textContent","ref":"e5"}`)
		})

		Convey("没有第二个参数时不带 ref;三个参数退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "eval", "1")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqual, `{"expression":"1"}`)
			code, _ = runCLI("page", "eval", "1", "e5", "e6")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 1)
		})

		Convey("非法 --timeout 与负的 --tab:退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "eval", "1", "--timeout", "-1s")
			So(code, ShouldEqual, exitError)
			code, _ = runCLI("page", "eval", "1", "--tab", "-2")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("DEBUGGER_DETACHED 退出码 2", func() {
			stubPageDaemon(t, pageError("DEBUGGER_DETACHED", "the debugger detached from tab 5 (canceled_by_user) while the command was running"))
			code, _, errOut, err := runCLIResult(strings.NewReader(""), "page", "eval", "1")
			So(code, ShouldEqual, exitVoided)
			So(errOut+err.Error(), ShouldContainSubstring, "canceled_by_user")
		})

		Convey("EVAL_ERROR、PAGE_NOT_AUTOMATABLE、NOT_FOUND 退出码 3,消息带原因", func() {
			for code, message := range map[string]string{
				"EVAL_ERROR":           "Error: boom",
				"PAGE_NOT_AUTOMATABLE": "Cannot access a chrome:// URL",
				"NOT_FOUND":            "no tab 9",
			} {
				stubPageDaemon(t, pageError(code, message))
				exit, _, _, err := runCLIResult(strings.NewReader(""), "page", "eval", "1")
				So(exit, ShouldEqual, exitError)
				So(err.Error(), ShouldContainSubstring, message)
			}
		})

		Convey("多个浏览器在线且未指定目标时提示 --browser", func() {
			stubPageDaemon(t, pageError("BROWSER_AMBIGUOUS", "several browsers are online"))
			exit, _, _, err := runCLIResult(strings.NewReader(""), "page", "eval", "1")
			So(exit, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "--browser")
		})
	})
}

func TestPageDetach(t *testing.T) {
	Convey("sctl page detach", t, func() {
		Convey("断开一个标签页:摘要写明 tabId", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5,"tabIds":[5]}`)})
			code, out := runCLI("page", "detach", "--tab", "5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 detached\n")
			So(stub.last.Action, ShouldEqual, "detach")
			So(*stub.last.TabID, ShouldEqual, 5)
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("目标没有被附加时也成功", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5,"tabIds":[]}`)})
			code, out := runCLI("page", "detach")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 was not attached\n")
		})

		Convey("--all 断开全部标签页", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[5,6]}`)})
			code, out := runCLI("page", "detach", "--all")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "detached tabs 5, 6\n")
			So(string(stub.last.Input), ShouldEqual, `{"all":true}`)
		})

		Convey("--all 时没有已附加的标签页", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[]}`)})
			code, out := runCLI("page", "detach", "--all")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "no tab was attached\n")
		})

		Convey("--all 与 --tab 不能同时给:退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[]}`)})
			code, _ := runCLI("page", "detach", "--all", "--tab", "5")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 0)
		})
	})
}

func TestPageSnapshot(t *testing.T) {
	Convey("sctl page snapshot", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		snapshot := "- heading \"Title\" [level=1] [ref=e1]\n- link \"Home\" [ref=e2]\n  - /url: https://example.com/"
		result, err := json.Marshal(map[string]any{"contentTrust": "untrusted-page-content", "tabId": 5, "snapshot": snapshot})
		So(err, ShouldBeNil)
		ok := control.CallResult{OK: true, Result: result}

		Convey("默认直接输出快照文本;不给 --root 时请求不带 root", func() {
			stub := stubPageDaemon(t, ok)
			code, out := runCLI("page", "snapshot")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, snapshot+"\n")
			So(stub.last.Action, ShouldEqual, "snapshot")
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("--root 原样转发引用或选择器", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "snapshot", "--root", "#main > form", "--tab", "5")
			So(code, ShouldEqual, exitOK)
			var input map[string]string
			So(json.Unmarshal(stub.last.Input, &input), ShouldBeNil)
			So(input, ShouldResemble, map[string]string{"root": "#main > form"})
			So(*stub.last.TabID, ShouldEqual, 5)
		})

		Convey("-o json 输出完整的结构化结果", func() {
			stubPageDaemon(t, ok)
			code, out := runCLI("page", "snapshot", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust": "untrusted-page-content"`)
			So(out, ShouldContainSubstring, `"snapshot": "- heading`)
		})

		Convey("快照为空时什么都不输出", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"snapshot":""}`)})
			code, out := runCLI("page", "snapshot")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "")
		})

		Convey("页面控制的文本里的控制字符以转义形式打印,行结构保持不变", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage("{\"contentTrust\":\"untrusted-page-content\",\"tabId\":5,\"snapshot\":\"- text: a\\u001b[2Jb\\n- text: c\u202e\"}")})
			code, out := runCLI("page", "snapshot")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "- text: a\\x1b[2Jb\n- text: c\\u202e\n")
		})

		Convey("位置参数:退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("page", "snapshot", "e5")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("STALE_REF 与 PAYLOAD_TOO_LARGE 退出码 3,消息带原因", func() {
			for code, message := range map[string]string{
				"STALE_REF":         "ref e5 is not valid on tab 5: take a new snapshot and use a ref from it",
				"PAYLOAD_TOO_LARGE": "the snapshot is 2000000 bytes, over the 1048576-byte limit: use --root to snapshot part of the page",
			} {
				stubPageDaemon(t, pageError(code, message))
				exit, _, _, err := runCLIResult(strings.NewReader(""), "page", "snapshot")
				So(exit, ShouldEqual, exitError)
				So(err.Error(), ShouldContainSubstring, message)
			}
		})
	})
}

func TestPageClickAndHover(t *testing.T) {
	Convey("sctl page click / hover", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		plain := control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.com/","title":"Example","navigated":false}`)}

		Convey("click 以位置参数给引用;默认输出一行摘要,只有 tabId", func() {
			stub := stubPageDaemon(t, plain)
			code, out := runCLI("page", "click", "e5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5\n")
			So(stub.last.Action, ShouldEqual, "click")
			So(string(stub.last.Input), ShouldEqualJSON, `{"ref":"e5"}`)
		})

		Convey("click --selector 与 --button、--count、--modifiers 转发为动作输入", func() {
			stub := stubPageDaemon(t, plain)
			code, _ := runCLI("page", "click", "--selector", "#main > button", "--button", "right", "--count", "2", "--modifiers", "Control,Shift", "--tab", "5", "--activate", "--timeout", "20s")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqualJSON, `{"selector":"#main > button","button":"right","count":2,"modifiers":["Control","Shift"]}`)
			So(*stub.last.TabID, ShouldEqual, 5)
			So(stub.last.Activate, ShouldBeTrue)
			So(stub.last.TimeoutMs, ShouldEqual, 20000)
		})

		Convey("点击导航后摘要写出导航后的 URL;打开新标签页时写出它的 ID;URL 里的控制字符被转义", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.com/next","title":"Next","navigated":true}`)})
			code, out := runCLI("page", "click", "e5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 navigated to https://example.com/next\n")

			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.com/","title":"Example","navigated":false,"newTabId":12}`)})
			code, out = runCLI("page", "click", "e5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 opened tab 12\n")

			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage("{\"contentTrust\":\"untrusted-page-content\",\"tabId\":5,\"url\":\"https://e.test/\\u001b[2J\",\"title\":\"\",\"navigated\":true}")})
			code, out = runCLI("page", "click", "e5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
		})

		Convey("-o json 输出完整的结构化结果", func() {
			stubPageDaemon(t, plain)
			code, out := runCLI("page", "hover", "e5", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"title": "Example"`)
			So(out, ShouldContainSubstring, `"navigated": false`)
		})

		Convey("hover 只转发目标", func() {
			stub := stubPageDaemon(t, plain)
			code, out := runCLI("page", "hover", "--selector", ".menu")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5\n")
			So(stub.last.Action, ShouldEqual, "hover")
			So(string(stub.last.Input), ShouldEqualJSON, `{"selector":".menu"}`)
		})

		Convey("引用与 --selector 必须恰好给一个:退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, plain)
			for _, args := range [][]string{
				{"page", "click"},
				{"page", "click", "e5", "--selector", "#a"},
				{"page", "click", "e5", "e6"},
				{"page", "hover"},
				{"page", "hover", "e5", "--selector", "#a"},
			} {
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("TIMEOUT、TARGET_AMBIGUOUS、PAGE_HIDDEN 退出码 3,消息带原因", func() {
			for code, message := range map[string]string{
				"TIMEOUT":          "page click did not finish within 10s: the element does not receive pointer events at its click point: obscured by div.modal-backdrop",
				"TARGET_AMBIGUOUS": `selector ".item" matches 2 elements; it must match exactly one`,
				"PAGE_HIDDEN":      "tab 5 is not rendering; retry with --activate",
			} {
				stubPageDaemon(t, pageError(code, message))
				exit, _, _, err := runCLIResult(strings.NewReader(""), "page", "click", "e5")
				So(exit, ShouldEqual, exitError)
				So(err.Error(), ShouldContainSubstring, message)
			}
		})
	})
}
