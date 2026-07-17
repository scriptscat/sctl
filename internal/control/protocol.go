// Package control 定义 sctl 前端(sctl mcp / CLI 动词)与常驻 daemon(sctl serve)之间的
// 「本机内部连接」:一套跑在 daemon loopback listener 上、与扩展 WS 面同端口但独立路径的
// HTTP/JSON 控制 API。它不是桥接协议(见仓库根 PROTOCOL.md)的一部分——桥接协议只约定
// 扩展 ↔ daemon 那条 WS 连接;控制 API 是同一二进制内部前端 → daemon 的私有通道。
//
// 信任模型(设计文档 §3.1 / §4):
//   - 控制 API 的唯一传输闸门是「控制令牌」——daemon 绑定端口后写入 0600 文件、只有同用户
//     进程能读。网页可以 new WebSocket / fetch 到该端口,但读不到令牌即被拒(防小人不防君子)。
//   - 携带控制令牌但不带 MCP 客户端令牌 = 内建 `sctl-cli` 身份,全量 scope、不走配对、不可撤销。
//   - 额外携带 MCP 客户端令牌 = 以该已配对客户端身份发起,受其 scope 限制、可被撤销。
//   - 无论哪种身份,写操作仍需过扩展侧人工审批(第二道闸门)。
package control

import "encoding/json"

// 控制 API 路径。健康检查故意不鉴权(仅暴露「端口开着」这一威胁模型已接受的信息)。
const (
	PathHealth     = "/control/health"
	PathCall       = "/control/call"
	PathPairClient = "/control/pair-client"
	PathWhoami     = "/control/whoami"
	PathPairExt    = "/control/pair-ext"
	PathStatus     = "/control/status"
)

// 控制 API 请求头。
const (
	// HeaderControlToken 携带 0600 控制令牌,证明调用方是同用户本机进程。所有非健康检查请求必带。
	HeaderControlToken = "X-Sctl-Control-Token"
	// HeaderClientToken 可选,携带某已配对 MCP 客户端令牌;带上即以该客户端身份(受限 scope)发起。
	HeaderClientToken = "X-Sctl-Client-Token"
)

// CLIClientID 是 CLI 动词使用的内建全量身份 clientId(桥接请求里回填,扩展侧特判为不可撤销全量)。
const CLIClientID = "sctl-cli"

// CallRequest 是 /control/call 的请求体:转发一次 bridge action 调用。
type CallRequest struct {
	Action string          `json:"action"`
	Input  json.RawMessage `json:"input"`
}

// CallResult 是 /control/call 的响应体,镜像桥接的 bridge.response(ok 二选一)。
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

// StatusResult 是 /control/status 的响应体:daemon 与扩展连接概览。
type StatusResult struct {
	DaemonVersion string `json:"daemonVersion"`
	ExtConnected  bool   `json:"extConnected"`
	ClientCount   int    `json:"clientCount"`
}

// WhoamiResult 是 /control/whoami 的响应体:解析 MCP 客户端令牌得到的授权信息。
type WhoamiResult struct {
	ClientID    string   `json:"clientId"`
	DisplayName string   `json:"displayName"`
	Scopes      []string `json:"scopes"`
}

// PairExtResult 是 /control/pair-ext 的响应体:打开扩展配对窗口后返回展示形配对码。
type PairExtResult struct {
	Code string `json:"code"`
}

// PairClientRequest 是 /control/pair-client 的请求体:发起一次 MCP 客户端配对。
type PairClientRequest struct {
	ClientName string   `json:"clientName"`
	Scopes     []string `json:"scopes"`
}

// PairClientEvent 是 /control/pair-client 的流式事件(换行分隔 JSON):
// 第一条只带 Code(供终端展示核对码),第二条带 Decision(扩展裁决 + 批准时的令牌)。
type PairClientEvent struct {
	Code     string           `json:"code,omitempty"`
	Decision *PairClientGrant `json:"decision,omitempty"`
}

// PairClientGrant 是配对裁决:批准时携带铸造出的 clientId/token 与实际授予的 scope。
type PairClientGrant struct {
	Approved bool     `json:"approved"`
	ClientID string   `json:"clientId,omitempty"`
	Token    string   `json:"token,omitempty"`
	Scopes   []string `json:"scopes,omitempty"`
}
