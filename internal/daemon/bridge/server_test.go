package bridge

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/audit"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// testHarness 承载一个运行中的 daemon 与用于对拍的密码学助手/存储。
type testHarness struct {
	srv    *Server
	crypto *auth.Crypto
	keys   *store.KeyStore
	url    string
	proto  *protocol.Protocol
}

func startTestServer(t *testing.T) *testHarness {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	dir := t.TempDir()
	keys := store.NewKeyStore(filepath.Join(dir, "pairing.key"))
	srv := NewServer("0.1.0", p, keys, zap.NewNop())

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln, nil) }()

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

func (e *extClient) read() Message {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := e.ws.Read(ctx)
	So(err, ShouldBeNil)
	var env Message
	So(json.Unmarshal(data, &env), ShouldBeNil)
	return env
}

func (e *extClient) writeRequest(method, id string, params any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	env, err := newRequest(id, method, params)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, env), ShouldBeNil)
}

func (e *extClient) writeResult(id string, result any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	message, err := newResult(id, result)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, message), ShouldBeNil)
}

// doSessionHandshake 以给定长期密钥完成会话握手,并消费 hello。
func (h *testHarness) doSessionHandshake(key []byte) *extClient {
	e := dial(h.url)
	challenge := e.read()
	So(challenge.Method, ShouldEqual, methodAuthenticate)
	var cp authChallengeParams
	So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)

	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ExtHMAC(auth.ModeSession, key, cp.NonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authenticatedParams
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(h.crypto.VerifyDaemonHMAC(auth.ModeSession, key, cp.NonceD, nonceE, okp.HMAC), ShouldBeTrue)

	hello := e.read()
	So(hello.Method, ShouldEqual, methodHello)
	methods := make([]string, 0, len(h.proto.Actions))
	for method := range h.proto.Actions {
		methods = append(methods, method)
	}
	capabilitiesID := uuid.NewString()
	e.writeRequest(methodCapabilities, capabilitiesID, capabilitiesParams{SchemaVersion: "1.0.0", Methods: methods})
	So(e.read().ID, ShouldEqual, capabilitiesID)
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
			var cp authChallengeParams
			_ = json.Unmarshal(challenge.Params, &cp)
			wrong, _ := auth.NewLongTermKey()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModeSession, wrong, cp.NonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})
			// 认证失败:后续读应报错(连接以 1008 关闭,不回显原因)。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := e.ws.Read(ctx)
			So(err, ShouldNotBeNil)
		})

		Convey("扩展声明不兼容 schemaVersion 时连接不进入可用状态", func() {
			e := dial(h.url)
			challenge := e.read()
			var cp authChallengeParams
			So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModeSession, key, cp.NonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})
			So(e.read().Method, ShouldEqual, methodAuthenticated)
			So(e.read().Method, ShouldEqual, methodHello)

			e.writeRequest(methodCapabilities, uuid.NewString(), capabilitiesParams{
				SchemaVersion: "999.0.0",
				Methods:       []string{"scripts.list"},
			})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err := e.ws.Read(ctx)
			So(err, ShouldNotBeNil)
			h.srv.mu.Lock()
			active := h.srv.active
			h.srv.mu.Unlock()
			So(active, ShouldBeNil)
		})
	})
}

func TestHeartbeatClosesAnUnresponsiveExtension(t *testing.T) {
	Convey("扩展不响应 daemon 主动 ping 时在硬超时后断开", t, func() {
		h := startTestServer(t)
		h.srv.pingInterval = 20 * time.Millisecond
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)
		e := h.doSessionHandshake(key)

		ping := e.read()
		So(ping.Method, ShouldEqual, methodPing)
		So(ping.ID, ShouldNotBeBlank)

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, _, err := e.ws.Read(ctx)
		So(err, ShouldNotBeNil)
	})
}

func TestPairingHandshakeFlow(t *testing.T) {
	Convey("配对模式握手(首次配对)", t, func() {
		h := startTestServer(t)

		Convey("配对码正确:下发的 K 可解出并已落盘 0600", func() {
			display, err := h.srv.BeginEnrollment()
			So(err, ShouldBeNil)
			So(display, ShouldContainSubstring, "-")

			kpMac, kpEnc, err := h.crypto.DerivePairingKeys(display)
			So(err, ShouldBeNil)

			e := dial(h.url)
			challenge := e.read()
			var cp authChallengeParams
			_ = json.Unmarshal(challenge.Params, &cp)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModePairing, kpMac, cp.NonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac})

			ok := e.read()
			So(ok.Method, ShouldEqual, methodAuthenticated)
			var okp authenticatedParams
			So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
			So(okp.Key, ShouldNotBeNil)
			So(h.crypto.VerifyDaemonHMAC(auth.ModePairing, kpMac, cp.NonceD, nonceE, okp.HMAC), ShouldBeTrue)

			k, err := h.crypto.OpenKey(kpEnc, okp.Key.Ciphertext, okp.Key.IV)
			So(err, ShouldBeNil)

			hello := e.read()
			So(hello.Method, ShouldEqual, methodHello)

			// 落盘的长期密钥与扩展解出的一致。
			saved, ok2, err := h.keys.Load()
			So(err, ShouldBeNil)
			So(ok2, ShouldBeTrue)
			So(saved, ShouldResemble, k)
		})

		Convey("配对码错误:握手失败且不下发密钥", func() {
			_, err := h.srv.BeginEnrollment()
			So(err, ShouldBeNil)
			_, wrongEnc, _ := h.crypto.DerivePairingKeys("WRONGWRONG")
			_ = wrongEnc
			wrongMac, _, _ := h.crypto.DerivePairingKeys("WRONGCODE")

			e := dial(h.url)
			challenge := e.read()
			var cp authChallengeParams
			_ = json.Unmarshal(challenge.Params, &cp)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModePairing, wrongMac, cp.NonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac})

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
			resp Response
			err  error
		}

		Convey("批准前请求方取消 → 扩展收到指向原请求 ID 的 $/cancelRequest", func() {
			callCtx, cancelCall := context.WithCancel(context.Background())
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(callCtx, Request{
					ClientID: "sctl-cli",
					Action:   "scripts.install.request",
					Input:    json.RawMessage(`{"url":"x"}`),
				}, true)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Method, ShouldEqual, "scripts.install.request")
			So(req.ID, ShouldNotBeBlank)

			cancelCall()

			cancelMsg := e.read()
			So(cancelMsg.Method, ShouldEqual, methodCancel)
			var cancelled cancelParams
			So(json.Unmarshal(cancelMsg.Params, &cancelled), ShouldBeNil)
			So(cancelled.ID, ShouldEqual, req.ID)

			r := <-resCh
			So(r.err, ShouldNotBeNil)
		})

		Convey("扩展如期应答 → Call 返回结果", func() {
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(context.Background(), Request{
					ClientID: "sctl-cli",
					Action:   "scripts.list",
					Input:    json.RawMessage(`{}`),
				}, false)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Method, ShouldEqual, "scripts.list")
			e.writeResult(req.ID, json.RawMessage(`{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`))

			r := <-resCh
			So(r.err, ShouldBeNil)
			So(r.resp.OK, ShouldBeTrue)
			So(string(r.resp.Result), ShouldEqual, `{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`)
		})

		Convey("扩展连接断开 → 在途请求返回 ErrDisconnected", func() {
			resCh := make(chan callResult, 1)
			go func() {
				resp, err := h.srv.Call(context.Background(), Request{
					ClientID: "sctl-cli",
					Action:   "scripts.list",
					Input:    json.RawMessage(`{}`),
				}, false)
				resCh <- callResult{resp, err}
			}()

			req := e.read()
			So(req.Method, ShouldEqual, "scripts.list")
			So(e.ws.Close(websocket.StatusNormalClosure, "bye"), ShouldBeNil)

			r := <-resCh
			So(r.err, ShouldEqual, ErrDisconnected)
		})
	})
}

func TestOriginWhitelist(t *testing.T) {
	Convey("Origin 白名单廉价挡掉普通网页直连(握手仍是真正闸门)", t, func() {
		h := startTestServer(t)

		dialOrigin := func(origin string) error {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			ws, _, err := websocket.Dial(ctx, h.url, &websocket.DialOptions{
				HTTPHeader: http.Header{"Origin": []string{origin}},
			})
			if ws != nil {
				_ = ws.Close(websocket.StatusNormalClosure, "")
			}
			return err
		}

		Convey("http(s):// 网页 Origin 被拒(WS 升级失败)", func() {
			So(dialOrigin("http://evil.example"), ShouldNotBeNil)
		})

		Convey("扩展 Origin(chrome-extension://)放行", func() {
			So(dialOrigin("chrome-extension://abcdefghijklmnop"), ShouldBeNil)
		})

		Convey("无 Origin(非浏览器进程)放行,由握手兜底", func() {
			e := dial(h.url)
			So(e.read().Method, ShouldEqual, methodAuthenticate)
		})

		Convey("被拒的网页连接记录为 origin.rejected 审计事件", func() {
			_ = dialOrigin("http://evil.example")
			deadline := time.Now().Add(3 * time.Second)
			var found bool
			for time.Now().Before(deadline) {
				for _, ev := range h.srv.audit.Snapshot() {
					if ev.Type == audit.TypeOriginRejected {
						found = true
					}
				}
				if found {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			So(found, ShouldBeTrue)
		})
	})
}
