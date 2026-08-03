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

	key []byte // 握手确立的长期共享密钥 K(会话建立后)
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
	if err := c.sendCtx(ctx, typeAuthChallenge, uuid.NewString(), authChallengePayload{NonceD: nonceD}); err != nil {
		return fmt.Errorf("send auth.challenge: %w", err)
	}

	env, err := c.readEnvelope(ctx)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return authFail(audit.TypeHandshakeFailed, audit.ReasonTimeout, "timed out waiting for auth.response: %w", err)
		}
		return fmt.Errorf("read auth.response: %w", err)
	}
	// 握手完成前,除 auth.response 外的任何消息导致立即断开(§3)。
	if env.Type != typeAuthResponse {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "got %s instead of auth.response during handshake", env.Type)
	}
	var resp authResponsePayload
	if err := json.Unmarshal(env.Payload, &resp); err != nil {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonProtocol, "parse auth.response: %w", err)
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

// handshakeSession 用已配对长期密钥 K 完成会话握手(§3.1)。
func (c *conn) handshakeSession(ctx context.Context, nonceD string, resp authResponsePayload) error {
	s := c.srv
	key, ok, err := s.keys.Load()
	if err != nil {
		return fmt.Errorf("load long-term key: %w", err)
	}
	if !ok {
		return errors.New("not paired yet, no long-term key")
	}
	if !s.crypto.VerifyExtHMAC(auth.ModeSession, key, nonceD, resp.NonceE, resp.HMAC) {
		return authFail(audit.TypeHandshakeFailed, audit.ReasonHMACMismatch, "session handshake HMAC verification failed")
	}
	okMAC := s.crypto.DaemonHMAC(auth.ModeSession, key, nonceD, resp.NonceE)
	if err := c.sendCtx(ctx, typeAuthOK, uuid.NewString(), authOKPayload{HMAC: okMAC}); err != nil {
		return fmt.Errorf("send auth.ok: %w", err)
	}
	c.key = key
	return nil
}

// handshakePairing 用接入码派生密钥完成首次接入握手,并以 AES-256-GCM 下发新长期密钥 K(§3.2)。
// 线上模式标识沿用 "pairing"(与扩展侧一致),语义即「接入 enrollment」。
func (c *conn) handshakePairing(ctx context.Context, nonceD string, resp authResponsePayload) error {
	s := c.srv
	if !s.enrollAttempts.Allow("enrollment") {
		return authFail(audit.TypePairingRateLimited, audit.ReasonPairExhausted, "too many enrollment attempts")
	}
	code, err := s.activeEnrollmentCode()
	if err != nil {
		return authFail(audit.TypePairingFailed, audit.ReasonPairExpired, "%w", err)
	}
	kpMac, kpEnc, err := s.crypto.DerivePairingKeys(code)
	if err != nil {
		return fmt.Errorf("derive enrollment keys: %w", err)
	}
	if !s.crypto.VerifyExtHMAC(auth.ModePairing, kpMac, nonceD, resp.NonceE, resp.HMAC) {
		s.failEnrollmentAttempt()
		return authFail(audit.TypePairingFailed, audit.ReasonHMACMismatch, "enrollment handshake HMAC verification failed")
	}

	k, err := auth.NewLongTermKey()
	if err != nil {
		return err
	}
	ct, iv, err := s.crypto.SealKey(kpEnc, k)
	if err != nil {
		return fmt.Errorf("seal long-term key: %w", err)
	}
	okMAC := s.crypto.DaemonHMAC(auth.ModePairing, kpMac, nonceD, resp.NonceE)
	if err := c.sendCtx(ctx, typeAuthOK, uuid.NewString(), authOKPayload{HMAC: okMAC, Key: &keyDelivery{Ciphertext: ct, IV: iv}}); err != nil {
		return fmt.Errorf("send auth.ok: %w", err)
	}
	// 下发成功后再落盘,避免下发失败却持久化了扩展拿不到的密钥。
	if err := s.keys.Save(k); err != nil {
		return fmt.Errorf("persist long-term key: %w", err)
	}
	s.clearEnrollment()
	c.key = k
	return nil
}

// readLoop 是握手后的消息分发循环,任何读错误即关闭连接返回。
func (c *conn) readLoop() {
	for {
		env, err := c.readEnvelope(c.ctx)
		if err != nil {
			c.close(websocket.StatusNormalClosure, "")
			return
		}
		// v 不等于 1:立即断开(§2)。
		if env.V != protocolV {
			c.close(websocket.StatusProtocolError, "unsupported protocol version")
			return
		}
		switch env.Type {
		case typeBridgeResponse:
			c.srv.handleBridgeResponse(env)
		case typePing:
			if err := c.send(typePong, env.RequestID, struct{}{}); err != nil {
				c.log.Debug("failed to reply with pong", zap.Error(err))
			}
		case typePong:
			// 心跳应答,v1 不做主动存活探测,无需处理。
		default:
			// 未知/非预期类型:忽略并记日志(前向兼容,§2)。
			c.log.Debug("ignoring unknown or unexpected message", zap.String("type", env.Type))
		}
	}
}

func (c *conn) readEnvelope(ctx context.Context) (Envelope, error) {
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return Envelope{}, err
	}
	var env Envelope
	if err := json.Unmarshal(data, &env); err != nil {
		return Envelope{}, fmt.Errorf("parse envelope: %w", err)
	}
	return env, nil
}

func (c *conn) sendCtx(ctx context.Context, typ, requestID string, payload any) error {
	env, err := newEnvelope(typ, requestID, payload)
	if err != nil {
		return err
	}
	return c.writeEnvelope(ctx, env)
}

func (c *conn) send(typ, requestID string, payload any) error {
	ctx, cancel := context.WithTimeout(c.ctx, writeTimeout)
	defer cancel()
	return c.sendCtx(ctx, typ, requestID, payload)
}

// writeEnvelope 串行化写出(coder/websocket 同一时刻只允许一个写者)。
func (c *conn) writeEnvelope(ctx context.Context, env Envelope) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return wsjson.Write(ctx, c.ws, env)
}

// close 幂等关闭:取消连接 ctx(解阻塞读)、关闭底层 WS、触发 closed 通道。
func (c *conn) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		c.cancel()
		_ = c.ws.Close(code, reason)
		close(c.closed)
	})
}
