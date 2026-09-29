package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// yesFlag 是 L1 命令的 --yes(docs/protocol.md §3 破坏级别)。
type yesFlag struct {
	yes bool
}

// addYesFlag 给一个 L1 命令挂 --yes。
func addYesFlag(cmd *cobra.Command) *yesFlag {
	f := &yesFlag{}
	cmd.Flags().BoolVar(&f.yes, "yes", false, "confirm this destructive operation; without it nothing runs")
	return f
}

// apply 在给了 --yes 时把 confirm: true 写进方法输入。没给时 CLI 不预先拦截,照常发出:确认由 daemon 统一检查,
// CLI、MCP 与直接调用 /control/call 走同一条路径,拒绝以 CONFIRMATION_REQUIRED 映射为退出码 3。
func (f *yesFlag) apply(input map[string]any) {
	if f.yes {
		input[protocol.ConfirmParam] = true
	}
}

// 列表类方法的条数上限(spec 设计决策 9):默认 100,最多 1000。
const (
	defaultListLimit = 100
	maxListLimit     = 1000
)

// limitFlag 是列表命令的 --limit。
type limitFlag struct {
	limit int
}

// addLimitFlag 给一个列表命令挂 --limit。
func addLimitFlag(cmd *cobra.Command) *limitFlag {
	f := &limitFlag{}
	cmd.Flags().IntVar(&f.limit, "limit", defaultListLimit, fmt.Sprintf("return at most this many items (1-%d)", maxListLimit))
	return f
}

// apply 校验 --limit 的范围并写进方法输入。命令行参数是不可信输入,越界时在发起调用前报错(退出码 3)。
func (f *limitFlag) apply(input map[string]any) error {
	if f.limit < 1 || f.limit > maxListLimit {
		return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid --limit %d: must be between 1 and %d", f.limit, maxListLimit)}
	}
	input["limit"] = f.limit
	return nil
}

// printHasMore 在列表结果还有未返回的条目时提示调大 --limit。提示写到 stderr:stdout 只承载结果本身。
func printHasMore(hasMore bool) {
	if hasMore {
		fmt.Fprintf(os.Stderr, "more items exist than shown; pass a larger --limit (at most %d) to see them\n", maxListLimit)
	}
}
