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
