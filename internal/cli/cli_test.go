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

	"github.com/scriptscat/sctl/internal/audit"
	"github.com/scriptscat/sctl/internal/control"
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
			code, out, errOut := runCLICapture("install", "--json", "https://example.com/x.user.js")
			So(code, ShouldEqual, exitOK)
			So(errOut, ShouldContainSubstring, "等待浏览器确认")
			// stdout 必须是干净可解析的 JSON,提示混进来就会解析失败。
			var parsed map[string]any
			So(json.Unmarshal([]byte(out), &parsed), ShouldBeNil)
		})

		Convey("只读动词不提示", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`[]`)})
			_, _, errOut := runCLICapture("scripts", "list", "--json")
			So(errOut, ShouldNotContainSubstring, "等待浏览器确认")
		})

		Convey("daemon 连不上时不该先报等待确认", func() {
			t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:1")
			dir := t.TempDir()
			t.Setenv("SCTL_DATA_DIR", dir)
			So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
			code, _, errOut := runCLICapture("rm", "u1")
			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "等待浏览器确认")
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
			So(out, ShouldContainSubstring, "近期安全事件")
			So(out, ShouldContainSubstring, "handshake.failed×2")
			So(out, ShouldContainSubstring, "request.rate_limited×1")
		})

		Convey("无事件时不打印安全事件行", func() {
			stubDaemonStatus(t, control.StatusResult{DaemonVersion: "0.1.0"})
			code, out := runCLI("status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldNotContainSubstring, "近期安全事件")
		})

		Convey("--json 输出完整事件供脚本消费", func() {
			stubDaemonStatus(t, control.StatusResult{
				DaemonVersion: "0.1.0",
				SecurityCount: 1,
				Security:      []audit.Event{{Type: audit.TypeHandshakeFailed, Reason: audit.ReasonHMACMismatch}},
			})
			code, out := runCLI("status", "--json")
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
			code, _ := runCLI("rm", "u1")
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
	Convey("--json 输出结构化结果", t, func() {
		Convey("scripts list --json 打印原始结果 JSON", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0"}]}`)})
			code, out := runCLI("--json", "scripts", "list")
			So(code, ShouldEqual, exitOK)
			// 输出可被解析回结构化对象,且含 scripts 键。
			var parsed map[string]any
			So(json.Unmarshal([]byte(out), &parsed), ShouldBeNil)
			So(parsed, ShouldContainKey, "scripts")
		})

		Convey("scripts list 人读表格含表头与脚本名", func() {
			stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0"}]}`)})
			code, out := runCLI("scripts", "list")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "UUID")
			So(out, ShouldContainSubstring, "demo")
		})
	})
}

func TestSourceToStdout(t *testing.T) {
	Convey("scripts source 把源码原样写 stdout", t, func() {
		stubDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"code":"// ==UserScript==\n","contentTrust":"untrusted-user-script-source"}`)})
		code, out := runCLI("scripts", "source", "u1")
		So(code, ShouldEqual, exitOK)
		So(out, ShouldEqual, "// ==UserScript==\n")
	})
}
