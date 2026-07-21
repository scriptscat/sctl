package bridge

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/control"
)

// registerControl 在 daemon listener 上挂载本机内部控制 API(见 internal/control 包注释)。
// 与扩展 WS 面同 listener、独立路径;除 /control/health 外均要求控制令牌。
func (s *Server) registerControl(mux *http.ServeMux) {
	mux.HandleFunc(control.PathHealth, s.handleControlHealth)
	mux.HandleFunc(control.PathCall, s.guard(s.handleControlCall))
	mux.HandleFunc(control.PathWhoami, s.guard(s.handleControlWhoami))
	mux.HandleFunc(control.PathStatus, s.guard(s.handleControlStatus))
	mux.HandleFunc(control.PathPairExt, s.guard(s.handleControlPairExt))
	mux.HandleFunc(control.PathPairClient, s.guard(s.handleControlPairClient))
}

// guard 包装需要控制令牌的处理器:恒定时间校验 X-Sctl-Control-Token,不符即 401(无细节)。
func (s *Server) guard(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !s.authControl(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		h(w, r)
	}
}

func (s *Server) authControl(r *http.Request) bool {
	s.mu.Lock()
	want := s.controlToken
	s.mu.Unlock()
	got := r.Header.Get(control.HeaderControlToken)
	if want == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(want), []byte(got)) == 1
}

func (s *Server) handleControlHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, control.HealthResult{OK: true, Version: s.version})
}

func (s *Server) handleControlStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	extConnected := s.active != nil
	s.mu.Unlock()
	events := s.audit.Snapshot()
	writeJSON(w, control.StatusResult{
		DaemonVersion: s.version,
		ExtConnected:  extConnected,
		ClientCount:   len(s.clients.List()),
		SecurityCount: len(events),
		Security:      events,
	})
}

// handleControlCall 转发一次 bridge action:解析客户端身份与 scope,再驱动阻塞的 Server.Call。
// 请求方(CLI/mcp)断开会取消 r.Context() → Server.Call 向扩展发 bridge.cancel 作废操作。
func (s *Server) handleControlCall(w http.ResponseWriter, r *http.Request) {
	var req control.CallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeControlError(w, errInvalidRequest, "请求体非法")
		return
	}
	action, ok := s.proto.Actions[req.Action]
	if !ok {
		writeControlError(w, errInvalidRequest, "未知 action")
		return
	}

	clientID := control.CLIClientID
	if token := r.Header.Get(control.HeaderClientToken); token != "" {
		rec, ok := s.clients.Verify(token)
		if !ok {
			writeControlError(w, errUnauthenticated, "MCP 客户端令牌无效或已撤销")
			return
		}
		if !hasScope(rec.Scopes, action.Scope) {
			writeControlError(w, errInsufficientScope, "客户端缺少所需 scope")
			return
		}
		clientID = rec.ClientID
	}

	resp, err := s.Call(r.Context(), BridgeRequest{
		ClientID: clientID,
		Action:   req.Action,
		Input:    req.Input,
	}, action.Write)
	switch {
	case err == nil:
		res := control.CallResult{OK: resp.OK, Result: resp.Result}
		if resp.Error != nil {
			res.Error = &control.CallError{Code: resp.Error.Code, Message: resp.Error.Message}
		}
		writeJSON(w, res)
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		// 请求方已断开(Ctrl-C / 超时),连接已消失,无需再写响应。
		return
	case errors.Is(err, ErrNotConnected):
		writeControlError(w, errInternal, "扩展未连接")
	case errors.Is(err, ErrDisconnected):
		writeControlError(w, errOperationExpired, "扩展连接已断开,操作作废")
	default:
		var be *BridgeError
		if errors.As(err, &be) {
			writeControlError(w, be.Code, be.Message)
			return
		}
		writeControlError(w, errInternal, "内部错误")
	}
}

func (s *Server) handleControlWhoami(w http.ResponseWriter, r *http.Request) {
	token := r.Header.Get(control.HeaderClientToken)
	if token == "" {
		writeControlError(w, errInvalidRequest, "缺少 MCP 客户端令牌")
		return
	}
	rec, ok := s.clients.Verify(token)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	writeJSON(w, control.WhoamiResult{
		ClientID:    rec.ClientID,
		DisplayName: rec.DisplayName,
		Scopes:      rec.Scopes,
	})
}

func (s *Server) handleControlPairExt(w http.ResponseWriter, _ *http.Request) {
	display, err := s.BeginExtPairing()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, control.PairExtResult{Code: display})
}

// handleControlPairClient 以换行分隔 JSON 流驱动 MCP 客户端配对:先推「配对码」事件供终端展示,
// 再阻塞等待扩展裁决后推「裁决」事件。请求方断开 → r.Context() 取消 → 配对随 TTL 作废。
func (s *Server) handleControlPairClient(w http.ResponseWriter, r *http.Request) {
	var req control.PairClientRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "请求体非法", http.StatusBadRequest)
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "流式响应不受支持", http.StatusInternalServerError)
		return
	}

	// 配对是流式响应,任何 pre-stream 失败都用非 200 HTTP 状态回,让前端 statusError 兜住。
	handle, err := s.BeginClientPairing(req.ClientName, req.Scopes)
	if err != nil {
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
		return
	}

	enc := json.NewEncoder(w)
	w.Header().Set("Content-Type", "application/x-ndjson")
	if err := enc.Encode(control.PairClientEvent{Code: handle.Code}); err != nil {
		s.log.Debug("推送配对码事件失败", zap.Error(err))
		return
	}
	flusher.Flush()

	d, err := handle.Await(r.Context())
	if err != nil {
		// 请求方断开或超时:不再推裁决,配对由 TTL 作废。
		return
	}
	grant := control.PairClientGrant{Approved: d.Approved}
	if d.Approved {
		grant.ClientID = d.ClientID
		grant.Token = d.Token
		grant.Scopes = d.Scopes
	}
	_ = enc.Encode(control.PairClientEvent{Decision: &grant})
	flusher.Flush()
}

func hasScope(scopes []string, want string) bool {
	for _, s := range scopes {
		if s == want {
			return true
		}
	}
	return false
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeControlError(w http.ResponseWriter, code, message string) {
	writeJSON(w, control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: message}})
}
