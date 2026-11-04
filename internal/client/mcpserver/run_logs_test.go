package mcpserver

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/cli/upgradehint"
	"github.com/scriptscat/sctl/internal/client/control"
)

const runLogsUUID = "11111111-2222-4333-8444-555555555555"

func okCaller(result string) *fakeCaller {
	return &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(result)}}
}

func callTool(t *testing.T, caller *fakeCaller, name string, args map[string]any) (*mcp.CallToolResult, error) {
	t.Helper()
	session := connect(t, Deps{Name: "s", Version: "v0", Proto: loadProto(t), Caller: caller}, nil)
	return session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
}

func TestScriptsRunAndLogsArgumentsMatchTheCLI(t *testing.T) {
	Convey("scripts_run / scripts_logs 的参数与边界和 CLI 等价,并按原样转发到对应方法", t, func() {
		Convey("scripts_run 转发 uuid/wait/timeoutMs,scriptcat 只作路由目标", func() {
			caller := okCaller(`{"status":"complete"}`)
			res, err := callTool(t, caller, "scripts_run", map[string]any{"uuid": runLogsUUID, "wait": false, "timeoutMs": 240000, "scriptcat": "work"})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(caller.actions, ShouldResemble, []string{"scripts.run"})
			So(caller.scriptCats, ShouldResemble, []string{"work"})
			So(string(caller.inputs[0]), ShouldEqual, `{"timeoutMs":240000,"uuid":"`+runLogsUUID+`","wait":false}`)
		})
		Convey("scripts_run 不替客户端补默认值,留给 ScriptCat 套用 wait=true、60s", func() {
			caller := okCaller(`{}`)
			_, err := callTool(t, caller, "scripts_run", map[string]any{"uuid": runLogsUUID})
			So(err, ShouldBeNil)
			So(string(caller.inputs[0]), ShouldEqual, `{"uuid":"`+runLogsUUID+`"}`)
		})
		Convey("scripts_run 的 timeoutMs 必须在 1..240000", func() {
			for _, bad := range []int{0, -1, 240001} {
				caller := okCaller(`{}`)
				_, err := callTool(t, caller, "scripts_run", map[string]any{"uuid": runLogsUUID, "timeoutMs": bad})
				So(err, ShouldNotBeNil)
				So(caller.actions, ShouldBeEmpty)
			}
		})
		Convey("scripts_run 缺 uuid 或带未知参数被拒绝", func() {
			caller := okCaller(`{}`)
			_, err := callTool(t, caller, "scripts_run", map[string]any{"wait": true})
			So(err, ShouldNotBeNil)
			_, err = callTool(t, caller, "scripts_run", map[string]any{"uuid": runLogsUUID, "force": true})
			So(err, ShouldNotBeNil)
			So(caller.actions, ShouldBeEmpty)
		})
		Convey("scripts_logs 转发 uuid/since/severity/text/limit", func() {
			caller := okCaller(`{"logs":[]}`)
			_, err := callTool(t, caller, "scripts_logs", map[string]any{"uuid": runLogsUUID, "since": 1700000000000, "severity": "warn", "text": "boom", "limit": 1000})
			So(err, ShouldBeNil)
			So(caller.actions, ShouldResemble, []string{"scripts.logs"})
			So(string(caller.inputs[0]), ShouldEqual, `{"limit":1000,"severity":"warn","since":1700000000000,"text":"boom","uuid":"`+runLogsUUID+`"}`)
		})
		Convey("scripts_logs 的 limit 必须在 1..1000,severity 限于 debug|info|warn|error,since 非负", func() {
			for _, bad := range []map[string]any{
				{"limit": 0}, {"limit": 1001}, {"severity": "fatal"}, {"since": -1}, {"since": "1h"},
			} {
				bad["uuid"] = runLogsUUID
				caller := okCaller(`{}`)
				_, err := callTool(t, caller, "scripts_logs", bad)
				So(err, ShouldNotBeNil)
				So(caller.actions, ShouldBeEmpty)
			}
		})
	})
}

func TestScriptsRunAndLogsResultsAndDescriptions(t *testing.T) {
	Convey("结果原样带回协议的 contentTrust;描述是静态能力说明,不含脚本可控文本", t, func() {
		Convey("contentTrust 随结构化结果返回", func() {
			caller := okCaller(`{"logs":[{"time":1,"severity":"info","message":"hi"}],"contentTrust":"untrusted-user-script-log"}`)
			res, err := callTool(t, caller, "scripts_logs", map[string]any{"uuid": runLogsUUID})
			So(err, ShouldBeNil)
			So(res.StructuredContent, ShouldContainKey, "contentTrust")
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, `"contentTrust":"untrusted-user-script-log"`)
		})
		Convey("描述把日志文本声明为不可信数据", func() {
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: loadProto(t), Caller: okCaller(`{}`)}, nil)
			list, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
			So(err, ShouldBeNil)
			found := 0
			for _, tl := range list.Tools {
				if tl.Name == "scripts_run" || tl.Name == "scripts_logs" {
					found++
					So(tl.Description, ShouldContainSubstring, "untrusted")
				}
			}
			So(found, ShouldEqual, 2)
		})
	})
}

func TestScriptsRunAndLogsDoNotReportWaitingForApproval(t *testing.T) {
	Convey("scripts_run / scripts_logs 不需要审批,阻塞期间不发审批 progress;其他 ScriptCat 工具保持原样", t, func() {
		old := progressInterval
		progressInterval = 10 * time.Millisecond
		defer func() { progressInterval = old }()

		var quiet atomic.Int32
		clock := make(chan struct{}, 64)
		block := make(chan struct{})
		caller := &fakeCaller{block: block, entered: make(chan struct{}, 4), result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		opts := &mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
				if tok, _ := req.Params.ProgressToken.(string); tok == "tok-run" || tok == "tok-logs" {
					quiet.Add(1)
					return
				}
				select {
				case clock <- struct{}{}:
				default:
				}
			},
		}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: loadProto(t), Caller: caller}, opts)
		call := func(name string, args map[string]any, token string) <-chan *mcp.CallToolResult {
			resCh := make(chan *mcp.CallToolResult, 1)
			go func() {
				res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args, Meta: mcp.Meta{"progressToken": token}})
				resCh <- res
			}()
			select {
			case <-caller.entered:
			case <-time.After(5 * time.Second):
				t.Fatalf("%s did not reach the bridge", name)
			}
			return resCh
		}
		runRes := call("scripts_run", map[string]any{"uuid": runLogsUUID}, "tok-run")
		logsRes := call("scripts_logs", map[string]any{"uuid": runLogsUUID}, "tok-logs")
		toggleRes := call("scripts_toggle_request", map[string]any{"uuid": "x", "enable": true}, "tok-toggle")
		for range 3 {
			select {
			case <-clock:
			case <-time.After(5 * time.Second):
				t.Fatal("the approval call reported no progress")
			}
		}
		close(block)
		So((<-runRes).IsError, ShouldBeFalse)
		So((<-logsRes).IsError, ShouldBeFalse)
		So((<-toggleRes).IsError, ShouldBeFalse)
		So(quiet.Load(), ShouldEqual, 0)
	})
}

func TestScriptsRunAndLogsUpgradeHint(t *testing.T) {
	Convey("旧版 ScriptCat 回 METHOD_NOT_FOUND 时附上与 CLI 相同的升级提示,其他错误不加", t, func() {
		notFound := &fakeCaller{result: control.CallResult{Error: &control.CallError{Code: "METHOD_NOT_FOUND", Message: "unknown method"}}}
		for _, name := range []string{"scripts_run", "scripts_logs"} {
			res, err := callTool(t, notFound, name, map[string]any{"uuid": runLogsUUID})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			text := res.Content[0].(*mcp.TextContent).Text
			So(text, ShouldStartWith, "METHOD_NOT_FOUND: unknown method")
			So(text, ShouldContainSubstring, upgradehint.Message)
		}
		other := &fakeCaller{result: control.CallResult{Error: &control.CallError{Code: "NOT_FOUND", Message: "no such script"}}}
		res, err := callTool(t, other, "scripts_run", map[string]any{"uuid": runLogsUUID})
		So(err, ShouldBeNil)
		So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, "NOT_FOUND: no such script")
		missing := &fakeCaller{result: control.CallResult{Error: &control.CallError{Code: "METHOD_NOT_FOUND", Message: "x"}}}
		res, err = callTool(t, missing, "scripts_list", map[string]any{})
		So(err, ShouldBeNil)
		So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, "METHOD_NOT_FOUND: x")
	})
}
