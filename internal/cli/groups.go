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

// groupColors 是 groups create/edit 接受的九种 Chrome 标签组颜色,与 protocol.json 的枚举一致。
var groupColors = []string{"grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"}

func requireGroupColor(color string) error {
	if !slices.Contains(groupColors, color) {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid color %q: want one of %s", color, strings.Join(groupColors, ", "))}
	}
	return nil
}

func parseGroupID(arg string) (int, error) {
	id, err := strconv.Atoi(arg)
	if err != nil {
		return 0, &ExitError{Code: exitError, Message: fmt.Sprintf("invalid group ID %q: %v", arg, err)}
	}
	return id, nil
}

// newGroupsCmd 构造 `sctl groups`:list/create/add/edit/ungroup,各对应一个 tabGroups.* 浏览器方法。
func newGroupsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "groups",
		Short: "Manage tab groups on a paired sctl Browser instance",
	}
	addBrowserFlag(cmd)
	cmd.AddCommand(newGroupsListCmd(), newGroupsCreateCmd(), newGroupsAddCmd(), newGroupsEditCmd(), newGroupsUngroupCmd())
	return cmd
}

func newGroupsListCmd() *cobra.Command {
	var window int
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List tab groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			input := map[string]any{}
			if cmd.Flags().Changed("window") {
				input["windowId"] = window
			}
			return dispatchBrowser(cmd, "tabGroups.list", browserTarget, mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printGroupsTable(result)
			})
		},
	}
	cmd.Flags().IntVar(&window, "window", 0, "restrict the listing to groups in this window ID")
	return cmd
}

func newGroupsCreateCmd() *cobra.Command {
	var title, color string
	cmd := &cobra.Command{
		Use:   "create <tabId>...",
		Short: "Group tabs of one window into a new group and print its group ID",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			input := map[string]any{"tabIds": ids}
			if cmd.Flags().Changed("title") {
				input["title"] = title
			}
			if cmd.Flags().Changed("color") {
				if err := requireGroupColor(color); err != nil {
					return err
				}
				input["color"] = color
			}
			return dispatchBrowser(cmd, "tabGroups.create", browserTarget, mustInput(input), printGroupIDResult)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "group title")
	cmd.Flags().StringVar(&color, "color", "", "group color: "+strings.Join(groupColors, ", "))
	return cmd
}

func newGroupsAddCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "add <groupId> <tabId>...",
		Short: "Add tabs to an existing group; all join or none do",
		Args:  cobra.MinimumNArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupID, err := parseGroupID(args[0])
			if err != nil {
				return err
			}
			ids, err := parseTabIDs(args[1:])
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			return dispatchBrowser(cmd, "tabGroups.add", browserTarget, mustInput(map[string]any{"groupId": groupID, "tabIds": ids}), printTabIDsResult)
		},
	}
}

func newGroupsEditCmd() *cobra.Command {
	var title, color string
	var collapse, expand bool
	cmd := &cobra.Command{
		Use:   "edit <groupId>",
		Short: "Change a group's title, color, or collapsed state",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			groupID, err := parseGroupID(args[0])
			if err != nil {
				return err
			}
			input := map[string]any{"groupId": groupID}
			if cmd.Flags().Changed("title") {
				input["title"] = title
			}
			if cmd.Flags().Changed("color") {
				if err := requireGroupColor(color); err != nil {
					return err
				}
				input["color"] = color
			}
			if collapse || expand {
				input["collapsed"] = collapse
			}
			if len(input) == 1 {
				return &ExitError{Code: exitError, Message: "nothing to change: give --title, --color, --collapse or --expand"}
			}
			return dispatchBrowser(cmd, "tabGroups.edit", browserTarget, mustInput(input), printGroupIDResult)
		},
	}
	cmd.Flags().StringVar(&title, "title", "", "new group title")
	cmd.Flags().StringVar(&color, "color", "", "new group color: "+strings.Join(groupColors, ", "))
	cmd.Flags().BoolVar(&collapse, "collapse", false, "collapse the group")
	cmd.Flags().BoolVar(&expand, "expand", false, "expand the group")
	cmd.MarkFlagsMutuallyExclusive("collapse", "expand")
	return cmd
}

func newGroupsUngroupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ungroup <tabId>...",
		Short: "Remove tabs from their groups; a group left empty is deleted by the browser",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids, err := parseTabIDs(args)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			return dispatchBrowser(cmd, "tabGroups.ungroup", browserTarget, mustInput(map[string]any{"tabIds": ids}), printTabIDsResult)
		},
	}
}

func printGroupIDResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		GroupId int `json:"groupId"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "GROUP ID")
	fmt.Fprintf(tw, "%d\n", payload.GroupId)
	return tw.Flush()
}

// groupRow 承载 tabGroups.list 表格所需字段。Browser 只在多实例汇总时非空(docs/protocol.md §3.1);
// Title 由网页或用户控制,只作为结构化字段打印。
type groupRow struct {
	GroupId   int         `json:"groupId"`
	WindowId  int         `json:"windowId"`
	Title     string      `json:"title"`
	Color     string      `json:"color"`
	Collapsed bool        `json:"collapsed"`
	TabCount  int         `json:"tabCount"`
	Browser   *browserRef `json:"browser,omitempty"`
}

func printGroupsTable(result json.RawMessage) error {
	var payload struct {
		Groups []groupRow `json:"groups"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	if len(payload.Groups) == 0 {
		fmt.Fprintln(os.Stdout, "(no groups)")
		return nil
	}
	multi := slices.ContainsFunc(payload.Groups, func(g groupRow) bool { return g.Browser != nil })
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	header := "GROUP ID\tWINDOW ID\tTITLE\tCOLOR\tCOLLAPSED\tTABS"
	if multi {
		header += "\tBROWSER"
	}
	fmt.Fprintln(tw, header)
	for _, g := range payload.Groups {
		row := fmt.Sprintf("%d\t%d\t%s\t%s\t%v\t%d", g.GroupId, g.WindowId, terminalSafe(g.Title), g.Color, g.Collapsed, g.TabCount)
		if multi {
			row += "\t" + g.Browser.Name
		}
		fmt.Fprintln(tw, row)
	}
	return tw.Flush()
}
