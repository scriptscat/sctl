package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newGetCmd 构造 `sctl get`:不带 uuid 打印已安装脚本表格;带 uuid 默认打印单行表格。
// -o json 输出完整元数据;-o source 输出源码到 stdout,且仅在带 uuid 时合法。
func newGetCmd() *cobra.Command {
	var lines string
	cmd := &cobra.Command{
		Use:   "get [scripts|script|sc] [<uuid>]",
		Short: "List installed scripts, or show one script (-o json for full metadata, -o source for raw code)",
		Long: "List installed scripts, show one script's metadata, or print its raw source with -o source.\n\n" +
			"For large scripts, use \"sctl grep <uuid> <query>\" to locate relevant code, then read\n" +
			"a line window instead of printing the whole source:\n\n" +
			"  sctl get <uuid> -o source --lines 100-250\n\n" +
			"Without --lines, -o source writes the complete source to stdout and can be redirected:\n\n" +
			"  sctl get <uuid> -o source > script.user.js",
		Args: func(cmd *cobra.Command, args []string) error {
			if n := len(stripResourceWord(args)); n > 1 {
				return &ExitError{
					Code:    exitError,
					Message: fmt.Sprintf("get accepts at most one uuid (after an optional resource word), got %d", n),
				}
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			args = stripResourceWord(args)
			startLine, endLine, hasLines, err := parseLinesFlag(lines)
			if err != nil {
				return &ExitError{Code: exitError, Message: err.Error()}
			}
			if hasLines && outputFormat != outputSource {
				return &ExitError{Code: exitError, Message: "--lines is only valid together with -o source"}
			}
			if len(args) == 0 {
				return dispatch(cmd, "scripts.list", mustInput(struct{}{}), func(result json.RawMessage) error {
					if outputFormat == outputJSON {
						return printResultJSON(result)
					}
					return printScriptListTable(result)
				})
			}
			uuid := args[0]
			if outputFormat == outputSource {
				input := map[string]any{"uuid": uuid}
				if hasLines {
					input["startLine"] = startLine
					input["endLine"] = endLine
				}
				return dispatch(cmd, "scripts.source.get", mustInput(input), printScriptSource)
			}
			return dispatch(cmd, "scripts.metadata.get", mustInput(map[string]string{"uuid": uuid}), func(result json.RawMessage) error {
				if outputFormat == outputJSON {
					return printResultJSON(result)
				}
				return printScriptRowTable(result)
			})
		},
	}
	cmd.Flags().StringVar(&lines, "lines", "", `read the inclusive 1-based line window "A-B" (only with -o source)`)
	return cmd
}

// parseLinesFlag 解析 --lines "A-B"(1-based 闭区间)。空值表示未传该标志(ok=false)。
func parseLinesFlag(v string) (start, end int, ok bool, err error) {
	if v == "" {
		return 0, 0, false, nil
	}
	a, b, found := strings.Cut(v, "-")
	if !found {
		return 0, 0, false, fmt.Errorf("--lines must be \"A-B\", got %q", v)
	}
	start, errA := strconv.Atoi(a)
	end, errB := strconv.Atoi(b)
	if errA != nil || errB != nil || start < 1 || end < start {
		return 0, 0, false, fmt.Errorf("--lines must be \"A-B\" with A>=1 and B>=A, got %q", v)
	}
	return start, end, true, nil
}

// scriptSummary 只承载人读表格所需字段;-o json 直接输出完整结果,不受此结构约束。
type scriptSummary struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Version string `json:"version"`
}

func printScriptListTable(result json.RawMessage) error {
	var payload struct {
		Scripts []scriptSummary `json:"scripts"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		// 结构不符预期时退回原样 JSON,不吞信息。
		return printResultJSON(result)
	}
	return printScriptTable(payload.Scripts)
}

func printScriptRowTable(result json.RawMessage) error {
	var s scriptSummary
	if err := json.Unmarshal(result, &s); err != nil {
		return printResultJSON(result)
	}
	return printScriptTable([]scriptSummary{s})
}

func printScriptTable(items []scriptSummary) error {
	if len(items) == 0 {
		fmt.Fprintln(os.Stdout, "(no scripts installed)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "UUID\tNAME\tENABLED\tVERSION")
	for _, s := range items {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", s.UUID, s.Name, s.Enabled, s.Version)
	}
	return tw.Flush()
}

func printScriptSource(result json.RawMessage) error {
	var payload struct {
		// 指针以区分「没有 code 字段」与「code 是空串」——空行窗(--lines 开在空行上)返回的正是
		// 空串,把它当成缺字段会把结果信封写进重定向出来的 .user.js。
		Code *string `json:"code"`
	}
	if err := json.Unmarshal(result, &payload); err == nil && payload.Code != nil {
		// 源码原样输出(不加尾换行,忠实可重定向为 .user.js)。
		_, err := os.Stdout.WriteString(*payload.Code)
		return err
	}
	// 无 code 字段时退回原样 JSON。
	return printResultJSON(result)
}
