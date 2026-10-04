package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// timeRangeFlags 是 history search/clear 共用的 --since/--until。两者都不给即不限时间范围。
type timeRangeFlags struct {
	since, until string
}

func addTimeRangeFlags(cmd *cobra.Command) *timeRangeFlags {
	f := &timeRangeFlags{}
	cmd.Flags().StringVar(&f.since, "since", "", "only entries at or after this time: RFC 3339 or a duration ago such as 7d, 12h, 30m")
	cmd.Flags().StringVar(&f.until, "until", "", "only entries at or before this time: RFC 3339 or a duration ago such as 7d, 12h, 30m")
	return f
}

// apply 解析并把时间范围写进方法输入(startTime/endTime,毫秒)。在发起调用前校验,范围颠倒同样是参数错误。
func (f *timeRangeFlags) apply(cmd *cobra.Command, input map[string]any) error {
	now := time.Now()
	var start, end int64
	if cmd.Flags().Changed("since") {
		ms, err := parseTimeFlag("--since", f.since, now)
		if err != nil {
			return err
		}
		start = ms
		input["startTime"] = ms
	}
	if cmd.Flags().Changed("until") {
		ms, err := parseTimeFlag("--until", f.until, now)
		if err != nil {
			return err
		}
		end = ms
		input["endTime"] = ms
	}
	if cmd.Flags().Changed("since") && cmd.Flags().Changed("until") && start > end {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("--since %q is later than --until %q", f.since, f.until)}
	}
	return nil
}

// newHistoryCmd 构造 `sctl history`:search/visits/rm/clear,各对应一个 history.* 浏览器方法。
func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history",
		Short: "Search and delete browsing history on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newHistorySearchCmd(), newHistoryVisitsCmd(), newHistoryRemoveCmd(), newHistoryClearCmd())
	return cmd
}

func newHistorySearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search [text]",
		Short: "Search history, newest first; without a time range all history is searched",
		Args:  cobra.MaximumNArgs(1),
	}
	limit := addLimitFlag(cmd)
	rng := addTimeRangeFlags(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{}
		if err := limit.apply(input); err != nil {
			return err
		}
		if err := rng.apply(cmd, input); err != nil {
			return err
		}
		if len(args) == 1 {
			input["text"] = args[0]
		}
		return dispatchBrowser(cmd, "history.search", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printHistoryTable(result)
		})
	}
	return cmd
}

func newHistoryVisitsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "visits <url>",
		Short: "List every visit of a URL, newest first",
		Args:  cobra.ExactArgs(1),
	}
	limit := addLimitFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"url": args[0]}
		if err := limit.apply(input); err != nil {
			return err
		}
		return dispatchBrowser(cmd, "history.visits", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printVisitsTable(result)
		})
	}
	return cmd
}

func newHistoryRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <url>...",
		Short: "Delete all visits of these URLs from history (requires --yes)",
		Args:  cobra.MinimumNArgs(1),
	}
	yes := addYesFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"urls": args}
		yes.apply(input)
		return dispatchBrowser(cmd, "history.remove", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printURLsTable(result)
		})
	}
	return cmd
}

func newHistoryClearCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "clear",
		Short: "Delete history in a time range, or all history when no range is given (requires --yes)",
		Args:  cobra.NoArgs,
	}
	rng := addTimeRangeFlags(cmd)
	yes := addYesFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{}
		if err := rng.apply(cmd, input); err != nil {
			return err
		}
		yes.apply(input)
		return dispatchBrowser(cmd, "history.clear", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printHistoryCleared(result)
		})
	}
	return cmd
}

// historyRow 承载 history.search 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// 标题与 URL 由网页控制,打印前一律经 terminalSafe。
type historyRow struct {
	URL           string      `json:"url"`
	Title         string      `json:"title"`
	LastVisitTime int64       `json:"lastVisitTime"`
	VisitCount    int         `json:"visitCount"`
	Browser       *browserRef `json:"browser,omitempty"`
}

func printHistoryTable(result json.RawMessage) error {
	var payload struct {
		Items   []historyRow `json:"items"`
		HasMore bool         `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Items) == 0 {
		fmt.Fprintln(os.Stdout, "(no history)")
		printHasMore(payload.HasMore)
		return nil
	}
	multi := slices.ContainsFunc(payload.Items, func(r historyRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "URL\tTITLE\tLAST VISIT\tVISITS"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range payload.Items {
		row := fmt.Sprintf("%s\t%s\t%s\t%d", terminalSafe(r.URL), terminalSafe(r.Title), formatMillis(r.LastVisitTime), r.VisitCount)
		if multi {
			row += "\t" + r.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMore(payload.HasMore)
	return nil
}

type visitRow struct {
	VisitTime  int64       `json:"visitTime"`
	Transition string      `json:"transition"`
	Browser    *browserRef `json:"browser,omitempty"`
}

func printVisitsTable(result json.RawMessage) error {
	var payload struct {
		Visits  []visitRow `json:"visits"`
		HasMore bool       `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Visits) == 0 {
		fmt.Fprintln(os.Stdout, "(no visits)")
		printHasMore(payload.HasMore)
		return nil
	}
	multi := slices.ContainsFunc(payload.Visits, func(r visitRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "VISITED\tSOURCE"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range payload.Visits {
		row := fmt.Sprintf("%s\t%s", formatMillis(r.VisitTime), terminalSafe(r.Transition))
		if multi {
			row += "\t" + r.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMore(payload.HasMore)
	return nil
}

func printHistoryCleared(result json.RawMessage) error {
	var payload struct {
		All bool `json:"all"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if payload.All {
		fmt.Fprintln(os.Stdout, "cleared all history")
	} else {
		fmt.Fprintln(os.Stdout, "cleared history in the given time range")
	}
	return nil
}
