package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// fakeCaller 是 BridgeCaller 的测试桩:可配置阻塞、返回值,并记录收到的调用。
type fakeCaller struct {
	mu            sync.Mutex
	actions       []string
	inputs        []json.RawMessage
	browserParams []string      // 记录每次 Call 的目标浏览器参数
	block         chan struct{} // 非 nil 则 Call 阻塞至其关闭或 ctx 取消
	entered       chan struct{} // 非 nil 则每次 Call 进入时发送一次,让测试确知调用已抵达桥接侧
	result        control.CallResult
	err           error
	sawCtx        atomic.Bool           // Call 是否因 ctx 取消而返回
	browsersList  []control.BrowserInfo // Browsers 的返回值
	browsersErr   error
	pages         []control.PageRequest // 记录每次 Page 的请求
}

func (f *fakeCaller) Call(ctx context.Context, action, browser string, input json.RawMessage) (control.CallResult, error) {
	f.mu.Lock()
	f.actions = append(f.actions, action)
	f.browserParams = append(f.browserParams, browser)
	f.inputs = append(f.inputs, append(json.RawMessage(nil), input...))
	block := f.block
	f.mu.Unlock()
	if f.entered != nil {
		f.entered <- struct{}{}
	}
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			f.sawCtx.Store(true)
			return control.CallResult{}, ctx.Err()
		}
	}
	return f.result, f.err
}

func (f *fakeCaller) Page(ctx context.Context, req control.PageRequest) (control.CallResult, error) {
	f.mu.Lock()
	f.pages = append(f.pages, req)
	f.mu.Unlock()
	return f.result, f.err
}

func (f *fakeCaller) Browsers(ctx context.Context) ([]control.BrowserInfo, error) {
	return f.browsersList, f.browsersErr
}

// connect 用内存 transport 把一个 mcpserver 与一个测试 MCP client 对接。
func connect(t *testing.T, deps Deps, clientOpts *mcp.ClientOptions) *mcp.ClientSession {
	t.Helper()
	srv := New(deps)
	st, ct := mcp.NewInMemoryTransports()
	ctx := context.Background()
	_, err := srv.Connect(ctx, st, nil)
	So(err, ShouldBeNil)
	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, clientOpts)
	session, err := client.Connect(ctx, ct, nil)
	So(err, ShouldBeNil)
	t.Cleanup(func() { _ = session.Close() })
	return session
}

func loadProto(t *testing.T) *protocol.Protocol {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	return p
}

func toolNames(res *mcp.ListToolsResult) []string {
	names := make([]string, 0, len(res.Tools))
	for _, tl := range res.Tools {
		names = append(names, tl.Name)
	}
	return names
}

func TestToolsListExposesAllTools(t *testing.T) {
	Convey("扁平信任:tools/list 暴露 protocol.json 定义的全部方法(工具)与特殊的 browsers_list", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}

		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		// 每个非内部 protocol 方法映射一个工具,加上 browsers_list (不是方法);内部方法(CDP 中转)不暴露。
		public := 0
		for _, action := range p.Actions {
			if !action.Internal {
				public++
			}
		}
		So(len(res.Tools), ShouldEqual, public+1+len(pageTools))
		So(toolNames(res), ShouldNotContain, "debugger_send")
		So(toolNames(res), ShouldNotContain, "debugger_detach")
		So(toolNames(res), ShouldContain, "scripts_list")
		So(toolNames(res), ShouldContain, "scripts_delete_request")
		So(toolNames(res), ShouldContain, "browsers_list")
		So(toolNames(res), ShouldContain, "tabs_list")
		So(toolNames(res), ShouldContain, "tabs_open")
		So(toolNames(res), ShouldContain, "tabs_close")
		So(toolNames(res), ShouldContain, "tabs_activate")
		So(toolNames(res), ShouldContain, "windows_list")
		So(toolNames(res), ShouldContain, "page_eval")
		So(toolNames(res), ShouldContain, "page_detach")
	})
}

func TestToolsListOmitsInternalActions(t *testing.T) {
	Convey("protocol.json 标记为 internal 的方法即使有工具定义也不注册为 MCP 工具", t, func() {
		p := loadProto(t)
		internal := *p
		internal.Actions = maps.Clone(p.Actions)
		action := internal.Actions["tabs.list"]
		action.Internal = true
		internal.Actions["tabs.list"] = action
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}

		session := connect(t, Deps{Name: "s", Version: "v0", Proto: &internal, Caller: caller}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		So(toolNames(res), ShouldNotContain, "tabs_list")
		So(toolNames(res), ShouldContain, "tabs_open")
	})
}

func toolByName(res *mcp.ListToolsResult, name string) *mcp.Tool {
	for _, tl := range res.Tools {
		if tl.Name == name {
			return tl
		}
	}
	return nil
}

// schemaPropsOf 取出工具输入 schema 的 properties 表,用于断言字段是否被声明。
func schemaPropsOf(tl *mcp.Tool) map[string]any {
	So(tl, ShouldNotBeNil)
	raw, err := json.Marshal(tl.InputSchema)
	So(err, ShouldBeNil)
	var schema struct {
		Properties map[string]any `json:"properties"`
	}
	So(json.Unmarshal(raw, &schema), ShouldBeNil)
	return schema.Properties
}

func TestSourceAndEditToolSchemas(t *testing.T) {
	Convey("行窗读取、grep 与编辑三个能力在工具 schema 与描述上如实声明", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)

		Convey("scripts_source_get 声明 startLine/endLine,客户端才知道能只取一段", func() {
			props := schemaPropsOf(toolByName(res, "scripts_source_get"))
			So(props, ShouldContainKey, "startLine")
			So(props, ShouldContainKey, "endLine")
		})

		Convey("scripts_source_grep 已注册,且 uuid/query 之外的检索参数齐备", func() {
			props := schemaPropsOf(toolByName(res, "scripts_source_grep"))
			for _, key := range []string{"uuid", "query", "mode", "ignoreCase", "contextLines", "maxMatches"} {
				So(props, ShouldContainKey, key)
			}
		})

		Convey("grep 描述写明默认 text 档是纯字面量、要模式匹配须显式传 regex", func() {
			// 不写明的话,模型会照惯例把 query 当正则用,再困惑于 `.*` 零命中。
			tl := toolByName(res, "scripts_source_grep")
			So(tl, ShouldNotBeNil)
			So(tl.Description, ShouldContainSubstring, "literal")
			So(tl.Description, ShouldContainSubstring, `"regex"`)
		})

		Convey("scripts_edit_request 已注册,input 是 uuid + edits 数组", func() {
			props := schemaPropsOf(toolByName(res, "scripts_edit_request"))
			So(props, ShouldContainKey, "uuid")
			So(props, ShouldContainKey, "edits")
		})

		Convey("edit 描述写明需人工确认,别让模型以为是静默写入", func() {
			tl := toolByName(res, "scripts_edit_request")
			So(tl, ShouldNotBeNil)
			So(tl.Description, ShouldContainSubstring, "approve")
		})
	})
}

func TestToolSchemasRejectInvalidCrossFieldArgumentsBeforeForwarding(t *testing.T) {
	Convey("MCP schema 在 bridge 前拒绝跨字段组合无效的请求", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		for _, tc := range []struct {
			name string
			args map[string]any
		}{
			{name: "scripts_install_request", args: map[string]any{}},
			{name: "scripts_install_request", args: map[string]any{"url": "https://example.test/a.user.js", "code": "// code"}},
			{name: "scripts_source_get", args: map[string]any{"uuid": "script-id", "startLine": 1}},
			{name: "scripts_source_get", args: map[string]any{"uuid": "script-id", "endLine": 2}},
		} {
			_, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: tc.name, Arguments: tc.args})
			if err == nil {
				t.Errorf("%s accepted invalid arguments %#v", tc.name, tc.args)
			}
		}

		caller.mu.Lock()
		defer caller.mu.Unlock()
		So(caller.actions, ShouldBeEmpty)
	})
}

func TestBrowserToolSchemasMatchTheProtocolParams(t *testing.T) {
	Convey("浏览器工具向 MCP 客户端声明的输入 schema 与 protocol.json 的参数类型一致,不在手写副本里丢约束", t, func() {
		p := loadProto(t)
		var definition struct {
			Types map[string]any `json:"types"`
		}
		So(json.Unmarshal(protocol.DefinitionJSON, &definition), ShouldBeNil)

		checked := 0
		var drifted []string
		for _, td := range toolDefs {
			action, ok := p.Actions[td.action]
			if !ok || action.Peer != protocol.PeerBrowser {
				continue
			}
			var declared any
			So(json.Unmarshal([]byte(td.inputSchema), &declared), ShouldBeNil)
			if !reflect.DeepEqual(declared, definition.Types[action.Params]) {
				drifted = append(drifted, td.name+" vs "+action.Params)
			}
			checked++
		}
		So(checked, ShouldBeGreaterThan, 0)
		So(drifted, ShouldBeEmpty)
	})
}

func TestSourceGetAppliesFullSourceBudgetOnlyWithoutLineWindow(t *testing.T) {
	Convey("MCP 全文读取带上下文预算,显式行窗读取不受该预算限制", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{"code":"source"}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "scripts_source_get",
			Arguments: map[string]any{"uuid": "script-id"},
		})
		So(err, ShouldBeNil)

		_, err = session.CallTool(context.Background(), &mcp.CallToolParams{
			Name: "scripts_source_get",
			Arguments: map[string]any{
				"uuid":      "script-id",
				"startLine": 1,
				"endLine":   200,
			},
		})
		So(err, ShouldBeNil)

		caller.mu.Lock()
		defer caller.mu.Unlock()
		So(caller.inputs, ShouldHaveLength, 2)
		So(string(caller.inputs[0]), ShouldContainSubstring, `"maxBytes":65536`)
		So(string(caller.inputs[1]), ShouldNotContainSubstring, "maxBytes")
	})
}

func TestToolCallResults(t *testing.T) {
	Convey("工具调用结果映射", t, func() {
		p := loadProto(t)

		Convey("成功 → 内容含结果 JSON,非 IsError", func() {
			caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{"scripts":[]}`)}}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "scripts_list", Arguments: map[string]any{}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeFalse)
			So(res.Content, ShouldNotBeEmpty)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "scripts")
			So(caller.actions, ShouldResemble, []string{"scripts.list"})
		})

		Convey("桥接业务错误 → IsError 工具结果(模型可见)", func() {
			caller := &fakeCaller{result: control.CallResult{OK: false, Error: &control.CallError{Code: "USER_REJECTED", Message: "用户拒绝"}}}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)
			res, err := session.CallTool(context.Background(), &mcp.CallToolParams{Name: "scripts_delete_request", Arguments: map[string]any{"uuid": "x"}})
			So(err, ShouldBeNil)
			So(res.IsError, ShouldBeTrue)
			So(res.Content[0].(*mcp.TextContent).Text, ShouldContainSubstring, "USER_REJECTED")
		})
	})
}

func TestBlockingProgressAndCancel(t *testing.T) {
	Convey("阻塞等待期间发 progress,且请求取消可传播到调用", t, func() {
		p := loadProto(t)
		old := progressInterval
		progressInterval = 20 * time.Millisecond
		defer func() { progressInterval = old }()

		Convey("带 progressToken 的阻塞调用会收到 progress 通知", func() {
			var progressCount atomic.Int32
			block := make(chan struct{})
			caller := &fakeCaller{block: block, result: control.CallResult{OK: true, Result: json.RawMessage(`{"uuid":"x","enabled":true}`)}}
			opts := &mcp.ClientOptions{
				ProgressNotificationHandler: func(_ context.Context, _ *mcp.ProgressNotificationClientRequest) {
					progressCount.Add(1)
				},
			}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, opts)

			resCh := make(chan *mcp.CallToolResult, 1)
			go func() {
				res, _ := session.CallTool(context.Background(), &mcp.CallToolParams{
					Name:      "scripts_toggle_request",
					Arguments: map[string]any{"uuid": "x", "enable": true},
					Meta:      mcp.Meta{"progressToken": "tok-1"},
				})
				resCh <- res
			}()

			// 等到至少收到一次 progress,再放行调用。
			deadline := time.After(2 * time.Second)
			for progressCount.Load() == 0 {
				select {
				case <-deadline:
					t.Fatal("超时仍未收到 progress 通知")
				case <-time.After(5 * time.Millisecond):
				}
			}
			close(block)
			res := <-resCh
			So(res.IsError, ShouldBeFalse)
			So(progressCount.Load(), ShouldBeGreaterThan, 0)
		})

		Convey("请求方取消 ctx → 调用观察到取消并返回错误", func() {
			block := make(chan struct{})
			defer close(block)
			caller := &fakeCaller{block: block, entered: make(chan struct{}, 1)}
			session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

			callCtx, cancel := context.WithCancel(context.Background())
			errCh := make(chan error, 1)
			go func() {
				_, err := session.CallTool(callCtx, &mcp.CallToolParams{Name: "scripts_list", Arguments: map[string]any{}})
				errCh <- err
			}()
			// 必须等调用确实抵达桥接侧再取消:若取消先于服务端派发到达,go-sdk 直接丢弃这个请求,
			// 桥接调用根本不会发生,本用例要验证的「进行中的调用被作废」也就无从观察。
			select {
			case <-caller.entered:
			case <-time.After(2 * time.Second):
				t.Fatal("调用未抵达桥接调用")
			}
			cancel()

			select {
			case err := <-errCh:
				So(err, ShouldNotBeNil)
			case <-time.After(2 * time.Second):
				t.Fatal("取消未传播到 CallTool")
			}
			// 客户端取消经 notifications/cancelled 异步传到服务端 handler ctx,轮询等待其传播到桥接调用侧。
			deadline := time.After(2 * time.Second)
			for !caller.sawCtx.Load() {
				select {
				case <-deadline:
					t.Fatal("ctx 取消未传播到桥接调用")
				case <-time.After(5 * time.Millisecond):
				}
			}
			So(caller.sawCtx.Load(), ShouldBeTrue)
		})
	})
}

func TestToolDescriptionsAreStatic(t *testing.T) {
	Convey("MCP 工具描述为静态文本，从不拼入页面控制数据如标签页标题与 URL", t, func() {
		p := loadProto(t)
		// 模拟浏览器调用返回包含特征标记字符串的标签页数据。
		markerTitle := "MARKER_TITLE_TESTDATA_12345"
		markerURL := "MARKER_URL_TESTDATA_67890"
		tabsResult := map[string]any{
			"tabs": []map[string]any{
				{
					"id":       1,
					"windowId": 1,
					"active":   true,
					"pinned":   false,
					"title":    markerTitle,
					"url":      markerURL,
				},
			},
		}
		resultJSON, err := json.Marshal(tabsResult)
		So(err, ShouldBeNil)

		caller := &fakeCaller{
			result: control.CallResult{OK: true, Result: resultJSON},
		}

		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		// 第一次列出工具，记录描述。
		res1, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		desc1 := make(map[string]string)
		for _, tool := range res1.Tools {
			desc1[tool.Name] = tool.Description
		}

		// 调用 tabs_list，让模拟器返回包含特征标记的数据。
		_, err = session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "tabs_list",
			Arguments: map[string]any{},
		})
		So(err, ShouldBeNil)

		// 第二次列出工具，再次记录描述。
		res2, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		desc2 := make(map[string]string)
		for _, tool := range res2.Tools {
			desc2[tool.Name] = tool.Description
		}

		So(desc2, ShouldContainKey, "tabs_list")

		Convey("任何工具描述都不含标签页标题的特征标记", func() {
			for _, desc := range desc2 {
				So(desc, ShouldNotContainSubstring, markerTitle)
			}
		})

		Convey("任何工具描述都不含标签页 URL 的特征标记", func() {
			for _, desc := range desc2 {
				So(desc, ShouldNotContainSubstring, markerURL)
			}
		})

		Convey("工具描述前后一致，不随数据调用而改变", func() {
			So(desc1, ShouldResemble, desc2)
		})
	})
}

func TestBrowserToolsForwardTheOptionalBrowserArgument(t *testing.T) {
	Convey("浏览器工具把可选的 browser 参数作为目标转发给 daemon,且不混入方法输入", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{result: control.CallResult{OK: true, Result: json.RawMessage(`{"tabId":7}`)}}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, nil)

		_, err := session.CallTool(context.Background(), &mcp.CallToolParams{
			Name:      "tabs_open",
			Arguments: map[string]any{"url": "https://example.com", "browser": "work"},
		})
		So(err, ShouldBeNil)
		_, err = session.CallTool(context.Background(), &mcp.CallToolParams{Name: "windows_list", Arguments: map[string]any{}})
		So(err, ShouldBeNil)

		So(caller.actions, ShouldResemble, []string{"tabs.open", "windows.list"})
		So(caller.browserParams, ShouldResemble, []string{"work", ""})
		So(string(caller.inputs[0]), ShouldEqual, `{"url":"https://example.com"}`)
	})

	Convey("除 browsers_list 外,每个浏览器工具都声明可选的 browser 参数", t, func() {
		p := loadProto(t)
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: &fakeCaller{}}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		for _, name := range []string{"tabs_list", "tabs_open", "tabs_close", "tabs_activate", "windows_list"} {
			So(schemaPropsOf(toolByName(res, name)), ShouldContainKey, "browser")
		}
		So(schemaPropsOf(toolByName(res, "browsers_list")), ShouldNotContainKey, "browser")
	})
}

func TestBrowserParameterDescribesListAggregation(t *testing.T) {
	Convey("browser 参数的说明与目标选择表一致:列表类工具汇总所有在线浏览器,操作类工具要求指定", t, func() {
		p := loadProto(t)
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: &fakeCaller{}}, nil)
		res, err := session.ListTools(context.Background(), &mcp.ListToolsParams{})
		So(err, ShouldBeNil)
		browserDescription := func(tool string) string {
			prop, ok := schemaPropsOf(toolByName(res, tool))["browser"].(map[string]any)
			So(ok, ShouldBeTrue)
			desc, _ := prop["description"].(string)
			return desc
		}
		for _, name := range []string{"tabs_list", "windows_list"} {
			So(browserDescription(name), ShouldContainSubstring, "every online browser")
			So(browserDescription(name), ShouldNotContainSubstring, "returns error")
		}
		for _, name := range []string{"tabs_open", "tabs_close", "tabs_activate"} {
			So(browserDescription(name), ShouldContainSubstring, "returns error")
		}
	})
}

func TestBrowserToolsNeverReportWaitingForApproval(t *testing.T) {
	Convey("浏览器工具没有人工审批,等待期间不发「等待浏览器审批」的 progress", t, func() {
		p := loadProto(t)
		old := progressInterval
		progressInterval = 10 * time.Millisecond
		defer func() { progressInterval = old }()

		var browserProgress atomic.Int32
		scriptProgress := make(chan struct{}, 64)
		block := make(chan struct{})
		caller := &fakeCaller{block: block, entered: make(chan struct{}, 2), result: control.CallResult{OK: true, Result: json.RawMessage(`{}`)}}
		opts := &mcp.ClientOptions{
			ProgressNotificationHandler: func(_ context.Context, req *mcp.ProgressNotificationClientRequest) {
				if req.Params.ProgressToken == "tok-browser" {
					browserProgress.Add(1)
					return
				}
				select {
				case scriptProgress <- struct{}{}:
				default:
				}
			},
		}
		session := connect(t, Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}, opts)

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
		// 浏览器调用先抵达桥接侧并阻塞;随后一个有审批的调用作为时钟:它的 progress 滴答了三次,
		// 说明浏览器调用阻塞期间已过去至少两个间隔,若浏览器调用也有 ticker 必已触发。不用固定睡眠,
		// 负载再高也不会在调用抵达前就放行而让本用例空过。
		browserRes := call("tabs_list", map[string]any{}, "tok-browser")
		scriptRes := call("scripts_toggle_request", map[string]any{"uuid": "x", "enable": true}, "tok-script")
		for range 3 {
			select {
			case <-scriptProgress:
			case <-time.After(5 * time.Second):
				t.Fatal("the approval call reported no progress")
			}
		}
		close(block)
		So((<-scriptRes).IsError, ShouldBeFalse)
		So((<-browserRes).IsError, ShouldBeFalse)
		So(browserProgress.Load(), ShouldEqual, 0)
	})
}

func TestToolsWithoutRequiredArgumentsAcceptAnOmittedArgumentsObject(t *testing.T) {
	Convey("MCP 允许省略 arguments:没有必填参数的工具照常执行,按空参数对象转发", t, func() {
		p := loadProto(t)
		caller := &fakeCaller{
			result:       control.CallResult{OK: true, Result: json.RawMessage(`{"windows":[]}`)},
			browsersList: []control.BrowserInfo{},
		}
		// Go SDK 的 client 会把缺省的 arguments 补成 {},其他客户端不会;这里直接写原始 JSON-RPC 帧。
		ctx := context.Background()
		st, ct := mcp.NewInMemoryTransports()
		_, err := New(Deps{Name: "s", Version: "v0", Proto: p, Caller: caller}).Connect(ctx, st, nil)
		So(err, ShouldBeNil)
		conn, err := ct.Connect(ctx)
		So(err, ShouldBeNil)
		t.Cleanup(func() { _ = conn.Close() })
		send := func(frame string) {
			msg, err := jsonrpc.DecodeMessage([]byte(frame))
			So(err, ShouldBeNil)
			So(conn.Write(ctx, msg), ShouldBeNil)
		}
		roundTrip := func(frame string) *jsonrpc.Response {
			send(frame)
			reply, err := conn.Read(ctx)
			So(err, ShouldBeNil)
			resp, ok := reply.(*jsonrpc.Response)
			So(ok, ShouldBeTrue)
			return resp
		}
		roundTrip(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{},"clientInfo":{"name":"raw","version":"v0"}}}`)
		send(`{"jsonrpc":"2.0","method":"notifications/initialized","params":{}}`)

		for i, name := range []string{"windows_list", "tabs_list", "scripts_list", "browsers_list"} {
			resp := roundTrip(fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":%q}}`, i+2, name))
			So(resp.Error, ShouldBeNil)
			var result mcp.CallToolResult
			So(json.Unmarshal(resp.Result, &result), ShouldBeNil)
			So(result.IsError, ShouldBeFalse)
		}
		So(caller.actions, ShouldResemble, []string{"windows.list", "tabs.list", "scripts.list"})
		So(caller.browserParams, ShouldResemble, []string{"", "", ""})
		for _, input := range caller.inputs {
			So(string(input), ShouldEqual, `{}`)
		}
	})
}
