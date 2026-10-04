package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
)

// newReadingListCmd 构造 `sctl reading-list`:list/add/mark-read/rm 四个子命令,各对应一个 readingList.*
// 浏览器方法(docs/specs 第 2 期「阅读列表」)。
func newReadingListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "reading-list",
		Short: "Manage the reading list on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newReadingListListCmd(), newReadingListAddCmd(), newReadingListMarkReadCmd(), newReadingListRemoveCmd())
	return cmd
}

func newReadingListListCmd() *cobra.Command {
	var read, unread bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List reading list entries, newest first",
		Args:  cobra.NoArgs,
	}
	limit := addLimitFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{}
		if err := limit.apply(input); err != nil {
			return err
		}
		switch {
		case read:
			input["read"] = true
		case unread:
			input["read"] = false
		}
		return dispatchBrowser(cmd, "readingList.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printReadingListTable(result)
		})
	}
	cmd.Flags().BoolVar(&read, "read", false, "list only entries marked as read")
	cmd.Flags().BoolVar(&unread, "unread", false, "list only entries not yet read")
	cmd.MarkFlagsMutuallyExclusive("read", "unread")
	return cmd
}

func newReadingListAddCmd() *cobra.Command {
	var title string
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add a URL to the reading list",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{"url": args[0]}
			if cmd.Flags().Changed("title") {
				input["title"] = title
			}
			return dispatchBrowser(cmd, "readingList.add", browserTarget, mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printReadingListAdded(result)
			})
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "entry title (default: the URL)")
	return cmd
}

func newReadingListMarkReadCmd() *cobra.Command {
	var unread bool
	cmd := &cobra.Command{
		Use:   "mark-read <url>...",
		Short: "Mark reading list entries as read, or as unread with --unread",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := mustInput(map[string]any{"urls": args, "read": !unread})
			return dispatchBrowser(cmd, "readingList.markRead", browserTarget, input, func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printReadingListMarked(result)
			})
		},
	}
	cmd.Flags().BoolVar(&unread, "unread", false, "mark the entries as unread instead")
	return cmd
}

func newReadingListRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "rm <url>...",
		Short: "Remove entries from the reading list (requires --yes)",
		Args:  cobra.MinimumNArgs(1),
	}
	yes := addYesFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"urls": args}
		yes.apply(input)
		return dispatchBrowser(cmd, "readingList.remove", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printURLsTable(result)
		})
	}
	return cmd
}

// readingListRow 承载 readingList.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// 标题由网页控制,只作为结构化字段打印。
type readingListRow struct {
	URL       string      `json:"url"`
	Title     string      `json:"title"`
	Read      bool        `json:"read"`
	CreatedAt int64       `json:"createdAt"`
	UpdatedAt int64       `json:"updatedAt"`
	Browser   *browserRef `json:"browser,omitempty"`
}

func printReadingListTable(result json.RawMessage) error {
	var payload struct {
		Entries []readingListRow `json:"entries"`
		HasMore bool             `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Entries) == 0 {
		fmt.Fprintln(os.Stdout, "(no entries)")
		printHasMore(payload.HasMore)
		return nil
	}
	multi := false
	for _, e := range payload.Entries {
		multi = multi || e.Browser != nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "URL\tTITLE\tREAD\tADDED\tUPDATED"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, e := range payload.Entries {
		row := fmt.Sprintf("%s\t%s\t%v\t%s\t%s", terminalSafe(e.URL), terminalSafe(e.Title), e.Read, formatMillis(e.CreatedAt), formatMillis(e.UpdatedAt))
		if multi {
			row += "\t" + e.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMore(payload.HasMore)
	return nil
}

// formatMillis 把浏览器给的毫秒时间戳按本地时区打印。
func formatMillis(ms int64) string {
	return time.UnixMilli(ms).Local().Format(time.DateTime)
}

func printReadingListAdded(result json.RawMessage) error {
	var payload struct {
		URL   string `json:"url"`
		Title string `json:"title"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "URL\tTITLE")
	fmt.Fprintf(tw, "%s\t%s\n", terminalSafe(payload.URL), terminalSafe(payload.Title))
	return tw.Flush()
}

func printReadingListMarked(result json.RawMessage) error {
	var payload struct {
		URLs []string `json:"urls"`
		Read bool     `json:"read"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "URL\tREAD")
	for _, u := range payload.URLs {
		fmt.Fprintf(tw, "%s\t%v\n", terminalSafe(u), payload.Read)
	}
	return tw.Flush()
}

// printURLsTable 打印结果里 urls 数组的一列表格。
func printURLsTable(result json.RawMessage) error {
	var payload struct {
		URLs []string `json:"urls"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "URL")
	for _, u := range payload.URLs {
		fmt.Fprintln(tw, terminalSafe(u))
	}
	return tw.Flush()
}
