package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// newScriptCatsCmd 构造 `sctl scriptcats`:裸调用等价于 `scriptcats list`,另挂 forget 子命令。
func newScriptCatsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scriptcats [list]",
		Short: "List paired ScriptCat instances (default), or forget one with the forget subcommand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScriptCatsList(cmd)
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "list",
		Short: "List paired ScriptCat instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runScriptCatsList(cmd)
		},
	}, newScriptCatsForgetCmd())
	return cmd
}

func runScriptCatsList(cmd *cobra.Command) error {
	ctx := cmd.Context()
	client, err := control.Connect(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	list, err := client.ScriptCats(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if outputFormat == outputJSON {
		return printValueJSON(list)
	}
	return printScriptCatsTable(list)
}

// newScriptCatsForgetCmd 构造 `sctl scriptcats forget <名称|ID>`:仅命令行,无 MCP 工具。删除该实例的密钥与登记,
// 在线时断开其连接;`scriptcat` 是不带实例 ID 的老版,忘记它会删除 pairing.key。
func newScriptCatsForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <name|id>",
		Short: "Forget a paired ScriptCat instance: delete its key and registry entry, disconnecting it if online",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := control.Dial(ctx)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			ref := args[0]
			if err := client.ForgetScriptCat(ctx, ref); err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			if outputFormat == outputJSON {
				return printValueJSON(map[string]string{"forgot": ref})
			}
			fmt.Fprintf(os.Stdout, "forgot ScriptCat instance: %s\n", ref)
			return nil
		},
	}
}

func printScriptCatsTable(list []control.ScriptCatInfo) error {
	if len(list) == 0 {
		fmt.Fprintln(os.Stdout, "(no paired ScriptCat instances)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tPRODUCT\tEXTENSION\tCONNECTED SINCE")
	for _, s := range list {
		status := "offline"
		connected := "-"
		if s.Online {
			status = "online"
			if !s.ConnectedAt.IsZero() {
				connected = s.ConnectedAt.Local().Format(time.RFC3339)
			}
		}
		id := s.ID
		if id == "" {
			id = "-"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, id, status, productLabel(control.BrowserInfo(s)), s.ExtensionVersion, connected)
	}
	return tw.Flush()
}
