// Package cli 定义 sctl 的 cobra 子命令。serve 引导一个 cago 应用并挂载桥接
// Component;其余命令为连接既有 daemon 的轻量客户端或纯本地操作。
//
// 日志约定:全局 stderr logger 已由 cmd/sctl 的 PersistentPreRunE 初始化,子命令
// 直接用 logger.Ctx(cmd.Context()) 记录诊断信息;stdout 仅用于用户可读结果 / --json。
package cli

import (
	"fmt"
	"log"

	"github.com/cago-frame/cago"
	"github.com/cago-frame/cago/configs"
	"github.com/cago-frame/cago/pkg/logger"
	"github.com/spf13/cobra"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/bridge"
	"github.com/scriptscat/sctl/internal/protocol"
)

// Version 由 goreleaser 通过 -ldflags 注入。
var Version = "0.0.0-dev"

// NewServeCmd 引导 cago 应用并挂载桥接 Component。
//
// 不使用 component.Core():cago 的 Core 把日志写 stdout,而桥接 daemon 未来可能由
// `sctl mcp` 自动拉起、其 stdout 需保持洁净;日志已由全局 stderr logger 承载。
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
			logger.Ctx(ctx).Info("启动 sctl serve", zap.String("version", Version))
			return cago.New(ctx, cfg).
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
			// 提醒:此命令的 stdout 是 MCP 协议通道,严禁写入日志/普通输出。
			logger.Ctx(cmd.Context()).Info("启动 sctl mcp(stdio)")
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
			logger.Ctx(cmd.Context()).Info("发起扩展配对")
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
			logger.Ctx(cmd.Context()).Debug("查询 daemon 状态")
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
				logger.Ctx(cmd.Context()).Error("加载内嵌协议失败", zap.Error(err))
				return err
			}
			// 结果写 stdout(用户可读),诊断走 stderr logger。
			fmt.Printf("sctl %s (protocol v%d, min daemon %s)\n", Version, p.ProtocolVersion, p.Versions.MinDaemonVersion)
			return nil
		},
	}
}
