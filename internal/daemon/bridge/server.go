package bridge

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/daemon/ratelimit"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/audit"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// auditCapacity 是守卫侧安全事件环形缓冲的容量,对齐扩展侧 MCP_AUDIT_RING_BUFFER_SIZE。
const auditCapacity = 500

var (
	// ErrNotConnected 表示当前没有已配对的扩展连接。
	ErrNotConnected = errors.New("extension not connected")
	// ErrDisconnected 表示在途请求因扩展连接断开而作废。
	ErrDisconnected = errors.New("extension connection lost")
)

// Server 是桥接 daemon 的 WS 服务核心:accept、双向认证握手、envelope 路由与阻塞写模型。
// ScriptCat 只保留一条连接,新完成握手的 ScriptCat 连接替换旧连接;浏览器实例按实例 ID
// 各保留一条连接,同一实例重连只替换它自己。
//
// 信任模型是扁平的:接入(enrollment)为每个对端建立长期密钥即确立信任,CLI 与所有 MCP agent 都
// 经这些可信通道继承信任,不再逐客户端配对/铸令牌/撤销(docs/threat-model.md)。
type Server struct {
	version  string
	proto    *protocol.Protocol
	crypto   *auth.Crypto
	keys     *store.KeyStore
	browsers *store.BrowserRegistry
	log      *zap.Logger

	// regMu 串行化浏览器实例的登记与忘记,使登记表与在线表的联合修改原子。
	regMu sync.Mutex

	audit *audit.Recorder

	enrollAttempts *ratelimit.Limiter

	authTimeout      time.Duration
	writeDecisionTTL time.Duration
	enrollTTL        time.Duration
	pingInterval     time.Duration
	maxFrameBytes    int64

	mu         sync.Mutex
	conns      map[*conn]struct{}
	scriptCat  *conn
	online     map[string]*conn
	pending    map[string]*pendingCall
	enrollment *pendingEnrollment

	httpServer   *http.Server
	baseCtx      context.Context
	baseCancel   context.CancelFunc
	shutdownOnce sync.Once
}

// NewServer 用协议定义的常量与持久化后端构造服务:keys 存 ScriptCat 的长期密钥,browsers 存浏览器实例的登记与密钥。
func NewServer(version string, p *protocol.Protocol, keys *store.KeyStore, browsers *store.BrowserRegistry, log *zap.Logger) *Server {
	if log == nil {
		log = zap.NewNop()
	}
	return &Server{
		version:          version,
		proto:            p,
		crypto:           auth.NewCrypto(p),
		keys:             keys,
		browsers:         browsers,
		log:              log,
		audit:            audit.NewRecorder(auditCapacity, log),
		enrollAttempts:   ratelimit.NewLimiter(5, time.Minute),
		authTimeout:      time.Duration(p.Limits.AuthTimeoutMs) * time.Millisecond,
		writeDecisionTTL: time.Duration(p.Limits.WriteDecisionTtlMs) * time.Millisecond,
		enrollTTL:        time.Duration(p.Limits.ExtPairingCodeTtlMs) * time.Millisecond,
		pingInterval:     time.Duration(p.Limits.PingIntervalMs) * time.Millisecond,
		maxFrameBytes:    int64(p.Limits.MaxFrameBytes),
		conns:            make(map[*conn]struct{}),
		online:           make(map[string]*conn),
		pending:          make(map[string]*pendingCall),
	}
}

// Version 返回注入的 daemon 版本(hello.daemonVersion 与控制 API 都读它)。
func (s *Server) Version() string { return s.version }

// Action 按名字查协议动作定义(scope 与读写属性),未知 action 返回 ok=false。
func (s *Server) Action(name string) (protocol.Action, bool) {
	a, ok := s.proto.Actions[name]
	return a, ok
}

// ExtConnected 报告当前是否有完成握手的 ScriptCat 连接。
func (s *Server) ExtConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.scriptCat != nil
}

// AuditSnapshot 返回守卫侧安全事件的快照。
func (s *Server) AuditSnapshot() []audit.Event { return s.audit.Snapshot() }

// Serve 在给定 listener 上运行 WS 服务,阻塞至 ctx 取消后优雅停机。
// mux 由调用方提供并可预先挂载其他路径(组装层在此挂上 /control/*,见 internal/daemon);
// WS 面注册在根路径 "/"。
func (s *Server) Serve(ctx context.Context, ln net.Listener, mux *http.ServeMux) error {
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())
	if mux == nil {
		mux = http.NewServeMux()
	}
	mux.HandleFunc("/", s.handleWS)
	s.httpServer = &http.Server{Handler: mux}

	shutdownDone := make(chan struct{})
	go func() {
		<-ctx.Done()
		s.shutdown()
		close(shutdownDone)
	}()

	err := s.httpServer.Serve(ln)
	<-shutdownDone
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// shutdown 通知所有已建立会话的扩展、断开所有连接并关闭 HTTP server。幂等。
func (s *Server) shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		established := make([]*conn, 0, len(s.online)+1)
		if s.scriptCat != nil {
			established = append(established, s.scriptCat)
		}
		for _, c := range s.online {
			established = append(established, c)
		}
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()

		for _, c := range established {
			_ = c.sendNotification(methodShutdown, struct{}{})
		}
		for _, c := range conns {
			c.close(websocket.StatusGoingAway, "server shutdown")
		}
		s.baseCancel()
		_ = s.httpServer.Close()
	})
}

// extensionOriginSchemes 是浏览器扩展页面(含 offscreen 文档)发起 WS 时 Origin 的合法前缀。
// 浏览器盖章 Origin、页面 JS 无法伪造,据此廉价挡掉普通网页直连(docs/threat-model.md)。
var extensionOriginSchemes = []string{"chrome-extension://", "moz-extension://", "safari-web-extension://"}

// originAllowed 判定 WS 请求的 Origin 是否放行。空 Origin 放行:只有扩展经 WS 面接入,非浏览器
// 进程(可伪造任意 Origin)由握手兜底;带 http(s):// 等网页 Origin 的连接直接拒。这是廉价前置
// 过滤,不是唯一闸门。
func originAllowed(origin string) bool {
	if origin == "" {
		return true
	}
	for _, scheme := range extensionOriginSchemes {
		if strings.HasPrefix(origin, scheme) {
			return true
		}
	}
	return false
}

// handleWS 接受一条 WS 连接并驱动其握手与消息循环。Origin 白名单是挡网页的前置过滤,
// 非浏览器进程可以伪造 Origin,因此握手仍是真正的认证闸门。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !originAllowed(origin) {
		s.log.Debug("rejected websocket connection from non-extension origin", zap.String("origin", origin))
		s.audit.Record(audit.Event{Type: audit.TypeOriginRejected})
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Warn("websocket accept failed", zap.Error(err))
		return
	}
	ws.SetReadLimit(s.maxFrameBytes)

	c := s.newConn(ws)
	defer s.removeConn(c)

	if err := c.handshake(); err != nil {
		// 认证失败统一以 1008 关闭且不回显原因,避免向探测者泄露细节。
		s.log.Debug("handshake failed, closing connection", zap.Error(err))
		// 只有被守卫判定为安全信号的失败才进审计;本地故障(密钥读写失败等)不混入。
		var ae *authError
		if errors.As(err, &ae) {
			s.audit.Record(audit.Event{Type: ae.evType, Reason: ae.reason, Client: c.auditClient()})
		}
		c.close(websocket.StatusPolicyViolation, "")
		return
	}

	s.audit.Record(audit.Event{Type: audit.TypeHandshakeOK, Client: c.auditClient()})
	if err := c.sendNotification(methodHello, helloParams{DaemonVersion: s.version}); err != nil {
		s.log.Debug("failed to send hello", zap.Error(err))
		c.close(websocket.StatusInternalError, "")
		return
	}
	capabilitiesID, err := c.receiveCapabilities()
	if err != nil {
		s.log.Debug("failed to negotiate extension capabilities", zap.Error(err))
		c.close(websocket.StatusProtocolError, "invalid capabilities")
		return
	}
	// 先登记再回复:扩展一收到回复就可能发起调用,此时连接必须已可路由。
	if err := s.register(c); err != nil {
		s.rejectRegistration(c, capabilitiesID, err)
		return
	}
	if err := c.sendResult(capabilitiesID, struct{}{}); err != nil {
		s.log.Debug("failed to reply to capabilities", zap.Error(err))
		return
	}
	s.log.Info("extension completed the handshake and is connected", zap.String("peer", string(c.kind)))
	go c.heartbeatLoop()
	c.readLoop()
}

// register 使完成能力声明的连接可被调用。
func (s *Server) register(c *conn) error {
	if c.kind == protocol.PeerBrowser {
		return s.registerBrowser(c)
	}
	s.setScriptCat(c)
	return nil
}

// rejectRegistration 处理登记失败:名称冲突在认证后的通道上回 CONFLICT(握手失败本身不回显原因),
// 其余情况直接断开。
func (s *Server) rejectRegistration(c *conn, capabilitiesID string, err error) {
	switch {
	case errors.Is(err, store.ErrNameTaken):
		if sendErr := c.sendError(capabilitiesID, CodeConflict, "name "+c.peer.Name+" is used by another browser"); sendErr != nil {
			s.log.Debug("failed to reply to capabilities", zap.Error(sendErr))
		}
		c.close(websocket.StatusNormalClosure, "")
	case errors.Is(err, store.ErrInstanceNotFound):
		c.close(websocket.StatusPolicyViolation, "")
	default:
		s.log.Warn("failed to register browser instance", zap.Error(err))
		c.close(websocket.StatusInternalError, "")
	}
}

func (s *Server) newConn(ws *websocket.Conn) *conn {
	ctx, cancel := context.WithCancel(s.baseCtx)
	c := &conn{
		ws:     ws,
		srv:    s,
		log:    s.log,
		closed: make(chan struct{}),
		ctx:    ctx,
		cancel: cancel,
		kind:   protocol.PeerScriptCat,
	}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	return c
}

func (s *Server) removeConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	if s.scriptCat == c {
		s.scriptCat = nil
	}
	if s.online[c.instanceID] == c {
		delete(s.online, c.instanceID)
	}
	s.mu.Unlock()
	c.close(websocket.StatusNormalClosure, "")
}

// setScriptCat 将 c 设为唯一的 ScriptCat 连接,替换并断开旧的 ScriptCat 连接。
func (s *Server) setScriptCat(c *conn) {
	s.mu.Lock()
	old := s.scriptCat
	s.scriptCat = c
	s.mu.Unlock()
	if old != nil && old != c {
		old.close(websocket.StatusNormalClosure, "replaced by new connection")
	}
}
