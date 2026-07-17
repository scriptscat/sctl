// Package cli 定义 sctl 的 cobra 子命令。serve 引导一个 cago 应用并挂载桥接
// Component;其余命令为连接既有 daemon 的轻量客户端或纯本地操作。
package cli

import (
	"fmt"
	"log"

	"github.com/cago-frame/cago"
	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/component"
	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/bridge"
	"github.com/scriptscat/sctl/internal/protocol"
)

// Version 由 goreleaser 通过 -ldflags 注入。
var Version = "0.0.0-dev"

// NewServeCmd 引导 cago 应用:Core(日志/trace)+ 桥接 Component。
func NewServeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "serve",
		Short: "运行桥接 daemon(WS server,仅监听 127.0.0.1)",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			cfg, err := configs.NewConfig("sctl")
			if err != nil {
				log.Fatalf("加载配置失败: %v", err)
			}
			return cago.New(ctx, cfg).
				Registry(component.Core()).
				RegistryCancel(bridge.Component()).
				Start()
		},
	}
}

// NewMcpCmd 以 stdio 运行 MCP server;daemon 未运行时自动拉起。
func NewMcpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "mcp",
		Short: "以 stdio 运行 MCP server(daemon 未运行时自动拉起)",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("mcp: 尚未实现")
		},
	}
}

// NewPairCmd 生成一次性配对码并等待扩展完成互信。
func NewPairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "生成一次性配对码,与 ScriptCat 扩展建立互信",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("pair: 尚未实现")
		},
	}
}

// NewStatusCmd 查询 daemon 与扩展连接状态。
func NewStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看 daemon 与扩展连接状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			return fmt.Errorf("status: 尚未实现")
		},
	}
}

// NewVersionCmd 打印版本与协议信息。
func NewVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本与协议信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := protocol.Load()
			if err != nil {
				return err
			}
			fmt.Printf("sctl %s (protocol v%d, min daemon %s)\n", Version, p.ProtocolVersion, p.Versions.MinDaemonVersion)
			return nil
		},
	}
}
