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

		Convey("page_eval 的 ref 留在动作输入里,schema 与静态描述都提到它", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_eval", Arguments: map[string]any{
				"expression": "el => el.textContent", "ref": "e5",
			}})
			So(err, ShouldBeNil)
			So(string(caller.pages[0].Input), ShouldEqualJSON, `{"expression":"el => el.textContent","ref":"e5"}`)
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			for _, tool := range tools.Tools {
				switch tool.Name {
				case "page_eval":
					schema, err := json.Marshal(tool.InputSchema)
					So(err, ShouldBeNil)
					So(string(schema), ShouldContainSubstring, `"ref"`)
					So(tool.Description, ShouldContainSubstring, "ref")
				case "page_snapshot":
					So(tool.Description, ShouldNotContainSubstring, "Same-process")
					So(tool.Description, ShouldContainSubstring, "[unavailable]")
				}
			}
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

		Convey("page_snapshot 把 root 作为动作输入转发,tabId 与 browser 作为目标", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_snapshot", Arguments: map[string]any{
				"root": "e5", "tabId": 9, "browser": "work",
			}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "snapshot")
			So(*req.TabID, ShouldEqual, 9)
			So(req.Browser, ShouldEqual, "work")
			So(string(req.Input), ShouldEqual, `{"root":"e5"}`)
		})

		Convey("page_snapshot 的参数都是可选的", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_snapshot", Arguments: map[string]any{}})
			So(err, ShouldBeNil)
			So(string(caller.pages[0].Input), ShouldEqual, `{}`)
		})

		Convey("page_snapshot 拒绝空的 root 与未知参数", func() {
			for _, args := range []map[string]any{{"root": ""}, {"ref": "e5"}} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_snapshot", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("参数不符合 schema 时不转发", func() {
			for _, args := range []map[string]any{
				{},
				{"expression": ""},
				{"expression": "1", "tabId": -1},
				{"expression": "1", "ref": ""},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_eval", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("page_click 把目标与鼠标参数作为动作输入转发,目标参数拆进请求字段", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_click", Arguments: map[string]any{
				"selector": "#go", "button": "middle", "count": 2, "modifiers": []string{"Alt", "Meta"}, "tabId": 9, "activate": true, "timeoutMs": 4000,
			}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "click")
			So(*req.TabID, ShouldEqual, 9)
			So(req.Activate, ShouldBeTrue)
			So(req.TimeoutMs, ShouldEqual, 4000)
			So(string(req.Input), ShouldEqualJSON, `{"selector":"#go","button":"middle","count":2,"modifiers":["Alt","Meta"]}`)
		})

		Convey("page_hover 只带引用时请求不带目标", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_hover", Arguments: map[string]any{"ref": "e5"}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "hover")
			So(req.TabID, ShouldBeNil)
			So(string(req.Input), ShouldEqualJSON, `{"ref":"e5"}`)
		})

		Convey("page_click 与 page_hover 的参数不符合 schema 时不转发", func() {
			for name, args := range map[string][]map[string]any{
				"page_click": {
					{"ref": "e5", "button": "back"},
					{"ref": "e5", "count": 0},
					{"ref": "e5", "modifiers": []string{"Hyper"}},
					{"ref": ""},
				},
				"page_hover": {
					{"ref": "e5", "button": "left"},
					{"selector": ""},
				},
			} {
				for _, a := range args {
					_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: a})
					So(err, ShouldNotBeNil)
				}
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("page_click 与 page_hover 的静态描述写明目标二选一、自动等待与结果", func() {
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			seen := 0
			for _, tool := range tools.Tools {
				if tool.Name != "page_click" && tool.Name != "page_hover" {
					continue
				}
				seen++
				So(tool.Description, ShouldContainSubstring, "exactly one of ref or selector")
				So(tool.Description, ShouldContainSubstring, "TARGET_AMBIGUOUS")
				So(tool.Description, ShouldContainSubstring, "PAGE_HIDDEN")
				schema, err := json.Marshal(tool.InputSchema)
				So(err, ShouldBeNil)
				So(string(schema), ShouldContainSubstring, `"activate"`)
			}
			So(seen, ShouldEqual, 2)
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
