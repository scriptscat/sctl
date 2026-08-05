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
		Short: "Generate a one-time pairing code to set up external access with the ScriptCat extension",
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
			fmt.Fprintf(os.Stdout, "pairing code: %s\nEnter it under \"Tools › External Access\" in the ScriptCat extension settings to finish connecting (valid for 2 minutes).\nThis code is shown only in this terminal — do not forward it.\n", code)
			return nil
		},
	}
}
