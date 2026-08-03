package cli

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

// grepInputOf 取出 CLI 拼给 scripts.source.grep 的全部检索参数。
func grepInputOf(input json.RawMessage) map[string]any {
	var in map[string]any
	So(json.Unmarshal(input, &in), ShouldBeNil)
	return in
}

func grepResultOf(matches string, extra string) control.CallResult {
	body := `{"uuid":"u1","name":"demo","matches":` + matches + `,"totalLines":10,"sha256":"h","contentTrust":"untrusted-user-script-source"` + extra + `}`
	return control.CallResult{OK: true, Result: json.RawMessage(body)}
}

func TestGrepInputMapping(t *testing.T) {
	Convey("grep 把标志映射到 scripts.source.grep 的检索参数", t, func() {
		Convey("默认只下发 uuid 与 query,其余字段留给扩展侧默认值", func() {
			req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0,"truncated":false,"skippedLongLines":0`))
			code, _ := runCLI("grep", "u1", "needle")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "scripts.source.grep")
			So(grepInputOf(req.Input), ShouldResemble, map[string]any{"uuid": "u1", "query": "needle"})
		})

		Convey("-E 才切到 regex 档;不给 -E 时不下发 mode(即字面量 text 档)", func() {
			req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
			code, _ := runCLI("grep", "u1", "a.*b", "-E")
			So(code, ShouldEqual, exitOK)
			So(grepInputOf(req.Input)["mode"], ShouldEqual, "regex")

			req = stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
			code, _ = runCLI("grep", "u1", "a.*b")
			So(code, ShouldEqual, exitOK)
			So(grepInputOf(req.Input), ShouldNotContainKey, "mode")
		})

		Convey("-i / -C / -m 分别映射为 ignoreCase / contextLines / maxMatches", func() {
			req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
			code, _ := runCLI("grep", "u1", "needle", "-i", "-C", "2", "-m", "5")
			So(code, ShouldEqual, exitOK)
			in := grepInputOf(req.Input)
			So(in["ignoreCase"], ShouldEqual, true)
			So(in["contextLines"], ShouldEqual, float64(2))
			So(in["maxMatches"], ShouldEqual, float64(5))
		})

		Convey("-C 0 显式给出时照样下发(与「没给」区分开)", func() {
			req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
			code, _ := runCLI("grep", "u1", "needle", "-C", "0")
			So(code, ShouldEqual, exitOK)
			So(grepInputOf(req.Input), ShouldContainKey, "contextLines")
		})

		Convey("资源词被吞掉后下发的仍是资源词之后的 uuid 与 query", func() {
			for _, word := range []string{"scripts", "script", "sc"} {
				req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
				code, _ := runCLI("grep", word, "u1", "needle")
				So(code, ShouldEqual, exitOK)
				in := grepInputOf(req.Input)
				So(in["uuid"], ShouldEqual, "u1")
				So(in["query"], ShouldEqual, "needle")
			}
		})
	})
}

func TestGrepArgumentErrors(t *testing.T) {
	// 起 stub daemon 并断言 req.Action 为空:daemon 连不上同样是退出码 3,不起 stub 的话把参数校验
	// 整段删掉这些断言照样绿。
	Convey("grep 的参数个数错误按退出码 3 报错,且不向桥接发起调用", t, func() {
		for _, args := range [][]string{
			{"grep"},
			{"grep", "u1"},
			{"grep", "sc", "u1"},
			{"grep", "u1", "needle", "extra"},
		} {
			req := stubDaemonCapturing(t, grepResultOf(`[]`, `,"totalMatches":0`))
			code, _ := runCLI(args...)
			So(code, ShouldEqual, exitError)
			So(req.Action, ShouldEqual, "")
		}
	})
}

func TestGrepOutput(t *testing.T) {
	Convey("grep 人读输出照 grep -n 的体例", t, func() {
		Convey("命中行打 <行号>:<内容>", func() {
			stubDaemon(t, grepResultOf(
				`[{"lineNumber":12,"line":"  const x = 1;","before":[],"after":[]},{"lineNumber":30,"line":"  const x = 2;","before":[],"after":[]}]`,
				`,"totalMatches":2,"truncated":false,"skippedLongLines":0`))
			code, out := runCLI("grep", "u1", "const x")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "12:  const x = 1;\n30:  const x = 2;\n")
		})

		Convey("-C 的上下文行按 <行号>-<内容> 编号,块之间以 -- 分隔", func() {
			stubDaemon(t, grepResultOf(
				`[{"lineNumber":2,"line":"hit a","before":["one"],"after":["three"]},{"lineNumber":8,"line":"hit b","before":["seven"],"after":["nine"]}]`,
				`,"totalMatches":2,"truncated":false,"skippedLongLines":0`))
			code, out := runCLI("grep", "u1", "hit", "-C", "1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "1-one\n2:hit a\n3-three\n--\n7-seven\n8:hit b\n9-nine\n")
		})

		Convey("零命中时退出码 0 且 stdout 为空(退出码 1 已被「用户拒绝」占用)", func() {
			stubDaemon(t, grepResultOf(`[]`, `,"totalMatches":0,"truncated":false,"skippedLongLines":0`))
			code, out := runCLI("grep", "u1", "nothing")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "")
		})

		Convey("截断与跳过长行的提示走 stderr,不混进 stdout", func() {
			stubDaemon(t, grepResultOf(
				`[{"lineNumber":1,"line":"hit","before":[],"after":[]}]`,
				`,"totalMatches":9,"truncated":true,"skippedLongLines":3`))
			code, out, errOut := runCLICapture("grep", "u1", "hit", "-m", "1")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "1:hit\n")
			So(errOut, ShouldContainSubstring, "9")
			So(errOut, ShouldContainSubstring, "3")
		})

		Convey("-o json 原样打印结果 JSON", func() {
			stubDaemon(t, grepResultOf(
				`[{"lineNumber":1,"line":"hit","before":[],"after":[]}]`,
				`,"totalMatches":1,"truncated":false,"skippedLongLines":0`))
			code, out := runCLI("grep", "u1", "hit", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var parsed map[string]any
			So(json.Unmarshal([]byte(out), &parsed), ShouldBeNil)
			So(parsed, ShouldContainKey, "matches")
		})
	})
}
