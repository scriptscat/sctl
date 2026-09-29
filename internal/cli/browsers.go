package cli

import (
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
)

// newBrowsersCmd 构造 `sctl browsers`:裸调用等价于 `browsers list`,另挂 forget 子命令。
func newBrowsersCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "browsers [list]",
		Short: "List paired sctl Browser instances (default), or forget one with the forget subcommand",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowsersList(cmd)
		},
	}
	cmd.AddCommand(newBrowsersListCmd(), newBrowsersForgetCmd())
	return cmd
}

func newBrowsersListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List paired sctl Browser instances",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runBrowsersList(cmd)
		},
	}
}

func runBrowsersList(cmd *cobra.Command) error {
	ctx := cmd.Context()
	client, err := control.Connect(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	list, err := client.Browsers(ctx)
	if err != nil {
		return &ExitError{Code: exitError, Message: err.Error()}
	}
	if outputFormat == outputJSON {
		return printValueJSON(list)
	}
	return printBrowsersTable(list)
}

// newBrowsersForgetCmd 构造 `sctl browsers forget <名称|ID>`:仅命令行,无 MCP 工具(见 spec 第 1 期
// 命令表)。按精确名称或精确实例 ID 删除该实例的密钥与登记表项,在线时断开其连接。
func newBrowsersForgetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "forget <name|id>",
		Short: "Forget a paired browser instance: delete its key and registry entry, disconnecting it if online",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := control.Dial(ctx)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			ref := args[0]
			if err := client.ForgetBrowser(ctx, ref); err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			if outputFormat == outputJSON {
				return printValueJSON(map[string]string{"forgot": ref})
			}
			fmt.Fprintf(os.Stdout, "forgot browser instance: %s\n", ref)
			return nil
		},
	}
}

func printBrowsersTable(list []control.BrowserInfo) error {
	if len(list) == 0 {
		fmt.Fprintln(os.Stdout, "(no paired browser instances)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tSTATUS\tPRODUCT\tEXTENSION\tCONNECTED SINCE")
	for _, b := range list {
		status := "offline"
		connected := "-"
		if b.Online {
			status = "online"
			connected = b.ConnectedAt.Local().Format(time.RFC3339)
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", b.Name, b.ID, status, productLabel(b), b.ExtensionVersion, connected)
	}
	return tw.Flush()
}

// productLabel 拼出「品牌 版本」,任一为空时原样返回另一个(避免多余空格)。
func productLabel(b control.BrowserInfo) string {
	switch {
	case b.Product == "":
		return b.ProductVersion
	case b.ProductVersion == "":
		return b.Product
	default:
		return fmt.Sprintf("%s %s", b.Product, b.ProductVersion)
	}
}
