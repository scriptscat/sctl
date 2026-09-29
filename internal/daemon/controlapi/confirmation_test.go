package controlapi

import (
	"encoding/json"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func TestL1CallWithoutConfirmationIsRejectedBeforeForwarding(t *testing.T) {
	Convey("L1 方法 readingList.remove 缺少 input.confirm === true 时 daemon 返回 CONFIRMATION_REQUIRED 且不转发给扩展", t, func() {
		h := startTestServer(t)
		a := h.connectBrowser(instanceA, "chrome-0123")

		for _, input := range []string{
			`{"urls":["https://a.example/"]}`,
			`{"urls":["https://a.example/"],"confirm":false}`,
			`{"urls":["https://a.example/"],"confirm":"true"}`,
			`{"urls":["https://a.example/"],"confirm":1}`,
			`[]`,
		} {
			res := h.callControl(control.CallRequest{Action: "readingList.remove", Input: json.RawMessage(input)})
			So(res.OK, ShouldBeFalse)
			So(errCode(res), ShouldEqual, generated.ErrorCodeConfirmationRequired)
			So(res.Error.Message, ShouldContainSubstring, "--yes")
			So(res.Error.Message, ShouldContainSubstring, "confirm: true")
		}
		a.idle()

		Convey("带 confirm: true 时照常转发并返回扩展的结果", func() {
			ch := h.goCall(control.CallRequest{Action: "readingList.remove", Input: json.RawMessage(`{"urls":["https://a.example/"],"confirm":true}`)})
			req := a.read()
			So(req.Method, ShouldEqual, "readingList.remove")
			var params rpcRequestParams
			So(json.Unmarshal(req.Params, &params), ShouldBeNil)
			So(string(params.Input), ShouldEqual, `{"urls":["https://a.example/"],"confirm":true}`)
			a.writeResult(req.ID, json.RawMessage(`{"urls":["https://a.example/"]}`))
			res := <-ch
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"urls":["https://a.example/"]}`)
		})
	})
}

func TestL2CallIsForwardedWithoutConfirmationAndWaitsForTheBrowsersDecision(t *testing.T) {
	Convey("L2 方法 bookmarks.remove 不要求 confirm:daemon 直接转发,调用一直阻塞到扩展给出审批结论", t, func() {
		h := startTestServer(t)
		a := h.connectBrowser(instanceA, "chrome-0123")

		ch := h.goCall(control.CallRequest{Action: "bookmarks.remove", Input: json.RawMessage(`{"ids":["14"]}`)})
		req := a.read()
		So(req.Method, ShouldEqual, "bookmarks.remove")
		select {
		case res := <-ch:
			t.Fatalf("the call returned %+v before the browser decided", res)
		case <-time.After(100 * time.Millisecond):
		}

		Convey("拒绝时调用方得到 USER_REJECTED", func() {
			a.writeError(req.ID, generated.ErrorCodeUserRejected, "rejected in the approval window")
			res := <-ch
			So(res.OK, ShouldBeFalse)
			So(errCode(res), ShouldEqual, generated.ErrorCodeUserRejected)
		})

		Convey("批准时调用方得到删除结果", func() {
			a.writeResult(req.ID, json.RawMessage(`{"ids":["14"],"bookmarks":1,"folders":0}`))
			res := <-ch
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"ids":["14"],"bookmarks":1,"folders":0}`)
		})
	})
}
