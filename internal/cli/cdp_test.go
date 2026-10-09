package cli

import (
	"encoding/json"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestCdpSend(t *testing.T) {
	Convey("sctl cdp send", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		ok := control.CallResult{OK: true, Result: json.RawMessage(`{"frameTree":{"frame":{"id":"F"}},"tabId":5,"contentTrust":"untrusted-page-content"}`)}

		Convey("请求 cdp.send 动作,输入带方法与参数,标签页交给 daemon 选择;默认输出缩进的 JSON", func() {
			stub := stubPageDaemon(t, ok)
			code, out := runCLI("cdp", "send", "Page.getFrameTree", "--params", `{"a":1}`)
			So(code, ShouldEqual, exitOK)
			So(stub.last.Action, ShouldEqual, "cdp.send")
			So(string(stub.last.Input), ShouldEqualJSON, `{"method":"Page.getFrameTree","params":{"a":1}}`)
			So(stub.last.TabID, ShouldBeNil)
			So(stub.last.Activate, ShouldBeFalse)
			So(out, ShouldContainSubstring, "{\n  \"contentTrust\": \"untrusted-page-content\"")
			So(out, ShouldContainSubstring, `"tabId": 5`)
		})

		Convey("省略 --params 时输入不带 params", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("cdp", "send", "Page.getFrameTree")
			So(code, ShouldEqual, exitOK)
			So(string(stub.last.Input), ShouldEqualJSON, `{"method":"Page.getFrameTree"}`)
		})

		Convey("--tab、--timeout、--browser 原样转发", func() {
			stub := stubPageDaemon(t, ok)
			code, _ := runCLI("cdp", "send", "Page.getFrameTree", "--tab", "9", "--timeout", "30s", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(*stub.last.TabID, ShouldEqual, 9)
			So(stub.last.TimeoutMs, ShouldEqual, 30000)
			So(stub.last.Browser, ShouldEqual, "work")
		})

		Convey("缺方法、多余参数、--params 不是 JSON 时退出码 3,不联系 daemon", func() {
			stub := stubPageDaemon(t, ok)
			for _, args := range [][]string{
				{"cdp", "send"}, {"cdp", "send", "A.b", "C.d"}, {"cdp", "send", "A.b", "--params", "{nope"},
			} {
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("daemon 的 INVALID_REQUEST 带着 Chrome 的消息,退出码 3;TIMEOUT 与 PAYLOAD_TOO_LARGE 同样", func() {
			for code, message := range map[string]string{
				"INVALID_REQUEST":   `{"code":-32601,"message":"'Foo.bar' wasn't found"}`,
				"PAYLOAD_TOO_LARGE": "result exceeds the frame limit",
				"TIMEOUT":           "page cdp.send did not finish within 10s",
			} {
				stubPageDaemon(t, pageError(code, message))
				exit, _, _, err := runCLIResult(strings.NewReader(""), "cdp", "send", "Foo.bar")
				So(exit, ShouldEqual, exitError)
				So(err.Error(), ShouldContainSubstring, message)
			}
		})

		Convey("帮助写明副作用与恢复办法", func() {
			cmd := newCdpCmd()
			send, _, err := cmd.Find([]string{"send"})
			So(err, ShouldBeNil)
			for _, want := range []string{"Fetch.disable", "Debugger.resume", "page detach", "Page.disable", "Page.handleJavaScriptDialog"} {
				So(send.Long, ShouldContainSubstring, want)
			}
		})
	})
}
