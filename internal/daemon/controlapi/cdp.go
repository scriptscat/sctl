package controlapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/daemon/cdpendpoint"
)

// cdpEndpoint 返回目标浏览器的原始 CDP 端点,没有时创建。端点给出这个浏览器全部标签页不经审批的完整控制,
// 与页面命令一样,控制令牌即全部授权(docs/threat-model.md)。
func (h *Handler) cdpEndpoint(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCDPRequest(w, r)
	if !ok {
		return
	}
	snap, err := h.endpoints.Create(req.Browser, r.Host)
	h.writeCDPSnapshot(w, snap, err)
}

// cdpStatus 返回目标浏览器的端点状态,不创建端点。
func (h *Handler) cdpStatus(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCDPRequest(w, r)
	if !ok {
		return
	}
	snap, err := h.endpoints.Status(req.Browser, r.Host)
	h.writeCDPSnapshot(w, snap, err)
}

// cdpClose 关闭目标浏览器的端点,等连着的客户端被断开、标签页还给 sctl 后才回答。
func (h *Handler) cdpClose(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeCDPRequest(w, r)
	if !ok {
		return
	}
	ref, closed, err := h.endpoints.Close(r.Context(), req.Browser)
	if err != nil {
		h.writeCDPError(w, err)
		return
	}
	h.writeCDPResult(w, control.CDPCloseResult{Browser: toCDPBrowserRef(ref), Closed: closed})
}

func decodeCDPRequest(w http.ResponseWriter, r *http.Request) (control.CDPRequest, bool) {
	var req control.CDPRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeControlError(w, bridge.CodeInvalidRequest, "malformed request body")
		return req, false
	}
	return req, true
}

func (h *Handler) writeCDPSnapshot(w http.ResponseWriter, snap cdpendpoint.Snapshot, err error) {
	if err != nil {
		h.writeCDPError(w, err)
		return
	}
	res := control.CDPEndpointResult{Browser: toCDPBrowserRef(snap.Browser)}
	if e := snap.Endpoint; e != nil {
		res.Endpoint = &control.CDPEndpoint{
			HTTPURL:         e.HTTPURL,
			WSURL:           e.WSURL,
			ClientConnected: e.ClientConnected,
			ConnectedAt:     e.ConnectedAt,
			ExpiresAt:       e.ExpiresAt,
		}
	}
	h.writeCDPResult(w, res)
}

func (h *Handler) writeCDPResult(w http.ResponseWriter, v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		h.log.Error("failed to encode a CDP endpoint result", zap.Error(err))
		writeControlError(w, bridge.CodeInternal, "internal error")
		return
	}
	writeJSON(w, control.CallResult{OK: true, Result: raw})
}

// writeCDPError 把浏览器目标的错误(BROWSER_OFFLINE、NO_BROWSER_CONNECTED 等)原样交给调用方。
func (h *Handler) writeCDPError(w http.ResponseWriter, err error) {
	var be *bridge.Error
	switch {
	case errors.Is(err, context.Canceled):
		// 请求方已断开,连接已消失。
		return
	case errors.As(err, &be):
		writeControlError(w, be.Code, be.Message)
	default:
		h.log.Error("CDP endpoint request failed", zap.Error(err))
		writeControlError(w, bridge.CodeInternal, "internal error")
	}
}

func toCDPBrowserRef(ref cdpendpoint.BrowserRef) control.CDPBrowserRef {
	return control.CDPBrowserRef{ID: ref.ID, Name: ref.Name}
}
