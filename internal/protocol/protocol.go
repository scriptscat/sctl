// Package protocol parses the embedded protocol.json — the single source of
// truth for bridge constants shared with the ScriptCat extension.
package protocol

import (
	"encoding/json"
	"fmt"
	"sync"

	sctl "github.com/scriptscat/sctl"
)

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

// Load parses the embedded protocol.json exactly once.
func Load() (*Protocol, error) {
	once.Do(func() {
		p := &Protocol{}
		if err := json.Unmarshal(sctl.ProtocolJSON, p); err != nil {
			parseErr = fmt.Errorf("parse embedded protocol.json: %w", err)
			return
		}
		parsed = p
	})
	return parsed, parseErr
}
