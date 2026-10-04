package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"
	"unicode"

	"github.com/spf13/cobra"
)

// browserTarget 是 --browser 的值,默认取自 SCTL_BROWSER(与 --data-dir/SCTL_DATA_DIR 同一先例,
// 见 cli.go:86):环境变量在构建标志时读取,显式传入的 --browser 优先于它。由 daemon 解析为
// 具体的已配对实例(docs/protocol.md §3.1「路由与目标选择」)。
var browserTarget string

// addBrowserFlag 给一棵浏览器命令树的父命令挂 --browser,其全部子命令通过 PersistentFlags 继承。
func addBrowserFlag(cmd *cobra.Command) {
	cmd.PersistentFlags().StringVar(&browserTarget, "browser", os.Getenv("SCTL_BROWSER"),
		"target browser instance: name or instance-ID prefix (env: SCTL_BROWSER; an explicit --browser wins)")
}

// browserRef 镜像多实例汇总结果里每一项携带的 "browser":{"id","name"}(docs/protocol.md §3.1)。
// daemon 汇总时给每一项都标上来源,所以只要有一项带它,每一项都带。
type browserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// newTabsCmd 构造 `sctl tabs`:第 1 期的 list/open/close/activate,加上整理用的 move/pin/unpin/mute/unmute/
// reload/duplicate,均路由到浏览器方法。
func newTabsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "tabs",
		Short: "Manage tabs on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newTabsListCmd(), newTabsOpenCmd(), newTabsCloseCmd(), newTabsActivateCmd(),
		newTabsMoveCmd(),
		newTabsSetCmd("pin", "Pin tabs", "tabs.pin"), newTabsSetCmd("unpin", "Unpin tabs", "tabs.unpin"),
		newTabsSetCmd("mute", "Mute tabs", "tabs.mute"), newTabsSetCmd("unmute", "Unmute tabs", "tabs.unmute"),
		newTabsReloadCmd(), newTabsDuplicateCmd())
	return cmd
}

func newTabsListCmd() *cobra.Command {
	var window int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tabs, optionally restricted to one window",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input := map[string]any{}
			if cmd.Flags().Changed("window") {
				input["windowId"] = window
			}
			return dispatchBrowser(cmd, "tabs.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printTabsTable(result)
			})
		},
	}
	cmd.Flags().IntVar(&window, "window", 0, "restrict the listing to tabs in this window ID")
	return cmd
}

func newTabsOpenCmd() *cobra.Command {
	var window int
	var background bool
	cmd := &cobra.Command{
		Use:   "open <url>",
		Short: "Open a URL in a new tab and print its tab ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{"url": args[0]}
			if cmd.Flags().Changed("window") {
				input["windowId"] = window
			}
			if cmd.Flags().Changed("background") {
				input["background"] = background
			}
			return dispatchBrowser(cmd, "tabs.open", browserTarget, mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printTabIDTable(result)
			})
		},
	}
	cmd.Flags().IntVar(&window, "window", 0, "open in this window ID (default: the last-focused window)")
	cmd.Flags().BoolVar(&background, "background", false, "open without activating the new tab (default: activates it)")
	return cmd
}

func newTabsCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <tabId>...",
		Short: "Close one or more tabs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			input := mustInput(map[string]any{"tabIds": ids})
			return dispatchBrowser(cmd, "tabs.close", browserTarget, input, func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printTabIDsTable(result)
			})
		},
	}
}

func newTabsActivateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "activate <tabId>",
		Short: "Activate a tab and focus its window",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := strconv.Atoi(args[0])
			if err != nil {
				return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid tab ID %q: %v", args[0], err)}
			}
			input := mustInput(map[string]any{"tabId": id})
			return dispatchBrowser(cmd, "tabs.activate", browserTarget, input, func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printActivatedTabTable(result)
			})
		},
	}
}

// parseTabIDs 把位置参数解析为标签 ID 列表;任一项非法整数就整体报错(不做部分容错)。
func parseTabIDs(args []string) ([]int, error) {
	ids := make([]int, 0, len(args))
	for _, a := range args {
		id, err := strconv.Atoi(a)
		if err != nil {
			return nil, fmt.Errorf("invalid tab ID %q: %w", a, err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// tabRow 承载 tabs.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// Title/URL 是网页可控数据,只作为结构化字段打印,绝不解释执行(spec「页面内容不可信」)。
type tabRow struct {
	TabId    int         `json:"tabId"`
	WindowId int         `json:"windowId"`
	Active   bool        `json:"active"`
	Pinned   bool        `json:"pinned"`
	Title    string      `json:"title"`
	URL      string      `json:"url"`
	Browser  *browserRef `json:"browser,omitempty"`
}

func printTabsTable(result json.RawMessage) error {
	var payload struct {
		Tabs []tabRow `json:"tabs"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		// 结构不符预期时退回原样 JSON,不吞信息(与 get.go 的既有约定一致)。
		return printResultJSON(result)
	}
	if len(payload.Tabs) == 0 {
		fmt.Fprintln(os.Stdout, "(no tabs)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	multi := anyTabHasBrowser(payload.Tabs)
	if multi {
		fmt.Fprintln(tw, "TAB ID\tWINDOW ID\tACTIVE\tPINNED\tTITLE\tURL\tBROWSER")
	} else {
		fmt.Fprintln(tw, "TAB ID\tWINDOW ID\tACTIVE\tPINNED\tTITLE\tURL")
	}
	for _, t := range payload.Tabs {
		title, url := terminalSafe(t.Title), terminalSafe(t.URL)
		if multi {
			fmt.Fprintf(tw, "%d\t%d\t%v\t%v\t%s\t%s\t%s\n", t.TabId, t.WindowId, t.Active, t.Pinned, title, url, t.Browser.Name)
		} else {
			fmt.Fprintf(tw, "%d\t%d\t%v\t%v\t%s\t%s\n", t.TabId, t.WindowId, t.Active, t.Pinned, title, url)
		}
	}
	return tw.Flush()
}

// terminalSafe 把不可打印字符换成 Go 转义形式。表格直接写到终端:网页标题里的 ESC 序列会被终端执行
// (改窗口标题、清屏、伪造输出),制表符与换行会拆出假的列和行,双向控制符会倒转显示顺序。
func terminalSafe(s string) string {
	if strings.IndexFunc(s, func(r rune) bool { return !unicode.IsPrint(r) }) < 0 {
		return s
	}
	var b strings.Builder
	for _, r := range s {
		if unicode.IsPrint(r) {
			b.WriteRune(r)
			continue
		}
		quoted := strconv.QuoteRune(r)
		b.WriteString(quoted[1 : len(quoted)-1])
	}
	return b.String()
}

func anyTabHasBrowser(rows []tabRow) bool {
	for _, r := range rows {
		if r.Browser != nil {
			return true
		}
	}
	return false
}

func printTabIDTable(result json.RawMessage) error {
	var payload struct {
		TabId int `json:"tabId"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TAB ID")
	fmt.Fprintf(tw, "%d\n", payload.TabId)
	return tw.Flush()
}

func printTabIDsTable(result json.RawMessage) error {
	var payload struct {
		TabIds []int `json:"tabIds"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TAB ID")
	for _, id := range payload.TabIds {
		fmt.Fprintf(tw, "%d\n", id)
	}
	return tw.Flush()
}

func printActivatedTabTable(result json.RawMessage) error {
	var payload struct {
		TabId    int `json:"tabId"`
		WindowId int `json:"windowId"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TAB ID\tWINDOW ID")
	fmt.Fprintf(tw, "%d\t%d\n", payload.TabId, payload.WindowId)
	return tw.Flush()
}
