package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDialDoesNotStartDaemon(t *testing.T) {
	Convey("daemon 未运行时只报告不可达,不尝试启动 serve", t, func() {
		SetAddress("127.0.0.1:1")
		t.Cleanup(func() { SetAddress("") })

		_, err := Dial(context.Background())

		So(errors.Is(err, ErrDaemonUnreachable), ShouldBeTrue)
	})
}

func TestResolveBaseURL(t *testing.T) {
	Convey("控制端点地址解析", t, func() {
		Convey("显式地址覆盖默认端口", func() {
			SetAddress("127.0.0.1:9999")
			defer SetAddress("")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:9999")
		})

		Convey("未设置时回退协议默认端口 8643", func() {
			SetAddress("")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:8643")
		})
	})
}

// captureCalls 起一个模拟控制服务,记录每次 /control/call 的原始请求体并回 ok。
func captureCalls(t *testing.T) (*Client, *[]map[string]json.RawMessage) {
	t.Helper()
	var bodies []map[string]json.RawMessage
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		_ = json.NewDecoder(r.Body).Decode(&body)
		bodies = append(bodies, body)
		_, _ = w.Write([]byte(`{"ok":true,"result":{"tabId":1}}`))
	}))
	t.Cleanup(srv.Close)
	return &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}, &bodies
}

func TestClientBrowsersListsPairedInstances(t *testing.T) {
	Convey("Browsers 从 /control/browsers 取回已配对实例列表", t, func() {
		var gotPath string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotPath = r.URL.Path
			_ = json.NewEncoder(w).Encode(BrowsersResult{Browsers: []BrowserInfo{
				{ID: "abc", Name: "chrome-abcd", Online: true, Product: "Chrome", ProductVersion: "128.0", ExtensionVersion: "0.1.0"},
			}})
		}))
		t.Cleanup(srv.Close)
		c := &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}

		list, err := c.Browsers(context.Background())

		So(err, ShouldBeNil)
		So(gotPath, ShouldEqual, PathBrowsers)
		So(list, ShouldHaveLength, 1)
		So(list[0].Name, ShouldEqual, "chrome-abcd")
		So(list[0].Online, ShouldBeTrue)
	})
}

func TestClientForgetBrowserRoundTrip(t *testing.T) {
	Convey("ForgetBrowser 把目标引用发给 /control/browsers/forget", t, func() {
		var gotPath string
		var gotBody ForgetBrowserRequest

		Convey("daemon 确认删除时返回 nil", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotPath = r.URL.Path
				_ = json.NewDecoder(r.Body).Decode(&gotBody)
				_ = json.NewEncoder(w).Encode(CallResult{OK: true})
			}))
			t.Cleanup(srv.Close)
			c := &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}

			err := c.ForgetBrowser(context.Background(), "chrome-abcd")

			So(err, ShouldBeNil)
			So(gotPath, ShouldEqual, PathBrowserForget)
			So(gotBody.Ref, ShouldEqual, "chrome-abcd")
		})

		Convey("目标不存在时返回携带该错误码的错误", func() {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = json.NewEncoder(w).Encode(CallResult{OK: false, Error: &CallError{Code: "NOT_FOUND", Message: "no paired browser instance matches nope"}})
			}))
			t.Cleanup(srv.Close)
			c := &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}

			err := c.ForgetBrowser(context.Background(), "nope")

			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "NOT_FOUND")
		})
	})
}

func TestCallCarriesTargetBrowser(t *testing.T) {
	Convey("控制客户端把目标浏览器随 /control/call 发给 daemon", t, func() {
		c, bodies := captureCalls(t)

		Convey("指定目标时请求体带 browser", func() {
			res, err := c.Call(context.Background(), "tabs.open", "edge-fedc", json.RawMessage(`{"url":"https://example.com/"}`), nil)
			So(err, ShouldBeNil)
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"tabId":1}`)
			So(*bodies, ShouldHaveLength, 1)
			So(string((*bodies)[0]["action"]), ShouldEqual, `"tabs.open"`)
			So(string((*bodies)[0]["browser"]), ShouldEqual, `"edge-fedc"`)
		})

		Convey("不指定目标时请求体不带 browser 字段", func() {
			_, err := c.Call(context.Background(), "scripts.list", "", nil, nil)
			So(err, ShouldBeNil)
			So(*bodies, ShouldHaveLength, 1)
			_, has := (*bodies)[0]["browser"]
			So(has, ShouldBeFalse)
			So(string((*bodies)[0]["input"]), ShouldEqual, `{}`)
		})
	})
}

func TestCallReportsApprovalPending(t *testing.T) {
	Convey("带 onPending 的调用请 daemon 报告审批开始,并在结论到达前回调", t, func() {
		var body map[string]json.RawMessage
		released := make(chan struct{})
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewDecoder(r.Body).Decode(&body)
			_, _ = w.Write([]byte(`{"ok":false,"pending":true}` + "\n"))
			w.(http.Flusher).Flush()
			// 结论要等测试确认 onPending 已经被调用才写出,证明回调发生在结论之前。
			select {
			case <-released:
			case <-time.After(3 * time.Second):
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"ids":["14"]}}` + "\n"))
		}))
		t.Cleanup(srv.Close)
		c := &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}

		res, err := c.Call(context.Background(), "bookmarks.remove", "", json.RawMessage(`{"ids":["14"]}`), func() { close(released) })

		So(err, ShouldBeNil)
		So(string(body["reportPending"]), ShouldEqual, "true")
		So(res.Pending, ShouldBeFalse)
		So(res.OK, ShouldBeTrue)
		So(string(res.Result), ShouldEqual, `{"ids":["14"]}`)
	})

	Convey("不带 onPending 的调用不请求 pending 行", t, func() {
		c, bodies := captureCalls(t)
		_, err := c.Call(context.Background(), "bookmarks.remove", "", json.RawMessage(`{"ids":["14"]}`), nil)
		So(err, ShouldBeNil)
		_, has := (*bodies)[0]["reportPending"]
		So(has, ShouldBeFalse)
	})
}

// serveCallBody 起一个模拟控制服务,对 /control/call 原样写出 body。
func serveCallBody(t *testing.T, body string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &Client{http: srv.Client(), base: srv.URL, controlToken: "t"}
}

func TestCallRejectsPendingLinesOutsideItsContract(t *testing.T) {
	Convey("daemon 写出调用方没要的或重复的 pending 行时,Call 报错而不是当作结论或再次回调", t, func() {
		Convey("没带 onPending 却收到 pending 行", func() {
			c := serveCallBody(t, `{"ok":false,"pending":true}`+"\n"+`{"ok":true,"result":{}}`+"\n")
			_, err := c.Call(context.Background(), "bookmarks.remove", "", json.RawMessage(`{"ids":["14"]}`), nil)
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "unrequested approval pending line")
		})

		Convey("同一次调用收到第二行 pending:onPending 至多被调用一次", func() {
			c := serveCallBody(t, `{"ok":false,"pending":true}`+"\n"+`{"ok":false,"pending":true}`+"\n"+`{"ok":true,"result":{}}`+"\n")
			calls := 0
			_, err := c.Call(context.Background(), "bookmarks.remove", "", json.RawMessage(`{"ids":["14"]}`), func() { calls++ })
			So(err, ShouldNotBeNil)
			So(err.Error(), ShouldContainSubstring, "duplicate approval pending line")
			So(calls, ShouldEqual, 1)
		})
	})
}
