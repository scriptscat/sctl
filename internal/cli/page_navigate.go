package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"
)

var loadStates = []string{"load", "domcontentloaded", "networkidle"}

const waitStateHelp = "--wait sets the load state to wait for: load (default), domcontentloaded, or networkidle (no network\n" +
	"request in flight for at least 500ms). The timeout defaults to 30s and is set with --timeout.\n" +
	"HTTP error statuses such as 404 are not failures; the status is reported. Network errors such as a refused\n" +
	"connection fail with NAVIGATION_FAILED and Chrome's error text. Element refs expire when the page navigates."

// validLoadState 在边界上检查加载状态,避免把必然被拒的请求发给 daemon。
func validLoadState(state, flag string) error {
	if !slices.Contains(loadStates, state) {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid %s %q: use load, domcontentloaded or networkidle", flag, state)}
	}
	return nil
}

// navigateCmd 构造一个导航子命令:input 由动作名与位置参数组成,--wait 只在给出时转发。
func navigateCmd(use, short, long string, args cobra.PositionalArgs, urlArg bool) *cobra.Command {
	var wait string
	action, _, _ := strings.Cut(use, " ")
	cmd := &cobra.Command{
		Use:   use,
		Short: short,
		Long:  long + "\n" + waitStateHelp,
		Args:  args,
		RunE: func(cmd *cobra.Command, positional []string) error {
			input := map[string]any{"action": action}
			if urlArg {
				input["url"] = positional[0]
			}
			if cmd.Flags().Changed("wait") {
				if err := validLoadState(wait, "--wait"); err != nil {
					return err
				}
				input["wait"] = wait
			}
			return dispatchPage(cmd, "navigate", mustInput(input), printNavigateResult)
		},
	}
	cmd.Flags().StringVar(&wait, "wait", "load", "load state to wait for: load, domcontentloaded or networkidle")
	return cmd
}

func newPageGotoCmd() *cobra.Command {
	return navigateCmd("goto <url>", "Navigate the tab to a URL and wait for it to load",
		"Navigate the tab to a URL and wait for the page to reach the requested load state.",
		func(_ *cobra.Command, args []string) error {
			if len(args) != 1 {
				return &ExitError{Code: exitError, Message: "page goto takes exactly one argument: the URL"}
			}
			return nil
		}, true)
}

func noArgs(action string) cobra.PositionalArgs {
	return func(_ *cobra.Command, args []string) error {
		if len(args) != 0 {
			return &ExitError{Code: exitError, Message: fmt.Sprintf("page %s takes no arguments", action)}
		}
		return nil
	}
}

func newPageBackCmd() *cobra.Command {
	return navigateCmd("back", "Go back one entry in the tab's history",
		"Go back one entry in the tab's history. With no entry to go back to it fails with NOT_FOUND.", noArgs("back"), false)
}

func newPageForwardCmd() *cobra.Command {
	return navigateCmd("forward", "Go forward one entry in the tab's history",
		"Go forward one entry in the tab's history. With no entry to go forward to it fails with NOT_FOUND.", noArgs("forward"), false)
}

func newPageReloadCmd() *cobra.Command {
	return navigateCmd("reload", "Reload the tab's page", "Reload the tab's page and wait for it to load.", noArgs("reload"), false)
}

// printNavigateResult 打印导航结果:-o json 时是完整结果,否则一行摘要 tabId、URL 与 HTTP 状态。
// URL 由网页控制,经 terminalSafe 转义。
func printNavigateResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		TabID      int    `json:"tabId"`
		URL        string `json:"url"`
		Navigated  bool   `json:"navigated"`
		HTTPStatus *int   `json:"httpStatus"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	verb := "at"
	if payload.Navigated {
		verb = "navigated to"
	}
	line := fmt.Sprintf("tab %d %s %s", payload.TabID, verb, terminalSafe(payload.URL))
	if payload.HTTPStatus != nil {
		line += fmt.Sprintf(" (HTTP %d)", *payload.HTTPStatus)
	}
	fmt.Fprintln(os.Stdout, line)
	return nil
}

func newPageWaitCmd() *cobra.Command {
	var text, gone, selector, selectorGone, urlPart, load string
	cmd := &cobra.Command{
		Use:   "wait (--text T | --gone T | --selector S | --selector-gone S | --url P | --load STATE)",
		Short: "Wait until a condition on the page holds",
		Long: "Wait until exactly one condition holds, polling the page; the default timeout is 10s (--timeout).\n" +
			"--text waits for the text to be visible; --gone for the text to disappear (removed or hidden);\n" +
			"--selector for an element matching the CSS selector to be visible; --selector-gone for no visible element to\n" +
			"match it; --url for the tab's URL to contain the substring; --load for the load state\n" +
			"(load, domcontentloaded or networkidle). Text and selectors are matched in the main document only.\n" +
			"On timeout the error names the condition.",
		Args: noArgs("wait"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			conditions := map[string]struct {
				field, value string
			}{
				"text": {"text", text}, "gone": {"gone", gone}, "selector": {"selector", selector},
				"selector-gone": {"selectorGone", selectorGone}, "url": {"url", urlPart}, "load": {"load", load},
			}
			input := map[string]any{}
			for flag, c := range conditions {
				if cmd.Flags().Changed(flag) {
					input[c.field] = c.value
				}
			}
			if len(input) != 1 {
				return &ExitError{Code: exitError, Message: "page wait takes exactly one of --text, --gone, --selector, --selector-gone, --url, --load"}
			}
			if cmd.Flags().Changed("load") {
				if err := validLoadState(load, "--load"); err != nil {
					return err
				}
			}
			return dispatchPage(cmd, "wait", mustInput(input), printWaitResult)
		},
	}
	f := cmd.Flags()
	f.StringVar(&text, "text", "", "wait for this text to be visible")
	f.StringVar(&gone, "gone", "", "wait for this text to disappear")
	f.StringVar(&selector, "selector", "", "wait for an element matching this CSS selector to be visible")
	f.StringVar(&selectorGone, "selector-gone", "", "wait until no visible element matches this CSS selector")
	f.StringVar(&urlPart, "url", "", "wait for the tab's URL to contain this substring")
	f.StringVar(&load, "load", "", "wait for a load state: load, domcontentloaded or networkidle")
	return cmd
}

func printWaitResult(result json.RawMessage) error {
	if outputFormat == outputJSON {
		return printResultJSON(result)
	}
	var payload struct {
		TabID int    `json:"tabId"`
		URL   string `json:"url"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		return printResultJSON(result)
	}
	fmt.Fprintf(os.Stdout, "tab %d at %s\n", payload.TabID, terminalSafe(payload.URL))
	return nil
}
