package bridge

import (
	"context"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/auth"
	"github.com/scriptscat/sctl/internal/protocol"
)

// testHarness 承载一个运行中的 daemon 与用于对拍的密码学助手/存储。
type testHarness struct {
	srv    *Server
	crypto *auth.Crypto
	keys   *auth.KeyStore
	url    string
	proto  *protocol.Protocol
}

func startTestServer(t *testing.T) *testHarness {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	dir := t.TempDir()
	keys := auth.NewKeyStore(filepath.Join(dir, "pairing.key"))
	clients, err := auth.NewClientStore(filepath.Join(dir, "clients.json"))
	So(err, ShouldBeNil)
	srv := NewServer("0.1.0", p, keys, clients, zap.NewNop())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln) }()

	return &testHarness{
		srv:    srv,
		crypto: auth.NewCrypto(p),
		keys:   keys,
		url:    "ws://" + ln.Addr().String() + "/",
		proto:  p,
	}
}

// extClient 模拟扩展侧的 WS client。
type extClient struct {
	ws *websocket.Conn
}

func dial(url string) *extClient {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ws, _, err := websocket.Dial(ctx, url, nil)
	So(err, ShouldBeNil)
	return &extClient{ws: ws}
}

func (e *extClient) read() Envelope {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := e.ws.Read(ctx)
	So(err, ShouldBeNil)
	var env Envelope
	So(json.Unmarshal(data, &env), ShouldBeNil)
	return env
}

func (e *extClient) write(typ, requestID string, payload any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	env, err := newEnvelope(typ, requestID, payload)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, env), ShouldBeNil)
}

// doSessionHandshake 以给定长期密钥完成会话握手,并消费 hello。
func (h *testHarness) doSessionHandshake(key []byte) *extClient {
	e := dial(h.url)
	challenge := e.read()
	So(challenge.Type, ShouldEqual, typeAuthChallenge)
	var cp authChallengePayload
	So(json.Unmarshal(challenge.Payload, &cp), ShouldBeNil)

	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ExtHMAC(auth.ModeSession, key, cp.NonceD, nonceE)
	e.write(typeAuthResponse, uuid.NewString(), authResponsePayload{Mode: modeSession, NonceE: nonceE, HMAC: mac})

	ok := e.read()
	So(ok.Type, ShouldEqual, typeAuthOK)
	var okp authOKPayload
	So(json.Unmarshal(ok.Payload, &okp), ShouldBeNil)
	So(h.crypto.VerifyDaemonHMAC(auth.ModeSession, key, cp.NonceD, nonceE, okp.HMAC), ShouldBeTrue)

	hello := e.read()
	So(hello.Type, ShouldEqual, typeHello)
	return e
}

func TestSessionHandshakeFlow(t *testing.T) {
	Convey("会话模式握手(已配对)", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)

		Convey("正确密钥可完成握手并收到 hello", func() {
			e := h.doSessionHandshake(key)
			So(e, ShouldNotBeNil)
		})

		Convey("错误密钥握手失败,连接被断开", func() {
			e := dial(h.url)
			challenge := e.read()
			var cp authChallengePayload
			_ = json.Unmarshal(challenge.Payload, &cp)
			wrong, _ := auth.NewLongTermKey()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModeSession, wrong, cp.NonceD, nonceE)
			e.write(typeAuthResponse, uuid.NewString(), authResponsePayload{Mode: modeSession, NonceE: nonceE, HMAC: mac})
			// 认证失败:后续读应报错(连接以 1008 关闭,不回显原因)。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := e.ws.Read(ctx)
			So(err, ShouldNotBeNil)
		})
	})
}

func TestPairingHandshakeFlow(t *testing.T) {
	Convey("配对模式握手(首次配对)", t, func() {
		h := startTestServer(t)

		Convey("配对码正确:下发的 K 可解出并已落盘 0600", func() {
			display, err := h.srv.BeginExtPairing()
			So(err, ShouldBeNil)
			So(display, ShouldContainSubstring, "-")

			kpMac, kpEnc, err := h.crypto.DerivePairingKeys(display)
			So(err, ShouldBeNil)

			e := dial(h.url)
			challenge := e.read()
			var cp authChallengePayload
			_ = json.Unmarshal(challenge.Payload, &cp)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModePairing, kpMac, cp.NonceD, nonceE)
			e.write(typeAuthResponse, uuid.NewString(), authResponsePayload{Mode: modePairing, NonceE: nonceE, HMAC: mac})

			ok := e.read()
			So(ok.Type, ShouldEqual, typeAuthOK)
			var okp authOKPayload
			So(json.Unmarshal(ok.Payload, &okp), ShouldBeNil)
			So(okp.Key, ShouldNotBeNil)
			So(h.crypto.VerifyDaemonHMAC(auth.ModePairing, kpMac, cp.NonceD, nonceE, okp.HMAC), ShouldBeTrue)

			k, err := h.crypto.OpenKey(kpEnc, okp.Key.Ciphertext, okp.Key.IV)
			So(err, ShouldBeNil)

			hello := e.read()
			So(hello.Type, ShouldEqual, typeHello)

			// 落盘的长期密钥与扩展解出的一致。
			saved, ok2, err := h.keys.Load()
			So(err, ShouldBeNil)
			So(ok2, ShouldBeTrue)
			So(saved, ShouldResemble, k)
		})

		Convey("配对码错误:握手失败且不下发密钥", func() {
			_, err := h.srv.BeginExtPairing()
			So(err, ShouldBeNil)
			_, wrongEnc, _ := h.crypto.DerivePairingKeys("WRONGWRONG")
			_ = wrongEnc
			wrongMac, _, _ := h.crypto.DerivePairingKeys("WRONGCODE")

			e := dial(h.url)
			challenge := e.read()
			var cp authChallengePayload
			_ = json.Unmarshal(challenge.Payload, &cp)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModePairing, wrongMac, cp.NonceD, nonceE)
			e.write(typeAuthResponse, uuid.NewString(), authResponsePayload{Mode: modePairing, NonceE: nonceE, HMAC: mac})

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, readErr := e.ws.Read(ctx)
			So(readErr, ShouldNotBeNil)
			_, saved, _ := h.keys.Load()
			So(saved, ShouldBeFalse)
		})
	})
}

func TestBlockingCallAndCancel(t *testing.T) {
	Convey("阻塞写调用与断开即作废", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)
		e := h.doSessionHandshake(key)

		type callResult struct {
			resp BridgeResponse
			err  error
		}

		Convey("批准前请求方取消 → 扩展收到同 requestId 的 bridge.cancel", func() {
			callCtx, cancelCall := context.WithCancel(context.Background())
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(callCtx, BridgeRequest{
					ClientID: "sctl-cli",
					Action:   "scripts.install.request",
					Input:    json.RawMessage(`{"url":"x"}`),
				}, true)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Type, ShouldEqual, typeBridgeRequest)
			So(req.RequestID, ShouldNotBeBlank)

			cancelCall()

			cancelMsg := e.read()
			So(cancelMsg.Type, ShouldEqual, typeBridgeCancel)
			So(cancelMsg.RequestID, ShouldEqual, req.RequestID)

			r := <-resCh
			So(r.err, ShouldNotBeNil)
		})

		Convey("扩展如期应答 → Call 返回结果", func() {
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(context.Background(), BridgeRequest{
					ClientID: "sctl-cli",
					Action:   "scripts.list",
					Input:    json.RawMessage(`{}`),
				}, false)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Type, ShouldEqual, typeBridgeRequest)
			e.write(typeBridgeResponse, req.RequestID, BridgeResponse{OK: true, Result: json.RawMessage(`{"scripts":[]}`)})

			r := <-resCh
			So(r.err, ShouldBeNil)
			So(r.resp.OK, ShouldBeTrue)
			So(string(r.resp.Result), ShouldEqual, `{"scripts":[]}`)
		})

		Convey("扩展连接断开 → 在途请求返回 ErrDisconnected", func() {
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(context.Background(), BridgeRequest{
					ClientID: "sctl-cli",
					Action:   "scripts.list",
					Input:    json.RawMessage(`{}`),
				}, false)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Type, ShouldEqual, typeBridgeRequest)
			So(e.ws.Close(websocket.StatusNormalClosure, "bye"), ShouldBeNil)

			r := <-resCh
			So(r.err, ShouldEqual, ErrDisconnected)
		})
	})
}

func TestClientPairingAndSync(t *testing.T) {
	Convey("MCP 客户端配对与 client.sync 广播", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)
		e := h.doSessionHandshake(key)

		Convey("扩展批准 → 铸造令牌、原文不过线、回推 client.sync 镜像", func() {
			handle, err := h.srv.BeginClientPairing("Claude", []string{"scripts:list"})
			So(err, ShouldBeNil)
			So(handle.Code, ShouldNotBeBlank)

			pr := e.read()
			So(pr.Type, ShouldEqual, typePairRequest)
			var prp pairRequestPayload
			So(json.Unmarshal(pr.Payload, &prp), ShouldBeNil)
			So(prp.ClientName, ShouldEqual, "Claude")
			So(len(prp.Code), ShouldEqual, 8)

			e.write(typePairDecision, uuid.NewString(), pairDecisionPayload{
				PairingID:     prp.PairingID,
				Approved:      true,
				GrantedScopes: []string{"scripts:list"},
			})

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			d, err := handle.Await(ctx)
			So(err, ShouldBeNil)
			So(d.Approved, ShouldBeTrue)
			So(d.ClientID, ShouldNotBeBlank)
			So(d.Token, ShouldNotBeBlank)

			sync := e.read()
			So(sync.Type, ShouldEqual, typeClientSync)
			var recs []auth.ClientRecord
			So(json.Unmarshal(sync.Payload, &recs), ShouldBeNil)
			So(len(recs), ShouldEqual, 1)
			So(recs[0].DisplayName, ShouldEqual, "Claude")
			So(recs[0].TokenHash, ShouldEqual, auth.HashToken(d.Token))
			So(strings.Contains(string(sync.Payload), d.Token), ShouldBeFalse)
		})

		Convey("扩展拒绝 → 不铸造令牌", func() {
			handle, err := h.srv.BeginClientPairing("Codex", []string{"scripts:list"})
			So(err, ShouldBeNil)
			pr := e.read()
			var prp pairRequestPayload
			_ = json.Unmarshal(pr.Payload, &prp)
			e.write(typePairDecision, uuid.NewString(), pairDecisionPayload{PairingID: prp.PairingID, Approved: false})

			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			d, err := handle.Await(ctx)
			So(err, ShouldBeNil)
			So(d.Approved, ShouldBeFalse)
			So(h.srv.clients.List(), ShouldBeEmpty)
		})
	})
}

func TestLoopbackOnly(t *testing.T) {
	Convey("仅允许绑定 loopback 地址", t, func() {
		So(validateLoopback("127.0.0.1:8643"), ShouldBeNil)
		So(validateLoopback("[::1]:8643"), ShouldBeNil)
		So(validateLoopback("localhost:8643"), ShouldBeNil)
		So(validateLoopback("0.0.0.0:8643"), ShouldNotBeNil)
		So(validateLoopback("192.168.1.10:8643"), ShouldNotBeNil)
		So(validateLoopback("garbage"), ShouldNotBeNil)
	})
}
