package bridge

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// pendingCall 是一条挂起的 bridge.request:阻塞直到应答/取消/断开/超时。
type pendingCall struct {
	clientID string
	respCh   chan Response
}

// Call 把一次 bridge action 转发给扩展并阻塞等待应答。write=true 的写调用可能挂起数分钟
// (等待用户在浏览器审批)。调用方 ctx 取消 / 超过 writeDecisionTTL / 扩展断开时:
//   - ctx 取消 / 超时:向扩展发 bridge.cancel 作废该操作,返回相应错误;
//   - 扩展断开:隐式作废全部在途请求,返回 ErrDisconnected。
func (s *Server) Call(ctx context.Context, req Request, write bool) (Response, error) {
	limiter := s.readLimit
	if write {
		limiter = s.writeLimit
	}
	if req.ClientID != "" && !limiter.Allow(req.ClientID) {
		// 限流在转发前拦下,扩展侧不会有任何记录,守卫侧不记就完全无痕。
		s.audit.Record(audit.Event{Type: audit.TypeRequestRateLimited, Client: req.ClientID})
		return Response{}, &Error{Code: CodeRateLimited, Message: "rate limited"}
	}

	req.ProtocolVersion = protocolV
	requestID := uuid.NewString()
	pc := &pendingCall{clientID: req.ClientID, respCh: make(chan Response, 1)}

	s.mu.Lock()
	active := s.active
	if active == nil {
		s.mu.Unlock()
		return Response{}, ErrNotConnected
	}
	s.pending[requestID] = pc
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	if err := active.send(typeBridgeRequest, requestID, req); err != nil {
		return Response{}, fmt.Errorf("发送 bridge.request: %w", err)
	}

	timer := time.NewTimer(s.writeDecisionTTL)
	defer timer.Stop()

	select {
	case resp := <-pc.respCh:
		return resp, nil
	case <-ctx.Done():
		s.cancelToExt(active, requestID)
		return Response{}, ctx.Err()
	case <-timer.C:
		s.cancelToExt(active, requestID)
		return Response{}, &Error{Code: CodeOperationExpired, Message: "operation expired"}
	case <-active.closed:
		return Response{}, ErrDisconnected
	}
}

// cancelToExt 向扩展发送 bridge.cancel,回填原 bridge.request 的 requestId,best-effort。
func (s *Server) cancelToExt(c *conn, requestID string) {
	if err := c.send(typeBridgeCancel, requestID, struct{}{}); err != nil {
		s.log.Debug("发送 bridge.cancel 失败", zap.Error(err))
	}
}

// handleBridgeResponse 把应答交付给对应的挂起调用;取消后迟到的应答无主,忽略。
func (s *Server) handleBridgeResponse(env Envelope) {
	var resp Response
	if err := json.Unmarshal(env.Payload, &resp); err != nil {
		s.log.Debug("解析 bridge.response 失败", zap.Error(err))
		return
	}
	s.mu.Lock()
	pc := s.pending[env.RequestID]
	s.mu.Unlock()
	if pc == nil {
		return
	}
	pc.respCh <- resp
	if pc.clientID != "" {
		if err := s.clients.Touch(pc.clientID); err != nil {
			s.log.Debug("刷新客户端 lastUsedAt 失败", zap.Error(err))
		}
	}
}
