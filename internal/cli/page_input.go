package cli

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

const selectorFlagHelp = "CSS selector matching exactly one element in the main document, instead of a ref"

// targetAndRest 校验带目标的动作的位置参数:没有 --selector 时第一个参数是元素引用,其余是动作自己的
// 参数,个数在 [min, max] 之间(max 为 -1 表示不限)。
func targetAndRest(action string, selector *string, rest string, min, max int) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		var got []string
		switch {
		case *selector != "":
			got = args
		case len(args) == 0:
			return &ExitError{Code: exitError, Message: fmt.Sprintf("page %s needs a target: a ref from a snapshot (e5) or --selector, then %s", action, rest)}
		default:
			got = args[1:]
		}
		if len(got) < min || (max >= 0 && len(got) > max) {
			return &ExitError{Code: exitError, Message: fmt.Sprintf("page %s takes a ref or --selector, then %s", action, rest)}
		}
		return nil
	}
}

// oneArg 校验动作恰好带一个位置参数。
func oneArg(action, what string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 1 {
			return &ExitError{Code: exitError, Message: fmt.Sprintf("page %s takes exactly one argument: %s", action, what)}
		}
		return nil
	}
}

// splitTarget 把已校验的位置参数拆成目标输入与动作自己的参数。
func splitTarget(args []string, selector string) (map[string]any, []string) {
	if selector != "" {
		return map[string]any{"selector": selector}, args
	}
	return map[string]any{"ref": args[0]}, args[1:]
}

func newPageFillCmd() *cobra.Command {
	var selector string
	cmd := &cobra.Command{
		Use:   "fill [<ref>] [--selector <css>] <text>",
		Short: "Clear an input, textarea or contenteditable element and fill in text",
		Long: "Clear an input, textarea or contenteditable element and fill in the text, firing input and change events.\n" +
			"An empty text clears the field. Checkbox and radio inputs are refused (use page click), file inputs too\n" +
			"(use page upload).\n" + targetHelp +
			"Before acting it scrolls the element into view and waits until it is attached, visible, enabled and editable\n" +
			"(not read-only); on timeout the error names the last unmet condition.",
		Args: targetAndRest("fill", &selector, "the text", 1, 1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, rest := splitTarget(args, selector)
			input["text"] = rest[0]
			return dispatchPage(cmd, "fill", mustInput(input), printActionResult)
		},
	}
	cmd.Flags().StringVar(&selector, "selector", "", selectorFlagHelp)
	return cmd
}

func newPageTypeCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "type <text>",
		Short: "Type text key by key into the element that currently has focus",
		Long: "Type text key by key into the element that currently has focus, with trusted keyboard events; newlines press\n" +
			"Enter and characters without a key on a US keyboard are inserted directly. Focus an element first, for\n" +
			"example with page click or page fill.",
		Args: oneArg("type", "the text to type"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchPage(cmd, "type", mustInput(map[string]any{"text": args[0]}), printActionResult)
		},
	}
}

func newPagePressCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "press <key>",
		Short: "Press a key or key combination, such as Enter, Control+A or Shift+Tab",
		Long: "Press a key or key combination in the element that currently has focus, with trusted keydown and keyup events.\n" +
			"The syntax is Playwright's: a key name such as Enter, Tab, Escape, Backspace, Delete, ArrowDown, Home, End,\n" +
			"PageDown, F5 or a single character, optionally after modifiers Alt, Control, Meta, Shift joined by +\n" +
			"(Control+A, Shift+Tab, Meta+V).",
		Args: oneArg("press", "the key"),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatchPage(cmd, "press", mustInput(map[string]any{"key": args[0]}), printActionResult)
		},
	}
}

func newPageSelectCmd() *cobra.Command {
	var selector string
	cmd := &cobra.Command{
		Use:   "select [<ref>] [--selector <css>] <value>...",
		Short: "Choose options of a <select> by value or visible text",
		Long: "Choose the options of a <select> whose value or visible text matches each given value, firing input and change\n" +
			"events. A multi-select takes several values and deselects the rest. A value that matches no option fails with\n" +
			"NOT_FOUND and changes nothing; an element that is not a <select> is refused.\n" + targetHelp +
			"Before acting it scrolls the element into view and waits until it is attached, visible and enabled.",
		Args: targetAndRest("select", &selector, "one or more values", 1, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, rest := splitTarget(args, selector)
			input["values"] = rest
			return dispatchPage(cmd, "select", mustInput(input), printActionResult)
		},
	}
	cmd.Flags().StringVar(&selector, "selector", "", selectorFlagHelp)
	return cmd
}

func newPageUploadCmd() *cobra.Command {
	var selector string
	cmd := &cobra.Command{
		Use:   "upload [<ref>] [--selector <css>] <file>...",
		Short: "Set the files of a file input",
		Long: "Set the files of a file input, firing input and change events. Relative paths are resolved against the current\n" +
			"directory; every file must exist and be readable, and several files need an input with the multiple attribute.\n" +
			"The file input may be hidden.\n" + targetHelp +
			"Before acting it scrolls the element into view and waits until it is attached and enabled.",
		Args: targetAndRest("upload", &selector, "one or more files", 1, -1),
		RunE: func(cmd *cobra.Command, args []string) error {
			input, rest := splitTarget(args, selector)
			files, err := absoluteFiles(rest)
			if err != nil {
				return err
			}
			input["files"] = files
			return dispatchPage(cmd, "upload", mustInput(input), printActionResult)
		},
	}
	cmd.Flags().StringVar(&selector, "selector", "", selectorFlagHelp)
	return cmd
}

// absoluteFiles 把相对路径按命令行的当前目录解析为绝对路径,并检查每个文件存在、可读。daemon 与浏览器
// 在同一台机器上,读文件的是浏览器,所以要在这里(唯一知道当前目录的地方)解析;daemon 仍会再检查一遍。
func absoluteFiles(files []string) ([]string, error) {
	out := make([]string, len(files))
	for i, file := range files {
		abs, err := filepath.Abs(file)
		if err != nil {
			return nil, &ExitError{Code: exitError, Message: fmt.Sprintf("resolve %q: %v", file, err)}
		}
		info, err := os.Stat(abs)
		if err != nil {
			return nil, &ExitError{Code: exitError, Message: fmt.Sprintf("cannot upload %s: %v", abs, err)}
		}
		if !info.Mode().IsRegular() {
			return nil, &ExitError{Code: exitError, Message: fmt.Sprintf("cannot upload %s: not a regular file", abs)}
		}
		f, err := os.Open(abs)
		if err != nil {
			return nil, &ExitError{Code: exitError, Message: fmt.Sprintf("cannot upload %s: %v", abs, err)}
		}
		if err := f.Close(); err != nil {
			return nil, fmt.Errorf("close %s: %w", abs, err)
		}
		out[i] = abs
	}
	return out, nil
}

func newPageScrollCmd() *cobra.Command {
	var (
		selector string
		dx, dy   float64
	)
	cmd := &cobra.Command{
		Use:   "scroll [<ref>] [--selector <css>] [--dx <px>] [--dy <px>]",
		Short: "Scroll an element into view, or scroll the viewport by pixels",
		Long: "With a target, scroll the element into view (it only has to be attached). Without a target, scroll the viewport\n" +
			"with the mouse wheel at its center by --dx pixels right and --dy pixels down (negative values scroll left and up).\n" +
			"A target and --dx/--dy cannot be combined.\n" + targetHelp,
		Args: func(_ *cobra.Command, args []string) error {
			hasTarget := len(args) > 0 || selector != ""
			switch {
			case len(args) > 1:
				return &ExitError{Code: exitError, Message: "page scroll takes one element ref"}
			case len(args) == 1 && selector != "":
				return &ExitError{Code: exitError, Message: "page scroll takes a ref or --selector, not both"}
			case hasTarget && (dx != 0 || dy != 0):
				return &ExitError{Code: exitError, Message: "page scroll takes a target to scroll into view or --dx/--dy to scroll the viewport, not both"}
			case !hasTarget && dx == 0 && dy == 0:
				return &ExitError{Code: exitError, Message: "page scroll needs a target (a ref or --selector) or a non-zero --dx/--dy"}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			input := map[string]any{}
			switch {
			case len(args) == 1 || selector != "":
				input = targetInput(args, selector)
			default:
				if dx != 0 {
					input["dx"] = dx
				}
				if dy != 0 {
					input["dy"] = dy
				}
			}
			return dispatchPage(cmd, "scroll", mustInput(input), printActionResult)
		},
	}
	cmd.Flags().StringVar(&selector, "selector", "", selectorFlagHelp)
	cmd.Flags().Float64Var(&dx, "dx", 0, "pixels to scroll the viewport to the right (negative: left)")
	cmd.Flags().Float64Var(&dy, "dy", 0, "pixels to scroll the viewport down (negative: up)")
	return cmd
}
