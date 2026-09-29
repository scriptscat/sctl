package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
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

func decodeBrowsers(resp *http.Response) control.BrowsersResult {
	defer resp.Body.Close()
	var res control.BrowsersResult
	So(json.NewDecoder(resp.Body).Decode(&res), ShouldBeNil)
	return res
}

func browserByID(list []control.BrowserInfo, id string) (control.BrowserInfo, bool) {
	for _, b := range list {
		if b.ID == id {
			return b, true
		}
	}
	return control.BrowserInfo{}, false
}

func TestControlBrowsersList(t *testing.T) {
	Convey("控制 API 列出已配对浏览器实例,在线与离线均含", t, func() {
		h := startTestServer(t)
		base := h.httpBase()
		h.pairedBrowser(instanceA, "chrome-a")
		h.connectBrowser(instanceB, "chrome-b")

		resp, err := postControl(context.Background(), base, control.PathBrowsers, testControlToken, "", nil)
		So(err, ShouldBeNil)
		So(resp.StatusCode, ShouldEqual, http.StatusOK)
		res := decodeBrowsers(resp)

		So(res.Browsers, ShouldHaveLength, 2)
		offline, ok := browserByID(res.Browsers, instanceA)
		So(ok, ShouldBeTrue)
		So(offline.Name, ShouldEqual, "chrome-a")
		So(offline.Online, ShouldBeFalse)
		So(offline.ConnectedAt.IsZero(), ShouldBeTrue)

		online, ok := browserByID(res.Browsers, instanceB)
		So(ok, ShouldBeTrue)
		So(online.Name, ShouldEqual, "chrome-b")
		So(online.Online, ShouldBeTrue)
		So(online.ConnectedAt.IsZero(), ShouldBeFalse)

		Convey("缺少控制令牌时被 401 拒绝", func() {
			resp, err := postControl(context.Background(), base, control.PathBrowsers, "", "", nil)
			So(err, ShouldBeNil)
			resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})
	})
}

func TestControlBrowserForget(t *testing.T) {
	Convey("控制 API 忘记一个已配对浏览器实例", t, func() {
		h := startTestServer(t)
		base := h.httpBase()

		Convey("按名称忘记一个离线实例:删除后不再出现在列表里", func() {
			h.pairedBrowser(instanceA, "chrome-a")

			resp, err := postControl(context.Background(), base, control.PathBrowserForget, testControlToken, "", control.ForgetBrowserRequest{Ref: "chrome-a"})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeTrue)

			listResp, err := postControl(context.Background(), base, control.PathBrowsers, testControlToken, "", nil)
			So(err, ShouldBeNil)
			So(decodeBrowsers(listResp).Browsers, ShouldHaveLength, 0)
		})

		Convey("按完整实例 ID 忘记一个在线实例:断开连接并从列表移除", func() {
			e := h.connectBrowser(instanceB, "chrome-b")

			resp, err := postControl(context.Background(), base, control.PathBrowserForget, testControlToken, "", control.ForgetBrowserRequest{Ref: instanceB})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeTrue)

			// 忘记后连接被服务端关闭:下一次读取以错误收尾,而不是继续收到消息。
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, err = e.ws.Read(ctx)
			So(err, ShouldNotBeNil)

			listResp, err := postControl(context.Background(), base, control.PathBrowsers, testControlToken, "", nil)
			So(err, ShouldBeNil)
			So(decodeBrowsers(listResp).Browsers, ShouldHaveLength, 0)
		})

		Convey("目标不匹配任何已配对实例 → BROWSER_NOT_FOUND,与路由时的目标错误码一致", func() {
			resp, err := postControl(context.Background(), base, control.PathBrowserForget, testControlToken, "", control.ForgetBrowserRequest{Ref: "nope"})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeFalse)
			So(res.Error.Code, ShouldEqual, generated.ErrorCodeBrowserNotFound)
		})

		Convey("缺少 ref 字段 → INVALID_REQUEST", func() {
			resp, err := postControl(context.Background(), base, control.PathBrowserForget, testControlToken, "", control.ForgetBrowserRequest{})
			So(err, ShouldBeNil)
			res := decodeCall(resp)
			So(res.OK, ShouldBeFalse)
			So(res.Error.Code, ShouldEqual, "INVALID_REQUEST")
		})

		Convey("缺少控制令牌时被 401 拒绝", func() {
			resp, err := postControl(context.Background(), base, control.PathBrowserForget, "", "", control.ForgetBrowserRequest{Ref: "chrome-a"})
			So(err, ShouldBeNil)
			resp.Body.Close()
			So(resp.StatusCode, ShouldEqual, http.StatusUnauthorized)
		})
	})
}

func TestControlStatusIncludesBrowsers(t *testing.T) {
	Convey("控制 API 的 status 附带已配对浏览器实例概览", t, func() {
		h := startTestServer(t)
		base := h.httpBase()
		h.pairedBrowser(instanceA, "chrome-a")
		h.connectBrowser(instanceB, "chrome-b")

		resp, err := postControl(context.Background(), base, control.PathStatus, testControlToken, "", nil)
		So(err, ShouldBeNil)
		defer resp.Body.Close()
		var st control.StatusResult
		So(json.NewDecoder(resp.Body).Decode(&st), ShouldBeNil)

		So(st.Browsers, ShouldHaveLength, 2)
		offline, ok := browserByID(st.Browsers, instanceA)
		So(ok, ShouldBeTrue)
		So(offline.Online, ShouldBeFalse)
		online, ok := browserByID(st.Browsers, instanceB)
		So(ok, ShouldBeTrue)
		So(online.Online, ShouldBeTrue)
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

// forgetFailingBridge 是只实现 ForgetInstance 的 Bridge 桩:登记表写盘失败时返回 cause。
type forgetFailingBridge struct {
	Bridge
	cause error
}

func (b forgetFailingBridge) ForgetInstance(string) error { return b.cause }

func TestControlBrowserForgetLogsRegistryFailure(t *testing.T) {
	Convey("忘记浏览器时登记表写不进去:调用方收到 INTERNAL_ERROR,daemon 日志记下真实原因", t, func() {
		core, logs := observer.New(zap.WarnLevel)
		cause := errors.New("write browsers.json: no space left on device")
		mux := http.NewServeMux()
		New(forgetFailingBridge{cause: cause}, testControlToken, zap.New(core)).Register(mux)

		body, err := json.Marshal(control.ForgetBrowserRequest{Ref: "chrome-a"})
		So(err, ShouldBeNil)
		req := httptest.NewRequest(http.MethodPost, control.PathBrowserForget, bytes.NewReader(body))
		req.Header.Set(control.HeaderControlToken, testControlToken)
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, req)

		var res control.CallResult
		So(json.NewDecoder(rec.Body).Decode(&res), ShouldBeNil)
		So(res.OK, ShouldBeFalse)
		So(res.Error.Code, ShouldEqual, "INTERNAL_ERROR")
		logged := logs.FilterFieldKey("error").All()
		So(logged, ShouldHaveLength, 1)
		So(logged[0].ContextMap()["error"], ShouldEqual, cause.Error())
	})
}
