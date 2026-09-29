package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocolschema"
)

// 本包的测试跑真实全栈:bridge.Server + Handler 挂同一 listener,由 extClient 从 WS 面对拍。
// 扩展侧一律按线上协议(docs/protocol.md)自己拼信封,不借 bridge 的未导出符号 —— 这样这些
// 测试同时也在守护「daemon 对扩展呈现的样子」。
const (
	testControlToken    = "test-control-token"
	testVersion         = "0.1.0"
	methodAuthenticate  = "$session.authenticate"
	methodAuthenticated = "$session.authenticated"
	methodHello         = "$session.hello"
	methodCapabilities  = "$session.capabilities"
	methodCancel        = "$/cancelRequest"

	modeSession = "session"
)

type authChallengePayload struct {
	NonceD string `json:"nonceD"`
}

type authResponsePayload struct {
	Mode   string           `json:"mode"`
	NonceE string           `json:"nonceE"`
	HMAC   string           `json:"hmac"`
	Peer   *authPeerPayload `json:"peer,omitempty"`
}

type authPeerPayload struct {
	Kind       string `json:"kind"`
	InstanceID string `json:"instanceId"`
}

type authOKPayload struct {
	HMAC string `json:"hmac"`
}

type rpcRequestParams struct {
	Input    json.RawMessage `json:"input"`
	ClientID string          `json:"clientId"`
}

type cancelParams struct {
	ID string `json:"id"`
}

// testHarness 承载一个运行中的 daemon(WS 面 + 控制 API)与用于对拍的密码学助手/存储。
type testHarness struct {
	srv      *bridge.Server
	crypto   *auth.Crypto
	keys     *store.KeyStore
	browsers *store.BrowserRegistry
	url      string
	proto    *protocol.Protocol
}

func startTestServer(t *testing.T) *testHarness {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	dir := t.TempDir()
	keys := store.NewKeyStore(filepath.Join(dir, "pairing.key"))
	browsers, err := store.LoadBrowserRegistry(filepath.Join(dir, "browsers.json"))
	So(err, ShouldBeNil)
	srv := bridge.NewServer(testVersion, p, keys, browsers, zap.NewNop())
	// daemon 首次校验帧时才编译 schema,这笔一次性开销在高负载下能拖过读消息的 3s 时限;先在时限外付掉。
	So(protocolschema.ValidateWireFrame([]byte(`{"jsonrpc":"2.0","id":"warm-up","method":"$session.ping","params":{}}`)), ShouldBeNil)

	mux := http.NewServeMux()
	New(srv, testControlToken, zap.NewNop()).Register(mux)

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = srv.Serve(ctx, ln, mux) }()

	return &testHarness{
		srv:      srv,
		crypto:   auth.NewCrypto(p),
		keys:     keys,
		browsers: browsers,
		url:      "ws://" + ln.Addr().String() + "/",
		proto:    p,
	}
}

// httpBase 从握手用的 ws:// url 推出控制 API 的 http:// 基址(两者同 listener)。
func (h *testHarness) httpBase() string {
	return strings.TrimSuffix(strings.Replace(h.url, "ws://", "http://", 1), "/")
}

// newKeyAndSave 生成并落盘长期密钥,返回它(供会话握手用)。
func newKeyAndSave(h *testHarness) ([]byte, error) {
	key, err := auth.NewLongTermKey()
	if err != nil {
		return nil, err
	}
	if err := h.keys.Save(key); err != nil {
		return nil, err
	}
	return key, nil
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

func (e *extClient) read() bridge.Message {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, data, err := e.ws.Read(ctx)
	So(err, ShouldBeNil)
	var env bridge.Message
	So(json.Unmarshal(data, &env), ShouldBeNil)
	return env
}

func (e *extClient) writeRequest(method, id string, params any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := json.Marshal(params)
	So(err, ShouldBeNil)
	env := bridge.Message{JSONRPC: "2.0", ID: id, Method: method, Params: raw}
	So(wsjson.Write(ctx, e.ws, env), ShouldBeNil)
}

func (e *extClient) writeResult(id string, result any) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	raw, err := json.Marshal(result)
	So(err, ShouldBeNil)
	So(wsjson.Write(ctx, e.ws, bridge.Message{JSONRPC: "2.0", ID: id, Result: raw}), ShouldBeNil)
}

// writeError 以应用错误(-32000,领域码放在 data.code)应答请求 id。
func (e *extClient) writeError(id, code, message string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	frame := map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": -32000, "message": message, "data": map[string]any{"code": code}}}
	So(wsjson.Write(ctx, e.ws, frame), ShouldBeNil)
}

// doSessionHandshake 以给定长期密钥完成会话握手,并消费 hello。
func (h *testHarness) doSessionHandshake(key []byte) *extClient {
	e := dial(h.url)
	challenge := e.read()
	So(challenge.Method, ShouldEqual, methodAuthenticate)
	var cp authChallengePayload
	So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)

	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ExtHMAC(auth.ModeSession, key, cp.NonceD, nonceE)
	e.writeResult(challenge.ID, authResponsePayload{Mode: modeSession, NonceE: nonceE, HMAC: mac})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authOKPayload
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(h.crypto.VerifyDaemonHMAC(auth.ModeSession, key, cp.NonceD, nonceE, okp.HMAC), ShouldBeTrue)

	hello := e.read()
	So(hello.Method, ShouldEqual, methodHello)
	methods := make([]string, 0, len(h.proto.Actions))
	for method := range h.proto.Actions {
		methods = append(methods, method)
	}
	capabilitiesID := uuid.NewString()
	e.writeRequest(methodCapabilities, capabilitiesID, map[string]any{"schemaVersion": "1.0.0", "methods": methods})
	So(e.read().ID, ShouldEqual, capabilitiesID)
	return e
}

// browserMethods 是浏览器实例在能力声明里给出的方法。
var browserMethods = []string{
	"tabs.list", "tabs.open", "tabs.close", "tabs.activate", "windows.list",
	"readingList.list", "readingList.add", "readingList.markRead", "readingList.remove", "bookmarks.remove",
}

// pairedBrowser 在登记表里放一个已配对实例并返回其密钥;不连接。
func (h *testHarness) pairedBrowser(id, name string) []byte {
	key, err := auth.NewLongTermKey()
	So(err, ShouldBeNil)
	So(h.browsers.Pair(store.BrowserInstance{ID: id, Name: name, Key: key}), ShouldBeNil)
	return key
}

// connectBrowser 配对并按线上协议连接一个浏览器实例,返回完成能力声明的连接。
func (h *testHarness) connectBrowser(id, name string) *extClient {
	key := h.pairedBrowser(id, name)
	e := dial(h.url)
	challenge := e.read()
	So(challenge.Method, ShouldEqual, methodAuthenticate)
	var cp authChallengePayload
	So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)

	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.BrowserExtHMAC(auth.ModeSession, id, key, cp.NonceD, nonceE)
	e.writeResult(challenge.ID, authResponsePayload{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: &authPeerPayload{Kind: "browser", InstanceID: id}})
	So(e.read().Method, ShouldEqual, methodAuthenticated)
	So(e.read().Method, ShouldEqual, methodHello)

	capabilitiesID := uuid.NewString()
	e.writeRequest(methodCapabilities, capabilitiesID, map[string]any{
		"schemaVersion": "1.0.0",
		"methods":       browserMethods,
		"peer":          map[string]any{"name": name},
	})
	reply := e.read()
	So(reply.ID, ShouldEqual, capabilitiesID)
	So(reply.Error, ShouldBeNil)
	return e
}

// postControl 发起一次控制 API 请求(可选控制令牌 / 客户端标签)。body 为 nil 时用 GET。
func postControl(ctx context.Context, base, path, controlToken, clientLabel string, body any) (*http.Response, error) {
	var r io.Reader
	method := http.MethodGet
	if body != nil {
		raw, _ := json.Marshal(body)
		r = bytes.NewReader(raw)
		method = http.MethodPost
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, r)
	if err != nil {
		return nil, err
	}
	if controlToken != "" {
		req.Header.Set(control.HeaderControlToken, controlToken)
	}
	if clientLabel != "" {
		req.Header.Set(control.HeaderClientLabel, clientLabel)
	}
	return http.DefaultClient.Do(req)
}

type callOut struct {
	resp *http.Response
	err  error
}

// goPostControl 在 goroutine 里发起(可能阻塞的)控制请求,把结果回传通道;断言留给测试 goroutine。
func goPostControl(ctx context.Context, base, path, controlToken, clientLabel string, body any) <-chan callOut {
	ch := make(chan callOut, 1)
	go func() {
		resp, err := postControl(ctx, base, path, controlToken, clientLabel, body)
		ch <- callOut{resp, err}
	}()
	return ch
}

func decodeCall(resp *http.Response) control.CallResult {
	defer resp.Body.Close()
	var res control.CallResult
	So(json.NewDecoder(resp.Body).Decode(&res), ShouldBeNil)
	return res
}
