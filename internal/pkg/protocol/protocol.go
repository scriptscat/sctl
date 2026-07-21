// Package protocol 解析内嵌的 protocol.json —— 与 ScriptCat 扩展共用的桥接常量单一事实源。
// 协议语义见 docs/protocol.md,两者冲突以 json 为准。
package protocol

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sync"
)

// protocolJSON 是桥接协议常量文件。权威副本在扩展仓库,CI 的 protocol-drift job
// 逐字节比对本镜像。
//
//go:embed protocol.json
var protocolJSON []byte

type Protocol struct {
	ProtocolVersion int               `json:"protocolVersion"`
	Transport       Transport         `json:"transport"`
	Versions        Versions          `json:"versions"`
	EnvelopeTypes   []string          `json:"envelopeTypes"`
	Scopes          []string          `json:"scopes"`
	Actions         map[string]Action `json:"actions"`
	ErrorCodes      []string          `json:"errorCodes"`
	Crypto          Crypto            `json:"crypto"`
	Limits          Limits            `json:"limits"`
	PairingCode     PairingCode       `json:"pairingCode"`
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
	Write    bool   `json:"write"`
	Blocking string `json:"blocking,omitempty"`
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
	McpPairingTtlMs     int `json:"mcpPairingTtlMs"`
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
		if err := json.Unmarshal(protocolJSON, p); err != nil {
			parseErr = fmt.Errorf("parse embedded protocol.json: %w", err)
			return
		}
		parsed = p
	})
	return parsed, parseErr
}
