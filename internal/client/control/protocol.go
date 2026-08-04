// Package control 定义 sctl 前端(sctl mcp / CLI 动词)与常驻 daemon(sctl serve)之间的
// 「本机内部连接」:一套跑在 daemon loopback listener 上、与扩展 WS 面同端口但独立路径的
// HTTP/JSON 控制 API。它不是桥接协议(见 docs/protocol.md)的一部分——桥接协议只约定
// 扩展 ↔ daemon 那条 WS 连接;控制 API 是同一二进制内部前端 → daemon 的私有通道。
//
// 信任模型(docs/threat-model.md §1 / §4)——扁平信任:
//   - 控制 API 的唯一传输闸门是「控制令牌」——daemon 绑定端口后写入 0600 文件、只有同用户
//     进程能读。网页可以 new WebSocket / fetch 到该端口,但读不到令牌即被拒(防小人不防君子)。
//   - 带上控制令牌即拥有全部能力:CLI 与所有 MCP agent 经已接入的可信通道继承信任,不再逐客户端
//     配对 / 铸令牌 / 撤销。可选的客户端标签仅用于审计归因,不构成授权。
//   - 无论哪种调用方,写操作仍需过扩展侧人工审批、源码读取仍需过披露闸门(第二道闸门)。
package control

import (
	"encoding/json"

	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// 控制 API 路径。健康检查故意不鉴权(仅暴露「端口开着」这一威胁模型已接受的信息)。
const (
	PathHealth = "/control/health"
	PathCall   = "/control/call"
	PathEnroll = "/control/enroll"
	PathStatus = "/control/status"
)

// 控制 API 请求头。
const (
	// HeaderControlToken 携带 0600 控制令牌,证明调用方是同用户本机进程。所有非健康检查请求必带。
	HeaderControlToken = "X-Sctl-Control-Token"
	// HeaderClientLabel 可选,携带调用方自报的客户端标签(如 MCP 客户端名)。仅用于审计归因,
	// 不构成授权;缺省即内建 CLI 身份标签。自报、未认证、可伪造——只入审计,不上审批界面。
	HeaderClientLabel = "X-Sctl-Client"
)

// CLIClientID 是缺省客户端标签(JSON-RPC params.clientId 回填,仅供扩展侧审计展示)。
const CLIClientID = "sctl-cli"

// CallRequest 是 /control/call 的请求体:转发一次 bridge action 调用。
type CallRequest struct {
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input"`
}

// CallResult 是 /control/call 的响应体,映射桥接的 JSON-RPC result/error。
type CallResult struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *CallError      `json:"error,omitempty"`
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

// StatusResult 是 /control/status 的响应体:daemon 与扩展连接概览,附守卫侧安全事件。
type StatusResult struct {
	DaemonVersion string        `json:"daemonVersion"`
	ExtConnected  bool          `json:"extConnected"`
	SecurityCount int           `json:"securityCount"`
	Security      []audit.Event `json:"security,omitempty"`
}

// EnrollResult 是 /control/enroll 的响应体:打开接入窗口后返回展示形配对码。
type EnrollResult struct {
	Code string `json:"code"`
}
