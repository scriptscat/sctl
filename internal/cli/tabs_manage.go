package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// windowStates 是 windows open/state 接受的窗口状态,与 protocol.json 的枚举一致。
var windowStates = []string{"normal", "minimized", "maximized", "fullscreen"}

func requireWindowState(state string) error {
	if !slices.Contains(windowStates, state) {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid window state %q: want one of %s", state, strings.Join(windowStates, ", "))}
	}
	return nil
}

func parseWindowIDs(args []string) ([]int, error) {
	ids := make([]int, 0, len(args))
	for _, a := range args {
		id, err := strconv.Atoi(a)
		if err != nil {
			return nil, &ExitError{Code: exitError, Message: fmt.Sprintf("invalid window ID %q: %v", a, err)}
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func newTabsMoveCmd() *cobra.Command {
	var window, index int
	cmd := &cobra.Command{
		Use:   "move <tabId>...",
		Short: "Move tabs to a window and position; all move or none do",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			input := map[string]any{"tabIds": ids}
			if cmd.Flags().Changed("window") {
				input["windowId"] = window
			}
			if cmd.Flags().Changed("index") {
				if index < -1 {
					return &ExitError{Code: exitError, Message: "--index must be -1 (the end) or a position from 0"}
				}
				input["index"] = index
			}
			return dispatchBrowser(cmd, "tabs.move", browserTarget, mustInput(input), printTabIDsResult)
		},
	}
	cmd.Flags().IntVar(&window, "window", 0, "move into this window ID (default: each tab's current window)")
	cmd.Flags().IntVar(&index, "index", -1, "position in the window, from 0; -1 is the end (default)")
	return cmd
}

// newTabsSetCmd 构造 pin/unpin/mute/unmute:它们是同一形状的标签页批量操作,协议方法各一个。
func newTabsSetCmd(verb, short, action string) *cobra.Command {
	return &cobra.Command{
		Use:   verb + " <tabId>...",
		Short: short + "; all change or none do",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			return dispatchBrowser(cmd, action, browserTarget, mustInput(map[string]any{"tabIds": ids}), printTabIDsResult)
		},
	}
}

func newTabsReloadCmd() *cobra.Command {
	var bypassCache bool
	cmd := &cobra.Command{
		Use:   "reload <tabId>...",
		Short: "Reload tabs; all reload or none do",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			input := map[string]any{"tabIds": ids}
			if bypassCache {
				input["bypassCache"] = true
			}
			return dispatchBrowser(cmd, "tabs.reload", browserTarget, mustInput(input), printTabIDsResult)
		},
	}
	cmd.Flags().BoolVar(&bypassCache, "bypass-cache", false, "reload without using the cache")
	return cmd
}

func newTabsDuplicateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "duplicate <tabId>",
		Short: "Duplicate a tab and print the new tab ID",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			return dispatchBrowser(cmd, "tabs.duplicate", browserTarget, mustInput(map[string]any{"tabId": ids[0]}), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printTabIDTable(result)
			})
		},
	}
}

func printTabIDsResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	return printTabIDsTable(result)
}

func printWindowIDResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	return printWindowIDsTable(result)
}

func newWindowsOpenCmd() *cobra.Command {
	var state string
	cmd := &cobra.Command{
		Use:   "open [<url>...]",
		Short: "Open a new window, optionally with URLs, and print its window ID",
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{}
			if len(args) > 0 {
				input["urls"] = args
			}
			if cmd.Flags().Changed("state") {
				if err := requireWindowState(state); err != nil {
					return err
				}
				input["state"] = state
			}
			return dispatchBrowser(cmd, "windows.open", browserTarget, mustInput(input), printWindowIDResult)
		},
	}
	cmd.Flags().StringVar(&state, "state", "", "window state: "+strings.Join(windowStates, ", "))
	return cmd
}

func newWindowsCloseCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "close <windowId>...",
		Short: "Close windows; all close or none do (recent restore can bring them back)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseWindowIDs(args)
			if err != nil {
				return err
			}
			return dispatchBrowser(cmd, "windows.close", browserTarget, mustInput(map[string]any{"windowIds": ids}), printWindowIDResult)
		},
	}
}

func newWindowsFocusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "focus <windowId>",
		Short: "Bring a window to the front",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseWindowIDs(args)
			if err != nil {
				return err
			}
			return dispatchBrowser(cmd, "windows.focus", browserTarget, mustInput(map[string]any{"windowId": ids[0]}), printWindowIDResult)
		},
	}
}

func newWindowsStateCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "state <windowId> <state>",
		Short: "Set a window's state: " + strings.Join(windowStates, ", "),
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseWindowIDs(args[:1])
			if err != nil {
				return err
			}
			if err := requireWindowState(args[1]); err != nil {
				return err
			}
			input := map[string]any{"windowId": ids[0], "state": args[1]}
			return dispatchBrowser(cmd, "windows.state", browserTarget, mustInput(input), printWindowIDResult)
		},
	}
}

// printWindowIDsTable 打印结果里的窗口 ID(windowId 单个或 windowIds 列表);结果带 state 时多一列。
func printWindowIDsTable(result json.RawMessage) error {
	var payload struct {
		WindowId  *int   `json:"windowId"`
		WindowIds []int  `json:"windowIds"`
		State     string `json:"state"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	ids := payload.WindowIds
	if payload.WindowId != nil {
		ids = []int{*payload.WindowId}
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	withState := payload.State != ""
	if withState {
		fmt.Fprintln(tw, "WINDOW ID\tSTATE")
	} else {
		fmt.Fprintln(tw, "WINDOW ID")
	}
	for _, id := range ids {
		if withState {
			fmt.Fprintf(tw, "%d\t%s\n", id, terminalSafe(payload.State))
		} else {
			fmt.Fprintf(tw, "%d\n", id)
		}
	}
	return tw.Flush()
}
