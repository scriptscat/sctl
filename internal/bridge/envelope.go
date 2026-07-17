package bridge

import (
	"encoding/json"
	"fmt"

	"github.com/scriptscat/sctl/internal/auth"
)

// Envelope 是所有 WS 消息的统一信封(PROTOCOL §2)。
type Envelope struct {
	V         int             `json:"v"`
	Type      string          `json:"type"`
	RequestID string          `json:"requestId,omitempty"`
	Payload   json.RawMessage `json:"payload,omitempty"`
}

// 协议版本常量。
const protocolV = 1

// envelope 类型(与 protocol.json envelopeTypes 一一对应)。
const (
	typeAuthChallenge  = "auth.challenge"
	typeAuthResponse   = "auth.response"
	typeAuthOK         = "auth.ok"
	typeHello          = "hello"
	typeBridgeRequest  = "bridge.request"
	typeBridgeResponse = "bridge.response"
	typeBridgeCancel   = "bridge.cancel"
	typePairRequest    = "pair.request"
	typePairDecision   = "pair.decision"
	typeClientRevoke   = "client.revoke"
	typeClientSync     = "client.sync"
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
	errInvalidRequest    = "INVALID_REQUEST"
	errUnauthenticated   = "UNAUTHENTICATED"
	errInsufficientScope = "INSUFFICIENT_SCOPE"
	errInternal          = "INTERNAL_ERROR"
	errRateLimited       = "RATE_LIMITED"
	errOperationExpired  = "OPERATION_EXPIRED"
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

// BridgeRequest 是转发给扩展执行的一次 action 调用(PROTOCOL §4)。
type BridgeRequest struct {
	ProtocolVersion int             `json:"protocolVersion"`
	ClientID        string          `json:"clientId"`
	Action          string          `json:"action"`
	Input           json.RawMessage `json:"input"`
}

// BridgeResponse 是扩展对 bridge.request 的应答(ok 二选一)。
type BridgeResponse struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *BridgeError    `json:"error,omitempty"`
}

// BridgeError 是失败应答里的结构化错误。
type BridgeError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *BridgeError) Error() string {
	if e == nil {
		return ""
	}
	return e.Code + ": " + e.Message
}

type pairRequestPayload struct {
	PairingID       string   `json:"pairingId"`
	ClientName      string   `json:"clientName"`
	RequestedScopes []string `json:"requestedScopes"`
	Code            string   `json:"code"`
}

type pairDecisionPayload struct {
	PairingID     string   `json:"pairingId"`
	Approved      bool     `json:"approved"`
	GrantedScopes []string `json:"grantedScopes"`
}

type clientRevokePayload struct {
	ClientID string `json:"clientId"`
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

// clientSyncPayload 直接是客户端记录数组。
func clientSyncEnvelope(records []auth.ClientRecord) (Envelope, error) {
	if records == nil {
		records = []auth.ClientRecord{}
	}
	return newEnvelope(typeClientSync, "", records)
}
