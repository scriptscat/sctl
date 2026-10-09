// Package control 定义 sctl 前端(sctl mcp / CLI 动词)与常驻 daemon(sctl serve)之间的
// 「控制连接」:一套跑在 daemon listener 上、与扩展 WS 面同端口但独立路径的
// HTTP/JSON 控制 API。它不是桥接协议(见 docs/protocol.md)的一部分——桥接协议只约定
// 扩展 ↔ daemon 那条 WS 连接;控制 API 是同一二进制内部前端 → daemon 的私有通道。
//
// 信任模型见 docs/threat-model.md——扁平信任:
//   - 控制 API 的唯一传输闸门是「控制令牌」——daemon 绑定端口后写入受限文件。网页可以
//     new WebSocket / fetch 到该端口,但读不到令牌即被拒；非默认监听地址由操作者承担传输风险。
//   - 带上控制令牌即拥有全部能力:CLI 与所有 MCP agent 经已接入的可信通道继承信任,不再逐客户端
//     配对 / 铸令牌 / 撤销。可选的客户端标签仅用于审计归因,不构成授权。
//   - 无论哪种调用方,写操作仍需过扩展侧人工审批、源码读取仍需过披露闸门(第二道闸门)。
package control

import (
	"encoding/json"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// 控制 API 路径。健康检查故意不鉴权(仅暴露「端口开着」这一威胁模型已接受的信息)。
const (
	PathHealth        = "/control/health"
	PathCall          = "/control/call"
	PathEnroll        = "/control/enroll"
	PathStatus        = "/control/status"
	PathBrowsers      = "/control/browsers"
	PathBrowserForget = "/control/browsers/forget"
	PathPage          = "/control/page"
	// 原始 CDP 端点:创建或查看、只查看、关闭。请求体都是 CDPRequest。
	PathCDPEndpoint = "/control/cdp/endpoint"
	PathCDPStatus   = "/control/cdp/status"
	PathCDPClose    = "/control/cdp/close"
)

// 控制 API 请求头。
const (
	// HeaderControlToken 携带控制令牌。所有非健康检查请求必带。
	HeaderControlToken = "X-Sctl-Control-Token"
	// HeaderClientLabel 可选,携带调用方自报的客户端标签(如 MCP 客户端名)。仅用于审计归因,
	// 不构成授权;缺省即内建 CLI 身份标签。自报、未认证、可伪造——只入审计,不上审批界面。
	HeaderClientLabel = "X-Sctl-Client"
)

// CLIClientID 是缺省客户端标签(JSON-RPC params.clientId 回填,仅供扩展侧审计展示)。
const CLIClientID = "sctl-cli"

// CallRequest 是 /control/call 的请求体:转发一次 bridge action 调用。
type CallRequest struct {
	Action string `json:"action"`
	// Browser 是浏览器方法的可选目标:实例名称或实例 ID 前缀,由 daemon 解析(docs/protocol.md §3.1)。
	Browser string          `json:"browser,omitempty"`
	Input   json.RawMessage `json:"input"`
	// ReportPending 请 daemon 在对端报告请求进入人工审批时先写出一行 CallResult{Pending: true},
	// 结论随后另起一行。不带时响应体只有结论一行。
	ReportPending bool `json:"reportPending,omitempty"`
}

// PageRequest 是 /control/page 的请求体:在一个浏览器标签页上执行一次页面动作,响应体是 CallResult。
// TabID 为 nil 时由 daemon 在命令开始时取该浏览器最后获得焦点窗口的激活标签页;Activate 先让目标成为
// 窗口内的激活标签页(不聚焦窗口);TimeoutMs 为 0 时使用动作的默认超时。Input 是动作自己的参数。
type PageRequest struct {
	Action    string          `json:"action"`
	Browser   string          `json:"browser,omitempty"`
	TabID     *int            `json:"tabId,omitempty"`
	Activate  bool            `json:"activate,omitempty"`
	TimeoutMs int             `json:"timeoutMs,omitempty"`
	Input     json.RawMessage `json:"input,omitempty"`
}

// CallResult 是 /control/call 的响应体,映射桥接的 JSON-RPC result/error。
// 列表类浏览器方法未指定目标且多个浏览器在线时,Result 是各实例结果按 mergeField 拼接后的汇总,
// 该数组的每一项多一个 "browser": {"id","name"} 字段;其余情况 Result 是单个对端的原始结果。
type CallResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *CallError      `json:"error,omitempty"`
	// Pending 只出现在 ReportPending 请求的中间行上:请求已进入浏览器里的人工审批,这一行不是结论。
	Pending bool `json:"pending,omitempty"`
}

// CallError 是失败调用的结构化错误(code 取桥接 protocol.json errorCodes)。
type CallError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *CallError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// HealthResult 是 /control/health 的响应体(无鉴权)。
type HealthResult struct {
	OK      bool   `json:"ok"`
	Version string `json:"version"`
}

// StatusResult 是 /control/status 的响应体:daemon 与扩展连接概览,附守卫侧安全事件与已配对浏览器实例。
type StatusResult struct {
	DaemonVersion string        `json:"daemonVersion"`
	ExtConnected  bool          `json:"extConnected"`
	SecurityCount int           `json:"securityCount"`
	Security      []audit.Event `json:"security,omitempty"`
	Browsers      []BrowserInfo `json:"browsers,omitempty"`
}

// EnrollResult 是 /control/enroll 的响应体:打开接入窗口后返回展示形配对码。
type EnrollResult struct {
	Code string `json:"code"`
}

// BrowserInfo 是一个已配对浏览器实例的当前视图,镜像 bridge.InstanceInfo(控制 API 不直接暴露
// bridge 类型,见 internal/daemon/controlapi 的窄接口约定)。离线实例的 ConnectedAt 为零值,JSON 中省略。
type BrowserInfo struct {
	ID               string    `json:"id"`
	Name             string    `json:"name"`
	Online           bool      `json:"online"`
	Product          string    `json:"product,omitempty"`
	ProductVersion   string    `json:"productVersion,omitempty"`
	ExtensionVersion string    `json:"extensionVersion,omitempty"`
	ConnectedAt      time.Time `json:"connectedAt,omitzero"`
}

// BrowsersResult 是 /control/browsers 的响应体:全部已配对浏览器实例(在线与离线)。
type BrowsersResult struct {
	Browsers []BrowserInfo `json:"browsers"`
}

// ForgetBrowserRequest 是 /control/browsers/forget 的请求体:Ref 是实例名称或完整实例 ID。
type ForgetBrowserRequest struct {
	Ref string `json:"ref"`
}

// CDPRequest 是 /control/cdp/* 的请求体:Browser 是目标浏览器(名称或实例 ID 前缀),空串由 daemon 按在线实例选择。
type CDPRequest struct {
	Browser string `json:"browser,omitempty"`
}

// CDPBrowserRef 标识端点所属的浏览器实例。
type CDPBrowserRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// CDPEndpoint 是一个原始 CDP 端点的当前视图。地址里带着密钥,本身就是凭据。ExpiresAt 在客户端连着时省略:
// 60 分钟的失效计时从客户端断开时才开始。
type CDPEndpoint struct {
	// HTTPURL 给 Playwright chromium.connectOverCDP。
	HTTPURL string `json:"httpUrl"`
	// WSURL 给 Puppeteer connect({browserWSEndpoint})。
	WSURL           string    `json:"wsUrl"`
	ClientConnected bool      `json:"clientConnected"`
	ConnectedAt     time.Time `json:"connectedAt,omitzero"`
	ExpiresAt       time.Time `json:"expiresAt,omitzero"`
}

// CDPEndpointResult 是 /control/cdp/endpoint 与 /control/cdp/status 的结果(CallResult.Result):
// Endpoint 为 null 表示这个浏览器没有端点(只有 status 会这样回答)。
type CDPEndpointResult struct {
	Browser  CDPBrowserRef `json:"browser"`
	Endpoint *CDPEndpoint  `json:"endpoint"`
}

// CDPCloseResult 是 /control/cdp/close 的结果:Closed 为 false 表示本来就没有端点。
type CDPCloseResult struct {
	Browser CDPBrowserRef `json:"browser"`
	Closed  bool          `json:"closed"`
}
