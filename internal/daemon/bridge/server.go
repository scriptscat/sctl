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
	"github.com/google/uuid"
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
	ErrNotConnected = errors.New("扩展未连接")
	// ErrDisconnected 表示在途请求因扩展连接断开而作废。
	ErrDisconnected = errors.New("扩展连接已断开")
)

// Server 是桥接 daemon 的 WS 服务核心:accept、双向认证握手、envelope 路由与阻塞写模型。
// v1 只允许一个扩展实例:新完成握手的连接替换旧连接。
//
// 信任模型是扁平的:接入(enrollment)建立唯一长期密钥 K 即确立信任,CLI 与所有 MCP agent 都
// 经这条可信通道继承信任,不再逐客户端配对/铸令牌/撤销(docs/threat-model.md)。
type Server struct {
	version string
	proto   *protocol.Protocol
	crypto  *auth.Crypto
	keys    *store.KeyStore
	log     *zap.Logger

	audit *audit.Recorder

	enrollAttempts *ratelimit.Limiter
	readLimit      *ratelimit.Limiter
	writeLimit     *ratelimit.Limiter

	authTimeout      time.Duration
	writeDecisionTTL time.Duration
	enrollTTL        time.Duration
	maxFrameBytes    int64

	mu         sync.Mutex
	conns      map[*conn]struct{}
	active     *conn
	pending    map[string]*pendingCall
	enrollment *pendingEnrollment

	httpServer   *http.Server
	baseCtx      context.Context
	baseCancel   context.CancelFunc
	shutdownOnce sync.Once
}

// NewServer 用协议常量与持久化后端构造服务。限流默认值取 docs/protocol.md §7(实现可调)。
func NewServer(version string, p *protocol.Protocol, keys *store.KeyStore, log *zap.Logger) *Server {
	if log == nil {
		log = zap.NewNop()
	}
	return &Server{
		version:          version,
		proto:            p,
		crypto:           auth.NewCrypto(p),
		keys:             keys,
		log:              log,
		audit:            audit.NewRecorder(auditCapacity, log),
		enrollAttempts:   ratelimit.NewLimiter(5, time.Minute),
		readLimit:        ratelimit.NewLimiter(60, time.Minute),
		writeLimit:       ratelimit.NewLimiter(10, time.Minute),
		authTimeout:      time.Duration(p.Limits.AuthTimeoutMs) * time.Millisecond,
		writeDecisionTTL: time.Duration(p.Limits.WriteDecisionTtlMs) * time.Millisecond,
		enrollTTL:        time.Duration(p.Limits.ExtPairingCodeTtlMs) * time.Millisecond,
		maxFrameBytes:    int64(p.Limits.MaxFrameBytes),
		conns:            make(map[*conn]struct{}),
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

// ExtConnected 报告当前是否有完成握手的扩展连接。
func (s *Server) ExtConnected() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active != nil
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

// shutdown 通知扩展、断开所有连接并关闭 HTTP server。幂等。
func (s *Server) shutdown() {
	s.shutdownOnce.Do(func() {
		s.mu.Lock()
		active := s.active
		conns := make([]*conn, 0, len(s.conns))
		for c := range s.conns {
			conns = append(conns, c)
		}
		s.mu.Unlock()

		if active != nil {
			// 计划退出前推送 bridge.shutdown(§3.4),best-effort。
			_ = active.send(typeBridgeShutdown, uuid.NewString(), struct{}{})
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

// handleWS 接受一条 WS 连接并驱动其握手与消息循环。Origin 白名单是廉价前置(挡网页),握手仍是
// 真正的闸门(docs/protocol.md §8:非浏览器进程可伪造任意 Origin)。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
	if origin := r.Header.Get("Origin"); !originAllowed(origin) {
		s.log.Debug("拒绝非扩展 Origin 的 WS 连接", zap.String("origin", origin))
		s.audit.Record(audit.Event{Type: audit.TypeOriginRejected})
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
	if err != nil {
		s.log.Warn("websocket accept 失败", zap.Error(err))
		return
	}
	ws.SetReadLimit(s.maxFrameBytes)

	c := s.newConn(ws)
	defer s.removeConn(c)

	if err := c.handshake(); err != nil {
		// 认证失败统一以 1008 关闭,不回显原因(不给探测者信息,§3)。
		s.log.Debug("握手失败,断开连接", zap.Error(err))
		// 只有被守卫判定为安全信号的失败才进审计;本地故障(密钥读写失败等)不混入。
		var ae *authError
		if errors.As(err, &ae) {
			s.audit.Record(audit.Event{Type: ae.evType, Reason: ae.reason})
		}
		c.close(websocket.StatusPolicyViolation, "")
		return
	}

	s.audit.Record(audit.Event{Type: audit.TypeHandshakeOK})
	s.setActive(c)
	if err := c.send(typeHello, uuid.NewString(), helloPayload{DaemonVersion: s.version, ProtocolVersion: protocolV}); err != nil {
		s.log.Debug("发送 hello 失败", zap.Error(err))
		c.close(websocket.StatusInternalError, "")
		return
	}
	s.log.Info("扩展已完成握手并连接")
	c.readLoop()
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
	}
	s.mu.Lock()
	s.conns[c] = struct{}{}
	s.mu.Unlock()
	return c
}

func (s *Server) removeConn(c *conn) {
	s.mu.Lock()
	delete(s.conns, c)
	if s.active == c {
		s.active = nil
	}
	s.mu.Unlock()
	c.close(websocket.StatusNormalClosure, "")
}

// setActive 将 c 设为唯一活动连接,替换并断开旧连接(v1 单实例)。
func (s *Server) setActive(c *conn) {
	s.mu.Lock()
	old := s.active
	s.active = c
	s.mu.Unlock()
	if old != nil && old != c {
		old.close(websocket.StatusNormalClosure, "replaced by new connection")
	}
}
