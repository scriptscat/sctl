package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newWindowsCmd 构造 `sctl windows`:list 之外还有 open/close/focus/state。
func newWindowsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "windows",
		Short: "Manage windows on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newWindowsListCmd(), newWindowsOpenCmd(), newWindowsCloseCmd(), newWindowsFocusCmd(), newWindowsStateCmd())
	return cmd
}

func newWindowsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List windows",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return dispatchBrowser(cmd, "windows.list", browserTarget, mustInput(map[string]any{}), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printWindowsTable(result)
			})
		},
	}
}

// windowRow 承载 windows.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1)。
type windowRow struct {
	WindowId int         `json:"windowId"`
	Focused  bool        `json:"focused"`
	State    string      `json:"state"`
	TabCount int         `json:"tabCount"`
	Browser  *browserRef `json:"browser,omitempty"`
}

func printWindowsTable(result json.RawMessage) error {
	var payload struct {
		Windows []windowRow `json:"windows"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Windows) == 0 {
		fmt.Fprintln(os.Stdout, "(no windows)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	multi := anyWindowHasBrowser(payload.Windows)
	if multi {
		fmt.Fprintln(tw, "WINDOW ID\tFOCUSED\tSTATE\tTABS\tBROWSER")
	} else {
		fmt.Fprintln(tw, "WINDOW ID\tFOCUSED\tSTATE\tTABS")
	}
	for _, w := range payload.Windows {
		if multi {
			fmt.Fprintf(tw, "%d\t%v\t%s\t%d\t%s\n", w.WindowId, w.Focused, w.State, w.TabCount, w.Browser.Name)
		} else {
			fmt.Fprintf(tw, "%d\t%v\t%s\t%d\n", w.WindowId, w.Focused, w.State, w.TabCount)
		}
	}
	return tw.Flush()
}

func anyWindowHasBrowser(rows []windowRow) bool {
	for _, r := range rows {
		if r.Browser != nil {
			return true
		}
	}
	return false
}
