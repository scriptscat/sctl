// Package page 是 daemon 内的页面自动化组件:把 `sctl page …` 与 page_* MCP 工具的一次动作翻译成
// 发往浏览器实例的 CDP 命令,并在 daemon 内存里保存跨命令的页面状态(调试器附加、空闲断开计时、快照的元素引用表)。
// 状态放在 daemon 而不是前端,因为只有 daemon 活得比单条命令长(docs/architecture.md)。
//
// 它只经窄接口 CDP 与浏览器交互:生产环境由 NewBridgeCDP 经 bridge 转发给 sctl Browser 扩展,
// 单元测试用假 CDP 替身。
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
	if cut, truncated := cutRunes(s, n); truncated {
		return cut + suffix
	}
	return s
}

// cutRunes 把 s 截到至多 n 个字符,返回是否截断。不切开多字节字符,也不为了计数复制整个字符串。
func cutRunes(s string, n int) (string, bool) {
	if len(s) <= n {
		return s, false
	}
	count := 0
	for i := range s {
		if count == n {
			return s[:i], true
		}
		count++
	}
	return s, false
}

// Command 是发往一个标签页的一条 CDP 命令;SessionID 非空时发往该标签页下的子会话(跨进程 iframe)。
type Command struct {
	TabID     int
	SessionID string
	Method    string
	Params    json.RawMessage
}

// BodyPart 选择取回请求体还是响应体。
type BodyPart string

const (
	BodyPartRequest  BodyPart = "request"
	BodyPartResponse BodyPart = "response"
)

// BodyQuery 是向扩展取一个请求的请求体或响应体;SessionID 非空时向标签页下的子会话(跨进程 iframe)取。
type BodyQuery struct {
	TabID     int
	SessionID string
	RequestID string
	Part      BodyPart
}

// Chrome 不再保留一个体的原因(protocol.json 的 DebuggerBodyResult.unavailable)。
const (
	// BodyNavigated:页面已导航离开,之前所有请求的体都被丢弃。
	BodyNavigated = "navigated"
	// BodyNoData:请求进行中、失败、没有体,或页面没有读取 fetch 的响应体。
	BodyNoData = "noData"
	// BodyEvicted:超出 Chrome 的缓冲(单个资源约 20 MB),或被后来的响应挤出。
	BodyEvicted = "evicted"
	// BodyNoPostData:请求没有请求体。
	BodyNoPostData = "noPostData"
)

// Body 是扩展取回的体,已在浏览器里截断到 1 MiB。Base64 表示 Text 是 base64 编码的二进制;Size 是截断前的字节数。
// Unavailable 非空时 Chrome 已不再保留它,其余字段为零值。
type Body struct {
	Text        string
	Base64      bool
	Size        int
	Truncated   bool
	Unavailable string
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
	// Record 让扩展对一个标签页开始(on 为 true)或停止录制:录制期间扩展不做兜底空闲断开,停止后重新计时。
	// 开始录制要求标签页已附加,否则返回 DEBUGGER_DETACHED;停止一个未附加的标签页什么都不做。
	Record(ctx context.Context, instanceID string, tabID int, on bool) error
	// Body 让扩展取回一个请求的请求体或响应体,截断到 1 MiB 后回传。Chrome 不再保留它时返回带 Unavailable 的 Body
	// 而不是错误;标签页未附加时返回 DEBUGGER_DETACHED,不附加它。
	Body(ctx context.Context, instanceID string, q BodyQuery) (Body, error)
}
