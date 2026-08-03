package cli

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// newStatusCmd 查询 daemon 与扩展连接状态。不自动拉起 daemon:未运行时如实报告。
func newStatusCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "status",
		Short: "查看 daemon 与扩展连接状态",
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			client, err := control.Connect(ctx)
			if err != nil {
				if outputFormat == outputJSON {
					return printValueJSON(control.StatusResult{})
				}
				fmt.Fprintln(os.Stdout, "daemon 未运行")
				return nil
			}
			st, err := client.Status(ctx)
			if err != nil {
				return err
			}
			if outputFormat == outputJSON {
				return printValueJSON(st)
			}
			fmt.Fprintf(os.Stdout, "daemon 版本: %s\n扩展已连接: %v\n", st.DaemonVersion, st.ExtConnected)
			// 人读输出只给一行摘要,完整事件走 --json,避免刷屏淹没状态本身。
			if summary := formatSecuritySummary(st.Security); summary != "" {
				fmt.Fprintf(os.Stdout, "近期安全事件: %s\n", summary)
			}
			return nil
		},
	}
}

// formatSecuritySummary 把安全事件聚合成一行「类型×次数」摘要,无事件时返回空串。
func formatSecuritySummary(events []audit.Event) string {
	counts := audit.Summarize(events)
	if len(counts) == 0 {
		return ""
	}
	parts := make([]string, 0, len(counts))
	for _, c := range counts {
		parts = append(parts, fmt.Sprintf("%s×%d", c.Type, c.Count))
	}
	return fmt.Sprintf("%d 条(%s)", len(events), strings.Join(parts, ", "))
}
