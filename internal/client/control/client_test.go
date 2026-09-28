package control

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

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

func TestCallCarriesTargetBrowser(t *testing.T) {
	Convey("控制客户端把目标浏览器随 /control/call 发给 daemon", t, func() {
		c, bodies := captureCalls(t)

		Convey("指定目标时请求体带 browser", func() {
			res, err := c.CallBrowser(context.Background(), "tabs.open", "edge-fedc", json.RawMessage(`{"url":"https://example.com/"}`))
			So(err, ShouldBeNil)
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"tabId":1}`)
			So(*bodies, ShouldHaveLength, 1)
			So(string((*bodies)[0]["action"]), ShouldEqual, `"tabs.open"`)
			So(string((*bodies)[0]["browser"]), ShouldEqual, `"edge-fedc"`)
		})

		Convey("不指定目标时请求体不带 browser 字段", func() {
			_, err := c.Call(context.Background(), "scripts.list", nil)
			So(err, ShouldBeNil)
			So(*bodies, ShouldHaveLength, 1)
			_, has := (*bodies)[0]["browser"]
			So(has, ShouldBeFalse)
			So(string((*bodies)[0]["input"]), ShouldEqual, `{}`)
		})
	})
}
