// Package mcpserver 用官方 go-sdk 构建 sctl 的 stdio MCP server:把 protocol.json 定义的 bridge
// action 暴露成 MCP 工具,并在阻塞等待(写审批 / 源码披露)期间周期发送 progress 通知(支持的
// 客户端可借此续期工具超时)。
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

// BridgeCaller 抽象「向 daemon 转发一次 bridge action 调用」,便于测试注入桩。
type BridgeCaller interface {
	Call(ctx context.Context, action string, input json.RawMessage) (control.CallResult, error)
}

// Deps 是构建 MCP server 所需依赖。扁平信任下不再按客户端 scope 过滤:注册 protocol.json 中
// 定义了的全部工具。
type Deps struct {
	Name    string
	Version string
	Proto   *protocol.Protocol
	Caller  BridgeCaller
}

// New 按依赖构建 MCP server,注册 protocol.json 里定义的全部 bridge action 工具。
func New(d Deps) *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: d.Name, Version: d.Version}, &mcp.ServerOptions{})
	for _, td := range toolDefs {
		if _, ok := d.Proto.Actions[td.action]; !ok {
			continue
		}
		registerTool(srv, td, d.Caller)
	}
	return srv
}

func registerTool(srv *mcp.Server, td toolDef, caller BridgeCaller) {
	schemaDoc, err := jsonschema.UnmarshalJSON(strings.NewReader(td.inputSchema))
	if err != nil {
		panic(fmt.Sprintf("invalid input schema for %s: %v", td.name, err))
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource(td.name+".json", schemaDoc); err != nil {
		panic(fmt.Sprintf("load input schema for %s: %v", td.name, err))
	}
	schema, err := compiler.Compile(td.name + ".json")
	if err != nil {
		panic(fmt.Sprintf("compile input schema for %s: %v", td.name, err))
	}
	tool := &mcp.Tool{
		Name:        td.name,
		Description: td.description,
		InputSchema: json.RawMessage(td.inputSchema),
	}
	action := td.action
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		input, err := jsonschema.UnmarshalJSON(bytes.NewReader(req.Params.Arguments))
		if err != nil {
			return nil, fmt.Errorf("invalid %s arguments: %w", td.name, err)
		}
		if err := schema.Validate(input); err != nil {
			return nil, fmt.Errorf("invalid %s arguments: %w", td.name, err)
		}
		if action == "scripts.source.get" {
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
		return handleCall(ctx, req, action, caller)
	})
}

// handleCall 转发工具调用到 daemon,并在等待期间发 progress。桥接业务错误(拒绝/过期/scope 等)
// 作为 IsError 工具结果返回(模型可见并自我纠正);传输/取消错误作为协议级错误返回。
func handleCall(ctx context.Context, req *mcp.CallToolRequest, action string, caller BridgeCaller) (*mcp.CallToolResult, error) {
	input := req.Params.Arguments
	stop := startProgress(ctx, req)
	res, err := caller.Call(ctx, action, input)
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
