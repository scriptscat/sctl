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

// maxRecentLimit 是 Chrome 保留的最近关闭项数上限,也是 recent list --limit 的上限。
const maxRecentLimit = 25

// newRecentCmd 构造 `sctl recent`: list/restore，各对应一个 recent.* 浏览器方法。
func newRecentCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "recent",
		Short: "Manage recently closed tabs and windows on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newRecentListCmd(), newRecentRestoreCmd())
	return cmd
}

func newRecentListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List recently closed tabs and windows, newest first; Chrome retains at most 25",
		Args:  cobra.NoArgs,
	}
	var limit int
	cmd.Flags().IntVar(&limit, "limit", maxRecentLimit, fmt.Sprintf("return at most this many items (1-%d)", maxRecentLimit))
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		if limit < 1 || limit > maxRecentLimit {
			return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid --limit %d: must be between 1 and %d", limit, maxRecentLimit)}
		}
		input := map[string]any{}
		if cmd.Flags().Changed("limit") {
			input["limit"] = limit
		}
		return dispatchBrowser(cmd, "recent.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printRecentTable(result)
		})
	}
	return cmd
}

func newRecentRestoreCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "restore [<sessionId>]",
		Short: "Restore a closed tab or window; without sessionId restores the most recently closed",
		Args:  cobra.MaximumNArgs(1),
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{}
		if len(args) == 1 {
			input["sessionId"] = args[0]
		}
		return dispatchBrowser(cmd, "recent.restore", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			var payload struct {
				TabID    int `json:"tabId,omitempty"`
				WindowID int `json:"windowId,omitempty"`
			}
			if err := json.Unmarshal(result, &payload); err != nil {
				return printResultJSON(result)
			}
			if payload.TabID != 0 {
				fmt.Fprintf(os.Stdout, "restored tab %d\n", payload.TabID)
			} else if payload.WindowID != 0 {
				fmt.Fprintf(os.Stdout, "restored window %d\n", payload.WindowID)
			}
			return nil
		})
	}
	return cmd
}

type recentRow struct {
	SessionID  string      `json:"sessionId"`
	Type       string      `json:"type"`
	ClosedTime int64       `json:"closedTime"`
	Title      string      `json:"title"`
	URL        string      `json:"url"`
	TabCount   int         `json:"tabCount,omitempty"`
	Browser    *browserRef `json:"browser,omitempty"`
}

func printRecentTable(result json.RawMessage) error {
	var payload struct {
		Items   []recentRow `json:"items"`
		HasMore bool        `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Items) == 0 {
		fmt.Fprintln(os.Stdout, "(no recently closed items)")
		printHasMoreUpTo(payload.HasMore, maxRecentLimit)
		return nil
	}
	multi := slices.ContainsFunc(payload.Items, func(r recentRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "SESSION\tTYPE\tCLOSED\tTABS\tTITLE\tURL"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range payload.Items {
		tabs := ""
		if r.Type == "window" {
			tabs = strconv.Itoa(r.TabCount)
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s", terminalSafe(r.SessionID), r.Type, formatMillis(r.ClosedTime), tabs, terminalSafe(r.Title), terminalSafe(r.URL))
		if multi {
			row += "\t" + r.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMoreUpTo(payload.HasMore, maxRecentLimit)
	return nil
}
