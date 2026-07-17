package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/auth"
	"github.com/scriptscat/sctl/internal/protocol"
	"github.com/scriptscat/sctl/internal/ratelimit"
)

var (
	// ErrNotConnected 表示当前没有已配对的扩展连接。
	ErrNotConnected = errors.New("扩展未连接")
	// ErrDisconnected 表示在途请求因扩展连接断开而作废。
	ErrDisconnected = errors.New("扩展连接已断开")
)

// pendingCall 是一条挂起的 bridge.request:阻塞直到应答/取消/断开/超时。
type pendingCall struct {
	clientID string
	respCh   chan BridgeResponse
}

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

// Server 是桥接 daemon 的 WS 服务核心:accept、双向认证握手、envelope 路由与阻塞写模型。
// v1 只允许一个扩展实例:新完成握手的连接替换旧连接。
type Server struct {
	version string
	proto   *protocol.Protocol
	crypto  *auth.Crypto
	keys    *auth.KeyStore
	clients *auth.ClientStore
	log     *zap.Logger

	pairAttempts *ratelimit.Limiter
	readLimit    *ratelimit.Limiter
	writeLimit   *ratelimit.Limiter

	authTimeout      time.Duration
	writeDecisionTTL time.Duration
	extPairTTL       time.Duration
	clientPairTTL    time.Duration
	maxFrameBytes    int64

	// controlToken 是本机内部控制 API 的鉴权凭据(由 daemon 绑定端口后注入,空则控制 API 全拒)。
	controlToken string

	mu             sync.Mutex
	conns          map[*conn]struct{}
	active         *conn
	pending        map[string]*pendingCall
	extPairing     *pendingExtPairing
	clientPairings map[string]*pendingClientPairing

	httpServer   *http.Server
	baseCtx      context.Context
	baseCancel   context.CancelFunc
	shutdownOnce sync.Once
}

// NewServer 用协议常量与持久化后端构造服务。限流默认值取 PROTOCOL §7(实现可调)。
func NewServer(version string, p *protocol.Protocol, keys *auth.KeyStore, clients *auth.ClientStore, log *zap.Logger) *Server {
	if log == nil {
		log = zap.NewNop()
	}
	return &Server{
		version:          version,
		proto:            p,
		crypto:           auth.NewCrypto(p),
		keys:             keys,
		clients:          clients,
		log:              log,
		pairAttempts:     ratelimit.NewLimiter(5, time.Minute),
		readLimit:        ratelimit.NewLimiter(60, time.Minute),
		writeLimit:       ratelimit.NewLimiter(10, time.Minute),
		authTimeout:      time.Duration(p.Limits.AuthTimeoutMs) * time.Millisecond,
		writeDecisionTTL: time.Duration(p.Limits.WriteDecisionTtlMs) * time.Millisecond,
		extPairTTL:       time.Duration(p.Limits.ExtPairingCodeTtlMs) * time.Millisecond,
		clientPairTTL:    time.Duration(p.Limits.McpPairingTtlMs) * time.Millisecond,
		maxFrameBytes:    int64(p.Limits.MaxFrameBytes),
		conns:            make(map[*conn]struct{}),
		pending:          make(map[string]*pendingCall),
		clientPairings:   make(map[string]*pendingClientPairing),
	}
}

// SetControlToken 注入本机内部控制 API 的鉴权凭据。须在 Serve 前调用;空令牌下控制 API 全拒。
func (s *Server) SetControlToken(token string) {
	s.mu.Lock()
	s.controlToken = token
	s.mu.Unlock()
}

// Serve 在给定 listener 上运行 WS 服务,阻塞至 ctx 取消后优雅停机。
// 同一 listener 上还挂载了本机内部控制 API(/control/*,独立路径),供 sctl mcp / CLI 动词驱动。
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	s.baseCtx, s.baseCancel = context.WithCancel(context.Background())
	mux := http.NewServeMux()
	s.registerControl(mux)
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

// handleWS 接受一条 WS 连接并驱动其握手与消息循环。故意不做 Origin 判别(§4)。
func (s *Server) handleWS(w http.ResponseWriter, r *http.Request) {
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
		c.close(websocket.StatusPolicyViolation, "")
		return
	}

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

func (s *Server) activeConn() *conn {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active
}

// Call 把一次 bridge action 转发给扩展并阻塞等待应答。write=true 的写调用可能挂起数分钟
// (等待用户在浏览器审批)。调用方 ctx 取消 / 超过 writeDecisionTTL / 扩展断开时:
//   - ctx 取消 / 超时:向扩展发 bridge.cancel 作废该操作,返回相应错误;
//   - 扩展断开:隐式作废全部在途请求,返回 ErrDisconnected。
func (s *Server) Call(ctx context.Context, req BridgeRequest, write bool) (BridgeResponse, error) {
	limiter := s.readLimit
	if write {
		limiter = s.writeLimit
	}
	if req.ClientID != "" && !limiter.Allow(req.ClientID) {
		return BridgeResponse{}, &BridgeError{Code: errRateLimited, Message: "rate limited"}
	}

	req.ProtocolVersion = protocolV
	requestID := uuid.NewString()
	pc := &pendingCall{clientID: req.ClientID, respCh: make(chan BridgeResponse, 1)}

	s.mu.Lock()
	active := s.active
	if active == nil {
		s.mu.Unlock()
		return BridgeResponse{}, ErrNotConnected
	}
	s.pending[requestID] = pc
	s.mu.Unlock()

	defer func() {
		s.mu.Lock()
		delete(s.pending, requestID)
		s.mu.Unlock()
	}()

	if err := active.send(typeBridgeRequest, requestID, req); err != nil {
		return BridgeResponse{}, fmt.Errorf("发送 bridge.request: %w", err)
	}

	timer := time.NewTimer(s.writeDecisionTTL)
	defer timer.Stop()

	select {
	case resp := <-pc.respCh:
		return resp, nil
	case <-ctx.Done():
		s.cancelToExt(active, requestID)
		return BridgeResponse{}, ctx.Err()
	case <-timer.C:
		s.cancelToExt(active, requestID)
		return BridgeResponse{}, &BridgeError{Code: errOperationExpired, Message: "operation expired"}
	case <-active.closed:
		return BridgeResponse{}, ErrDisconnected
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
	var resp BridgeResponse
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
		pc.respCh <- BridgeResponse{OK: false, Error: &BridgeError{Code: errUnauthenticated, Message: "client revoked"}}
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

// failPairingAttempt 记一次配对失败,累计 3 次即作废配对窗口(PROTOCOL §3.2)。
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
