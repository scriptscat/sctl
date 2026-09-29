package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/pkg/audit"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocolschema"
)

// writeTimeout 是单帧写出的兜底超时,避免卡死的对端拖住写锁。
const writeTimeout = 10 * time.Second

// conn 是一条 WS 连接的会话状态:写串行化、生命周期取消、握手确立的长期密钥。
type conn struct {
	ws     *websocket.Conn
	srv    *Server
	log    *zap.Logger
	ctx    context.Context
	cancel context.CancelFunc

	writeMu   sync.Mutex
	closed    chan struct{}
	closeOnce sync.Once
	pingMu    sync.Mutex
	pingID    string
	pingAck   chan struct{}

	key          []byte // 握手确立的长期共享密钥(ScriptCat 的 K 或浏览器实例密钥)
	capabilities map[string]struct{}

	// kind 默认是 ScriptCat;认证响应声明浏览器身份并通过校验后才改为浏览器。
	kind       protocol.Peer
	instanceID string
	// paired 表示浏览器实例在本连接完成了首次配对,其登记在能力声明被接受时才落盘。
	paired      bool
	peer        capabilitiesPeer
	connectedAt time.Time
}

// authError 是被守卫判定为安全信号的握手失败,携带审计分类。handleWS 据此统一记录一次,
// 避免在各失败点散落审计调用而漏记;不带此类型的失败(密钥读写等本地故障)不进审计。
type authError struct {
	evType audit.Type
	reason audit.Reason
	err    error
}

func (e *authError) Error() string { return e.err.Error() }
func (e *authError) Unwrap() error { return e.err }

func authFail(evType audit.Type, reason audit.Reason, format string, args ...any) *authError {
	return &authError{evType: evType, reason: reason, err: fmt.Errorf(format, args...)}
}

// handshake 执行每连接一次的双向 HMAC 挑战应答(会话或配对模式),须在 authTimeout 内完成。
func (c *conn) handshake() error {
	s := c.srv
	ctx, cancel := context.WithTimeout(c.ctx, s.authTimeout)
	defer cancel()

	nonceD, err := auth.RandomNonceHex(s.proto.Crypto.NonceBytes)
	if err != nil {
		return err
	}
	challengeID := uuid.NewString()
	if err := c.sendRequestCtx(ctx, challengeID, methodAuthenticate, authChallengeParams{NonceD: nonceD}); err != nil {
		return fmt.Errorf("send authenticate request: %w", err)
	}

	env, err := c.readMessage(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return authFail(audit.TypeHandshakeFailed, audit.ReasonTimeout, "timed out waiting for authentication response: %w", err)
		}
		return fmt.Errorf("read authentication response: %w", err)
	}
	if env.ID != challengeID || env.Method != "" || env.Error != nil || len(env.Result) == 0 {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "invalid authentication response")
	}
	var resp authResponseResult
	if err := json.Unmarshal(env.Result, &resp); err != nil {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "parse authentication response: %w", err)
	}
	if resp.Peer != nil {
		if err := c.claimBrowser(*resp.Peer); err != nil {
			return err
		}
	}

	switch resp.Mode {
	case modeSession:
		return c.handshakeSession(ctx, nonceD, resp)
	case modePairing:
		return c.handshakePairing(ctx, nonceD, resp)
	default:
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "unknown handshake mode %q", resp.Mode)
	}
}

// claimBrowser 在边界校验认证响应里声明的浏览器身份;通过后本连接按该实例选用密钥与 MAC 上下文。
func (c *conn) claimBrowser(peer authPeer) error {
	if peer.Kind != peerKindBrowser {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "unknown peer kind")
	}
	if !instanceIDPattern.MatchString(peer.InstanceID) {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "malformed browser instance ID")
	}
	c.kind = protocol.PeerBrowser
	c.instanceID = peer.InstanceID
	return nil
}

// auditClient 是审计事件的客户端标注:浏览器实例标注为 browser:<实例 ID>,ScriptCat 留空。
func (c *conn) auditClient() string {
	if c.kind == protocol.PeerBrowser {
		return "browser:" + c.instanceID
	}
	return ""
}

// extMACValid 按对端类型校验扩展应答的 HMAC;浏览器实例的 MAC 绑定其实例 ID。
func (c *conn) extMACValid(mode auth.Mode, key []byte, nonceD string, resp authResponseResult) bool {
	if c.kind == protocol.PeerBrowser {
		return c.srv.crypto.VerifyBrowserExtHMAC(mode, c.instanceID, key, nonceD, resp.NonceE, resp.HMAC)
	}
	return c.srv.crypto.VerifyExtHMAC(mode, key, nonceD, resp.NonceE, resp.HMAC)
}

func (c *conn) daemonMAC(mode auth.Mode, key []byte, nonceD, nonceE string) string {
	if c.kind == protocol.PeerBrowser {
		return c.srv.crypto.BrowserDaemonHMAC(mode, c.instanceID, key, nonceD, nonceE)
	}
	return c.srv.crypto.DaemonHMAC(mode, key, nonceD, nonceE)
}

// sessionKey 取会话握手所用的长期密钥:ScriptCat 用 pairing.key,浏览器实例用登记表里的实例密钥。
func (c *conn) sessionKey() ([]byte, error) {
	if c.kind == protocol.PeerBrowser {
		inst, ok := c.srv.browsers.Get(c.instanceID)
		if !ok {
			return nil, authFail(audit.TypeHandshakeFailed, audit.ReasonUnknownInstance, "browser instance is not paired")
		}
		return inst.Key, nil
	}
	key, ok, err := c.srv.keys.Load()
	if err != nil {
		return nil, fmt.Errorf("load long-term key: %w", err)
	}
	if !ok {
		return nil, errors.New("not paired yet, no long-term key")
	}
	return key, nil
}

// handshakeSession 用已配对长期密钥完成会话握手。
func (c *conn) handshakeSession(ctx context.Context, nonceD string, resp authResponseResult) error {
	key, err := c.sessionKey()
	if err != nil {
		return err
	}
	if !c.extMACValid(auth.ModeSession, key, nonceD, resp) {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonHMACMismatch, "session handshake HMAC verification failed")
	}
	okMAC := c.daemonMAC(auth.ModeSession, key, nonceD, resp.NonceE)
	if err := c.sendNotificationCtx(ctx, methodAuthenticated, authenticatedParams{HMAC: okMAC}); err != nil {
		return fmt.Errorf("send authenticated notification: %w", err)
	}
	c.key = key
	return nil
}

// handshakePairing 用接入码派生密钥完成首次接入握手,并以 AES-256-GCM 下发新长期密钥 K。
// 线上模式标识沿用 "pairing"(与扩展侧一致),语义即「接入 enrollment」。
func (c *conn) handshakePairing(ctx context.Context, nonceD string, resp authResponseResult) error {
	s := c.srv
	if !s.enrollAttempts.Allow("enrollment") {
		return authFail(audit.TypePairingRateLimited, audit.ReasonPairExhausted, "too many enrollment attempts")
	}
	enrollment, err := s.activeEnrollment()
	if err != nil {
		return authFail(audit.TypePairingFailed, audit.ReasonPairExpired, "%w", err)
	}
	kpMac, kpEnc, err := s.crypto.DerivePairingKeys(enrollment.code)
	if err != nil {
		return fmt.Errorf("derive enrollment keys: %w", err)
	}
	if !c.extMACValid(auth.ModePairing, kpMac, nonceD, resp) {
		s.failEnrollmentAttempt()
		return authFail(audit.TypePairingFailed, audit.ReasonHMACMismatch, "enrollment handshake HMAC verification failed")
	}
	if !s.takeEnrollment(enrollment) {
		return authFail(audit.TypePairingFailed, audit.ReasonPairExpired, "enrollment window already used")
	}

	k, err := auth.NewLongTermKey()
	if err != nil {
		return err
	}
	ct, iv, err := s.crypto.SealKey(kpEnc, k)
	if err != nil {
		return fmt.Errorf("seal long-term key: %w", err)
	}
	okMAC := c.daemonMAC(auth.ModePairing, kpMac, nonceD, resp.NonceE)
	if err := c.sendNotificationCtx(ctx, methodAuthenticated, authenticatedParams{HMAC: okMAC, Key: &keyDelivery{Ciphertext: ct, IV: iv}}); err != nil {
		return fmt.Errorf("send authenticated notification: %w", err)
	}
	// 下发成功后再落盘,避免下发失败却持久化了扩展拿不到的密钥。浏览器实例的密钥连同名称
	// 一起写入登记表,名称随能力声明才到达,因此推迟到登记时;pairing.key 只属于 ScriptCat。
	if c.kind == protocol.PeerBrowser {
		c.paired = true
	} else if err := s.keys.Save(k); err != nil {
		return fmt.Errorf("persist long-term key: %w", err)
	}
	c.key = k
	return nil
}

// readLoop 是握手后的消息分发循环,任何读错误即关闭连接返回。
func (c *conn) readLoop() {
	for {
		message, err := c.readMessage(c.ctx)
		if err != nil {
			c.close(websocket.StatusNormalClosure, "")
			return
		}
		if message.Method == "" {
			if c.acknowledgePing(message) {
				continue
			}
			c.srv.handleRPCResponse(c, message)
			continue
		}
		switch message.Method {
		case methodPing:
			if message.ID != "" {
				if err := c.sendResult(message.ID, struct{}{}); err != nil {
					c.log.Debug("failed to reply to ping", zap.Error(err))
				}
			}
		default:
			c.log.Debug("ignoring unexpected JSON-RPC method", zap.String("method", message.Method))
		}
	}
}

// heartbeatLoop 主动探测半开连接；对端必须在下一个间隔前应答，否则连接及全部在途调用作废。
func (c *conn) heartbeatLoop() {
	ticker := time.NewTicker(c.srv.pingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.closed:
			return
		case <-ticker.C:
		}

		id := uuid.NewString()
		ack := make(chan struct{}, 1)
		c.pingMu.Lock()
		c.pingID = id
		c.pingAck = ack
		c.pingMu.Unlock()
		if err := c.sendRequest(id, methodPing, struct{}{}); err != nil {
			c.close(websocket.StatusGoingAway, "heartbeat failed")
			return
		}

		timer := time.NewTimer(c.srv.pingInterval)
		select {
		case <-ack:
			if !timer.Stop() {
				<-timer.C
			}
		case <-timer.C:
			c.close(websocket.StatusGoingAway, "heartbeat timeout")
			return
		case <-c.closed:
			if !timer.Stop() {
				<-timer.C
			}
			return
		}
	}
}

func (c *conn) acknowledgePing(message Message) bool {
	c.pingMu.Lock()
	defer c.pingMu.Unlock()
	if message.ID == "" || message.ID != c.pingID || c.pingAck == nil {
		return false
	}
	if message.Error == nil {
		c.pingAck <- struct{}{}
		c.pingID = ""
		c.pingAck = nil
	}
	return true
}

// receiveCapabilities 读取并校验能力声明,返回请求 ID;回复由调用方在登记连接之后发出,
// 保证扩展收到回复时连接已可调用。
func (c *conn) receiveCapabilities() (string, error) {
	ctx, cancel := context.WithTimeout(c.ctx, c.srv.authTimeout)
	defer cancel()
	message, err := c.readMessage(ctx)
	if err != nil {
		return "", err
	}
	if message.Method != methodCapabilities || message.ID == "" {
		return "", fmt.Errorf("expected capabilities request")
	}
	var payload capabilitiesParams
	if err := json.Unmarshal(message.Params, &payload); err != nil {
		return "", err
	}
	if payload.SchemaVersion != c.srv.proto.SchemaVersion {
		return "", fmt.Errorf("incompatible protocol schema version %q", payload.SchemaVersion)
	}
	if c.kind == protocol.PeerBrowser {
		if !validCapabilitiesPeer(payload.Peer) {
			return "", errors.New("browser instance declared a missing or invalid peer")
		}
		c.peer = *payload.Peer
	}
	capabilities := make(map[string]struct{}, len(payload.Methods))
	for _, method := range payload.Methods {
		capabilities[method] = struct{}{}
	}
	c.capabilities = capabilities
	return message.ID, nil
}

func (c *conn) sendError(id, code, message string) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.writeMessage(ctx, newError(id, code, message))
}

func (c *conn) readMessage(ctx context.Context) (Message, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return Message{}, err
	}
	if err := protocolschema.ValidateWireFrame(data); err != nil {
		return Message{}, fmt.Errorf("validate JSON-RPC message: %w", err)
	}
	var message Message
	if err := json.Unmarshal(data, &message); err != nil {
		return Message{}, fmt.Errorf("parse JSON-RPC message: %w", err)
	}
	return message, nil
}

func (c *conn) sendRequestCtx(ctx context.Context, id, method string, params any) error {
	message, err := newRequest(id, method, params)
	if err != nil {
		return err
	}
	return c.writeMessage(ctx, message)
}

func (c *conn) sendRequest(id, method string, params any) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.sendRequestCtx(ctx, id, method, params)
}

func (c *conn) sendNotificationCtx(ctx context.Context, method string, params any) error {
	message, err := newNotification(method, params)
	if err != nil {
		return err
	}
	return c.writeMessage(ctx, message)
}

func (c *conn) sendNotification(method string, params any) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.sendNotificationCtx(ctx, method, params)
}

func (c *conn) sendResultCtx(ctx context.Context, id string, result any) error {
	message, err := newResult(id, result)
	if err != nil {
		return err
	}
	return c.writeMessage(ctx, message)
}

func (c *conn) sendResult(id string, result any) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.sendResultCtx(ctx, id, result)
}

// writeEnvelope 串行化写出 JSON-RPC 消息(coder/websocket 同一时刻只允许一个写者)。
func (c *conn) writeMessage(ctx context.Context, message Message) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.ws, message)
}

// closeInBackground 在后台断开一条已被撤下(被新连接替换或实例被忘记)的连接。WS 关闭握手要等
// 对端回应关闭帧(库内最长 5s),而这类对端往往已失联或正忙;同步关闭会让新连接的登记与能力声明
// 回复、或持有 regMu 的忘记操作陪着等。调用方须先把它移出在线表,它就不会再被选为调用目标。
func (c *conn) closeInBackground(code websocket.StatusCode, reason string) {
	go c.close(code, reason)
}

// close 幂等关闭:取消连接 ctx(解阻塞读)、关闭底层 WS、触发 closed 通道。
func (c *conn) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.ws.Close(code, reason)
		close(c.closed)
	})
}
