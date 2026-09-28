package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// newConnectCmd 打开一次接入窗口并打印一次性配对码,同一个码可以配对 ScriptCat 或 sctl Browser
// 扩展(2 分钟内有效);两者的配对与握手各自独立,谁先输入谁先配对成功,窗口关闭前都能再配一个。
// 接入只需一次:此后 CLI 与所有 MCP agent 都经这条可信通道继承信任。
func newConnectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "connect",
		Short: "Generate a one-time pairing code to set up external access with the ScriptCat or sctl Browser extension",
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
			fmt.Fprintf(os.Stdout, "pairing code: %s\nEnter it under \"Tools › External Access\" in the ScriptCat extension settings, or in the sctl Browser extension's popup, to finish connecting (valid for 2 minutes).\nThis code is shown only in this terminal — do not forward it.\n", code)
			return nil
		},
	}
}
