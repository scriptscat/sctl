// Package protocol 解析从 sctl 权威 schema 生成并嵌入的协议元数据。
package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
)

// DefinitionJSON is the sole maintained protocol definition embedded directly from protocol.json.
//
//go:embed protocol.json
var DefinitionJSON []byte

type Protocol struct {
	JSONRPCVersion string            `json:"jsonrpc"`
	SchemaVersion  string            `json:"schemaVersion"`
	Transport      Transport         `json:"transport"`
	SessionMethods []string          `json:"sessionMethods"`
	Scopes         []string          `json:"scopes"`
	Actions        map[string]Action `json:"methods"`
	ErrorCodes     []ErrorCode       `json:"errorCodes"`
	Crypto         Crypto            `json:"crypto"`
	Limits         Limits            `json:"limits"`
	PairingCode    PairingCode       `json:"pairingCode"`
}

type Transport struct {
	DefaultURL  string `json:"defaultUrl"`
	DefaultPort int    `json:"defaultPort"`
	Frame       string `json:"frame"`
}

type Action struct {
	Scope    string `json:"scope"`
	Write    bool   `json:"-"`
	Effect   string `json:"effect"`
	Blocking string `json:"blocking,omitempty"`
	Level    Level  `json:"level"`
	Params   string `json:"params"`
	Result   string `json:"result"`
	Peer     Peer   `json:"peer"`
	// MergeField 是结果中的数组字段名:多个浏览器同时在线时 daemon 按它合并各实例的列表;非列表方法为空。
	MergeField string `json:"mergeField,omitempty"`
}

// Level 是方法的破坏级别。daemon 只执行 L1 的确认检查;L2 的人工审批由扩展侧完成,
// daemon 只是等待它的结果(与 blocking 为 approval/disclosure 的方法同一套阻塞语义)。
type Level string

const (
	// LevelDirect 直接执行。
	LevelDirect Level = "L0"
	// LevelConfirm 要求调用方在 input 里显式传 confirm: true,否则 daemon 在转发前拒绝。
	LevelConfirm Level = "L1"
	// LevelApproval 要求用户在浏览器里批准后才执行。
	LevelApproval Level = "L2"
)

// ConfirmParam 是 L1 方法参数里承载显式确认的字段名,值必须恰好是 true。
const ConfirmParam = "confirm"

// Peer 是协议定义的归属对端:方法由哪种扩展实现,错误码与握手常量由哪些扩展使用。
type Peer string

const (
	PeerScriptCat Peer = "scriptcat"
	PeerBrowser   Peer = "browser"
)

type ErrorCode struct {
	Code  string `json:"code"`
	Peers []Peer `json:"peers"`
}

// CryptoContext 把 context 键映射到握手字符串;protocol.json 中每项另带归属标注,只在生成各对端代码时使用。
type CryptoContext map[string]string

func (c *CryptoContext) UnmarshalJSON(data []byte) error {
	var entries map[string]struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(data, &entries); err != nil {
		return err
	}
	values := make(CryptoContext, len(entries))
	for key, entry := range entries {
		values[key] = entry.Value
	}
	*c = values
	return nil
}

type Crypto struct {
	MAC        string        `json:"mac"`
	KDF        string        `json:"kdf"`
	AEAD       string        `json:"aead"`
	NonceBytes int           `json:"nonceBytes"`
	Encoding   string        `json:"encoding"`
	Context    CryptoContext `json:"context"`
}

type Limits struct {
	AuthTimeoutMs       int `json:"authTimeoutMs"`
	WriteDecisionTtlMs  int `json:"writeDecisionTtlMs"`
	ExtPairingCodeTtlMs int `json:"extPairingCodeTtlMs"`
	MaxSourceBytes      int `json:"maxSourceBytes"`
	MaxFrameBytes       int `json:"maxFrameBytes"`
	PingIntervalMs      int `json:"pingIntervalMs"`
}

type PairingCode struct {
	Alphabet string `json:"alphabet"`
	Length   int    `json:"length"`
	Display  string `json:"display"`
}

var (
	once     sync.Once
	parsed   *Protocol
	parseErr error
)

// Load 解析内嵌的 protocol.json,只解析一次。
func Load() (*Protocol, error) {
	once.Do(func() {
		p := &Protocol{}
		if err := json.Unmarshal(DefinitionJSON, p); err != nil {
			parseErr = fmt.Errorf("parse embedded protocol.json: %w", err)
			return
		}
		scopeSet := make(map[string]struct{})
		for name, action := range p.Actions {
			action.Write = action.Effect == "write"
			p.Actions[name] = action
			scopeSet[action.Scope] = struct{}{}
		}
		for scope := range scopeSet {
			p.Scopes = append(p.Scopes, scope)
		}
		sort.Strings(p.Scopes)
		parsed = p
	})
	return parsed, parseErr
}
