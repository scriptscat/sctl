package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestScriptCatTargetFlag(t *testing.T) {
	ok := control.CallResult{OK: true, Result: []byte(`{"scripts":[],"uuid":"u1","code":"x","matches":[],"name":"n"}`)}
	// 每个 scripts 动词的最小可运行命令行(写动词会阻塞在审批,这里的假 daemon 立即应答)。
	verbs := map[string][]string{
		"get":     {"get", "u1"},
		"grep":    {"grep", "u1", "q"},
		"edit":    {"edit", "u1", "--replace", "a", "--with", "b"},
		"install": {"install", "https://example.com/a.user.js"},
		"enable":  {"enable", "u1"},
		"disable": {"disable", "u1"},
		"delete":  {"delete", "u1"},
	}

	Convey("get/grep/edit/install/enable/disable/delete 的 --scriptcat 随 /control/call 发给 daemon", t, func() {
		for _, args := range verbs {
			req := stubDaemonCapturing(t, ok)
			t.Setenv("SCTL_SCRIPTCAT", "")
			code, _ := runCLI(append(append([]string{}, args...), "--scriptcat", "work")...)
			So(code, ShouldEqual, exitOK)
			So(req.ScriptCat, ShouldEqual, "work")
			So(req.Browser, ShouldEqual, "")
		}
	})

	Convey("SCTL_SCRIPTCAT 是默认目标,显式 --scriptcat 优先,都不设置时目标为空", t, func() {
		for _, args := range verbs {
			req := stubDaemonCapturing(t, ok)
			t.Setenv("SCTL_SCRIPTCAT", "env-sc")
			code, _ := runCLI(args...)
			So(code, ShouldEqual, exitOK)
			So(req.ScriptCat, ShouldEqual, "env-sc")

			req = stubDaemonCapturing(t, ok)
			code, _ = runCLI(append(append([]string{}, args...), "--scriptcat", "flag-sc")...)
			So(code, ShouldEqual, exitOK)
			So(req.ScriptCat, ShouldEqual, "flag-sc")

			req = stubDaemonCapturing(t, ok)
			t.Setenv("SCTL_SCRIPTCAT", "")
			code, _ = runCLI(args...)
			So(code, ShouldEqual, exitOK)
			So(req.ScriptCat, ShouldEqual, "")
		}
	})

	Convey("get 的 source 与 metadata 形态同样带上目标", t, func() {
		req := stubDaemonCapturing(t, ok)
		t.Setenv("SCTL_SCRIPTCAT", "")
		code, _ := runCLI("get", "u1", "-o", "source", "--scriptcat", "work")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "scripts.source.get")
		So(req.ScriptCat, ShouldEqual, "work")
		code, _ = runCLI("get", "--scriptcat", "work2")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "scripts.list")
		So(req.ScriptCat, ShouldEqual, "work2")
	})
}

func TestScriptCatTargetErrors(t *testing.T) {
	Convey("未指定目标的 SCRIPTCAT_AMBIGUOUS 在候选列表后提示 --scriptcat 与 SCTL_SCRIPTCAT,退出码 3", t, func() {
		t.Setenv("SCTL_SCRIPTCAT", "")
		stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "SCRIPTCAT_AMBIGUOUS", Message: "several ScriptCats are online: a (id-a), b (id-b)"}})
		code, _, _, err := runCLIResult(strings.NewReader(""), "get", "u1")
		So(code, ShouldEqual, exitError)
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "a (id-a), b (id-b)")
		So(err.Error(), ShouldContainSubstring, "; pass --scriptcat <name> or set SCTL_SCRIPTCAT")
	})

	Convey("写动词的 SCRIPTCAT_AMBIGUOUS 同样带提示", t, func() {
		t.Setenv("SCTL_SCRIPTCAT", "")
		stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "SCRIPTCAT_AMBIGUOUS", Message: "several"}})
		_, _, _, err := runCLIResult(strings.NewReader(""), "delete", "u1")
		So(err, ShouldNotBeNil)
		So(err.Error(), ShouldContainSubstring, "SCTL_SCRIPTCAT")
	})

	Convey("已给目标时不追加提示;OFFLINE 与 NOT_FOUND 按一般错误退出", t, func() {
		t.Setenv("SCTL_SCRIPTCAT", "")
		for _, c := range []string{"SCRIPTCAT_AMBIGUOUS", "SCRIPTCAT_OFFLINE", "SCRIPTCAT_NOT_FOUND"} {
			stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: c, Message: "msg"}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "get", "u1", "--scriptcat", "x")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldNotContainSubstring, "pass --scriptcat")
		}
		stubDaemon(t, control.CallResult{OK: false, Error: &control.CallError{Code: "SCRIPTCAT_OFFLINE", Message: "msg"}})
		code, _ := runCLI("get", "u1")
		So(code, ShouldEqual, exitError)
	})
}

func TestGetListShowsSourceColumnOnlyWhenMerged(t *testing.T) {
	Convey("sctl get 表格仅在列表项带 scriptcat 标签时多出来源实例列", t, func() {
		t.Setenv("SCTL_SCRIPTCAT", "")
		plain := `{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0"}]}`
		merged := `{"scripts":[{"uuid":"u1","name":"demo","enabled":true,"version":"1.0","scriptcat":{"id":"id-a","name":"work"}},` +
			`{"uuid":"u2","name":"other","enabled":false,"version":"2.0","scriptcat":{"name":"scriptcat"}}]}`

		stubDaemon(t, control.CallResult{OK: true, Result: []byte(plain)})
		_, before := runCLI("get")
		So(before, ShouldNotContainSubstring, "SCRIPTCAT")

		// 未合并(无标签)时输出与改动前逐字节相同。
		So(before, ShouldEqual, "UUID  NAME  ENABLED  VERSION\nu1    demo  true     1.0\n")

		stubDaemon(t, control.CallResult{OK: true, Result: []byte(merged)})
		code, out := runCLI("get")
		So(code, ShouldEqual, exitOK)
		So(out, ShouldContainSubstring, "SCRIPTCAT")
		So(out, ShouldContainSubstring, "work")
		So(out, ShouldContainSubstring, "scriptcat")
		lines := strings.Split(strings.TrimSpace(out), "\n")
		So(lines, ShouldHaveLength, 3)
		So(strings.Fields(lines[1])[len(strings.Fields(lines[1]))-1], ShouldEqual, "work")
	})
}
