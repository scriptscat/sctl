package bridge

import (
	"encoding/json"
	"fmt"
)

// Envelope 是所有 WS 消息的统一信封(docs/protocol.md §2)。
type Envelope struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// 协议版本常量。
const protocolV = 1

// envelope 类型。扁平信任把 pair.*/client.* 从 protocol.json envelopeTypes 中移除后,下列即
// 全集:envelopeTypes.session(握手/存活/生命周期)+ envelopeTypes.bridge(能力 RPC)。未知
// 类型按前向兼容忽略(§2)。
const (
	typeAuthChallenge  = "auth.challenge"
	typeAuthResponse   = "auth.response"
	typeAuthOK         = "auth.ok"
	typeHello          = "hello"
	typeBridgeRequest  = "bridge.request"
	typeBridgeResponse = "bridge.response"
	typeBridgeCancel   = "bridge.cancel"
	typePing           = "ping"
	typePong           = "pong"
	typeBridgeShutdown = "bridge.shutdown"
)

// 握手模式标识(auth.response.mode)。
const (
	modeSession = "session"
	modePairing = "pairing"
)

// 常用错误码(全集见 protocol.json errorCodes;此处仅列 daemon 侧会主动产生的)。
const (
	CodeInvalidRequest   = "INVALID_REQUEST"
	CodeInternal         = "INTERNAL_ERROR"
	CodeRateLimited      = "RATE_LIMITED"
	CodeOperationExpired = "OPERATION_EXPIRED"
)

// --- payload 结构 ---

type authChallengePayload struct {
	NonceD string `json:"nonceD"`
}

type authResponsePayload struct {
	Mode   string `json:"mode"`
	NonceE string `json:"nonceE"`
	HMAC   string `json:"hmac"`
}

type keyDelivery struct {
	Ciphertext string `json:"ciphertext"`
	IV         string `json:"iv"`
}

type authOKPayload struct {
	HMAC string       `json:"hmac"`
	Key  *keyDelivery `json:"key,omitempty"`
}

type helloPayload struct {
	DaemonVersion   string `json:"daemonVersion"`
	ProtocolVersion int    `json:"protocolVersion"`
}

// Request 是转发给扩展执行的一次 action 调用(docs/protocol.md §4)。
type Request struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ClientID        string          `json:"clientId"`
	Action          string          `json:"action"`
	Input           json.RawMessage `json:"input"`
}

// Response 是扩展对 bridge.request 的应答(ok 二选一)。
type Response struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error 是失败应答里的结构化错误。
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

// newEnvelope 组装一条信封,payload 为 nil 时省略该字段。
func newEnvelope(typ, requestID string, payload any) (Envelope, error) {
	env := Envelope{V: protocolV, Type: typ, RequestID: requestID}
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return Envelope{}, fmt.Errorf("序列化 %s payload: %w", typ, err)
		}
		env.Payload = raw
	}
	return env, nil
}
