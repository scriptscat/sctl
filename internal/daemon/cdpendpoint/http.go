package cdpendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strings"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// PathPrefix 是端点在 daemon listener 上的路径前缀:/cdp/<密钥>/json/version 与 /cdp/<密钥>/devtools/browser/<id>。
const PathPrefix = "/cdp/"

// protocolVersion 是 /json/version 报告的 CDP 版本,与 Chrome 相同。
const protocolVersion = "1.3"

// ServeHTTP 处理 /cdp/ 下的请求。检查顺序是先 Host、Origin,后密钥:前两者的拒绝与端点存在与否无关,
// 密钥错误与路径不存在得到同一个 404,不透露是否有端点。
func (m *Manager) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !hostAllowed(r.Host) {
		// 网页经 DNS 重绑定把自己的域名指向 127.0.0.1 时,Host 是那个域名。
		http.Error(w, "Host header is not an IP address or localhost", http.StatusForbidden)
		return
	}
	if r.Header.Get("Origin") != "" {
		// Playwright 与 Puppeteer 在 Node 里不带 Origin;浏览器里的页面一定带。同 Chrome 远程调试的默认做法,
		// 不区分网页与扩展、DevTools 的 Origin,一律拒绝。
		http.Error(w, "requests with an Origin header are rejected", http.StatusForbidden)
		return
	}
	secret, rest, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, PathPrefix), "/")
	ep := m.lookup(secret)
	if ep == nil {
		http.NotFound(w, r)
		return
	}
	switch rest {
	case "json/version", "json/version/":
		m.serveVersion(w, r, ep)
	case "devtools/browser/" + ep.browserID:
		m.serveClient(w, r, ep)
	default:
		http.NotFound(w, r)
	}
}

// hostAllowed 只放行 IP 字面量与 localhost:端点地址本身用 IP 字面量,用 --listen-address 绑定其他网卡时那是网卡的 IP。
func hostAllowed(hostport string) bool {
	host := hostport
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		host = h
	}
	host = strings.TrimSuffix(strings.TrimPrefix(host, "["), "]")
	return strings.EqualFold(host, "localhost") || net.ParseIP(host) != nil
}

var chromeProductPattern = regexp.MustCompile(`(?:Headless)?Chrome/[0-9.]+`)

func (m *Manager) serveVersion(w http.ResponseWriter, r *http.Request, ep *endpoint) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var res generated.DebuggerUserAgentResult
	err := call(r.Context(), m.deps.Bridge, ep.instanceID, generated.MethodDebuggerUserAgent, generated.DebuggerUserAgentParams{}, &res)
	if err != nil {
		m.log.Debug("failed to read the browser's user agent", zap.String("browser", ep.instanceID), zap.Error(err))
		http.Error(w, "the browser is not available: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	userAgent := res.UserAgent
	w.Header().Set("Content-Type", "application/json; charset=UTF-8")
	_ = json.NewEncoder(w).Encode(map[string]string{
		"Browser":              chromeProduct(userAgent),
		"Protocol-Version":     protocolVersion,
		"User-Agent":           userAgent,
		"webSocketDebuggerUrl": wsURL(r.Host, ep.secret, ep.browserID),
	})
}

// serveClient 让一个客户端占用端点:先把这个浏览器的标签页从 sctl 手里收过来,再升级 WS 并运行会话,会话结束后清理。
func (m *Manager) serveClient(w http.ResponseWriter, r *http.Request, ep *endpoint) {
	// 先于交出检查:一个不是 WS 升级的请求不该让 sctl 断开自己的标签页。
	if r.Method != http.MethodGet || !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
		http.Error(w, "this address expects a WebSocket upgrade", http.StatusBadRequest)
		return
	}
	c, status := m.reserve(ep)
	switch status {
	case http.StatusNotFound:
		// 端点在查到之后、占用之前失效。
		http.NotFound(w, r)
		return
	case http.StatusConflict:
		http.Error(w, "a client is already connected to this CDP endpoint; only one client can use it at a time", http.StatusConflict)
		return
	}
	// 交出期间请求方断开或端点失效都中止交出。
	handOverCtx, stop := context.WithCancel(c.ctx)
	unlink := context.AfterFunc(r.Context(), stop)
	err := m.deps.Pages.HandOver(handOverCtx, ep.instanceID)
	unlink()
	stop()
	if err != nil {
		// HandOver 失败时自己已回滚,sctl 命令照常可用,不需要 Reclaim。
		m.release(ep, c)
		http.Error(w, "cannot take the browser's tabs over from sctl: "+err.Error(), http.StatusServiceUnavailable)
		return
	}
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		m.log.Debug("CDP endpoint websocket upgrade failed", zap.Error(err))
		m.deps.Pages.Reclaim(ep.instanceID)
		m.release(ep, c)
		return
	}
	conn.SetReadLimit(m.deps.MaxMessageBytes)
	m.runSession(ep, c, conn)
}

// reserve 在端点空闲时让一个新客户端占用它并停止失效计时,返回 http.StatusOK;端点已失效时返回 404,已被占用时返回 409。
func (m *Manager) reserve(ep *endpoint) (*client, int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case ep.ctx.Err() != nil:
		return nil, http.StatusNotFound
	case ep.client != nil:
		return nil, http.StatusConflict
	}
	ctx, cancel := context.WithCancelCause(ep.ctx)
	c := &client{ctx: ctx, cancel: cancel, connectedAt: m.clock.Now(), done: make(chan struct{})}
	ep.client = c
	ep.stopExpiryLocked()
	return c, http.StatusOK
}

// release 结束 c 对端点的占用;端点还有效时重新开始 60 分钟计时。
func (m *Manager) release(ep *endpoint, c *client) {
	c.cancel(errClientGone)
	m.mu.Lock()
	ep.client = nil
	if m.byInstance[ep.instanceID] == ep {
		m.armExpiryLocked(ep)
	}
	m.mu.Unlock()
	close(c.done)
}

// errClientGone 是客户端会话正常结束(客户端断开、会话返回)的原因。
var errClientGone = errors.New("the client disconnected")

// closeReason 给出 ctx 结束时发给客户端的关闭原因。
func closeReason(ctx context.Context) (websocket.StatusCode, string) {
	switch cause := context.Cause(ctx); {
	case errors.Is(cause, errInstanceGone):
		return websocket.StatusGoingAway, "the browser disconnected from sctl"
	case errors.Is(cause, errSessionOverflow):
		return websocket.StatusTryAgainLater, "the client fell too far behind the browser's events"
	case errors.Is(cause, context.Canceled):
		// 端点的 ctx 结束:sctl cdp close、失效、浏览器被忘记或 daemon 退出。
		return websocket.StatusGoingAway, "the CDP endpoint was closed"
	default:
		return websocket.StatusNormalClosure, ""
	}
}
