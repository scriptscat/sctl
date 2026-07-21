package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

// newScriptsCmd 构造 `sctl scripts` 及其只读子命令 list / info / source。
func newScriptsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "scripts",
		Short: "查看扩展中的用户脚本(list / info / source)",
	}
	cmd.AddCommand(newScriptsListCmd(), newScriptsInfoCmd(), newScriptsSourceCmd())
	return cmd
}

func newScriptsListCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "列出已安装脚本",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "scripts.list", mustInput(struct{}{}), func(result json.RawMessage) error {
				if jsonOutput {
					return printResultJSON(result)
				}
				return printScriptList(result)
			})
		},
	}
}

func newScriptsInfoCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "info <uuid>",
		Short: "查看单个脚本的元数据",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "scripts.metadata.get", mustInput(map[string]string{"uuid": args[0]}), func(result json.RawMessage) error {
				// 元数据结构由扩展定义,统一以美化 JSON 呈现(--json 与人读同源)。
				return printResultJSON(result)
			})
		},
	}
}

func newScriptsSourceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "source <uuid>",
		Short: "输出单个脚本的源码到 stdout(可重定向)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return dispatch(cmd, "scripts.source.get", mustInput(map[string]string{"uuid": args[0]}), func(result json.RawMessage) error {
				if jsonOutput {
					return printResultJSON(result)
				}
				return printScriptSource(result)
			})
		},
	}
}

// scriptSummary 是 scripts.list 结果元素的宽松视图:只取人读表格所需字段,其余忽略;
// --json 路径始终原样输出完整结果,不受此结构约束。
type scriptSummary struct {
	UUID    string `json:"uuid"`
	Name    string `json:"name"`
	Enabled bool   `json:"enabled"`
	Version string `json:"version"`
}

func printScriptList(result json.RawMessage) error {
	var payload struct {
		Scripts []scriptSummary `json:"scripts"`
	}
	if err := json.Unmarshal(result, &payload); err != nil {
		// 结构不符预期时退回原样 JSON,不吞信息。
		return printResultJSON(result)
	}
	if len(payload.Scripts) == 0 {
		fmt.Fprintln(os.Stdout, "(无已安装脚本)")
		return nil
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "UUID\tNAME\tENABLED\tVERSION")
	for _, s := range payload.Scripts {
		fmt.Fprintf(tw, "%s\t%s\t%v\t%s\n", s.UUID, s.Name, s.Enabled, s.Version)
	}
	return tw.Flush()
}

func printScriptSource(result json.RawMessage) error {
	var payload struct {
		Code string `json:"code"`
	}
	if err := json.Unmarshal(result, &payload); err == nil && payload.Code != "" {
		// 源码原样输出(不加尾换行,忠实可重定向为 .user.js)。
		_, err := os.Stdout.WriteString(payload.Code)
		return err
	}
	// 无 code 字段时退回原样 JSON。
	return printResultJSON(result)
}
