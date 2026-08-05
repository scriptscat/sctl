package controlapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestControlHealthAndAuth(t *testing.T) {
	Convey("控制 API 鉴权与健康检查", t, func() {
		h := startTestServer(t)
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
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		Convey("sctl-cli 身份的读调用:扩展应答后 CallResult.ok=true", func() {
			ch := goPostControl(context.Background(), base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})

			req := e.read()
			So(req.Method, ShouldEqual, "scripts.list")
			var br rpcRequestParams
			So(json.Unmarshal(req.Params, &br), ShouldBeNil)
			So(br.ClientID, ShouldEqual, control.CLIClientID)
			e.writeResult(req.ID, json.RawMessage(`{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`))

			out := <-ch
			So(out.err, ShouldBeNil)
			res := decodeCall(out.resp)
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`)
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
	Convey("写调用阻塞期间请求方取消 → 扩展收到指向原请求 ID 的 $/cancelRequest", t, func() {
		h := startTestServer(t)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		callCtx, cancelCall := context.WithCancel(context.Background())
		ch := goPostControl(callCtx, base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.install.request", Input: json.RawMessage(`{"url":"https://x/y.user.js"}`)})

		// 扩展收到业务请求(阻塞审批中,不应答)。
		req := e.read()
		So(req.Method, ShouldEqual, "scripts.install.request")
		So(req.ID, ShouldNotBeBlank)

		// 请求方 Ctrl-C:取消 HTTP 请求。
		cancelCall()

		// 扩展应收到指向原请求 ID 的 $/cancelRequest(daemon 作废该操作)。
		cancelMsg := e.read()
		So(cancelMsg.Method, ShouldEqual, methodCancel)
		var cancelled cancelParams
		So(json.Unmarshal(cancelMsg.Params, &cancelled), ShouldBeNil)
		So(cancelled.ID, ShouldEqual, req.ID)

		// 客户端侧 Do 返回被取消错误。
		out := <-ch
		So(out.err, ShouldNotBeNil)
	})
}

func TestControlClientLabelForwarding(t *testing.T) {
	Convey("扁平信任:控制令牌即全部能力,客户端标签只作审计归因", t, func() {
		h := startTestServer(t)
		key, err := newKeyAndSave(h)
		So(err, ShouldBeNil)
		e := h.doSessionHandshake(key)
		base := h.httpBase()

		Convey("带客户端标签的调用被放行,并以该标签作为 clientId 转发", func() {
			ch := goPostControl(context.Background(), base, control.PathCall, testControlToken, "scriptcat-claude", control.CallRequest{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			req := e.read()
			var br rpcRequestParams
			So(json.Unmarshal(req.Params, &br), ShouldBeNil)
			So(br.ClientID, ShouldEqual, "scriptcat-claude")
			e.writeResult(req.ID, json.RawMessage(`{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`))
			out := <-ch
			So(out.err, ShouldBeNil)
			So(decodeCall(out.resp).OK, ShouldBeTrue)
		})

		Convey("无标签的写调用被放行,并以内建 sctl-cli 标签转发(授权在扩展审批闸门)", func() {
			ch := goPostControl(context.Background(), base, control.PathCall, testControlToken, "", control.CallRequest{Action: "scripts.delete.request", Input: json.RawMessage(`{"uuid":"x"}`)})
			req := e.read()
			var br rpcRequestParams
			So(json.Unmarshal(req.Params, &br), ShouldBeNil)
			So(br.ClientID, ShouldEqual, control.CLIClientID)
			So(req.Method, ShouldEqual, "scripts.delete.request")
			e.writeResult(req.ID, json.RawMessage(`{"uuid":"x","deleted":true}`))
			out := <-ch
			So(out.err, ShouldBeNil)
			So(decodeCall(out.resp).OK, ShouldBeTrue)
		})
	})
}

func TestControlEnroll(t *testing.T) {
	Convey("控制 enroll:打开接入窗口并返回展示形配对码", t, func() {
		h := startTestServer(t)
		base := h.httpBase()

		resp, err := postControl(context.Background(), base, control.PathEnroll, testControlToken, "", struct{}{})
		So(err, ShouldBeNil)
		defer resp.Body.Close()
		So(resp.StatusCode, ShouldEqual, http.StatusOK)
		var res control.EnrollResult
		So(json.NewDecoder(resp.Body).Decode(&res), ShouldBeNil)
		So(res.Code, ShouldContainSubstring, "-")
	})
}
