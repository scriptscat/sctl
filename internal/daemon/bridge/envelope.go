package bridge

import (
	"encoding/json"
	"fmt"
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
)

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
	Mode   string `json:"mode"`
	NonceE string `json:"nonceE"`
	HMAC   string `json:"hmac"`
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
	SchemaVersion string   `json:"schemaVersion"`
	Methods       []string `json:"methods"`
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

func newResult(id string, result any) (Message, error) {
	raw, err := json.Marshal(result)
	if err != nil {
		return Message{}, fmt.Errorf("marshal result: %w", err)
	}
	return Message{JSONRPC: jsonRPCVersion, ID: id, Result: raw}, nil
}
