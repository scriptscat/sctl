package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newBookmarksCmd 构造 `sctl bookmarks`:list/search/add/mkdir/move/edit/rm 七个子命令,各对应一个 bookmarks.*
// 浏览器方法(docs/specs 第 2 期「书签」)。
func newBookmarksCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "bookmarks",
		Short: "Manage bookmarks on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newBookmarksListCmd(), newBookmarksSearchCmd(), newBookmarksAddCmd(), newBookmarksMkdirCmd(),
		newBookmarksMoveCmd(), newBookmarksEditCmd(), newBookmarksRemoveCmd())
	return cmd
}

// indexFlag 是 add/mkdir/move 的 --index:节点在文件夹里的位置。
type indexFlag struct {
	index int
}

func addIndexFlag(cmd *cobra.Command) *indexFlag {
	f := &indexFlag{}
	cmd.Flags().IntVar(&f.index, "index", 0, "position inside the folder, counted from 0 (default: append)")
	return f
}

// apply 只在给了 --index 时写进方法输入(0 是合法位置,不能按零值判断)。命令行参数是不可信输入,负数在发起调用前报错。
func (f *indexFlag) apply(cmd *cobra.Command, input map[string]any) error {
	if !cmd.Flags().Changed("index") {
		return nil
	}
	if f.index < 0 {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid --index %d: must be 0 or greater", f.index)}
	}
	input["index"] = f.index
	return nil
}

func newBookmarksListCmd() *cobra.Command {
	var folder string
	var recursive bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the children of a bookmark folder (default: the top-level folders)",
		Args:  cobra.NoArgs,
	}
	limit := addLimitFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{}
		if recursive {
			// 整棵子树不受条数上限约束(spec 书签一节),因此不下发 limit。
			input["recursive"] = true
		} else if err := limit.apply(input); err != nil {
			return err
		}
		if cmd.Flags().Changed("folder") {
			input["folder"] = folder
		}
		return dispatchBrowser(cmd, "bookmarks.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printBookmarkTable(result, false)
		})
	}
	cmd.Flags().StringVar(&folder, "folder", "", "folder ID to list (default: the top-level folders)")
	cmd.Flags().BoolVar(&recursive, "recursive", false, "return the whole subtree instead of the direct children")
	cmd.MarkFlagsMutuallyExclusive("recursive", "limit")
	return cmd
}

func newBookmarksSearchCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search bookmarks by title and URL, showing each result's folder path",
		Args:  cobra.ExactArgs(1),
	}
	limit := addLimitFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"query": args[0]}
		if err := limit.apply(input); err != nil {
			return err
		}
		return dispatchBrowser(cmd, "bookmarks.search", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printBookmarkTable(result, true)
		})
	}
	return cmd
}

func newBookmarksAddCmd() *cobra.Command {
	var title, folder string
	cmd := &cobra.Command{
		Use:   "add <url>",
		Short: "Add a bookmark (default folder: Other bookmarks)",
		Args:  cobra.ExactArgs(1),
	}
	index := addIndexFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"url": args[0]}
		if cmd.Flags().Changed("title") {
			input["title"] = title
		}
		if cmd.Flags().Changed("folder") {
			input["folder"] = folder
		}
		if err := index.apply(cmd, input); err != nil {
			return err
		}
		return dispatchBrowser(cmd, "bookmarks.add", browserTarget, mustInput(input), printIDResult)
	}
	cmd.Flags().StringVar(&title, "title", "", "bookmark title (default: the URL)")
	cmd.Flags().StringVar(&folder, "folder", "", "folder ID to add to (default: Other bookmarks)")
	return cmd
}

func newBookmarksMkdirCmd() *cobra.Command {
	var folder string
	cmd := &cobra.Command{
		Use:   "mkdir <title>",
		Short: "Create a bookmark folder (default parent: Other bookmarks)",
		Args:  cobra.ExactArgs(1),
	}
	index := addIndexFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"title": args[0]}
		if cmd.Flags().Changed("folder") {
			input["folder"] = folder
		}
		if err := index.apply(cmd, input); err != nil {
			return err
		}
		return dispatchBrowser(cmd, "bookmarks.mkdir", browserTarget, mustInput(input), printIDResult)
	}
	cmd.Flags().StringVar(&folder, "folder", "", "parent folder ID (default: Other bookmarks)")
	return cmd
}

func newBookmarksMoveCmd() *cobra.Command {
	var folder string
	cmd := &cobra.Command{
		Use:   "move <id>... --folder <id>",
		Short: "Move bookmarks or folders into a folder; all move or none do",
		Args:  cobra.MinimumNArgs(1),
	}
	index := addIndexFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"ids": args, "folder": folder}
		if err := index.apply(cmd, input); err != nil {
			return err
		}
		return dispatchBrowser(cmd, "bookmarks.move", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printIDsTable(result)
		})
	}
	cmd.Flags().StringVar(&folder, "folder", "", "destination folder ID")
	_ = cmd.MarkFlagRequired("folder")
	return cmd
}

func newBookmarksEditCmd() *cobra.Command {
	var title, url string
	cmd := &cobra.Command{
		Use:   "edit <id>",
		Short: "Change a bookmark's title or URL, or a folder's title",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{"id": args[0]}
			if cmd.Flags().Changed("title") {
				input["title"] = title
			}
			if cmd.Flags().Changed("url") {
				input["url"] = url
			}
			return dispatchBrowser(cmd, "bookmarks.edit", browserTarget, mustInput(input), printIDResult)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new title")
	cmd.Flags().StringVar(&url, "url", "", "new URL (bookmarks only; a folder has no URL)")
	cmd.MarkFlagsOneRequired("title", "url")
	return cmd
}

// newBookmarksRemoveCmd 构造 `sctl bookmarks rm`:L2,扩展打开审批窗口,命令阻塞到用户在浏览器里批准或拒绝。
func newBookmarksRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <id>...",
		Short: "Delete bookmarks and folders (with everything inside) after approval in the browser",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{"ids": args}
			return dispatchBrowserApproval(cmd, "bookmarks.remove", browserTarget, mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printRemovedCounts(result)
			})
		},
	}
}

// printRemovedCounts 打印 bookmarks.remove 删除的书签数与文件夹数(含被删文件夹里的内容)。
func printRemovedCounts(result json.RawMessage) error {
	var payload struct {
		Bookmarks int `json:"bookmarks"`
		Folders   int `json:"folders"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "BOOKMARKS\tFOLDERS")
	fmt.Fprintf(tw, "%d\t%d\n", payload.Bookmarks, payload.Folders)
	return tw.Flush()
}

// bookmarkRow 承载 bookmarks.list/search 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// 标题、URL 与路径由网页控制,只作为结构化字段打印。
type bookmarkRow struct {
	ID         string      `json:"id"`
	Type       string      `json:"type"`
	Title      string      `json:"title"`
	URL        string      `json:"url"`
	ParentID   string      `json:"parentId"`
	Index      int         `json:"index"`
	AddedAt    *int64      `json:"addedAt"`
	ChildCount *int        `json:"childCount"`
	Path       []string    `json:"path"`
	Browser    *browserRef `json:"browser,omitempty"`
}

// printBookmarkTable 打印 bookmarks.list(withPath=false)与 bookmarks.search(withPath=true)的结果。
func printBookmarkTable(result json.RawMessage, withPath bool) error {
	var payload struct {
		Nodes   []bookmarkRow `json:"nodes"`
		HasMore bool          `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Nodes) == 0 {
		fmt.Fprintln(os.Stdout, "(no bookmarks)")
		printHasMore(payload.HasMore)
		return nil
	}
	multi := false
	for _, n := range payload.Nodes {
		multi = multi || n.Browser != nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "ID\tTYPE\tTITLE\tURL\tPARENT\tINDEX\tADDED\tCHILDREN"
	if withPath {
		header += "\tPATH"
	}
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, n := range payload.Nodes {
		added, children := "", ""
		if n.AddedAt != nil {
			added = formatMillis(*n.AddedAt)
		}
		if n.ChildCount != nil {
			children = fmt.Sprint(*n.ChildCount)
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%d\t%s\t%s", terminalSafe(n.ID), n.Type, terminalSafe(n.Title), terminalSafe(n.URL),
			terminalSafe(n.ParentID), n.Index, added, children)
		if withPath {
			safe := make([]string, len(n.Path))
			for i, part := range n.Path {
				safe[i] = terminalSafe(part)
			}
			row += "\t" + strings.Join(safe, " / ")
		}
		if multi {
			row += "\t" + n.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	printHasMore(payload.HasMore)
	return nil
}

// printIDResult 打印结果里单个 id 的一列表格(add/mkdir/edit);-o json 原样输出。
func printIDResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID")
	fmt.Fprintln(tw, terminalSafe(payload.ID))
	return tw.Flush()
}

// printIDsTable 打印结果里 ids 数组的一列表格。
func printIDsTable(result json.RawMessage) error {
	var payload struct {
		IDs []string `json:"ids"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "ID")
	for _, id := range payload.IDs {
		fmt.Fprintln(tw, terminalSafe(id))
	}
	return tw.Flush()
}
