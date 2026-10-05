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

func TestDebugNetwork(t *testing.T) {
	Convey("sctl debug network", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
		result := func(hasMore bool) control.CallResult {
			raw, err := json.Marshal(map[string]any{
				"contentTrust": "untrusted-page-content", "tabId": 5, "attachedAt": at, "recording": false, "dropped": 0,
				"records": []map[string]any{
					{"id": 1, "method": "GET", "url": "https://app.test/", "type": "document", "state": "redirected", "status": 302, "startTime": at, "durationMs": 12.5, "transferSize": 90, "fromCache": false, "pageUrl": "https://app.test/"},
					{"id": 2, "method": "POST", "url": "https://app.test/api\x1b[2J", "type": "fetch", "state": "finished", "status": 200, "startTime": at.Add(time.Second), "durationMs": 40, "transferSize": 512, "fromCache": false, "redirectedFrom": 1, "pageUrl": "https://app.test/"},
					{"id": 3, "method": "GET", "url": "https://app.test/a.png", "type": "image", "state": "finished", "status": 200, "startTime": at.Add(time.Second), "durationMs": 1, "fromCache": true, "pageUrl": "https://app.test/"},
					{"id": 4, "method": "GET", "url": "https://cdn.test/x.js", "type": "script", "state": "failed", "startTime": at.Add(time.Second), "error": "net::ERR_NAME_NOT_RESOLVED", "fromCache": false, "pageUrl": "https://app.test/"},
					{"id": 5, "method": "GET", "url": "https://app.test/poll", "type": "xhr", "state": "pending", "startTime": at.Add(time.Second), "fromCache": false, "pageUrl": "https://app.test/"},
				},
				"next": "00000000000000ab.5", "hasMore": hasMore, "cursorReset": false,
			})
			So(err, ShouldBeNil)
			return control.CallResult{OK: true, Result: raw}
		}
		local := func(d time.Duration) string { return at.Add(d).Local().Format("15:04:05.000") }

		Convey("默认输出表格:ID、时间、方法、状态、类型、大小、耗时与 URL;进行中、失败与缓存写明;请求不带筛选", func() {
			stub := stubPageDaemon(t, result(false))
			code, out, errOut := runCLICapture("debug", "network")
			So(code, ShouldEqual, exitOK)
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			So(lines, ShouldHaveLength, 6)
			So(strings.Fields(lines[0]), ShouldResemble, []string{"ID", "TIME", "METHOD", "STATUS", "TYPE", "SIZE", "DURATION", "URL"})
			So(strings.Fields(lines[1]), ShouldResemble, []string{"1", local(0), "GET", "302", "document", "90", "12.5ms", "https://app.test/"})
			So(strings.Fields(lines[2]), ShouldResemble, []string{"2", local(time.Second), "POST", "200", "fetch", "512", "40ms", `https://app.test/api\x1b[2J`})
			So(strings.Fields(lines[3]), ShouldResemble, []string{"3", local(time.Second), "GET", "200", "image", "(cache)", "1ms", "https://app.test/a.png"})
			So(strings.Fields(lines[4]), ShouldResemble, []string{"4", local(time.Second), "GET", "failed:net::ERR_NAME_NOT_RESOLVED", "script", "-", "-", "https://cdn.test/x.js"})
			So(strings.Fields(lines[5]), ShouldResemble, []string{"5", local(time.Second), "GET", "pending", "xhr", "-", "-", "https://app.test/poll"})
			So(errOut, ShouldBeEmpty)
			So(stub.last.Action, ShouldEqual, "debug.network")
			So(string(stub.last.Input), ShouldEqual, `{"limit":100}`)
		})

		Convey("筛选、游标与条数原样转发", func() {
			stub := stubPageDaemon(t, result(false))
			code, _ := runCLI("debug", "network", "--url", "/api", "--method", "post", "--status", "4xx", "--type", "fetch", "--failed",
				"--after", "00000000000000ab.2", "--limit", "5", "--tab", "9")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqualJSON, `{"url":"/api","method":"post","status":"4xx","type":"fetch","failed":true,"after":"00000000000000ab.2","limit":5}`)
			So(*stub.last.TabID, ShouldEqual, 9)
		})

		Convey("还有更多记录时 stderr 提示续查;-o json 输出完整结果", func() {
			stubPageDaemon(t, result(true))
			_, _, errOut := runCLICapture("debug", "network")
			So(errOut, ShouldContainSubstring, "--after 00000000000000ab.5")
			code, out := runCLI("debug", "network", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"redirectedFrom": 1`)
		})

		Convey("多余的参数与越界的 --limit 退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, result(false))
			for _, args := range [][]string{{"debug", "network", "extra"}, {"debug", "network", "--limit", "0"}} {
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})
	})
}

func TestDebugRequest(t *testing.T) {
	Convey("sctl debug request", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
		details := func(responseBody map[string]any) control.CallResult {
			v := map[string]any{
				"contentTrust": "untrusted-page-content", "tabId": 5, "attachedAt": at, "recording": false, "dropped": 0,
				"id": 2, "method": "POST", "url": "https://app.test/api/login", "type": "fetch", "state": "finished", "status": 200, "statusText": "OK",
				"startTime": at, "durationMs": 40, "transferSize": 512, "fromCache": false, "redirectedFrom": 1, "pageUrl": "https://app.test/",
				"requestHeaders":  map[string]string{"Cookie": "sid=secret", "Authorization": "Bearer t0ken"},
				"requestBody":     map[string]any{"body": "{\"user\":\"a\"}\nline2\x1b[2J", "size": 20},
				"responseHeaders": map[string]string{"Content-Type": "text/plain"},
				"timing":          map[string]float64{"dnsMs": 3, "waitMs": 18},
				"remoteAddress":   "[2001:db8::1]:443",
			}
			if responseBody != nil {
				v["responseBody"] = responseBody
			}
			raw, err := json.Marshal(v)
			So(err, ShouldBeNil)
			return control.CallResult{OK: true, Result: raw}
		}

		Convey("默认输出可读的摘要、头与体:头按名字排序、未打码,体里的控制字符转义但保留换行;请求带 ID", func() {
			stub := stubPageDaemon(t, details(nil))
			code, out, errOut := runCLICapture("debug", "request", "2")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldBeEmpty)
			So(out, ShouldStartWith, "POST https://app.test/api/login\n")
			So(out, ShouldContainSubstring, "Status: 200 OK\n")
			So(out, ShouldContainSubstring, "Redirected from: 1\n")
			So(out, ShouldContainSubstring, "Remote address: [2001:db8::1]:443\n")
			So(out, ShouldContainSubstring, "Timing: dns 3ms, wait 18ms\n")
			So(out, ShouldContainSubstring, "Request headers:\n  Authorization: Bearer t0ken\n  Cookie: sid=secret\n")
			So(out, ShouldContainSubstring, "Request body (20 bytes):\n{\"user\":\"a\"}\nline2\\x1b[2J\n")
			So(out, ShouldContainSubstring, "Response headers:\n  Content-Type: text/plain\n")
			So(out, ShouldNotContainSubstring, "Response body")
			So(stub.last.Action, ShouldEqual, "debug.request")
			So(string(stub.last.Input), ShouldEqual, `{"id":2}`)
		})

		Convey("--body 一并请求响应体;截断、base64 与不可用都写明", func() {
			stub := stubPageDaemon(t, details(map[string]any{"body": "hello", "size": 5242880, "truncated": true}))
			code, out := runCLI("debug", "request", "2", "--body")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqualJSON, `{"id":2,"body":true}`)
			So(out, ShouldContainSubstring, "Response body (first 5 of 5242880 bytes):\nhello\n")

			// 文本按字符截断,返回的字节数可以少于 1 MiB;写出的是实际返回的字节数。
			text := strings.Repeat("中", (1<<20)/3)
			stubPageDaemon(t, details(map[string]any{"body": text, "size": 5242880, "truncated": true}))
			_, out = runCLI("debug", "request", "2", "--body")
			So(out, ShouldContainSubstring, "Response body (first 1048575 of 5242880 bytes):\n")

			stubPageDaemon(t, details(map[string]any{"body": "AAEC", "base64Encoded": true, "size": 5242880, "truncated": true}))
			_, out = runCLI("debug", "request", "2", "--body")
			So(out, ShouldContainSubstring, "Response body (first 3 of 5242880 bytes, base64):\nAAEC\n")

			stubPageDaemon(t, details(map[string]any{"body": "AAEC", "base64Encoded": true, "size": 3}))
			_, out = runCLI("debug", "request", "2", "--body")
			So(out, ShouldContainSubstring, "Response body (3 bytes, base64):\nAAEC\n")

			stubPageDaemon(t, details(map[string]any{"body": nil, "unavailable": "the page navigated away"}))
			code, out = runCLI("debug", "request", "2", "--body")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "Response body: unavailable: the page navigated away\n")
		})

		Convey("-o json 输出完整结果", func() {
			stubPageDaemon(t, details(nil))
			_, out := runCLI("debug", "request", "2", "-o", "json")
			So(out, ShouldContainSubstring, `"Cookie": "sid=secret"`)
		})

		Convey("ID 不是正整数或个数不对时退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, details(nil))
			for _, args := range [][]string{{"debug", "request"}, {"debug", "request", "x"}, {"debug", "request", "0"}, {"debug", "request", "1", "2"}} {
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("daemon 的 NOT_FOUND 退出码 3", func() {
			stubPageDaemon(t, pageError("NOT_FOUND", "no request 99 in the records of tab 5"))
			code, _, _, err := runCLIResult(strings.NewReader(""), "debug", "request", "99")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "no request 99")
		})
	})
}

func TestDebugStart(t *testing.T) {
	Convey("sctl debug start 开始录制,摘要写明 tabId 与 60 分钟自动结束", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		result := control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5,"attachedAt":"2026-10-04T10:00:00Z","recording":true,"remainingMs":3600000,"console":{"records":0,"dropped":0},"network":{"records":0,"dropped":0}}`)}

		Convey("请求不带输入,--tab 作为目标", func() {
			stub := stubPageDaemon(t, result)
			code, out := runCLI("debug", "start", "--tab", "5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 is recording: the debugger stays attached until sctl debug stop, or 60 minutes without a debug command\n")
			So(stub.last.Action, ShouldEqual, "debug.start")
			So(*stub.last.TabID, ShouldEqual, 5)
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("无法附加的页面退出码 3", func() {
			stubPageDaemon(t, pageError("PAGE_NOT_AUTOMATABLE", "cannot attach to chrome:// pages"))
			code, _ := runCLI("debug", "start")
			So(code, ShouldEqual, exitError)
		})

		Convey("多余的参数退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, result)
			code, _ := runCLI("debug", "start", "extra")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 0)
		})
	})
}

func TestDebugStop(t *testing.T) {
	Convey("sctl debug stop", t, func() {
		t.Setenv("SCTL_BROWSER", "")

		Convey("结束一个标签页的录制:摘要写明 tabId", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5,"tabIds":[5]}`)})
			code, out := runCLI("debug", "stop", "--tab", "5")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 stopped recording\n")
			So(stub.last.Action, ShouldEqual, "debug.stop")
			So(*stub.last.TabID, ShouldEqual, 5)
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("目标不在录制时也成功", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5,"tabIds":[]}`)})
			code, out := runCLI("debug", "stop")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tab 5 was not recording\n")
		})

		Convey("--all 结束全部录制", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[5,6]}`)})
			code, out := runCLI("debug", "stop", "--all")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "tabs 5, 6 stopped recording\n")
			So(string(stub.last.Input), ShouldEqual, `{"all":true}`)
		})

		Convey("--all 时没有录制中的标签页", func() {
			stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[]}`)})
			code, out := runCLI("debug", "stop", "--all")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "no tab was recording\n")
		})

		Convey("--all 与 --tab 不能同时给:退出码 3,不发请求", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabIds":[]}`)})
			code, _ := runCLI("debug", "stop", "--all", "--tab", "5")
			So(code, ShouldEqual, exitError)
			So(stub.calls, ShouldEqual, 0)
		})
	})
}

func TestDebugStatus(t *testing.T) {
	Convey("sctl debug status", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		at := time.Date(2026, 10, 4, 10, 0, 0, 0, time.UTC)
		raw, err := json.Marshal(map[string]any{"tabs": []map[string]any{
			{"tabId": 5, "attachedAt": at, "recording": true, "remainingMs": 2_430_400, "console": map[string]any{"records": 3, "dropped": 0}, "network": map[string]any{"records": 1000, "dropped": 12}},
			{"tabId": 6, "attachedAt": at.Add(time.Minute), "recording": false, "console": map[string]any{"records": 0, "dropped": 0}, "network": map[string]any{"records": 2, "dropped": 0}},
		}})
		So(err, ShouldBeNil)
		result := control.CallResult{OK: true, Result: raw}

		Convey("默认输出表格:标签页、录制状态、剩余时间、附加时间、各缓存条数与丢弃数;请求不带输入", func() {
			stub := stubPageDaemon(t, result)
			code, out := runCLI("debug", "status")
			So(code, ShouldEqual, exitOK)
			lines := strings.Split(strings.TrimSuffix(out, "\n"), "\n")
			So(lines, ShouldHaveLength, 3)
			So(strings.Fields(lines[0]), ShouldResemble, []string{"TAB", "RECORDING", "REMAINING", "ATTACHED", "CONSOLE", "NETWORK"})
			So(strings.Fields(lines[1]), ShouldResemble, []string{"5", "yes", "40m30s", at.Local().Format("15:04:05"), "3", "1000", "(12", "dropped)"})
			So(strings.Fields(lines[2]), ShouldResemble, []string{"6", "no", "-", at.Add(time.Minute).Local().Format("15:04:05"), "0", "2"})
			So(stub.last.Action, ShouldEqual, "debug.status")
			So(stub.last.TabID, ShouldBeNil)
			So(string(stub.last.Input), ShouldEqual, `{}`)
		})

		Convey("--tab 原样转发;没有被附加的标签页时写明", func() {
			stub := stubPageDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"tabs":[]}`)})
			code, out := runCLI("debug", "status", "--tab", "9")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "no tab is attached\n")
			So(*stub.last.TabID, ShouldEqual, 9)
		})

		Convey("-o json 输出完整结果", func() {
			stubPageDaemon(t, result)
			code, out := runCLI("debug", "status", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"remainingMs": 2430400`)
		})
	})
}
