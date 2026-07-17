// sctl — ScriptCat 控制工具:本地桥接 daemon、MCP stdio server 与脚本管理命令。
// 桥接协议见仓库根 PROTOCOL.md。
package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/cli"
)

func main() {
	root := &cobra.Command{
		Use:           "sctl",
		Short:         "ScriptCat 控制工具:本地桥接 daemon、MCP server 与脚本管理命令",
		SilenceUsage:  true,
		SilenceErrors: true,
	}
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
