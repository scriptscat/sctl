package mcpserver

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestCdpSendTool(t *testing.T) {
	Convey("cdp_send 经 /control/page 转发", t, func() {
		p := loadProto(t)
		result := json.RawMessage(`{"tabId":5,"contentTrust":"untrusted-page-content"}`)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: result}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		Convey("method、params 进动作输入,tabId、browser、timeoutMs 拆进请求字段;没有 activate", func() {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "cdp_send", Arguments: map[string]any{
				"method": "DOM.getDocument", "params": map[string]any{"depth": 1}, "tabId": 9, "browser": "work", "timeoutMs": 2000,
			}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldEqual, string(result))
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "cdp.send")
			So(*req.TabID, ShouldEqual, 9)
			So(req.Browser, ShouldEqual, "work")
			So(req.TimeoutMs, ShouldEqual, 2000)
			So(string(req.Input), ShouldEqualJSON, `{"method":"DOM.getDocument","params":{"depth":1}}`)
		})

		Convey("缺 method、params 不是对象、带 activate 时不转发", func() {
			for _, args := range []map[string]any{
				{}, {"method": "Page.enable", "params": []any{}}, {"method": "Page.enable", "activate": true}, {"method": 5},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "cdp_send", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("daemon 的错误原样成为工具错误", func() {
			caller.result = control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: `{"code":-32601,"message":"'Foo.bar' wasn't found"}`}}
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "cdp_send", Arguments: map[string]any{"method": "Foo.bar"}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "wasn't found")
		})

		Convey("描述是静态文本,写明拒绝的命令、副作用与恢复办法", func() {
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			var desc string
			for _, tool := range tools.Tools {
				if tool.Name == "cdp_send" {
					desc = tool.Description
				}
			}
			for _, want := range []string{
				"Fetch.disable", "Debugger.resume", "page_detach", "Target.setAutoAttach", "Page.disable",
				"Page.handleJavaScriptDialog", "INVALID_REQUEST", "PAYLOAD_TOO_LARGE", "untrusted",
			} {
				So(desc, ShouldContainSubstring, want)
			}
			_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "cdp_send", Arguments: map[string]any{"method": "Page.enable"}})
			So(err, ShouldBeNil)
			tools, err = session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			for _, tool := range tools.Tools {
				if tool.Name == "cdp_send" {
					So(tool.Description, ShouldEqual, desc)
				}
			}
		})
	})
}
