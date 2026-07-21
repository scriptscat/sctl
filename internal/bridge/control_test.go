package bridge

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/auth"
	"github.com/scriptscat/sctl/internal/control"
)

const testControlToken = "test-control-token"

// httpBase 从握手用的 ws:// url 推出控制 API 的 http:// 基址(两者同 listener)。
func (h *testHarness) httpBase() string {
	return strings.TrimSuffix(strings.Replace(h.url, "ws://", "http://", 1), "/")
}

// postControl 发起一次控制 API 请求(可选控制令牌 / MCP 客户端令牌)。body 为 nil 时用 GET。
func postControl(ctx context.Context, base, path, controlToken, clientToken string, body any) (*http.Response, error) {
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
	if clientToken != "" {
		req.Header.Set(control.HeaderClientToken, clientToken)
	}
	return http.DefaultClient.Do(req)
}

type callOut struct {
	resp *http.Response
	err  error
}

// goPostControl 在 goroutine 里发起(可能阻塞的)控制请求,把结果回传通道;断言留给测试 goroutine。
func goPostControl(ctx context.Context, base, path, controlToken, clientToken string, body any) <-chan callOut {
	ch := make(chan callOut, 1)
	go func() {
		resp, err := postControl(ctx, base, path, controlToken, clientToken, body)
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

func TestControlHealthAndAuth(t *testing.T) {
	Convey("控制 API 鉴权与健康检查", t, func() {
		h := startTestServer(t)
		h.srv.SetControlToken(testControlToken)
		base := h.httpBase()
		ctx := context.Background()

		Convey("健康检查不需令牌,返回 ok 与版本", func() {
			resp, err := postControl(ctx, base, control.PathHealth, "", "", nil)
			So(err, ShouldBeNil)
			defer resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusOK)
			var hr control.HealthResult
			So(json.NewDecoder(resp.Body).Decode(&hr), ShouldBeNil)
			So(hr.OK, ShouldBeTrue)
			So(hr.Version, ShouldEqual, "0.1.0")
		})

		Convey("缺少控制令牌的 call 被 401 拒绝", func() {
			resp, err := postControl(ctx, base, control.PathCall, "", "", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
			resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})

		Convey("控制令牌错误的 call 被 401 拒绝", func() {
			resp, err := postControl(ctx, base, control.PathCall, "wrong-token", "", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
			resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})
	})
}

func TestControlCallForwarding(t *testing.T) {
	Convey("控制 call 转发到扩展并回传应答", t, func() {
		h := startTestServer(t)
		h.srv.SetControlToken(testControlToken)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		Convey("sctl-cli 身份的读调用:扩展应答后 CallResult.ok=true", func() {
			ch := goPostControl(context.Background(), base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})

			req := e.read()
			So(req.Type, ShouldEqual, typeBridgeRequest)
			var br BridgeRequest
			So(json.Unmarshal(req.Payload, &br), ShouldBeNil)
			So(br.ClientID, ShouldEqual, control.CLIClientID)
			So(br.Action, ShouldEqual, "scripts.list")
			e.write(typeBridgeResponse, req.RequestID, BridgeResponse{OK: true, Result: json.RawMessage(`{"scripts":[]}`)})

			out := <-ch
			So(out.err, ShouldBeNil)
			res := decodeCall(out.resp)
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"scripts":[]}`)
		})

		Convey("未知 action → INVALID_REQUEST", func() {
			resp, err := postControl(context.Background(), base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.nope", Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeFalse)
			So(res.Error.Code, ShouldEqual, "INVALID_REQUEST")
		})
	})
}

func TestControlWriteCancelPropagation(t *testing.T) {
	Convey("写调用阻塞期间请求方取消 → 扩展收到同 requestId 的 bridge.cancel", t, func() {
		h := startTestServer(t)
		h.srv.SetControlToken(testControlToken)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		callCtx, cancelCall := context.WithCancel(context.Background())
		ch := goPostControl(callCtx, base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.install.request", Input: json.RawMessage(`{"url":"https://x/y.user.js"}`)})

		// 扩展收到 bridge.request(阻塞审批中,不应答)。
		req := e.read()
		So(req.Type, ShouldEqual, typeBridgeRequest)
		So(req.RequestID, ShouldNotBeBlank)

		// 请求方 Ctrl-C:取消 HTTP 请求。
		cancelCall()

		// 扩展应收到同 requestId 的 bridge.cancel(daemon 作废该操作)。
		cancelMsg := e.read()
		So(cancelMsg.Type, ShouldEqual, typeBridgeCancel)
		So(cancelMsg.RequestID, ShouldEqual, req.RequestID)

		// 客户端侧 Do 返回被取消错误。
		out := <-ch
		So(out.err, ShouldNotBeNil)
	})
}

func TestControlClientTokenScope(t *testing.T) {
	Convey("携带 MCP 客户端令牌:scope 决定放行/拒绝", t, func() {
		h := startTestServer(t)
		h.srv.SetControlToken(testControlToken)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		_, token, _, err := h.srv.clients.Mint("Claude", []string{"scripts:list"})
		So(err, ShouldBeNil)

		Convey("scope 命中的读调用被放行,并以真实 clientId 转发", func() {
			ch := goPostControl(context.Background(), base, control.PathCall, testControlToken, token, control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			req := e.read()
			var br BridgeRequest
			So(json.Unmarshal(req.Payload, &br), ShouldBeNil)
			So(br.ClientID, ShouldNotEqual, control.CLIClientID) // 真实 clientId,非内建
			e.write(typeBridgeResponse, req.RequestID, BridgeResponse{OK: true, Result: json.RawMessage(`{"scripts":[]}`)})
			out := <-ch
			So(out.err, ShouldBeNil)
			So(decodeCall(out.resp).OK, ShouldBeTrue)
		})

		Convey("scope 缺失的写调用被 INSUFFICIENT_SCOPE 拒绝(不转发给扩展)", func() {
			resp, err := postControl(context.Background(), base, control.PathCall, testControlToken, token, control.CallRequest{Action: "scripts.delete.request", Input: json.RawMessage(`{"uuid":"x"}`)})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeFalse)
			So(res.Error.Code, ShouldEqual, "INSUFFICIENT_SCOPE")
		})

		Convey("无效客户端令牌 → UNAUTHENTICATED", func() {
			resp, err := postControl(context.Background(), base, control.PathCall, testControlToken, "bogus", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeFalse)
			So(res.Error.Code, ShouldEqual, "UNAUTHENTICATED")
		})

		Convey("whoami 解析客户端令牌返回 scope", func() {
			resp, err := postControl(context.Background(), base, control.PathWhoami, testControlToken, token, nil)
			So(err, ShouldBeNil)
			defer resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusOK)
			var who control.WhoamiResult
			So(json.NewDecoder(resp.Body).Decode(&who), ShouldBeNil)
			So(who.DisplayName, ShouldEqual, "Claude")
			So(who.Scopes, ShouldResemble, []string{"scripts:list"})
		})
	})
}

func TestControlPairClientStream(t *testing.T) {
	Convey("控制 pair-client 流:先推配对码,扩展批准后推裁决", t, func() {
		h := startTestServer(t)
		h.srv.SetControlToken(testControlToken)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		// pair-client 是流式:Do 返回后先解出配对码事件(daemon 已同步向扩展推 pair.request)。
		resp, err := postControl(context.Background(), base, control.PathPairClient, testControlToken, "",
			control.PairClientRequest{ClientName: "Claude", Scopes: []string{"scripts:list"}})
		So(err, ShouldBeNil)
		defer resp.Body.Close()
		So(resp.StatusCode, ShouldEqual, http.StatusOK)
		dec := json.NewDecoder(resp.Body)

		var first control.PairClientEvent
		So(dec.Decode(&first), ShouldBeNil)
		So(first.Code, ShouldNotBeBlank)

		// 扩展收到 pair.request,回批准裁决。
		pr := e.read()
		So(pr.Type, ShouldEqual, typePairRequest)
		var prp pairRequestPayload
		So(json.Unmarshal(pr.Payload, &prp), ShouldBeNil)
		e.write(typePairDecision, uuid.NewString(), pairDecisionPayload{
			PairingID:     prp.PairingID,
			Approved:      true,
			GrantedScopes: []string{"scripts:list"},
		})

		// 第二个事件是裁决,携带铸造出的 clientId/token。
		var second control.PairClientEvent
		So(dec.Decode(&second), ShouldBeNil)
		So(second.Decision, ShouldNotBeNil)
		So(second.Decision.Approved, ShouldBeTrue)
		So(second.Decision.ClientID, ShouldNotBeBlank)
		So(second.Decision.Token, ShouldNotBeBlank)
	})
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
