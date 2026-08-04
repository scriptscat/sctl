// Package controlapi 实现 daemon listener 上的本机内部控制 API(/control/*)。
//
// 它是守卫侧的 controller 层:只做协议转换(HTTP/JSON ↔ 桥接调用)与控制令牌鉴权,一切有状态
// 逻辑交给 Bridge。扁平信任下控制令牌是唯一传输闸门:带令牌即拥有全部能力,不再逐客户端令牌 /
// scope 判定;调用方自报的客户端标签只用于审计归因。与扩展 WS 面共用同一 listener、走独立路径;
// 除 /control/health 外均要求控制令牌。请求/响应 DTO 与前端共享,见 internal/client/control。
package controlapi

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/audit"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// Bridge 是控制 API 依赖的守卫侧能力面,由 *bridge.Server 实现。
// 收窄到这几个方法既是为了让本包能独立测试,也是为了标明控制 API 不该碰 WS 连接状态。
type Bridge interface {
	Version() string
	Action(name string) (protocol.Action, bool)
	ExtConnected() bool
	AuditSnapshot() []audit.Event
	Call(ctx context.Context, req bridge.Request, write bool) (bridge.Response, error)
	BeginEnrollment() (string, error)
}

// Handler 是控制 API 的处理器集合。
type Handler struct {
	bridge Bridge
	token  string
	log    *zap.Logger
}

// New 构造控制 API。token 是 daemon 绑定端口后落盘的 0600 控制令牌;空令牌下除健康检查外全拒。
func New(b Bridge, token string, log *zap.Logger) *Handler {
	if log == nil {
		log = zap.NewNop()
	}
	return &Handler{bridge: b, token: token, log: log}
}

// Register 把控制 API 挂到 daemon listener 的 mux 上。
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc(control.PathHealth, h.health)
	mux.HandleFunc(control.PathCall, h.guard(h.call))
	mux.HandleFunc(control.PathStatus, h.guard(h.status))
	mux.HandleFunc(control.PathEnroll, h.guard(h.enroll))
}

// guard 包装需要控制令牌的处理器:恒定时间校验 X-Sctl-Control-Token,不符即 401(无细节)。
func (h *Handler) guard(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !h.authenticated(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (h *Handler) authenticated(r *http.Request) bool {
	got := r.Header.Get(control.HeaderControlToken)
	if h.token == "" || got == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(h.token), []byte(got)) == 1
}

func (h *Handler) health(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, control.HealthResult{OK: true, Version: h.bridge.Version()})
}

func (h *Handler) status(w http.ResponseWriter, _ *http.Request) {
	events := h.bridge.AuditSnapshot()
	writeJSON(w, control.StatusResult{
		DaemonVersion: h.bridge.Version(),
		ExtConnected:  h.bridge.ExtConnected(),
		SecurityCount: len(events),
		Security:      events,
	})
}

// call 转发一次 bridge action:解析调用方自报标签(仅审计),再驱动阻塞的 Bridge.Call。
// 请求方(CLI/mcp)断开会取消 r.Context() → Call 向扩展发 $/cancelRequest 作废操作。
func (h *Handler) call(w http.ResponseWriter, r *http.Request) {
	var req control.CallRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeControlError(w, bridge.CodeInvalidRequest, "malformed request body")
		return
	}
	action, ok := h.bridge.Action(req.Action)
	if !ok {
		writeControlError(w, bridge.CodeInvalidRequest, "unknown action")
		return
	}

	clientID := control.CLIClientID
	if label := r.Header.Get(control.HeaderClientLabel); label != "" {
		clientID = label
	}

	resp, err := h.bridge.Call(r.Context(), bridge.Request{
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
	case errors.Is(err, bridge.ErrNotConnected):
		writeControlError(w, bridge.CodeInternal, "extension not connected")
	case errors.Is(err, bridge.ErrDisconnected):
		writeControlError(w, bridge.CodeOperationExpired, "extension connection lost, operation voided")
	default:
		var be *bridge.Error
		if errors.As(err, &be) {
			writeControlError(w, be.Code, be.Message)
			return
		}
		writeControlError(w, bridge.CodeInternal, "internal error")
	}
}

// enroll 打开一次接入窗口并返回展示形配对码(供 sctl connect 在终端展示)。
func (h *Handler) enroll(w http.ResponseWriter, _ *http.Request) {
	display, err := h.bridge.BeginEnrollment()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, control.EnrollResult{Code: display})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeControlError(w http.ResponseWriter, code, message string) {
	writeJSON(w, control.CallResult{OK: false, Error: &control.CallError{Code: code, Message: message}})
}
