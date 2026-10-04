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

// downloadStates 是 downloads list --state 的合法取值,与 protocol.json 的 DownloadsListParams.state 一致。
var downloadStates = []string{"in_progress", "complete", "interrupted"}

// newDownloadsCmd 构造 `sctl downloads`:list/start/pause/resume/cancel/erase/delete-file/show,
// 各对应一个 downloads.* 浏览器方法。
func newDownloadsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "downloads",
		Short: "Manage downloads on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(
		newDownloadsListCmd(),
		newDownloadsStartCmd(),
		newDownloadsIDCmd("pause", "Pause an in-progress download", "downloads.pause", "paused download %d", false),
		newDownloadsIDCmd("resume", "Resume a paused download", "downloads.resume", "resumed download %d", false),
		newDownloadsIDCmd("cancel", "Cancel an in-progress download (requires --yes)", "downloads.cancel", "canceled download %d", true),
		newDownloadsEraseCmd(),
		newDownloadsIDCmd("delete-file", "Delete the file of a completed download from disk, keeping its record (requires --yes)", "downloads.deleteFile", "deleted the file of download %d", true),
		newDownloadsIDCmd("show", "Show a download's file in the system file manager", "downloads.show", "showing download %d", false),
	)
	return cmd
}

func newDownloadsListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List downloads, newest first",
		Args:  cobra.NoArgs,
	}
	limit := addLimitFlag(cmd)
	var state, query string
	cmd.Flags().StringVar(&state, "state", "", "only downloads in this state: in_progress, complete or interrupted")
	cmd.Flags().StringVar(&query, "query", "", "only downloads whose URL or file name contains this text")
	cmd.RunE = func(cmd *cobra.Command, _ []string) error {
		input := map[string]any{}
		if err := limit.apply(input); err != nil {
			return err
		}
		if cmd.Flags().Changed("state") {
			if !slices.Contains(downloadStates, state) {
				return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid --state %q: must be one of in_progress, complete, interrupted", state)}
			}
			input["state"] = state
		}
		if cmd.Flags().Changed("query") {
			input["query"] = query
		}
		return dispatchBrowser(cmd, "downloads.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printDownloadsTable(result)
		})
	}
	return cmd
}

func newDownloadsStartCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "start <url>",
		Short: "Start a download into the browser's default download directory; an existing file is never overwritten",
		Args:  cobra.ExactArgs(1),
	}
	var filename string
	cmd.Flags().StringVar(&filename, "filename", "", "save under this relative path inside the download directory (no absolute paths, no ..)")
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		input := map[string]any{"url": args[0]}
		if cmd.Flags().Changed("filename") {
			input["filename"] = filename
		}
		return dispatchBrowser(cmd, "downloads.start", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printDownloadID(result, "started download %d")
		})
	}
	return cmd
}

// newDownloadsIDCmd 构造只带一个下载 ID 的子命令;l1 为真时挂 --yes。
func newDownloadsIDCmd(use, short, method, doneFormat string, l1 bool) *cobra.Command {
	cmd := &cobra.Command{
		Use:   use + " <id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
	}
	var yes *yesFlag
	if l1 {
		yes = addYesFlag(cmd)
	}
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		id, err := parseDownloadID(args[0])
		if err != nil {
			return err
		}
		input := map[string]any{"id": id}
		if yes != nil {
			yes.apply(input)
		}
		return dispatchBrowser(cmd, method, browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			return printDownloadID(result, doneFormat)
		})
	}
	return cmd
}

func newDownloadsEraseCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "erase <id>...",
		Short: "Remove downloads from the download list without deleting their files (requires --yes)",
		Args:  cobra.MinimumNArgs(1),
	}
	yes := addYesFlag(cmd)
	cmd.RunE = func(cmd *cobra.Command, args []string) error {
		ids := make([]int, 0, len(args))
		for _, arg := range args {
			id, err := parseDownloadID(arg)
			if err != nil {
				return err
			}
			ids = append(ids, id)
		}
		input := map[string]any{"ids": ids}
		yes.apply(input)
		return dispatchBrowser(cmd, "downloads.erase", browserTarget, mustInput(input), func(result json.RawMessage) error {
			if outputFormat == outputJSON {
				return printResultJSON(result)
			}
			var payload struct {
				IDs []int `json:"ids"`
			}
			if err := json.Unmarshal(result, &payload); err != nil {
				return printResultJSON(result)
			}
			fmt.Fprintf(os.Stdout, "erased %d download records\n", len(payload.IDs))
			return nil
		})
	}
	return cmd
}

// parseDownloadID 校验命令行上的下载 ID(不可信输入),非负整数以外的一律在发起调用前报错。
func parseDownloadID(arg string) (int, error) {
	id, err := strconv.Atoi(arg)
	if err != nil || id < 0 {
		return 0, &ExitError{Code: exitError, Message: fmt.Sprintf("invalid download id %q: must be a non-negative integer", arg)}
	}
	return id, nil
}

func printDownloadID(result json.RawMessage, format string) error {
	var payload struct {
		ID int `json:"id"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	fmt.Fprintf(os.Stdout, format+"\n", payload.ID)
	return nil
}

// downloadRow 承载 downloads.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// URL 与文件路径由网页或服务器控制,打印前一律经 terminalSafe。
type downloadRow struct {
	ID            int         `json:"id"`
	URL           string      `json:"url"`
	Filename      string      `json:"filename"`
	State         string      `json:"state"`
	BytesReceived int64       `json:"bytesReceived"`
	TotalBytes    int64       `json:"totalBytes"`
	StartTime     int64       `json:"startTime"`
	Exists        bool        `json:"exists"`
	Browser       *browserRef `json:"browser,omitempty"`
}

func printDownloadsTable(result json.RawMessage) error {
	var payload struct {
		Items   []downloadRow `json:"items"`
		HasMore bool          `json:"hasMore"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Items) == 0 {
		fmt.Fprintln(os.Stdout, "(no downloads)")
		printHasMore(payload.HasMore)
		return nil
	}
	multi := slices.ContainsFunc(payload.Items, func(r downloadRow) bool { return r.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "ID\tSTATE\tRECEIVED\tTOTAL\tSTARTED\tEXISTS\tFILE\tURL"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, r := range payload.Items {
		total := strconv.FormatInt(r.TotalBytes, 10)
		if r.TotalBytes < 0 {
			total = "unknown"
		}
		row := fmt.Sprintf("%d\t%s\t%d\t%s\t%s\t%t\t%s\t%s",
			r.ID, r.State, r.BytesReceived, total, formatMillis(r.StartTime), r.Exists, terminalSafe(r.Filename), terminalSafe(r.URL))
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
