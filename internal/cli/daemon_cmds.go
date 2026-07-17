package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/control"
	"github.com/scriptscat/sctl/internal/protocol"
)

// newPairCmd 打开一次扩展配对窗口并打印一次性配对码,供用户填入扩展设置(2 分钟内有效)。
func newPairCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "pair",
		Short: "生成一次性配对码,与 ScriptCat 扩展建立互信",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			client, err := control.Dial(ctx)
			if err != nil {
				return err
			}
			code, err := client.PairExt(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "扩展配对码: %s\n请在 ScriptCat 扩展设置的「本地桥接」中填入此码完成配对(2 分钟内有效)。\n", code)
			return nil
		},
	}
}

// newStatusCmd 查询 daemon 与扩展连接状态。不自动拉起 daemon:未运行时如实报告。
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看 daemon 与扩展连接状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := control.Connect(ctx)
			if err != nil {
				if jsonOutput {
					return printValueJSON(control.StatusResult{})
				}
				fmt.Fprintln(os.Stdout, "daemon 未运行")
				return nil
			}
			st, err := client.Status(ctx)
			if err != nil {
				return err
			}
			if jsonOutput {
				return printValueJSON(st)
			}
			fmt.Fprintf(os.Stdout, "daemon 版本: %s\n扩展已连接: %v\n已配对客户端数: %d\n", st.DaemonVersion, st.ExtConnected, st.ClientCount)
			return nil
		},
	}
}

// newVersionCmd 打印版本与协议信息。
func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "打印版本与协议信息",
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := protocol.Load()
			if err != nil {
				return err
			}
			if jsonOutput {
				return printValueJSON(map[string]any{
					"version":          Version,
					"protocolVersion":  p.ProtocolVersion,
					"minDaemonVersion": p.Versions.MinDaemonVersion,
				})
			}
			fmt.Fprintf(os.Stdout, "sctl %s (protocol v%d, min daemon %s)\n", Version, p.ProtocolVersion, p.Versions.MinDaemonVersion)
			return nil
		},
	}
}
