package controlapi

import (
	"encoding/json"
	"maps"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// withLevel 返回把 method 改标为 level 的协议副本:当前协议里还没有真正的 L1 浏览器方法,
// 只能借一个已有方法驱动 daemon 的确认检查。
func withLevel(t *testing.T, method string, level protocol.Level) *protocol.Protocol {
	t.Helper()
	p, err := protocol.Load()
	So(err, ShouldBeNil)
	copied := *p
	copied.Actions = maps.Clone(p.Actions)
	action := copied.Actions[method]
	action.Level = level
	copied.Actions[method] = action
	return &copied
}

func TestL1CallWithoutConfirmationIsRejectedBeforeForwarding(t *testing.T) {
	Convey("L1 方法缺少 input.confirm === true 时 daemon 返回 CONFIRMATION_REQUIRED 且不转发给扩展", t, func() {
		h := startTestServerWithProtocol(t, withLevel(t, "tabs.close", protocol.LevelConfirm))
		a := h.connectBrowser(instanceA, "chrome-0123")

		for _, input := range []string{
			`{"tabIds":[1]}`,
			`{"tabIds":[1],"confirm":false}`,
			`{"tabIds":[1],"confirm":"true"}`,
			`{"tabIds":[1],"confirm":1}`,
			`[]`,
		} {
			res := h.callControl(control.CallRequest{Action: "tabs.close", Input: json.RawMessage(input)})
			So(res.OK, ShouldBeFalse)
			So(errCode(res), ShouldEqual, generated.ErrorCodeConfirmationRequired)
			So(res.Error.Message, ShouldContainSubstring, "confirm")
		}
		a.idle()

		Convey("带 confirm: true 时照常转发并返回扩展的结果", func() {
			ch := h.goCall(control.CallRequest{Action: "tabs.close", Input: json.RawMessage(`{"tabIds":[1],"confirm":true}`)})
			req := a.read()
			So(req.Method, ShouldEqual, "tabs.close")
			var params rpcRequestParams
			So(json.Unmarshal(req.Params, &params), ShouldBeNil)
			So(string(params.Input), ShouldEqual, `{"tabIds":[1],"confirm":true}`)
			a.writeResult(req.ID, json.RawMessage(`{"tabIds":[1]}`))
			res := <-ch
			So(res.OK, ShouldBeTrue)
			So(string(res.Result), ShouldEqual, `{"tabIds":[1]}`)
		})
	})
}
