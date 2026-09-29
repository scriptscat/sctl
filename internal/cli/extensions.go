package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newExtensionsCmd 构造 `sctl extensions`:list/enable/disable/uninstall,各对应一个 extensions.* 浏览器方法。
func newExtensionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "extensions",
		Short: "Manage extensions and apps installed in a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newExtensionsListCmd(), newExtensionsEnableCmd(), newExtensionsDisableCmd(), newExtensionsUninstallCmd())
	return cmd
}

func newExtensionsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List installed extensions and apps",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dispatchBrowser(cmd, "extensions.list", browserTarget, mustInput(map[string]any{}), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printExtensionsTable(result)
			})
		},
	}
}

func newExtensionsEnableCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "enable <id>",
		Short: "Enable an extension or app",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchBrowser(cmd, "extensions.enable", browserTarget, mustInput(map[string]any{"id": args[0]}), func(result json.RawMessage) error {
				return printEnabledState(result, "enabled extension %s\n")
			})
		},
	}
}

func newExtensionsDisableCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "disable <id> --yes",
		Short: "Disable an extension or app (requires --yes)",
		Long: "Disable an extension or app (requires --yes).\n\n" +
			"sctl Browser cannot disable itself, and extensions installed by enterprise policy cannot be disabled. " +
			"Disabling ScriptCat is allowed, but it disconnects ScriptCat from the daemon until it is enabled again.",
		Args: cobra.ExactArgs(1),
	}
	yes := addYesFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"id": args[0]}
		yes.apply(input)
		return dispatchBrowser(cmd, "extensions.disable", browserTarget, mustInput(input), func(result json.RawMessage) error {
			return printEnabledState(result, "disabled extension %s\n")
		})
	}
	return cmd
}

// newExtensionsUninstallCmd 构造 `sctl extensions uninstall`:L2,扩展打开审批窗口;用户点「卸载」后 Chrome 还会弹出
// 自己的确认框,命令阻塞到两步都有结论。
func newExtensionsUninstallCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "uninstall <id>",
		Short: "Uninstall an extension or app after approval in the browser and in Chrome's own confirmation dialog",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchBrowserApproval(cmd, "extensions.uninstall", browserTarget, mustInput(map[string]any{"id": args[0]}), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				var payload struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}
				if err := json.Unmarshal(result, &payload); err != nil {
					return printResultJSON(result)
				}
				// 名称由扩展作者决定,打印前经 terminalSafe。
				fmt.Fprintf(os.Stdout, "uninstalled extension %s (%s)\n", payload.ID, terminalSafe(payload.Name))
				return nil
			})
		},
	}
}

func printEnabledState(result json.RawMessage, format string) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	fmt.Fprintf(os.Stdout, format, payload.ID)
	return nil
}

// extensionRow 承载 extensions.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// 名称由扩展作者决定,打印前经 terminalSafe。
type extensionRow struct {
	ID          string      `json:"id"`
	Name        string      `json:"name"`
	Version     string      `json:"version"`
	Enabled     bool        `json:"enabled"`
	Type        string      `json:"type"`
	InstallType string      `json:"installType"`
	MayDisable  bool        `json:"mayDisable"`
	Browser     *browserRef `json:"browser,omitempty"`
}

func printExtensionsTable(result json.RawMessage) error {
	var payload struct {
		Items []extensionRow `json:"items"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Items) == 0 {
		fmt.Fprintln(os.Stdout, "(no extensions)")
		return nil
	}
	multi := slices.ContainsFunc(payload.Items, func(r extensionRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "ID\tNAME\tVERSION\tENABLED\tTYPE\tINSTALL\tMAY DISABLE"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range payload.Items {
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s\t%s",
			terminalSafe(r.ID), terminalSafe(r.Name), terminalSafe(r.Version), strconv.FormatBool(r.Enabled),
			terminalSafe(r.Type), terminalSafe(r.InstallType), strconv.FormatBool(r.MayDisable))
		if multi {
			browser := ""
			if r.Browser != nil {
				browser = terminalSafe(r.Browser.Name)
			}
			row += "\t" + browser
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}
