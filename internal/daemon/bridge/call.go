package bridge

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocolschema"
)

// pendingCall 是一条挂起的 JSON-RPC 请求:阻塞直到应答/取消/断开/超时。
type pendingCall struct {
	clientID string
	method   string
	respCh   chan Response
}

// Call 把一次 bridge action 转发给扩展并阻塞等待应答。write=true 的写调用可能挂起数分钟
// (等待用户在浏览器审批)。调用方 ctx 取消 / 超过 writeDecisionTTL / 扩展断开时:
//   - ctx 取消 / 超时:向扩展发 $/cancelRequest 作废该操作,返回相应错误;
//   - 扩展断开:隐式作废全部在途请求,返回 ErrDisconnected。
func (s *Server) Call(ctx context.Context, req Request) (Response, error) {
	requestID := uuid.NewString()
	pc := &pendingCall{clientID: req.ClientID, method: req.Action, respCh: make(chan Response, 1)}

	s.mu.Lock()
	active := s.active
	if active == nil {
		s.mu.Unlock()
		return Response{}, ErrNotConnected
	}
	if _, supported := active.capabilities[req.Action]; !supported {
		s.mu.Unlock()
		return Response{}, &Error{Code: "METHOD_NOT_FOUND", Message: "extension does not support " + req.Action}
	}
	s.pending[requestID] = pc
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	params := businessParams{Input: req.Input, ClientID: req.ClientID}
	if err := active.sendRequest(requestID, req.Action, params); err != nil {
		return Response{}, fmt.Errorf("send JSON-RPC request: %w", err)
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

// cancelToExt sends a JSON-RPC cancellation notification for an in-flight request.
func (s *Server) cancelToExt(c *conn, requestID string) {
	if err := c.sendNotification(methodCancel, cancelParams{ID: requestID}); err != nil {
		s.log.Debug("failed to send cancellation notification", zap.Error(err))
	}
}

// handleRPCResponse delivers a JSON-RPC response to its pending call.
func (s *Server) handleRPCResponse(message Message) {
	s.mu.Lock()
	pc := s.pending[message.ID]
	s.mu.Unlock()
	if pc == nil {
		return
	}
	if message.Error == nil {
		if err := protocolschema.ValidateMethodResult(pc.method, message.Result); err != nil {
			s.log.Debug("invalid rpc result", zap.Error(err))
			pc.respCh <- Response{OK: false, Error: &Error{Code: CodeInternal, Message: "invalid response from extension"}}
			return
		}
		pc.respCh <- Response{OK: true, Result: message.Result}
		return
	}
	domainCode := CodeInternal
	operationID := ""
	if message.Error.Data != nil {
		domainCode = message.Error.Data.Code
		operationID = message.Error.Data.OperationID
	}
	pc.respCh <- Response{OK: false, Error: &Error{Code: domainCode, Message: message.Error.Message, OperationID: operationID}}
}
