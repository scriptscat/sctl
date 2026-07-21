package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/auth"
)

// pendingExtPairing 是一次进行中的扩展配对窗口(由 sctl pair 打开)。
type pendingExtPairing struct {
	code         string
	expiresAt    time.Time
	attemptsLeft int
}

// pendingClientPairing 是一次进行中的 MCP 客户端配对(等待扩展 pair.decision)。
type pendingClientPairing struct {
	clientName string
	scopes     []string
	resultCh   chan ClientDecision
}

// ClientDecision 是扩展对 MCP 客户端配对的最终裁决。
type ClientDecision struct {
	Approved bool
	ClientID string
	Token    string
	Scopes   []string
}

// ClientPairing 是 BeginClientPairing 返回的句柄:先取 Code 在终端展示,再 Await 裁决。
type ClientPairing struct {
	PairingID string
	Code      string
	resultCh  <-chan ClientDecision
}

// Await 阻塞等待扩展裁决,直到裁决到达或调用方 ctx 取消。
func (cp *ClientPairing) Await(ctx context.Context) (ClientDecision, error) {
	select {
	case d := <-cp.resultCh:
		return d, nil
	case <-ctx.Done():
		return ClientDecision{}, ctx.Err()
	}
}

// BeginExtPairing 打开一次扩展配对窗口,返回展示形配对码(XXXX-XXXX)。供 sctl pair 使用。
func (s *Server) BeginExtPairing() (display string, err error) {
	canonical, display, err := auth.NewPairingCode()
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	s.extPairing = &pendingExtPairing{
		code:         canonical,
		expiresAt:    time.Now().Add(s.extPairTTL),
		attemptsLeft: 3,
	}
	s.mu.Unlock()
	return display, nil
}

func (s *Server) activePairingCode() (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.extPairing == nil {
		return "", errors.New("无进行中的配对窗口")
	}
	if time.Now().After(s.extPairing.expiresAt) {
		s.extPairing = nil
		return "", errors.New("配对窗口已过期")
	}
	return s.extPairing.code, nil
}

// failPairingAttempt 记一次配对失败,累计 3 次即作废配对窗口(docs/protocol.md §3.2)。
func (s *Server) failPairingAttempt() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.extPairing == nil {
		return
	}
	s.extPairing.attemptsLeft--
	if s.extPairing.attemptsLeft <= 0 {
		s.extPairing = nil
	}
}

func (s *Server) clearPairing() {
	s.mu.Lock()
	s.extPairing = nil
	s.mu.Unlock()
}

// BeginClientPairing 发起一次 MCP 客户端配对:推送 pair.request 给扩展,返回句柄供 Await 裁决。
func (s *Server) BeginClientPairing(clientName string, scopes []string) (*ClientPairing, error) {
	active := s.activeConn()
	if active == nil {
		return nil, ErrNotConnected
	}
	pairingID := uuid.NewString()
	canonical, display, err := auth.NewPairingCode()
	if err != nil {
		return nil, err
	}
	ch := make(chan ClientDecision, 1)
	pp := &pendingClientPairing{clientName: clientName, scopes: scopes, resultCh: ch}

	s.mu.Lock()
	s.clientPairings[pairingID] = pp
	s.mu.Unlock()

	err = active.send(typePairRequest, uuid.NewString(), pairRequestPayload{
		PairingID:       pairingID,
		ClientName:      clientName,
		RequestedScopes: scopes,
		Code:            canonical,
	})
	if err != nil {
		s.mu.Lock()
		delete(s.clientPairings, pairingID)
		s.mu.Unlock()
		return nil, fmt.Errorf("推送 pair.request: %w", err)
	}

	// TTL 未决即作废(§6:2 分钟)。
	time.AfterFunc(s.clientPairTTL, func() {
		s.mu.Lock()
		pending, ok := s.clientPairings[pairingID]
		if ok {
			delete(s.clientPairings, pairingID)
		}
		s.mu.Unlock()
		if ok {
			pending.resultCh <- ClientDecision{Approved: false}
		}
	})

	return &ClientPairing{PairingID: pairingID, Code: display, resultCh: ch}, nil
}

// handlePairDecision 处理扩展对 MCP 客户端配对的裁决:批准即铸造令牌并回推 client.sync。
func (s *Server) handlePairDecision(env Envelope) {
	var p pairDecisionPayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		s.log.Debug("解析 pair.decision 失败", zap.Error(err))
		return
	}
	s.mu.Lock()
	pp := s.clientPairings[p.PairingID]
	delete(s.clientPairings, p.PairingID)
	s.mu.Unlock()
	if pp == nil {
		return
	}
	if !p.Approved {
		pp.resultCh <- ClientDecision{Approved: false}
		return
	}
	clientID, token, rec, err := s.clients.Mint(pp.clientName, p.GrantedScopes)
	if err != nil {
		s.log.Error("铸造客户端令牌失败", zap.Error(err))
		pp.resultCh <- ClientDecision{Approved: false}
		return
	}
	pp.resultCh <- ClientDecision{Approved: true, ClientID: clientID, Token: token, Scopes: rec.Scopes}
	s.broadcastClientSync()
}
