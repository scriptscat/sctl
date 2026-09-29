package controlapi

import (
	"encoding/json"
	"testing"

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
