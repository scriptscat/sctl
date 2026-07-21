package cli

import (
	"fmt"
	"os"
	"os/signal"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
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
