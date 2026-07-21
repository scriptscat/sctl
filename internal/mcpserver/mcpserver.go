// Package mcpserver 用官方 go-sdk 构建 sctl 的 stdio MCP server:把 6 个 bridge action 暴露成
// MCP 工具、按客户端 scope 过滤 tools/list、并在阻塞等待(写审批 / 源码披露)期间周期发送
// progress 通知(支持的客户端可借此续期工具超时,设计文档 §5.1)。
//
// stdout 由 MCP 协议独占:本包绝不向 stdout 写任何东西,日志走全局 stderr/文件 logger。
package mcpserver

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/scriptscat/sctl/internal/control"
	"github.com/scriptscat/sctl/internal/protocol"
)

// progressInterval 是阻塞等待期间发送 progress 通知的间隔。取远小于常见 MCP 客户端 60s 级工具
// 超时的值,使每次通知都能刷新其倒计时。以 var 暴露仅为便于测试压缩等待。
var progressInterval = 10 * time.Second

// BridgeCaller 抽象「向 daemon 转发一次 bridge action 调用」,便于测试注入桩。
type BridgeCaller interface {
	Call(ctx context.Context, action string, input json.RawMessage) (control.CallResult, error)
}

// Deps 是构建 MCP server 所需依赖。Scopes 是当前客户端已授予的 scope,决定注册哪些工具;
// Paired=false 表示尚未配对(注册零工具,并在 initialize instructions 中提示去配对)。
type Deps struct {
	Name    string
	Version string
	Proto   *protocol.Protocol
	Scopes  []string
	Caller  BridgeCaller
	Paired  bool
}

// New 按依赖构建 MCP server。tools/list 通过「只注册 scope 允许的工具」天然完成 scope 过滤。
func New(d Deps) *mcp.Server {
	opts := &mcp.ServerOptions{}
	if !d.Paired {
		opts.Instructions = "此 sctl MCP server 尚未与 ScriptCat 扩展配对。请在终端运行 `sctl mcp pair`(可加 --name 区分配置)完成配对,然后重启本 server 即可使用脚本工具。"
	}
	srv := mcp.NewServer(&mcp.Implementation{Name: d.Name, Version: d.Version}, opts)
	for _, td := range toolDefs {
		def, ok := d.Proto.Actions[td.action]
		if !ok {
			continue
		}
		if !hasScope(d.Scopes, def.Scope) {
			continue
		}
		registerTool(srv, td, d.Caller)
	}
	return srv
}

func registerTool(srv *mcp.Server, td toolDef, caller BridgeCaller) {
	tool := &mcp.Tool{
		Name:        td.name,
		Description: td.description,
		InputSchema: json.RawMessage(td.inputSchema),
	}
	action := td.action
	srv.AddTool(tool, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		return handleCall(ctx, req, action, caller)
	})
}

// handleCall 转发工具调用到 daemon,并在等待期间发 progress。桥接业务错误(拒绝/过期/scope 等)
// 作为 IsError 工具结果返回(模型可见并自我纠正);传输/取消错误作为协议级错误返回。
func handleCall(ctx context.Context, req *mcp.CallToolRequest, action string, caller BridgeCaller) (*mcp.CallToolResult, error) {
	input := json.RawMessage(req.Params.Arguments)
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
					Message:       "等待浏览器确认…",
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
		out.StructuredContent = json.RawMessage(result)
	}
	return out
}

func errorResult(e *control.CallError) *mcp.CallToolResult {
	msg := "调用失败"
	if e != nil {
		msg = e.Code
		if e.Message != "" {
			msg += ": " + e.Message
		}
	}
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: msg}}}
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}
