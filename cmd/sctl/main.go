// sctl — ScriptCat 控制工具:本地桥接 daemon、MCP stdio server 与脚本管理命令。
// 桥接协议见仓库根 PROTOCOL.md。
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/cli"
	"github.com/scriptscat/sctl/internal/logging"
)

func main() {
	var logLevel string
	root := &cobra.Command{
		Use:           "sctl",
		Short:         "ScriptCat 控制工具:本地桥接 daemon、MCP server 与脚本管理命令",
		SilenceUsage:  true,
		SilenceErrors: true,
		// 在任何子命令逻辑前初始化全局 stderr logger(stdout 留给 MCP 协议与 --json 输出)。
		PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
			logging.Setup(logLevel)
			return nil
		},
	}
	root.PersistentFlags().StringVar(&logLevel, "log-level", "info", "日志级别 debug|info|warn|error(始终输出到 stderr)")
	root.AddCommand(
		cli.NewServeCmd(),
		cli.NewMcpCmd(),
		cli.NewPairCmd(),
		cli.NewStatusCmd(),
		cli.NewVersionCmd(),
	)
	if err := root.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}
