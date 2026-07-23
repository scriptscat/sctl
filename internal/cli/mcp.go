package cli

import (
	"os"
	"os/signal"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/client/mcpserver"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// newMcpCmd 构造 `sctl mcp`(stdio MCP server)。扁平信任下 MCP agent 经已接入的可信通道继承信任、
// 无需各自配对;--name 仅作审计标签,区分同机多份 MCP 配置在扩展审计日志中的归因。
func newMcpCmd() *cobra.Command {
	var name string
	cmd := &cobra.Command{
		Use:   "mcp",
		Short: "以 stdio 运行 MCP server(daemon 未运行时自动拉起)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMcpServe(cmd, name)
		},
	}
	cmd.Flags().StringVar(&name, "name", "default", "MCP 实例名(审计标签,区分多份配置)")
	return cmd
}

// runMcpServe 连上 daemon 后以 stdio 提供 MCP 服务,暴露全部脚本工具(授权由扩展侧审批闸门把关)。
// stdout 由 MCP 协议独占——本函数除经 transport 外绝不写 stdout。
func runMcpServe(cmd *cobra.Command, name string) error {
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
	defer stop()

	client, err := control.Dial(ctx)
	if err != nil {
		return err
	}
	p, err := protocol.Load()
	if err != nil {
		return err
	}

	srv := mcpserver.New(mcpserver.Deps{
		Name:    "scriptcat-" + name,
		Version: Version,
		Proto:   p,
		Caller:  client.WithClientLabel("scriptcat-" + name),
	})
	return srv.Run(ctx, &mcp.StdioTransport{})
}
