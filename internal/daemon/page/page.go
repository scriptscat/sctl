// Package page 是 daemon 内的页面自动化组件:把 `sctl page …` 与 page_* MCP 工具的一次动作翻译成
// 发往浏览器实例的 CDP 命令,并在 daemon 内存里保存跨命令的页面状态(调试器附加、空闲断开计时、快照的元素引用表)。
// 状态放在 daemon 而不是前端,因为只有 daemon 活得比单条命令长(docs/architecture.md)。
//
// 它只经窄接口 CDP 与浏览器交互:生产环境由 NewBridgeCDP 经 bridge 转发给 sctl Browser 扩展,
// 集成测试由 pagetest.Chrome 直连 headless Chrome。
package page

import (
	"context"
	"encoding/json"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// Error 是页面命令的领域错误,Code 取 protocol.json 的 errorCodes,经控制 API 原样交给调用方。
type Error struct {
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func invalidRequest(message string) *Error {
	return &Error{Code: generated.ErrorCodeInvalidRequest, Message: message}
}

// truncateRunes 把网页控制、可以任意长的文字截到至多 n 个字符,截断时加上 suffix。按字符截,不切开多字节字符。
func truncateRunes(s string, n int, suffix string) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + suffix
}

// Command 是发往一个标签页的一条 CDP 命令;SessionID 非空时发往该标签页下的子会话(跨进程 iframe)。
type Command struct {
	TabID     int
	SessionID string
	Method    string
	Params    json.RawMessage
}

// CDP 是页面自动化对浏览器实例的全部依赖。失败以 *Error 报告领域错误;ctx 结束时返回 ctx 的错误。
type CDP interface {
	// ResolveBrowser 按目标选择规则(docs/protocol.md §3.1)把名称或实例 ID 前缀解析为在线实例 ID。
	ResolveBrowser(target string) (string, error)
	// CurrentTab 返回实例里最后获得焦点的普通窗口中的激活标签页,没有时返回 NOT_FOUND。
	CurrentTab(ctx context.Context, instanceID string) (int, error)
	// Tabs 返回实例里全部标签页的 ID;动作据此找出它打开的新标签页。
	Tabs(ctx context.Context, instanceID string) ([]int, error)
	// SelectTab 让标签页成为所在窗口的激活标签页,但不聚焦窗口;标签页不存在时返回 NOT_FOUND。
	SelectTab(ctx context.Context, instanceID string, tabID int) error
	// Send 执行一条 CDP 命令,标签页尚未附加时先附加;附加被拒返回 PAGE_NOT_AUTOMATABLE,
	// 标签页不存在返回 NOT_FOUND,命令执行中调试器分离返回 DEBUGGER_DETACHED。
	Send(ctx context.Context, instanceID string, cmd Command) (json.RawMessage, error)
	// Detach 断开一个标签页(tabID 非 nil)或实例上全部已附加标签页的调试器,返回实际断开的标签页。
	// 断开由调用方发起,不再产生 debugger.detached 通知。
	Detach(ctx context.Context, instanceID string, tabID *int) ([]int, error)
}
