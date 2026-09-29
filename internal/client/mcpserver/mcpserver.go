// Package mcpserver 用官方 go-sdk 构建 sctl 的 stdio MCP server:把 protocol.json 定义的 bridge
// action 暴露成 MCP 工具(第 1 期的方法一方法一工具,之后的浏览器领域按领域合并成一个工具),并在
// 调用等待期间周期发送 progress 通知(支持的客户端可借此续期工具超时;哪些调用发见 sendsProgress)。
//
// 扁平信任:接入(enrollment)建立可信通道后,MCP agent 继承信任、无需各自配对,故 tools/list
// 暴露全部工具;权威授权仍在扩展侧(写操作审批 / 源码披露闸门)。
//
// stdout 由 MCP 协议独占:本包绝不向 stdout 写任何东西,日志走全局 stderr/文件 logger。
package mcpserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	jsonschema "github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

const maxFullSourceResponseBytes = 64 * 1024

// progressInterval 是阻塞等待期间发送 progress 通知的间隔。取远小于常见 MCP 客户端 60s 级工具
// 超时的值,使每次通知都能刷新其倒计时。以 var 暴露仅为便于测试压缩等待。
var progressInterval = 10 * time.Second

// BridgeCaller 抽象「向 daemon 转发 bridge action 调用与查询浏览器列表」,便于测试注入桩。
// browser 是浏览器方法的可选目标,其余方法传空串。
type BridgeCaller interface {
	Call(ctx context.Context, action, browser string, input json.RawMessage) (control.CallResult, error)
	Browsers(ctx context.Context) ([]control.BrowserInfo, error)
}

// Deps 是构建 MCP server 所需依赖。扁平信任下不再按客户端 scope 过滤:注册 protocol.json 中
// 定义了的全部工具。
type Deps struct {
	Name    string
	Version string
	Proto   *protocol.Protocol
	Caller  BridgeCaller
}

// New 按依赖构建 MCP server,注册 protocol.json 里定义的逐方法工具、按领域合并的工具以及 browsers_list。
// 方法是否是浏览器方法、是否汇总多实例、是否等待人工决定,都取自 protocol.json 的 peer、mergeField 与
// blocking,不按方法名推断。
func New(d Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: d.Name, Version: d.Version}, &mcp.ServerOptions{})
	for _, td := range toolDefs {
		action, ok := d.Proto.Actions[td.action]
		if !ok {
			continue
		}
		browser := action.Peer == protocol.PeerBrowser
		if browser {
			td.inputSchema = schemaWithOptionalBrowser(td.inputSchema, browserParamDescription(action))
		}
		registerTool(srv, td, action, d.Caller)
	}
	for _, dt := range domainTools {
		registerDomainTool(srv, dt, d.Proto, d.Caller)
	}
	// browsers_list 特殊处理:不是 bridge action,由控制 API 直接提供已配对实例列表。
	registerBrowsersListTool(srv, d.Caller)
	return srv
}

// compileInputSchema 编译工具的输入 schema;schema 是本包的静态常量,编译失败是编程错误。
func compileInputSchema(name, inputSchema string) *jsonschema.Schema {
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(inputSchema))
	if err != nil {
		panic(fmt.Sprintf("invalid input schema for %s: %v", name, err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(name+".json", schemaDoc); err != nil {
		panic(fmt.Sprintf("load input schema for %s: %v", name, err))
	}
	schema, err := compiler.Compile(name + ".json")
	if err != nil {
		panic(fmt.Sprintf("compile input schema for %s: %v", name, err))
	}
	return schema
}

// defaultArguments 把省略的 arguments(MCP 允许)当作空对象,之后的校验与转发都只面对一个 JSON 对象。
func defaultArguments(req *mcp.CallToolRequest) {
	if len(req.Params.Arguments) == 0 {
		req.Params.Arguments = json.RawMessage(`{}`)
	}
}

// validateArguments 在转发之前按工具 schema 校验 MCP 客户端给的参数。
func validateArguments(name string, schema *jsonschema.Schema, arguments json.RawMessage) error {
	input, err := jsonschema.UnmarshalJSON(bytes.NewReader(arguments))
	if err != nil {
		return fmt.Errorf("invalid %s arguments: %w", name, err)
	}
	if err := schema.Validate(input); err != nil {
		return fmt.Errorf("invalid %s arguments: %w", name, err)
	}
	return nil
}

// splitBrowserParam 从已校验的工具参数中取出可选的 browser 目标并删掉它:它交给 daemon 选目标,
// 不属于方法输入。其余字段保持原始 JSON。
func splitBrowserParam(arguments json.RawMessage) (string, json.RawMessage, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(arguments, &fields); err != nil {
		return "", nil, err
	}
	browser, err := takeString(fields, "browser")
	if err != nil {
		return "", nil, err
	}
	rest, err := json.Marshal(fields)
	return browser, rest, err
}

// takeString 取出并删掉 fields 里可选的字符串参数 key;缺省时返回空串。
func takeString(fields map[string]json.RawMessage, key string) (string, error) {
	raw, ok := fields[key]
	if !ok {
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", err
	}
	delete(fields, key)
	return value, nil
}

func registerTool(srv *mcp.Server, td toolDef, action protocol.Action, caller BridgeCaller) {
	schema := compileInputSchema(td.name, td.inputSchema)
	tool := &mcp.Tool{
		Name:        td.name,
		Description: td.description,
		InputSchema: json.RawMessage(td.inputSchema),
	}
	browserMethod := action.Peer == protocol.PeerBrowser
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		defaultArguments(req)
		if err := validateArguments(td.name, schema, req.Params.Arguments); err != nil {
			return nil, err
		}
		var browser string
		var err error
		if browserMethod {
			browser, req.Params.Arguments, err = splitBrowserParam(req.Params.Arguments)
			if err != nil {
				return nil, fmt.Errorf("split %s arguments: %w", td.name, err)
			}
		}
		if td.action == "scripts.source.get" {
			var args map[string]any
			if err := json.Unmarshal(req.Params.Arguments, &args); err != nil {
				return nil, fmt.Errorf("decode %s arguments: %w", td.name, err)
			}
			if _, windowed := args["startLine"]; !windowed {
				args["maxBytes"] = maxFullSourceResponseBytes
				req.Params.Arguments, err = json.Marshal(args)
				if err != nil {
					return nil, fmt.Errorf("encode %s arguments: %w", td.name, err)
				}
			}
		}
		return handleCall(ctx, req, td.action, req.Params.Arguments, sendsProgress(action), browser, caller)
	})
}

// registerBrowsersListTool 注册 browsers_list 工具,它不是 bridge action,由控制 API 提供。
func registerBrowsersListTool(srv *mcp.Server, caller BridgeCaller) {
	const (
		toolName        = "browsers_list"
		toolDescription = "List all paired browser instances: name, instance ID, online/offline status, brand and version, extension version, and connection time."
	)
	schema := compileInputSchema(toolName, schemaEmpty)
	tool := &mcp.Tool{
		Name:        toolName,
		Description: toolDescription,
		InputSchema: json.RawMessage(schemaEmpty),
	}
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		defaultArguments(req)
		if err := validateArguments(toolName, schema, req.Params.Arguments); err != nil {
			return nil, err
		}
		browsers, err := caller.Browsers(ctx)
		if err != nil {
			return nil, err
		}
		resultJSON, err := json.Marshal(control.BrowsersResult{Browsers: browsers})
		if err != nil {
			return nil, fmt.Errorf("encode %s result: %w", toolName, err)
		}
		return okResult(resultJSON), nil
	})
}

// sendsProgress 报告调用等待期间是否发 progress。浏览器方法只在等待人工决定(blocking 不是 none)时发,否则客户端
// 会看到从不存在的审批;ScriptCat 方法保持第 1 期的行为,等待期间都发(第 1 期行为不变是第 2 期的约束)。
func sendsProgress(action protocol.Action) bool {
	return action.Peer != protocol.PeerBrowser || action.Blocking != protocol.BlockingNone
}

// handleCall 把方法 action 的输入 input 转发到 daemon。桥接业务错误(拒绝/过期/scope 等)作为 IsError
// 工具结果返回(模型可见并自我纠正);传输/取消错误作为协议级错误返回。
func handleCall(ctx context.Context, req *mcp.CallToolRequest, action string, input json.RawMessage, progress bool, browser string, caller BridgeCaller) (*mcp.CallToolResult, error) {
	stop := func() {}
	if progress {
		stop = startProgress(ctx, req)
	}
	res, err := caller.Call(ctx, action, browser, input)
	stop()
	if err != nil {
		return nil, err
	}
	if res.OK {
		return okResult(res.Result), nil
	}
	return errorResult(res.Error), nil
}

// startProgress 若请求带 progressToken,则起一个 ticker 周期发 progress 通知,返回停止函数。
// 无 token 时为 no-op。通知失败(客户端不接收)被忽略。
func startProgress(ctx context.Context, req *mcp.CallToolRequest) func() {
	token := req.Params.GetProgressToken()
	if token == nil {
		return func() {}
	}
	done := make(chan struct{})
	go func() {
		ticker := time.NewTicker(progressInterval)
		defer ticker.Stop()
		var n float64
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				n++
				_ = req.Session.NotifyProgress(ctx, &mcp.ProgressNotificationParams{
					ProgressToken: token,
					Message:       "Waiting for approval in the browser…",
					Progress:      n,
				})
			}
		}
	}()
	return func() { close(done) }
}

func okResult(result json.RawMessage) *mcp.CallToolResult {
	text := strings.TrimSpace(string(result))
	if text == "" {
		text = "{}"
	}
	out := &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: text}}}
	if strings.HasPrefix(text, "{") {
		out.StructuredContent = result
	}
	return out
}

func errorResult(e *control.CallError) *mcp.CallToolResult {
	msg := "call failed"
	if e != nil {
		msg = e.Code
		if e.Message != "" {
			msg += ": " + e.Message
		}
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}
