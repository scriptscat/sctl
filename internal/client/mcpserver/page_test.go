package mcpserver

import (
	"context"
	"encoding/base64"
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

		Convey("page_dialog 把 action 与 text 作为动作输入,目标参数拆进请求字段", func() {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_dialog", Arguments: map[string]any{
				"action": "accept", "text": "Ada", "tabId": 4,
			}})
			So(err, ShouldBeNil)
			req := caller.pages[0]
			So(req.Action, ShouldEqual, "dialog")
			So(*req.TabID, ShouldEqual, 4)
			So(string(req.Input), ShouldEqualJSON, `{"action":"accept","text":"Ada"}`)
		})

		Convey("page_dialog 拒绝缺失或未知的 action;静态描述提到 DIALOG_OPEN 与 NOT_FOUND", func() {
			for _, args := range []map[string]any{{}, {"action": "ignore"}, {"action": "accept", "extra": 1}} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_dialog", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			found := false
			for _, tool := range tools.Tools {
				if tool.Name == "page_dialog" {
					found = true
					So(tool.Description, ShouldContainSubstring, "DIALOG_OPEN")
					So(tool.Description, ShouldContainSubstring, "NOT_FOUND")
				}
			}
			So(found, ShouldBeTrue)
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

		Convey("输入类工具把目标之外的参数作为动作输入转发,目标参数拆进请求字段", func() {
			for _, c := range []struct {
				tool, action string
				args         map[string]any
				input        string
			}{
				{"page_fill", "fill", map[string]any{"ref": "e3", "text": "hello", "tabId": 9}, `{"ref":"e3","text":"hello"}`},
				{"page_fill", "fill", map[string]any{"selector": "#name", "text": ""}, `{"selector":"#name","text":""}`},
				{"page_type", "type", map[string]any{"text": "abc", "activate": true}, `{"text":"abc"}`},
				{"page_press", "press", map[string]any{"key": "Control+A"}, `{"key":"Control+A"}`},
				{"page_select", "select", map[string]any{"ref": "e7", "values": []string{"red", "Green"}}, `{"ref":"e7","values":["red","Green"]}`},
				{"page_upload", "upload", map[string]any{"selector": "#file", "files": []string{"/tmp/a.txt"}}, `{"selector":"#file","files":["/tmp/a.txt"]}`},
				{"page_scroll", "scroll", map[string]any{"dy": 300, "dx": -10.5}, `{"dy":300,"dx":-10.5}`},
				{"page_scroll", "scroll", map[string]any{"ref": "e5"}, `{"ref":"e5"}`},
			} {
				caller.pages = nil
				res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: c.tool, Arguments: c.args})
				So(err, ShouldBeNil)
				So(res.IsError, ShouldBeFalse)
				So(caller.pages, ShouldHaveLength, 1)
				So(caller.pages[0].Action, ShouldEqual, c.action)
				So(string(caller.pages[0].Input), ShouldEqualJSON, c.input)
			}
		})

		Convey("输入类工具的参数不符合 schema 时不转发", func() {
			for name, args := range map[string][]map[string]any{
				"page_fill":   {{"ref": "e3"}, {"ref": "e3", "text": 5}, {"ref": "", "text": "x"}},
				"page_type":   {{}, {"text": ""}, {"text": "x", "ref": "e3"}},
				"page_press":  {{}, {"key": ""}},
				"page_select": {{"ref": "e7"}, {"ref": "e7", "values": []string{}}, {"ref": "e7", "values": "red"}},
				"page_upload": {{"ref": "e9"}, {"ref": "e9", "files": []string{}}, {"ref": "e9", "files": "/tmp/a"}},
				"page_scroll": {{"dy": "x"}, {"ref": "e5", "tabId": -1}},
			} {
				for _, a := range args {
					_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: a})
					So(err, ShouldNotBeNil)
				}
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("输入类工具的静态描述写明目标规则、专有行为与错误", func() {
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			wants := map[string][]string{
				"page_fill":   {"exactly one of ref or selector", "INVALID_REQUEST", "page_upload", "page_click", "input and change"},
				"page_type":   {"focus", "Enter"},
				"page_press":  {"Control+A", "Shift+Tab", "Meta+V"},
				"page_select": {"NOT_FOUND", "exactly one of ref or selector", "visible text"},
				"page_upload": {"absolute", "INVALID_REQUEST", "exactly one of ref or selector"},
				"page_scroll": {"dx", "dy", "into view"},
			}
			seen := 0
			for _, tool := range tools.Tools {
				want, ok := wants[tool.Name]
				if !ok {
					continue
				}
				seen++
				for _, sub := range want {
					So(tool.Description, ShouldContainSubstring, sub)
				}
				So(tool.Description, ShouldContainSubstring, "untrusted")
				schema, err := json.Marshal(tool.InputSchema)
				So(err, ShouldBeNil)
				So(string(schema), ShouldContainSubstring, `"activate"`)
				So(string(schema), ShouldContainSubstring, `"timeoutMs"`)
			}
			So(seen, ShouldEqual, len(wants))
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

func TestPageNavigateAndWaitTools(t *testing.T) {
	Convey("page_navigate 与 page_wait", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":5}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
		call := func(name string, args map[string]any) error {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
			return err
		}

		Convey("page_navigate 用 action 区分四种导航,以 navigate 动作转发,目标参数拆进请求字段", func() {
			for _, c := range []struct {
				args  map[string]any
				input string
			}{
				{map[string]any{"action": "goto", "url": "https://example.test/", "wait": "networkidle", "tabId": 9, "timeoutMs": 45000}, `{"action":"goto","url":"https://example.test/","wait":"networkidle"}`},
				{map[string]any{"action": "back"}, `{"action":"back"}`},
				{map[string]any{"action": "forward", "wait": "domcontentloaded"}, `{"action":"forward","wait":"domcontentloaded"}`},
				{map[string]any{"action": "reload", "activate": true}, `{"action":"reload"}`},
			} {
				caller.pages = nil
				So(call("page_navigate", c.args), ShouldBeNil)
				So(caller.pages, ShouldHaveLength, 1)
				So(caller.pages[0].Action, ShouldEqual, "navigate")
				So(string(caller.pages[0].Input), ShouldEqualJSON, c.input)
			}
			So(caller.pages[0].Activate, ShouldBeTrue)
		})

		Convey("page_wait 的每个条件作为动作输入转发", func() {
			for _, c := range []struct {
				args  map[string]any
				input string
			}{
				{map[string]any{"text": "Done", "timeoutMs": 2000}, `{"text":"Done"}`},
				{map[string]any{"gone": "Loading"}, `{"gone":"Loading"}`},
				{map[string]any{"selector": "#ready"}, `{"selector":"#ready"}`},
				{map[string]any{"selectorGone": ".spinner"}, `{"selectorGone":".spinner"}`},
				{map[string]any{"url": "/done"}, `{"url":"/done"}`},
				{map[string]any{"load": "load", "tabId": 3}, `{"load":"load"}`},
			} {
				caller.pages = nil
				So(call("page_wait", c.args), ShouldBeNil)
				So(caller.pages[0].Action, ShouldEqual, "wait")
				So(string(caller.pages[0].Input), ShouldEqualJSON, c.input)
			}
		})

		Convey("不符合 schema 的参数不转发", func() {
			for name, args := range map[string][]map[string]any{
				"page_navigate": {{}, {"action": "jump"}, {"action": "goto"}, {"action": "back", "url": "https://example.test/"}, {"action": "goto", "url": "x", "wait": "idle"}, {"action": "goto", "url": ""}},
				"page_wait":     {{}, {"text": "a", "gone": "b"}, {"load": "idle"}, {"text": ""}, {"url": "a", "selector": "b"}},
			} {
				for _, a := range args {
					So(call(name, a), ShouldNotBeNil)
				}
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("静态描述写明等待状态、错误码与默认超时", func() {
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			wants := map[string][]string{
				"page_navigate": {"goto", "back", "forward", "reload", "networkidle", "500 ms", "NAVIGATION_FAILED", "NOT_FOUND", "httpStatus", "30000", "untrusted"},
				"page_wait":     {"text", "gone", "selectorGone", "TIMEOUT", "10000", "visible"},
			}
			seen := 0
			for _, tool := range tools.Tools {
				want, ok := wants[tool.Name]
				if !ok {
					continue
				}
				seen++
				for _, sub := range want {
					So(tool.Description, ShouldContainSubstring, sub)
				}
				schema, err := json.Marshal(tool.InputSchema)
				So(err, ShouldBeNil)
				So(string(schema), ShouldContainSubstring, `"timeoutMs"`)
			}
			So(seen, ShouldEqual, len(wants))
		})
	})
}

func TestPageScreenshotReturnsImageContent(t *testing.T) {
	Convey("page_screenshot", t, func() {
		p := loadProto(t)
		image := []byte("\x89PNG-bytes\x00\xff")
		result := json.RawMessage(`{"contentTrust":"untrusted-page-content","tabId":5,"url":"https://example.test/","title":"Example","navigated":false,` +
			`"data":"` + base64.StdEncoding.EncodeToString(image) + `","mimeType":"image/png"}`)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: result}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		Convey("以 MCP 图片内容返回图像,另附不含图像数据的简短文本", func() {
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_screenshot", Arguments: map[string]any{
				"full": true, "format": "png", "tabId": 9,
			}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content, ShouldHaveLength, 2)
			img, ok := res.Content[0].(*mcp.ImageContent)
			So(ok, ShouldBeTrue)
			So(img.MIMEType, ShouldEqual, "image/png")
			So(img.Data, ShouldResemble, image)
			text, ok := res.Content[1].(*mcp.TextContent)
			So(ok, ShouldBeTrue)
			So(text.Text, ShouldNotContainSubstring, "data")
			So(text.Text, ShouldContainSubstring, `"tabId":5`)
			So(text.Text, ShouldContainSubstring, `"mimeType":"image/png"`)
			So(len(text.Text), ShouldBeLessThan, 500)
			So(caller.pages[0].Action, ShouldEqual, "screenshot")
			So(*caller.pages[0].TabID, ShouldEqual, 9)
			So(string(caller.pages[0].Input), ShouldEqualJSON, `{"full":true,"format":"png"}`)
		})

		Convey("参数在工具边界上校验:quality 越界、jpeg 以外的格式被拒", func() {
			for _, args := range []map[string]any{
				{"quality": 101},
				{"format": "gif"},
				{"unknown": true},
			} {
				_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_screenshot", Arguments: args})
				So(err, ShouldNotBeNil)
			}
			So(caller.pages, ShouldBeEmpty)
		})

		Convey("描述是静态文本,写明模式、大小上限与 PAGE_HIDDEN", func() {
			tools, err := session.ListTools(context.Background(), nil)
			So(err, ShouldBeNil)
			for _, tool := range tools.Tools {
				if tool.Name != "page_screenshot" {
					continue
				}
				for _, sub := range []string{"full", "PAYLOAD_TOO_LARGE", "jpeg", "PAGE_HIDDEN", "activate"} {
					So(tool.Description, ShouldContainSubstring, sub)
				}
			}
		})

		Convey("错误结果仍是文本错误", func() {
			caller.result = control.CallResult{OK: false, Error: &control.CallError{Code: "PAGE_HIDDEN", Message: "retry with activate"}}
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "page_screenshot", Arguments: map[string]any{}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			So(res.Content, ShouldHaveLength, 1)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "PAGE_HIDDEN")
		})
	})
}
