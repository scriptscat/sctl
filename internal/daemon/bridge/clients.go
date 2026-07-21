package bridge

import (
	"encoding/json"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// Clients 返回全部 MCP 客户端授权记录的快照。
func (s *Server) Clients() []store.ClientRecord { return s.clients.List() }

// VerifyClient 按令牌原文校验 MCP 客户端身份,撤销或未知令牌返回 ok=false。
func (s *Server) VerifyClient(token string) (store.ClientRecord, bool) {
	return s.clients.Verify(token)
}

// handleClientRevoke 撤销客户端:立即失效令牌、作废其在途请求(触发 bridge.cancel)、回推 client.sync。
func (s *Server) handleClientRevoke(env Envelope) {
	var p clientRevokePayload
	if err := json.Unmarshal(env.Payload, &p); err != nil {
		s.log.Debug("解析 client.revoke 失败", zap.Error(err))
		return
	}
	ok, err := s.clients.Revoke(p.ClientID)
	if err != nil {
		s.log.Error("撤销客户端失败", zap.Error(err))
		return
	}
	if !ok {
		return
	}
	s.audit.Record(audit.Event{Type: audit.TypeClientRevoked, Client: p.ClientID})

	s.mu.Lock()
	active := s.active
	victims := make(map[string]*pendingCall)
	for id, pc := range s.pending {
		if pc.clientID == p.ClientID {
			victims[id] = pc
		}
	}
	s.mu.Unlock()

	for id, pc := range victims {
		if active != nil {
			s.cancelToExt(active, id)
		}
		pc.respCh <- Response{OK: false, Error: &Error{Code: CodeUnauthenticated, Message: "client revoked"}}
	}
	s.broadcastClientSync()
}

// broadcastClientSync 向活动连接推送全量客户端镜像(v1 单实例,广播=发给唯一连接)。
func (s *Server) broadcastClientSync() {
	env, err := clientSyncEnvelope(s.clients.List())
	if err != nil {
		s.log.Error("构造 client.sync 失败", zap.Error(err))
		return
	}
	if c := s.activeConn(); c != nil {
		if err := c.sendRaw(env); err != nil {
			s.log.Debug("推送 client.sync 失败", zap.Error(err))
		}
	}
}
