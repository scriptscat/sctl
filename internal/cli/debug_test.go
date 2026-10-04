package cli

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestDebugConsole(t *testing.T) {
	Convey("sctl debug console", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
		result := func(hasMore, reset bool) control.CallResult {
			raw, err := json.Marshal(map[string]any{
				"contentTrust": "untrusted-page-content", "tabId": 5, "attachedAt": at, "recording": false, "dropped": 0,
				"records": []map[string]any{
					{"seq": 1, "time": at.Add(1500 * time.Millisecond), "source": "console", "level": "info", "text": "hello\nworld", "url": "https://app.test/app.js", "line": 42, "column": 8, "pageUrl": "https://app.test/"},
					{"seq": 2, "time": at.Add(2 * time.Second), "source": "exception", "level": "error", "text": "Uncaught Error: \x1b[2Jboom", "pageUrl": "https://app.test/"},
				},
				"next": "00000000000000ab.2", "hasMore": hasMore, "cursorReset": reset,
			})
			So(err, ShouldBeNil)
			return control.CallResult{OK: true, Result: raw}
		}
		local := func(d time.Duration) string { return at.Add(d).Local().Format("15:04:05.000") }

		Convey("默认输出表格:序号、时间、级别、来源、位置与文本;页面控制的文字转义控制字符;请求不带任何筛选", func() {
			stub := stubPageDaemon(t, result(false, false))
			code, out, errOut := runCLICapture("debug", "console")
			So(code, ShouldEqual, exitOK)
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			So(lines, ShouldHaveLength, 3)
			So(strings.Fields(lines[0]), ShouldResemble, []string{"SEQ", "TIME", "LEVEL", "SOURCE", "LOCATION", "TEXT"})
			So(strings.Fields(lines[1]), ShouldResemble, []string{"1", local(1500 * time.Millisecond), "info", "console", "https://app.test/app.js:42:8", `hello\nworld`})
			So(strings.Fields(lines[2]), ShouldResemble, []string{"2", local(2 * time.Second), "error", "exception", "-", "Uncaught", `Error:`, `\x1b[2Jboom`})
			So(errOut, ShouldBeEmpty)
			So(stub.last.Action, ShouldEqual, "debug.console")
			So(string(stub.last.Input), ShouldEqual, `{"limit":100}`)
			So(stub.last.TabID, ShouldBeNil)
			So(stub.last.Activate, ShouldBeFalse)
			So(stub.last.TimeoutMs, ShouldEqual, 0)
		})

		Convey("筛选、游标与条数原样转发,--tab 与 --browser 作为目标", func() {
			stub := stubPageDaemon(t, result(false, false))
			code, _ := runCLI("debug", "console", "--level", "warning", "--source", "exception", "--text", "Boom",
				"--after", "00000000000000ab.2", "--limit", "5", "--tab", "9", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqualJSON, `{"level":"warning","source":"exception","text":"Boom","after":"00000000000000ab.2","limit":5}`)
			So(*stub.last.TabID, ShouldEqual, 9)
			So(stub.last.Browser, ShouldEqual, "work")
		})

		Convey("-o json 输出完整结果", func() {
			stubPageDaemon(t, result(false, false))
			code, out := runCLI("debug", "console", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust": "untrusted-page-content"`)
			So(out, ShouldContainSubstring, `"next": "00000000000000ab.2"`)
		})

		Convey("还有更多记录时在 stderr 提示用 --after 或更大的 --limit 继续,stdout 只有表格", func() {
			stubPageDaemon(t, result(true, false))
			code, out, errOut := runCLICapture("debug", "console")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "--after 00000000000000ab.2")
			So(errOut, ShouldContainSubstring, "--limit")
			So(out, ShouldNotContainSubstring, "--after")
		})

		Convey("游标不属于当前缓存时在 stderr 说明从开头返回", func() {
			stubPageDaemon(t, result(false, true))
			_, _, errOut := runCLICapture("debug", "console", "--after", "00000000000000ff.9")
			So(errOut, ShouldContainSubstring, "from the start")
		})

		Convey("越界的 --limit、多余的参数与负的 --tab 退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, result(false, false))
			for _, args := range [][]string{
				{"debug", "console", "--limit", "0"},
				{"debug", "console", "--limit", "1001"},
				{"debug", "console", "extra"},
				{"debug", "console", "--tab", "-1"},
			} {
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("daemon 的 INVALID_REQUEST 与 NOT_FOUND 退出码 3,消息原样", func() {
			stubPageDaemon(t, pageError("INVALID_REQUEST", `unknown level "log": use debug, info, warning or error`))
			code, _, _, err := runCLIResult(strings.NewReader(""), "debug", "console", "--level", "log")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, `unknown level "log"`)
		})
	})
}

func TestDebugClear(t *testing.T) {
	Convey("sctl debug clear 清空缓存,摘要写明 tabId", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"attachedAt":"2026-10-04T10:00:00Z","recording":false,"dropped":0}`)})
		code, out := runCLI("debug", "clear", "--tab", "5")
		So(code, ShouldEqual, exitOK)
		So(out, ShouldEqual, "tab 5 debug records cleared\n")
		So(stub.last.Action, ShouldEqual, "debug.clear")
		So(*stub.last.TabID, ShouldEqual, 5)
		So(string(stub.last.Input), ShouldEqual, `{}`)
	})
}
