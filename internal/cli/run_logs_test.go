package cli

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/cli/upgradehint"
	"github.com/scriptscat/sctl/internal/client/control"
)

const runLogLine = `{"time":1730000000123,"severity":"info","message":"hello","component":"GM_log","fields":[{"name":"n","value":"1"}]}`

func runResult(status, extra string) control.CallResult {
	return control.CallResult{OK: true, Result: json.RawMessage(
		`{"contentTrust":"untrusted-user-script-log","status":"` + status + `","startedAt":1730000000000,` + extra + `"logs":[` + runLogLine + `]}`)}
}

func inputMap(input json.RawMessage) map[string]any {
	var m map[string]any
	So(json.Unmarshal(input, &m), ShouldBeNil)
	return m
}

func TestRunCommand(t *testing.T) {
	Convey("sctl run <uuid>", t, func() {
		Convey("默认等待:调用 scripts.run,只下发 uuid,默认超时由扩展侧补", func() {
			req := stubDaemonCapturing(t, runResult("complete", `"endedAt":1730000001500,`))
			code, out := runCLI("run", "u1")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.run")
			So(inputMap(req.Input), ShouldResemble, map[string]any{"uuid": "u1"})
			So(out, ShouldContainSubstring, "complete")
			So(out, ShouldContainSubstring, "1.5s")
			So(out, ShouldContainSubstring, "GM_log")
			So(out, ShouldContainSubstring, "hello")
			So(out, ShouldContainSubstring, "n=1")
		})

		Convey("--timeout 换算为 timeoutMs;--no-wait 下发 wait:false", func() {
			req := stubDaemonCapturing(t, runResult("complete", `"endedAt":1730000001500,`))
			code, _ := runCLI("run", "u1", "--timeout", "90s")
			So(code, ShouldEqual, exitOK)
			So(inputMap(req.Input), ShouldResemble, map[string]any{"uuid": "u1", "timeoutMs": float64(90000)})

			req = stubDaemonCapturing(t, runResult("running", ""))
			code, out := runCLI("run", "u1", "--no-wait")
			So(code, ShouldEqual, exitOK)
			So(inputMap(req.Input), ShouldResemble, map[string]any{"uuid": "u1", "wait": false})
			So(out, ShouldContainSubstring, "still running")
		})

		Convey("--timeout 超过 240s 或不为正:退出码 3,且不发起调用", func() {
			for _, bad := range []string{"241s", "5m", "0s", "-1s"} {
				rec := stubDaemonRecording(t, runResult("complete", ""))
				code, _ := runCLI("run", "u1", "--timeout", bad)
				So(code, ShouldEqual, exitError)
				So(rec.snapshot(), ShouldBeEmpty)
			}
			req := stubDaemonCapturing(t, runResult("complete", `"endedAt":1730000001500,`))
			code, _ := runCLI("run", "u1", "--timeout", "240s")
			So(code, ShouldEqual, exitOK)
			So(inputMap(req.Input)["timeoutMs"], ShouldEqual, float64(240000))
		})

		Convey("脚本出错:状态行带错误信息,命令仍以 0 退出", func() {
			stubDaemon(t, runResult("error", `"endedAt":1730000000400,"error":"boom\u001b[31m",`))
			code, out := runCLI("run", "u1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "error")
			So(out, ShouldContainSubstring, "boom")
			So(out, ShouldNotContainSubstring, "\x1b")
		})

		Convey("-o json 原样输出结果对象", func() {
			stubDaemon(t, runResult("complete", `"endedAt":1730000001500,`))
			code, out := runCLI("run", "u1", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var got map[string]any
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got["status"], ShouldEqual, "complete")
		})

		Convey("资源词与 --scriptcat 目标", func() {
			req := stubDaemonCapturing(t, runResult("complete", `"endedAt":1730000001500,`))
			code, _ := runCLI("run", "script", "u1", "--scriptcat", "work")
			So(code, ShouldEqual, exitOK)
			So(uuidOfInput(req.Input), ShouldEqual, "u1")
			So(req.ScriptCat, ShouldEqual, "work")
		})

		Convey("缺 uuid 退出码 3", func() {
			code, _ := runCLI("run")
			So(code, ShouldEqual, exitError)
		})

		Convey("等待期间扩展断开(OPERATION_EXPIRED):退出码 2", func() {
			stubDaemon(t, control.CallResult{Error: &control.CallError{Code: "OPERATION_EXPIRED", Message: "gone"}})
			code, _ := runCLI("run", "u1")
			So(code, ShouldEqual, exitVoided)
		})

		Convey("Ctrl-C:退出码 2", func() {
			arrived := stubDaemonHolding(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go func() {
				<-arrived
				cancel()
			}()
			code, _, _, err := runCLIContext(ctx, strings.NewReader(""), "run", "u1")
			So(code, ShouldEqual, exitVoided)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "may already be running in ScriptCat")
			So(err.Error(), ShouldNotContainSubstring, "voided")
		})

		Convey("--timeout 越界的报错用 240s,与帮助文字一致", func() {
			stubDaemon(t, runResult("complete", ""))
			code, _, _, err := runCLIResult(strings.NewReader(""), "run", "u1", "--timeout", "5m")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "240s")
			So(err.Error(), ShouldNotContainSubstring, "4m0s")
		})

		Convey("出错但没有 error 文本:状态行没有悬空冒号", func() {
			stubDaemon(t, runResult("error", `"endedAt":1730000000400,`))
			code, out := runCLI("run", "u1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldStartWith, "error in 400ms\n")
		})

		Convey("日志被截断:提示指向 sctl logs <uuid>,而不是 run 没有的筛选参数", func() {
			stubDaemon(t, runResult("complete", `"endedAt":1730000001500,"truncated":true,`))
			code, _, errOut := runCLICapture("run", "u1")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "sctl logs <uuid>")
			So(errOut, ShouldNotContainSubstring, "--since")
			So(errOut, ShouldNotContainSubstring, "--level")
		})

		Convey("旧版 ScriptCat(METHOD_NOT_FOUND):退出码 3 并提示升级", func() {
			stubDaemon(t, control.CallResult{Error: &control.CallError{Code: "METHOD_NOT_FOUND", Message: "scripts.run"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "run", "u1")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, upgradehint.Message)
			So(upgradehint.Message, ShouldContainSubstring, "upgrade ScriptCat")
			So(upgradehint.Message, ShouldContainSubstring, "restart")
		})

		Convey("NOT_FOUND 不带升级提示", func() {
			stubDaemon(t, control.CallResult{Error: &control.CallError{Code: "NOT_FOUND", Message: "no script"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "run", "u1")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldNotContainSubstring, "upgrade")
		})
	})
}

func logsResult(truncated bool) control.CallResult {
	t := "false"
	if truncated {
		t = "true"
	}
	return control.CallResult{OK: true, Result: json.RawMessage(
		`{"contentTrust":"untrusted-user-script-log","truncated":` + t + `,"logs":[` + runLogLine + `,{"time":1730000002000,"severity":"error","message":"bad\nline"}]}`)}
}

func TestLogsCommand(t *testing.T) {
	Convey("sctl logs <uuid>", t, func() {
		Convey("默认 since=1h、limit=100,调用 scripts.logs", func() {
			req := stubDaemonCapturing(t, logsResult(false))
			before := time.Now().Add(-time.Hour).UnixMilli()
			code, out := runCLI("logs", "u1")
			after := time.Now().Add(-time.Hour).UnixMilli()
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.logs")
			in := inputMap(req.Input)
			So(in["uuid"], ShouldEqual, "u1")
			So(in["limit"], ShouldEqual, float64(100))
			since := int64(in["since"].(float64))
			So(since, ShouldBeBetweenOrEqual, before, after)
			_, hasLevel := in["severity"]
			_, hasText := in["text"]
			So(hasLevel || hasText, ShouldBeFalse)
			So(out, ShouldContainSubstring, "hello")
			So(out, ShouldContainSubstring, "GM_log")
			So(out, ShouldContainSubstring, `bad\nline`)
		})

		Convey("--since 接受相对时长与 RFC3339", func() {
			req := stubDaemonCapturing(t, logsResult(false))
			code, _ := runCLI("logs", "u1", "--since", "2026-01-01T00:00:00Z")
			So(code, ShouldEqual, exitOK)
			So(inputMap(req.Input)["since"], ShouldEqual, float64(1767225600000))
			before := time.Now().Add(-30 * time.Minute).UnixMilli()
			code, _ = runCLI("logs", "u1", "--since", "30m")
			after := time.Now().Add(-30 * time.Minute).UnixMilli()
			So(code, ShouldEqual, exitOK)
			So(int64(inputMap(req.Input)["since"].(float64)), ShouldBeBetweenOrEqual, before, after)
		})

		Convey("--level 映射为 severity,warning 是 warn 的别名;--text、--limit 下发", func() {
			req := stubDaemonCapturing(t, logsResult(false))
			code, _ := runCLI("logs", "u1", "--level", "warning", "--text", "abc", "--limit", "5")
			So(code, ShouldEqual, exitOK)
			in := inputMap(req.Input)
			So(in["severity"], ShouldEqual, "warn")
			So(in["text"], ShouldEqual, "abc")
			So(in["limit"], ShouldEqual, float64(5))
			code, _ = runCLI("logs", "u1", "--level", "error")
			So(code, ShouldEqual, exitOK)
			So(inputMap(req.Input)["severity"], ShouldEqual, "error")
		})

		Convey("非法 --level/--since/--limit:退出码 3,且不发起调用", func() {
			for _, args := range [][]string{
				{"--level", "fatal"}, {"--since", "yesterday"}, {"--limit", "0"}, {"--limit", "1001"},
			} {
				rec := stubDaemonRecording(t, logsResult(false))
				code, _ := runCLI(append([]string{"logs", "u1"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(rec.snapshot(), ShouldBeEmpty)
			}
		})

		Convey("无记录:表格不炸,退出 0", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"contentTrust":"untrusted-user-script-log","truncated":false,"logs":[]}`)})
			code, _ := runCLI("logs", "u1")
			So(code, ShouldEqual, exitOK)
		})

		Convey("truncated:提示写到 stderr", func() {
			stubDaemon(t, logsResult(true))
			code, _, errOut := runCLICapture("logs", "u1")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "truncated")
			So(errOut, ShouldContainSubstring, "--since")
		})

		Convey("component、fields 与 severity 里的控制字符经转义,不会原样到终端", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(
				`{"truncated":false,"logs":[{"time":1730000000123,"severity":"in\u001b[31mfo","message":"m","component":"GM\u001b]0;x\u0007_log","fields":[{"name":"k\u001b[2J","value":"v\u001b[2J\nz"}]}]}`)})
			code, out := runCLI("logs", "u1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "\x1b")
			So(out, ShouldNotContainSubstring, "\x07")
			So(out, ShouldContainSubstring, `\n`)
		})

		Convey("-o json 输出结果对象", func() {
			stubDaemon(t, logsResult(false))
			code, out := runCLI("logs", "u1", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var got map[string]any
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got["logs"], ShouldHaveLength, 2)
		})

		Convey("旧版 ScriptCat:退出码 3 并提示升级;断开:退出码 2", func() {
			stubDaemon(t, control.CallResult{Error: &control.CallError{Code: "METHOD_NOT_FOUND", Message: "scripts.logs"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "logs", "u1")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "upgrade ScriptCat")
			stubDaemon(t, control.CallResult{Error: &control.CallError{Code: "OPERATION_EXPIRED", Message: "gone"}})
			code, _ = runCLI("logs", "u1")
			So(code, ShouldEqual, exitVoided)
		})
	})
}
