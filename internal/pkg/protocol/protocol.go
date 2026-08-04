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
	Versions       Versions          `json:"versions"`
	SessionMethods []string          `json:"sessionMethods"`
	Scopes         []string          `json:"scopes"`
	Actions        map[string]Action `json:"methods"`
	ErrorCodes     []string          `json:"errorCodes"`
	Crypto         Crypto            `json:"crypto"`
	Limits         Limits            `json:"limits"`
	PairingCode    PairingCode       `json:"pairingCode"`
}

type Transport struct {
	DefaultURL  string `json:"defaultUrl"`
	DefaultPort int    `json:"defaultPort"`
	Frame       string `json:"frame"`
}

type Versions struct {
	MinDaemonVersion string `json:"minDaemonVersion"`
}

type Action struct {
	Scope    string `json:"scope"`
	Write    bool   `json:"-"`
	Effect   string `json:"effect"`
	Blocking string `json:"blocking,omitempty"`
	Params   string `json:"params"`
	Result   string `json:"result"`
}

type Crypto struct {
	MAC        string            `json:"mac"`
	KDF        string            `json:"kdf"`
	AEAD       string            `json:"aead"`
	NonceBytes int               `json:"nonceBytes"`
	Encoding   string            `json:"encoding"`
	Context    map[string]string `json:"context"`
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
