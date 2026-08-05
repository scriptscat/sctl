package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"github.com/spf13/cobra"
)

// newGrepCmd 构造 `sctl grep`:在单个脚本的源码里逐行检索。默认是纯字面量子串匹配,-E 才切到正则
// (沿用 grep -E 的记忆)。返回的是源码内容,因此与 `get -o source` 走同一道披露闸门。
func newGrepCmd() *cobra.Command {
	var (
		regexMode  bool
		ignoreCase bool
		contextN   int
		maxMatches int
	)
	cmd := &cobra.Command{
		Use:   "grep [scripts|script|sc] <uuid> <query>",
		Short: "Search one script's source and print matching lines with line numbers",
		Long: "Search one script's source and print matching lines with line numbers.\n\n" +
			"The query is a literal substring by default — characters like * ? . and | match\n" +
			"themselves; pass -E to compile it as a regular expression instead.\n" +
			"Matching lines carry source content, so the first search may need the same source\n" +
			"disclosure approval in the browser as \"get <uuid> -o source\".\n" +
			"No match is not an error: the exit code stays 0 and stdout is empty.",
		Args: func(cmd *cobra.Command, args []string) error {
			if n := len(stripResourceWord(args)); n != 2 {
				return &ExitError{
					Code:    exitError,
					Message: fmt.Sprintf("grep requires a uuid and a query (after an optional resource word), got %d", n),
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			args = stripResourceWord(args)
			input := map[string]any{"uuid": args[0], "query": args[1]}
			if regexMode {
				input["mode"] = "regex"
			}
			if ignoreCase {
				input["ignoreCase"] = true
			}
			// 只在用户真给了标志时下发,缺省值由扩展侧持有——两侧各存一份默认值迟早会漂。
			if cmd.Flags().Changed("context") {
				input["contextLines"] = contextN
			}
			if cmd.Flags().Changed("max-count") {
				input["maxMatches"] = maxMatches
			}
			return dispatch(cmd, "scripts.source.grep", mustInput(input), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printGrepResult(result)
			})
		},
	}
	cmd.Flags().BoolVarP(&regexMode, "extended-regexp", "E", false, "treat the query as a regular expression instead of a literal substring")
	cmd.Flags().BoolVarP(&ignoreCase, "ignore-case", "i", false, "match case-insensitively")
	cmd.Flags().IntVarP(&contextN, "context", "C", 0, "print N lines of context around each match")
	cmd.Flags().IntVarP(&maxMatches, "max-count", "m", 0, "stop after N matches")
	return cmd
}

// grepMatch / grepResult 是 scripts.source.grep 结果的宽松视图:只取人读输出所需字段。-o json
// 路径始终原样输出完整结果,不受此结构约束。
type grepMatch struct {
	LineNumber int      `json:"lineNumber"`
	Line       string   `json:"line"`
	Before     []string `json:"before"`
	After      []string `json:"after"`
}

type grepResult struct {
	Matches          []grepMatch `json:"matches"`
	TotalMatches     int         `json:"totalMatches"`
	Truncated        bool        `json:"truncated"`
	SkippedLongLines int         `json:"skippedLongLines"`
}

func printGrepResult(result json.RawMessage) error {
	var r grepResult
	if err := json.Unmarshal(result, &r); err != nil {
		// 结构不符预期时退回原样 JSON,不吞信息。
		return printResultJSON(result)
	}
	if err := writeGrepMatches(os.Stdout, r.Matches); err != nil {
		return err
	}
	// 提示走 stderr:stdout 全是命中行,混进提示会污染 `grep … | cut -d: -f1` 这类下游。
	if r.Truncated {
		fmt.Fprintf(os.Stderr, "truncated: showing %d of %d matches (raise -m to see more)\n", len(r.Matches), r.TotalMatches)
	}
	if r.SkippedLongLines > 0 {
		fmt.Fprintf(os.Stderr, "skipped %d line(s) too long to search with a regular expression\n", r.SkippedLongLines)
	}
	return nil
}

// writeGrepMatches 照 grep -n 的体例渲染:命中行 <行号>:<内容>,上下文行 <行号>-<内容>,块间以 --
// 分隔。上下文行不带自己的行号,由命中行号与数组长度推出——扩展侧给的是紧邻命中的连续行。
func writeGrepMatches(w io.Writer, matches []grepMatch) error {
	for i, m := range matches {
		if i > 0 && (len(m.Before) > 0 || len(matches[i-1].After) > 0) {
			if _, err := fmt.Fprintln(w, "--"); err != nil {
				return err
			}
		}
		for j, line := range m.Before {
			if _, err := fmt.Fprintf(w, "%d-%s\n", m.LineNumber-len(m.Before)+j, line); err != nil {
				return err
			}
		}
		if _, err := fmt.Fprintf(w, "%d:%s\n", m.LineNumber, m.Line); err != nil {
			return err
		}
		for j, line := range m.After {
			if _, err := fmt.Fprintf(w, "%d-%s\n", m.LineNumber+1+j, line); err != nil {
				return err
			}
		}
	}
	return nil
}
