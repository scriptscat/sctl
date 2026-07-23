package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// newConnectCmd 打开一次接入窗口并打印一次性配对码,供用户输入到扩展的「外部接入」页面完成接入
// (2 分钟内有效)。接入只需一次:此后 CLI 与所有 MCP agent 都经这条可信通道继承信任。
func newConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect",
		Short: "生成一次性配对码,与 ScriptCat 扩展建立外部接入",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt)
			defer stop()
			client, err := control.Dial(ctx)
			if err != nil {
				return err
			}
			code, err := client.Enroll(ctx)
			if err != nil {
				return err
			}
			fmt.Fprintf(os.Stdout, "接入配对码: %s\n请在 ScriptCat 扩展设置的「工具 › 外部接入」中输入此码完成接入(2 分钟内有效)。\n此码只在本终端显示,请勿转发。\n", code)
			return nil
		},
	}
}
