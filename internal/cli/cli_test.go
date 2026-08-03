package cli

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// stubDaemon 起一个假 daemon 控制端点:健康检查恒 200,/control/call 恒返回给定 CallResult,
// 并把前端指向它(SCTL_BRIDGE_ADDR + 临时 SCTL_DATA_DIR 里的 control.token)。
func stubDaemon(t *testing.T, result control.CallResult) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathCall, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
}

// runCLI 执行一次子命令,返回退出码与 stdout。
func runCLI(args ...string) (int, string) {
	code, out, _ := runCLICapture(args...)
	return code, out
}

// runCLICapture 执行一次子命令,分别返回退出码、stdout 与 stderr。
func runCLICapture(args ...string) (int, string, string) {
	return runCLIStdin(strings.NewReader(""), args...)
}

// runCLIStdin 与 runCLICapture 相同,但为命令喂入给定 stdin(`edit -f -` 从 stdin 读 edits 用)。
func runCLIStdin(stdin io.Reader, args ...string) (int, string, string) {
	oldOut, oldErr := os.Stdout, os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout, os.Stderr = wOut, wErr
	outCh, errCh := make(chan string, 1), make(chan string, 1)
	go func() {
		b, _ := io.ReadAll(rOut)
		outCh <- string(b)
	}()
	go func() {
		b, _ := io.ReadAll(rErr)
		errCh <- string(b)
	}()

	root := NewRootCmd()
	root.SetArgs(args)
	root.SetIn(stdin)
	err := root.Execute()

	wOut.Close()
	wErr.Close()
	os.Stdout, os.Stderr = oldOut, oldErr
	return exitCodeOf(err), <-outCh, <-errCh
}

func exitCodeOf(err error) int {
	if err == nil {
		return exitOK
	}
	var ee *ExitError
	if errors.As(err, &ee) {
		return ee.Code
	}
	return -1
}

// stubDaemonCapturing 与 stubDaemon 相同,额外把最近一次 /control/call 的请求体记录到返回的
// *control.CallRequest 里,供断言 CLI 实际拼出的 action/input(如 get --lines 传下去的行窗字段)。
func stubDaemonCapturing(t *testing.T, result control.CallResult) *control.CallRequest {
	t.Helper()
	captured := &control.CallRequest{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathCall, func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(captured)
		_ = json.NewEncoder(w).Encode(result)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return captured
}

// uuidOfInput 取出 CLI 拼给桥接的 input 里的 uuid,用于断言资源词被吞掉之后下发的仍是真 uuid。
func uuidOfInput(input json.RawMessage) string {
	var in struct {
		UUID string `json:"uuid"`
	}
	So(json.Unmarshal(input, &in), ShouldBeNil)
	return in.UUID
}

// stubDaemonStatus 起一个假 daemon,/control/status 恒返回给定状态。
func stubDaemonStatus(t *testing.T, st control.StatusResult) {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc(control.PathStatus, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(st)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
}

func TestBlockingHint(t *testing.T) {
	Convey("写动词在等待浏览器裁决期间给出提示", t, func() {
		Convey("提示走 stderr,不污染 stdout 的结构化输出", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"x"}`)})
			code, out, errOut := runCLICapture("install", "-o", "json", "https://example.com/x.user.js")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "waiting for approval in the browser")
			// stdout 必须是干净可解析的 JSON,提示混进来就会解析失败。
			var parsed map[string]any
			So(json.Unmarshal([]byte(out), &parsed), ShouldBeNil)
		})

		Convey("只读动词不提示", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[]}`)})
			_, _, errOut := runCLICapture("get", "-o", "json")
			So(errOut, ShouldNotContainSubstring, "waiting for approval in the browser")
		})

		Convey("daemon 连不上时不该先报等待确认", func() {
			t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:1")
			dir := t.TempDir()
			t.Setenv("SCTL_DATA_DIR", dir)
			So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
			code, _, errOut := runCLICapture("delete", "u1")
			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "waiting for approval in the browser")
		})
	})
}

func TestStatusSecurityEvents(t *testing.T) {
	Convey("status 呈现守卫侧安全事件", t, func() {
		Convey("有事件时人读输出给出按类型聚合的摘要", func() {
			stubDaemonStatus(t, control.StatusResult{
				DaemonVersion: "0.1.0",
				SecurityCount: 3,
				Security: []audit.Event{
					{Type: audit.TypeHandshakeFailed, Reason: audit.ReasonHMACMismatch},
					{Type: audit.TypeHandshakeFailed, Reason: audit.ReasonHMACMismatch},
					{Type: audit.TypeRequestRateLimited, Client: "c1"},
				},
			})
			code, out := runCLI("status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "recent security events")
			So(out, ShouldContainSubstring, "handshake.failed×2")
			So(out, ShouldContainSubstring, "request.rate_limited×1")
		})

		Convey("无事件时不打印安全事件行", func() {
			stubDaemonStatus(t, control.StatusResult{DaemonVersion: "0.1.0"})
			code, out := runCLI("status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "recent security events")
		})

		Convey("-o json 输出完整事件供脚本消费", func() {
			stubDaemonStatus(t, control.StatusResult{
				DaemonVersion: "0.1.0",
				SecurityCount: 1,
				Security:      []audit.Event{{Type: audit.TypeHandshakeFailed, Reason: audit.ReasonHMACMismatch}},
			})
			code, out := runCLI("status", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var got control.StatusResult
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got.Security, ShouldHaveLength, 1)
			So(got.Security[0].Reason, ShouldEqual, audit.ReasonHMACMismatch)
		})
	})
}

func TestWriteVerbExitCodes(t *testing.T) {
	Convey("写动词按用户决策映射退出码", t, func() {
		Convey("批准 → 退出码 0", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"x","enabled":false}`)})
			code, _ := runCLI("install", "https://example.com/x.user.js")
			So(code, ShouldEqual, exitOK)
		})

		Convey("拒绝(USER_REJECTED)→ 退出码 1", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "USER_REJECTED"}})
			code, _ := runCLI("delete", "u1")
			So(code, ShouldEqual, exitRejected)
		})

		Convey("作废(OPERATION_EXPIRED)→ 退出码 2", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "OPERATION_EXPIRED"}})
			code, _ := runCLI("enable", "u1")
			So(code, ShouldEqual, exitVoided)
		})

		Convey("其他错误(NOT_FOUND)→ 退出码 3", func() {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no such script"}})
			code, _ := runCLI("disable", "u1")
			So(code, ShouldEqual, exitError)
		})
	})
}

func TestJSONOutput(t *testing.T) {
	Convey("-o json 输出结构化结果", t, func() {
		Convey("get -o json 打印原始结果 JSON", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0"}]}`)})
			code, out := runCLI("-o", "json", "get")
			So(code, ShouldEqual, exitOK)
			// 输出可被解析回结构化对象,且含 scripts 键。
			var parsed map[string]any
			So(json.Unmarshal([]byte(out), &parsed), ShouldBeNil)
			So(parsed, ShouldContainKey, "scripts")
		})

		Convey("get 人读表格含表头与脚本名", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0"}]}`)})
			code, out := runCLI("get")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "UUID")
			So(out, ShouldContainSubstring, "demo")
		})
	})
}

func TestSourceToStdout(t *testing.T) {
	Convey("get <uuid> -o source 把源码原样写 stdout", t, func() {
		Convey("源码不加尾换行,可重定向为 .user.js", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"// ==UserScript==\n","contentTrust":"untrusted-user-script-source"}`)})
			code, out := runCLI("get", "u1", "-o", "source")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "// ==UserScript==\n")
		})

		// --lines 开一个空行的窗口就会返回空 code,此时 stdout 必须是空的:退回打印结果 JSON 会把
		// 信封写进重定向出来的 .user.js。
		Convey("空源码就输出空,不退回打印结果 JSON", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"","contentTrust":"untrusted-user-script-source"}`)})
			code, out := runCLI("get", "u1", "-o", "source")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "")
		})
	})
}

func TestOutputFlagValidation(t *testing.T) {
	Convey("--output 只接受 table/json/source", t, func() {
		Convey("非法取值报错退出", func() {
			code, _ := runCLI("status", "-o", "bogus")
			So(code, ShouldEqual, exitError)
		})

		Convey("--json 全局标志已删除,只剩 -o/--output", func() {
			code, _ := runCLI("--json", "status")
			So(code, ShouldNotEqual, exitOK)
		})
	})
}

func TestOutputSourceRestriction(t *testing.T) {
	Convey("-o source 仅在 get <uuid> 下合法", t, func() {
		Convey("get 不带 uuid 时报错", func() {
			code, _ := runCLI("get", "-o", "source")
			So(code, ShouldEqual, exitError)
		})

		Convey("非 get 命令上用 -o source 报错", func() {
			code, _ := runCLI("status", "-o", "source")
			So(code, ShouldEqual, exitError)

			code, _ = runCLI("delete", "u1", "-o", "source")
			So(code, ShouldEqual, exitError)
		})

		Convey("get <uuid> -o source 合法", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"abc"}`)})
			code, out := runCLI("get", "u1", "-o", "source")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "abc")
		})

		Convey("被拒绝时不向桥接发起调用", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[]}`)})
			code, _ := runCLI("get", "-o", "source")
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		})
	})
}

func TestResourceWordOptional(t *testing.T) {
	Convey("get / delete / enable / disable 接受可省略的资源词 scripts|script|sc", t, func() {
		Convey("get 支持三种资源词简称,下发的仍是资源词之后的 uuid", func() {
			for _, word := range []string{"scripts", "script", "sc"} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"x","enabled":true,"version":"1.0"}`)})
				code, out := runCLI("get", word, "u1")
				So(code, ShouldEqual, exitOK)
				So(out, ShouldContainSubstring, "u1")
				So(req.Action, ShouldEqual, "scripts.metadata.get")
				So(uuidOfInput(req.Input), ShouldEqual, "u1")
			}
		})

		Convey("delete/enable/disable 同样接受资源词,下发的仍是资源词之后的 uuid", func() {
			for _, c := range []struct {
				args   []string
				action string
			}{
				{[]string{"delete", "sc", "u1"}, "scripts.delete.request"},
				{[]string{"enable", "script", "u1"}, "scripts.toggle.request"},
				{[]string{"disable", "scripts", "u1"}, "scripts.toggle.request"},
			} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1"}`)})
				code, _ := runCLI(c.args...)
				So(code, ShouldEqual, exitOK)
				So(req.Action, ShouldEqual, c.action)
				So(uuidOfInput(req.Input), ShouldEqual, "u1")
			}
		})

		Convey("资源词只在首个位置参数上被吞掉", func() {
			Convey("非首位的资源词按 uuid 计数,delete <uuid> sc 是参数错误", func() {
				code, _ := runCLI("delete", "u1", "sc")
				So(code, ShouldEqual, exitError)
			})

			Convey("uuid 恰好叫 sc 时,get sc sc 仍能指名到它", func() {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"sc"}`)})
				code, _ := runCLI("get", "sc", "sc")
				So(code, ShouldEqual, exitOK)
				So(req.Action, ShouldEqual, "scripts.metadata.get")
				So(uuidOfInput(req.Input), ShouldEqual, "sc")
			})
		})

		Convey("参数个数错误按约定退出码 3 报错,不占用「用户拒绝」的 1", func() {
			code, _, _ := runCLICapture("get", "scripts", "u1", "extra")
			So(code, ShouldEqual, exitError)

			code, _, _ = runCLICapture("delete", "scripts")
			So(code, ShouldEqual, exitError)

			code, _, _ = runCLICapture("enable")
			So(code, ShouldEqual, exitError)

			code, _, _ = runCLICapture("disable", "sc")
			So(code, ShouldEqual, exitError)
		})
	})
}

func TestDeleteAliasAndRemovedCommands(t *testing.T) {
	Convey("del 是 delete 唯一的别名,rm 与顶层 scripts 子命令已移除", t, func() {
		Convey("del 触发与 delete 相同的动作", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1"}`)})
			code, _ := runCLI("del", "u1")
			So(code, ShouldEqual, exitOK)
		})

		Convey("rm 命令不再存在", func() {
			code, _, _ := runCLICapture("rm", "u1")
			So(code, ShouldNotEqual, exitOK)
		})

		Convey("顶层 scripts 子命令不再存在", func() {
			code, _, _ := runCLICapture("scripts", "list")
			So(code, ShouldNotEqual, exitOK)
		})
	})
}

func TestGetCommand(t *testing.T) {
	Convey("get <uuid> 默认打印单行表格,-o json 才给完整元数据", t, func() {
		Convey("默认表格只含摘要字段", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"demo","enabled":true,"version":"1.0","description":"a very long field not in the table"}`)})
			code, out := runCLI("get", "u1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "UUID")
			So(out, ShouldContainSubstring, "demo")
			So(out, ShouldNotContainSubstring, "a very long field not in the table")
		})

		Convey("-o json 给出完整元数据", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"u1","name":"demo","description":"full metadata"}`)})
			code, out := runCLI("get", "u1", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "full metadata")
		})
	})
}

func TestGetLinesFlag(t *testing.T) {
	Convey("--lines 只与 -o source 同用,且透传给 scripts.source.get", t, func() {
		Convey("缺少 -o source 时报错", func() {
			code, _ := runCLI("get", "u1", "--lines", "1-3")
			So(code, ShouldEqual, exitError)
		})

		Convey("格式非法时报错", func() {
			code, _ := runCLI("get", "u1", "-o", "source", "--lines", "abc")
			So(code, ShouldEqual, exitError)
		})

		Convey("与 -o source 同用时把行窗传给 scripts.source.get", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"line2\nline3"}`)})
			code, _ := runCLI("get", "u1", "-o", "source", "--lines", "2-3")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.source.get")
			var in struct {
				UUID      string `json:"uuid"`
				StartLine int    `json:"startLine"`
				EndLine   int    `json:"endLine"`
			}
			So(json.Unmarshal(req.Input, &in), ShouldBeNil)
			So(in.UUID, ShouldEqual, "u1")
			So(in.StartLine, ShouldEqual, 2)
			So(in.EndLine, ShouldEqual, 3)
		})

		Convey("不带 --lines 时不下发行窗字段(扩展据此返回整份源码)", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"whole"}`)})
			code, _ := runCLI("get", "u1", "-o", "source")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldNotContainSubstring, "startLine")
			So(string(req.Input), ShouldNotContainSubstring, "endLine")
		})
	})
}

func TestParseLinesFlag(t *testing.T) {
	Convey("--lines 解析 1-based 闭区间", t, func() {
		Convey("空值表示未传该标志", func() {
			_, _, ok, err := parseLinesFlag("")
			So(err, ShouldBeNil)
			So(ok, ShouldBeFalse)
		})

		Convey("合法区间原样解出,单行窗口 A=B 合法", func() {
			start, end, ok, err := parseLinesFlag("2-3")
			So(err, ShouldBeNil)
			So(ok, ShouldBeTrue)
			So(start, ShouldEqual, 2)
			So(end, ShouldEqual, 3)

			start, end, ok, err = parseLinesFlag("3-3")
			So(err, ShouldBeNil)
			So(ok, ShouldBeTrue)
			So(start, ShouldEqual, 3)
			So(end, ShouldEqual, 3)
		})

		Convey("非法区间一律报错,不静默降级为「无行窗」", func() {
			// 依次为:非数字 / 缺分隔符 / 缺一端 / 多余分隔符 / 起点越界 / 起点为负 / 区间反向 /
			// 夹带空白 / 溢出 int64。
			for _, v := range []string{"abc", "5", "1-", "-3", "1-3-5", "0-3", "-1-3", "3-1", "1 - 3", "1-99999999999999999999"} {
				_, _, ok, err := parseLinesFlag(v)
				So(err, ShouldNotBeNil)
				So(ok, ShouldBeFalse)
			}
		})
	})
}
