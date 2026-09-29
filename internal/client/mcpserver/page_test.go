package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestPageToolsForwardToThePageEndpoint(t *testing.T) {
	Convey("page_* 工具经 /control/page 转发", t, func() {
		p := loadProto(t)
		result := json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"value":3}`)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: result}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		Convey("page_eval 把 browser、tabId、activate、timeoutMs 拆成请求字段,其余作为动作输入", func() {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_eval", Arguments: map[string]any{
				"expression": "1 + 2", "browser": "work", "tabId": 9, "activate": true, "timeoutMs": 2500,
			}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, string(result))
			So(caller.pages, ShouldHaveLength, 1)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "eval")
			So(req.Browser, ShouldEqual, "work")
			So(*req.TabID, ShouldEqual, 9)
			So(req.Activate, ShouldBeTrue)
			So(req.TimeoutMs, ShouldEqual, 2500)
			So(string(req.Input), ShouldEqual, `{"expression":"1 + 2"}`)
			So(caller.actions, ShouldBeEmpty)
		})

		Convey("省略可选参数时请求不带目标,标签页交给 daemon 选择", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_detach", Arguments: map[string]any{"all": true}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "detach")
			So(req.Browser, ShouldEqual, "")
			So(req.TabID, ShouldBeNil)
			So(req.Activate, ShouldBeFalse)
			So(req.TimeoutMs, ShouldEqual, 0)
			So(string(req.Input), ShouldEqual, `{"all":true}`)
		})

		Convey("参数不符合 schema 时不转发", func() {
			for _, args := range []map[string]any{
				{},
				{"expression": ""},
				{"expression": "1", "tabId": -1},
				{"expression": "1", "ref": "e5"},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_eval", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("页面错误作为工具错误结果返回,带错误码与消息", func() {
			caller.result = control.CallResult{OK: false, Error: &control.CallError{Code: "EVAL_ERROR", Message: "Error: boom"}}
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_eval", Arguments: map[string]any{"expression": "boom()"}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, "EVAL_ERROR: Error: boom")
		})
	})
}
