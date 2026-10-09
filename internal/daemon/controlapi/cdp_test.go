package controlapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/daemon/cdpendpoint"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// fakeEndpoints 是端点管理器的桩:记录收到的浏览器目标与 Host,按配置回答。
type fakeEndpoints struct {
	browsers []string
	hosts    []string
	snapshot cdpendpoint.Snapshot
	closed   bool
	err      error
}

func (f *fakeEndpoints) Create(browser, host string) (cdpendpoint.Snapshot, error) {
	f.browsers = append(f.browsers, "create "+browser)
	f.hosts = append(f.hosts, host)
	return f.snapshot, f.err
}

func (f *fakeEndpoints) Status(browser, host string) (cdpendpoint.Snapshot, error) {
	f.browsers = append(f.browsers, "status "+browser)
	f.hosts = append(f.hosts, host)
	return f.snapshot, f.err
}

func (f *fakeEndpoints) Close(_ context.Context, browser string) (cdpendpoint.BrowserRef, bool, error) {
	f.browsers = append(f.browsers, "close "+browser)
	return f.snapshot.Browser, f.closed, f.err
}

func postCDP(mux *http.ServeMux, path, token, host string, body any) (int, control.CallResult) {
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(raw))
	req.Host = host
	if token != "" {
		req.Header.Set(control.HeaderControlToken, token)
	}
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	var res control.CallResult
	_ = json.NewDecoder(rec.Body).Decode(&res)
	return rec.Code, res
}

func TestControlCDPEndpoint(t *testing.T) {
	Convey("/control/cdp/*", t, func() {
		connectedAt := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
		endpoints := &fakeEndpoints{snapshot: cdpendpoint.Snapshot{
			Browser: cdpendpoint.BrowserRef{ID: "inst-work", Name: "work"},
			Endpoint: &cdpendpoint.Info{
				HTTPURL: "http://127.0.0.1:8643/cdp/s", WSURL: "ws://127.0.0.1:8643/cdp/s/devtools/browser/b",
				ClientConnected: true, ConnectedAt: connectedAt,
			},
		}}
		mux := http.NewServeMux()
		New(nil, nil, endpoints, testControlToken, zap.NewNop()).Register(mux)

		Convey("没有控制令牌时 401,不碰端点", func() {
			for _, path := range []string{control.PathCDPEndpoint, control.PathCDPStatus, control.PathCDPClose} {
				code, _ := postCDP(mux, path, "", "127.0.0.1:8643", control.CDPRequest{})
				So(code, ShouldEqual, http.StatusUnauthorized)
			}
			So(endpoints.browsers, ShouldBeEmpty)
		})

		Convey("endpoint 与 status 转发浏览器目标,地址用调用方连到 daemon 的 Host,结果映射为控制 DTO", func() {
			for _, path := range []string{control.PathCDPEndpoint, control.PathCDPStatus} {
				_, res := postCDP(mux, path, testControlToken, "127.0.0.1:9000", control.CDPRequest{Browser: "work"})
				So(res.OK, ShouldBeTrue)
				So(string(res.Result), ShouldEqualJSON, `{"browser":{"id":"inst-work","name":"work"},"endpoint":{`+
					`"httpUrl":"http://127.0.0.1:8643/cdp/s","wsUrl":"ws://127.0.0.1:8643/cdp/s/devtools/browser/b",`+
					`"clientConnected":true,"connectedAt":"2026-10-09T12:00:00Z"}}`)
			}
			So(endpoints.browsers, ShouldResemble, []string{"create work", "status work"})
			So(endpoints.hosts, ShouldResemble, []string{"127.0.0.1:9000", "127.0.0.1:9000"})
		})

		Convey("status 在没有端点时 endpoint 为 null", func() {
			endpoints.snapshot.Endpoint = nil
			_, res := postCDP(mux, control.PathCDPStatus, testControlToken, "127.0.0.1:8643", control.CDPRequest{})
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqualJSON, `{"browser":{"id":"inst-work","name":"work"},"endpoint":null}`)
		})

		Convey("close 报告是否关闭了端点;没有端点时也成功", func() {
			_, res := postCDP(mux, control.PathCDPClose, testControlToken, "127.0.0.1:8643", control.CDPRequest{Browser: "work"})
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqualJSON, `{"browser":{"id":"inst-work","name":"work"},"closed":false}`)
			endpoints.closed = true
			_, res = postCDP(mux, control.PathCDPClose, testControlToken, "127.0.0.1:8643", control.CDPRequest{Browser: "work"})
			So(string(res.Result), ShouldEqualJSON, `{"browser":{"id":"inst-work","name":"work"},"closed":true}`)
		})

		Convey("浏览器目标错误原样成为控制错误", func() {
			endpoints.err = &bridge.Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser work is not connected"}
			for _, path := range []string{control.PathCDPEndpoint, control.PathCDPStatus, control.PathCDPClose} {
				_, res := postCDP(mux, path, testControlToken, "127.0.0.1:8643", control.CDPRequest{Browser: "work"})
				So(res.OK, ShouldBeFalse)
				So(res.Error.Code, ShouldEqual, generated.ErrorCodeBrowserOffline)
				So(res.Error.Message, ShouldEqual, "browser work is not connected")
			}
		})

		Convey("请求体不是 JSON 时 INVALID_REQUEST,不碰端点", func() {
			req := httptest.NewRequest(http.MethodPost, control.PathCDPEndpoint, bytes.NewReader([]byte("{nope")))
			req.Header.Set(control.HeaderControlToken, testControlToken)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, req)
			var res control.CallResult
			So(json.NewDecoder(rec.Body).Decode(&res), ShouldBeNil)
			So(res.Error.Code, ShouldEqual, "INVALID_REQUEST")
			So(endpoints.browsers, ShouldBeEmpty)
		})
	})
}
