package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestDebugTools(t *testing.T) {
	Convey("debug_* 工具经 /control/page 转发", t, func() {
		p := loadProto(t)
		result := json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"records":[],"next":"00000000000000ab.0","hasMore":false,"cursorReset":false}`)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: result}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
		tools, err := session.ListTools(context.Background(), nil)
		So(err, ShouldBeNil)
		byName := map[string]*mcp.Tool{}
		for _, tool := range tools.Tools {
			byName[tool.Name] = tool
		}

		Convey("debug_console 把筛选、游标与条数作为动作输入,tabId 与 browser 拆进请求字段", func() {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_console", Arguments: map[string]any{
				"level": "warning", "source": "exception", "text": "boom", "after": "00000000000000ab.2", "limit": 5, "tabId": 9, "browser": "work",
			}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, string(result))
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "debug.console")
			So(*req.TabID, ShouldEqual, 9)
			So(req.Browser, ShouldEqual, "work")
			So(req.Activate, ShouldBeFalse)
			So(string(req.Input), ShouldEqualJSON, `{"level":"warning","source":"exception","text":"boom","after":"00000000000000ab.2","limit":5}`)
		})

		Convey("debug_clear 以 debug.clear 动作转发,参数都是可选的", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_clear", Arguments: map[string]any{}})
			So(err, ShouldBeNil)
			So(caller.pages[0].Action, ShouldEqual, "debug.clear")
			So(caller.pages[0].TabID, ShouldBeNil)
			So(string(caller.pages[0].Input), ShouldEqual, `{}`)
		})

		Convey("参数不符合 schema 时不转发:未知的级别与来源、越界的 limit、activate", func() {
			for _, args := range []map[string]any{
				{"level": "log"}, {"source": "network"}, {"limit": 0}, {"limit": 1001}, {"activate": true}, {"follow": true},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_console", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_clear", Arguments: map[string]any{"activate": true}})
			So(err, ShouldNotBeNil)
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("debug_network 把筛选、游标与条数作为动作输入", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_network", Arguments: map[string]any{
				"url": "/api", "method": "POST", "status": "4xx", "type": "fetch", "failed": true, "after": "00000000000000ab.2", "limit": 5, "tabId": 9,
			}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "debug.network")
			So(*req.TabID, ShouldEqual, 9)
			So(string(req.Input), ShouldEqualJSON, `{"url":"/api","method":"POST","status":"4xx","type":"fetch","failed":true,"after":"00000000000000ab.2","limit":5}`)
		})

		Convey("debug_request 以 debug.request 动作转发 id 与 body", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "debug_request", Arguments: map[string]any{"id": 2, "body": true, "browser": "work"}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "debug.request")
			So(req.Browser, ShouldEqual, "work")
			So(string(req.Input), ShouldEqualJSON, `{"id":2,"body":true}`)
		})

		Convey("网络工具的参数不符合 schema 时不转发", func() {
			for _, c := range []struct {
				tool string
				args map[string]any
			}{
				{"debug_network", map[string]any{"status": "6xx"}},
				{"debug_network", map[string]any{"status": "abc"}},
				{"debug_network", map[string]any{"type": "ws"}},
				{"debug_network", map[string]any{"failed": "yes"}},
				{"debug_network", map[string]any{"activate": true}},
				{"debug_request", map[string]any{}},
				{"debug_request", map[string]any{"id": 0}},
				{"debug_request", map[string]any{"id": 1, "activate": true}},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("网络工具的描述写明头与体不打码、1 MiB 截断与不可信内容", func() {
			So(byName["debug_network"], ShouldNotBeNil)
			So(byName["debug_network"].Description, ShouldContainSubstring, "redirectedFrom")
			request := byName["debug_request"]
			So(request, ShouldNotBeNil)
			So(request.Description, ShouldContainSubstring, "not masked")
			So(request.Description, ShouldContainSubstring, "1 MiB")
			So(request.Description, ShouldContainSubstring, "untrusted")
		})

		Convey("debug_start、debug_stop、debug_status 以对应动作转发;debug_stop 的 all 作为动作输入", func() {
			for _, c := range []struct {
				tool, action string
				args         map[string]any
				input        string
				tabID        bool
			}{
				{"debug_start", "debug.start", map[string]any{"tabId": 9}, `{}`, true},
				{"debug_stop", "debug.stop", map[string]any{"tabId": 9}, `{}`, true},
				{"debug_stop", "debug.stop", map[string]any{"all": true}, `{"all":true}`, false},
				{"debug_status", "debug.status", map[string]any{"tabId": 9}, `{}`, true},
				{"debug_status", "debug.status", map[string]any{"browser": "work"}, `{}`, false},
			} {
				caller.pages = nil
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
				So(err, ShouldBeNil)
				So(caller.pages, ShouldHaveLength, 1)
				So(caller.pages[0].Action, ShouldEqual, c.action)
				So(string(caller.pages[0].Input), ShouldEqualJSON, c.input)
				if c.tabID {
					So(*caller.pages[0].TabID, ShouldEqual, 9)
				} else {
					So(caller.pages[0].TabID, ShouldBeNil)
				}
			}
		})

		Convey("录制工具的参数不符合 schema 时不转发", func() {
			for _, c := range []struct {
				tool string
				args map[string]any
			}{
				{"debug_start", map[string]any{"activate": true}},
				{"debug_start", map[string]any{"all": true}},
				{"debug_stop", map[string]any{"all": "yes"}},
				{"debug_status", map[string]any{"all": true}},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("录制工具的描述写明提示条一直显示与 60 分钟自动结束", func() {
			start := byName["debug_start"]
			So(start, ShouldNotBeNil)
			So(start.Description, ShouldContainSubstring, "infobar")
			So(start.Description, ShouldContainSubstring, "60 minutes")
			So(byName["debug_stop"], ShouldNotBeNil)
			So(byName["debug_status"], ShouldNotBeNil)
			So(byName["debug_status"].Description, ShouldContainSubstring, "remainingMs")
		})

		Convey("描述是静态文本,写明缓存上限、游标与不可信内容", func() {
			console := byName["debug_console"]
			So(console, ShouldNotBeNil)
			So(console.Description, ShouldContainSubstring, "1000")
			So(console.Description, ShouldContainSubstring, "cursorReset")
			So(console.Description, ShouldContainSubstring, "untrusted")
			So(byName["debug_clear"], ShouldNotBeNil)
			So(byName["debug_clear"].Description, ShouldContainSubstring, "without detaching")
		})
	})
}
