package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

const jsonRPCVersion = "2.0"

const (
	methodAuthenticate  = "$session.authenticate"
	methodAuthenticated = "$session.authenticated"
	methodHello         = "$session.hello"
	methodCapabilities  = "$session.capabilities"
	methodPing          = "$session.ping"
	methodShutdown      = "$session.shutdown"
	methodCancel        = "$/cancelRequest"
)

const (
	modeSession = "session"
	modePairing = "pairing"
)

const (
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeInternal         = "INTERNAL_ERROR"
	CodeRateLimited      = "RATE_LIMITED"
	CodeOperationExpired = "OPERATION_EXPIRED"
	CodeConflict         = "CONFLICT"
)

// rpcApplicationError 是应用层失败的 JSON-RPC 错误码,领域错误码放在 error.data.code(docs/protocol.md §4)。
const rpcApplicationError = -32000

// Message is one JSON-RPC 2.0 request, notification, success response, or error response.
type Message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      string          `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *RPCError       `json:"error,omitempty"`
}

type RPCError struct {
	Code    int        `json:"code"`
	Message string     `json:"message"`
	Data    *ErrorData `json:"data,omitempty"`
}

type ErrorData struct {
	Code        string `json:"code"`
	OperationID string `json:"operationId,omitempty"`
}

type authChallengeParams struct {
	NonceD string `json:"nonceD"`
}

type authResponseResult struct {
	Mode   string    `json:"mode"`
	NonceE string    `json:"nonceE"`
	HMAC   string    `json:"hmac"`
	Peer   *authPeer `json:"peer,omitempty"`
}

// peerKindBrowser 是浏览器实例在认证响应里声明的对端类型;不声明 peer 的连接是 ScriptCat。
const peerKindBrowser = string(protocol.PeerBrowser)

// authPeer 是浏览器实例在认证响应里声明的身份,参与握手 MAC 计算并决定选用哪把实例密钥。
type authPeer struct {
	Kind       string `json:"kind"`
	InstanceID string `json:"instanceId"`
}

type keyDelivery struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
}

type authenticatedParams struct {
	HMAC string       `json:"hmac"`
	Key  *keyDelivery `json:"key,omitempty"`
}

type helloParams struct {
	DaemonVersion string `json:"daemonVersion"`
}

type capabilitiesParams struct {
	SchemaVersion string            `json:"schemaVersion"`
	Methods       []string          `json:"methods"`
	Peer          *capabilitiesPeer `json:"peer,omitempty"`
}

// capabilitiesPeer 是浏览器实例在能力声明里给出的名称与自报信息;名称以 daemon 登记为准。
type capabilitiesPeer struct {
	Name             string `json:"name"`
	Product          string `json:"product,omitempty"`
	ProductVersion   string `json:"productVersion,omitempty"`
	ExtensionVersion string `json:"extensionVersion,omitempty"`
}

type cancelParams struct {
	ID string `json:"id"`
}

type businessParams struct {
	ClientID string          `json:"clientId,omitempty"`
	Input    json.RawMessage `json:"input"`
}

type Request struct {
	ClientID string          `json:"clientId"`
	Action   string          `json:"action"`
	Input    json.RawMessage `json:"input"`
}

type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

type Error struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	OperationID string `json:"operationId,omitempty"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

func newRequest(id, method string, params any) (Message, error) {
	raw, err := json.Marshal(params)
	if err != nil {
		return Message{}, fmt.Errorf("marshal %s params: %w", method, err)
	}
	return Message{JSONRPC: jsonRPCVersion, ID: id, Method: method, Params: raw}, nil
}

func newNotification(method string, params any) (Message, error) {
	message, err := newRequest("", method, params)
	return message, err
}

func newError(id, code, message string) Message {
	return Message{JSONRPC: jsonRPCVersion, ID: id, Error: &RPCError{Code: rpcApplicationError, Message: message, Data: &ErrorData{Code: code}}}
}

func newResult(id string, result any) (Message, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Message{}, fmt.Errorf("marshal result: %w", err)
	}
	return Message{JSONRPC: jsonRPCVersion, ID: id, Result: raw}, nil
}
