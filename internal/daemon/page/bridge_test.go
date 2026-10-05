package page

import (
	"context"
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
)

// bodyServer 是只应答 debugger.body 的 bridge,记下收到的请求。
type bodyServer struct {
	result string
	last   bridge.Request
}

func (s *bodyServer) ResolveBrowser(string) (bridge.InstanceInfo, error) {
	return bridge.InstanceInfo{}, nil
}

func (s *bodyServer) CallInstance(_ context.Context, _ string, req bridge.Request) (bridge.Response, error) {
	s.last = req
	return bridge.Response{OK: true, Result: json.RawMessage(s.result)}, nil
}

func TestBridgeBody(t *testing.T) {
	Convey("取体经 debugger.body 交给扩展,回答在边界上校验", t, func() {
		srv := &bodyServer{}
		cdp := NewBridgeCDP(srv)
		get := func(result string, q BodyQuery) (Body, error) {
			srv.result = result
			return cdp.Body(context.Background(), testInstance, q)
		}

		Convey("请求带上会话、请求 ID 与哪一种体;体、base64、原始大小与截断原样交回", func() {
			b, err := get(`{"body":"QUJD","base64Encoded":true,"size":3000000,"truncated":true}`, BodyQuery{TabID: 3, SessionID: "S1", RequestID: "R1", Part: BodyPartResponse})
			So(err, ShouldBeNil)
			So(b, ShouldResemble, Body{Text: "QUJD", Base64: true, Size: 3000000, Truncated: true})
			So(srv.last.Action, ShouldEqual, "debugger.body")
			So(string(srv.last.Input), ShouldEqualJSON, `{"tabId":3,"sessionId":"S1","requestId":"R1","part":"response"}`)
		})

		Convey("顶层会话不带 sessionId;没给 base64Encoded 与 truncated 时为 false", func() {
			b, err := get(`{"body":"a=1","size":3}`, BodyQuery{TabID: 3, RequestID: "R1", Part: BodyPartRequest})
			So(err, ShouldBeNil)
			So(b, ShouldResemble, Body{Text: "a=1", Size: 3})
			So(string(srv.last.Input), ShouldEqualJSON, `{"tabId":3,"requestId":"R1","part":"request"}`)
		})

		Convey("不可用时交回原因", func() {
			b, err := get(`{"unavailable":"navigated"}`, BodyQuery{TabID: 3, RequestID: "R1", Part: BodyPartResponse})
			So(err, ShouldBeNil)
			So(b, ShouldResemble, Body{Unavailable: BodyNavigated})
		})

		Convey("既没有体也没有原因,或两者都有,是扩展的错误回答", func() {
			for _, bad := range []string{`{}`, `{"body":"x"}`, `{"size":1}`, `{"body":"x","size":1,"unavailable":"noData"}`} {
				_, err := get(bad, BodyQuery{TabID: 3, RequestID: "R1", Part: BodyPartResponse})
				So(err, ShouldNotBeNil)
			}
		})
	})
}
