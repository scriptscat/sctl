package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

// cdpStub 是只回答 /control/cdp/* 的假 daemon,记录最近一次请求的路径与请求体。
type cdpStub struct {
	calls int
	path  string
	last  control.CDPRequest
}

func stubCDPDaemon(t *testing.T, result control.CallResult) *cdpStub {
	t.Helper()
	stub := &cdpStub{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	for _, path := range []string{control.PathCDPEndpoint, control.PathCDPStatus, control.PathCDPClose} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			stub.calls++
			stub.path = r.URL.Path
			stub.last = control.CDPRequest{}
			_ = json.NewDecoder(r.Body).Decode(&stub.last)
			_ = json.NewEncoder(w).Encode(result)
		})
	}
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)
	return stub
}

func TestCdpEndpointCommands(t *testing.T) {
	Convey("sctl cdp endpoint / status / close", t, func() {
		t.Setenv("SCTL_BROWSER", "")
		expires := time.Date(2026, 10, 9, 13, 0, 0, 0, time.UTC)
		connected := time.Date(2026, 10, 9, 12, 30, 0, 0, time.UTC)
		idle := control.CallResult{OK: true, Result: json.RawMessage(`{"browser":{"id":"inst-work","name":"work"},"endpoint":{` +
			`"httpUrl":"http://127.0.0.1:8643/cdp/s","wsUrl":"ws://127.0.0.1:8643/cdp/s/devtools/browser/b",` +
			`"clientConnected":false,"expiresAt":"2026-10-09T13:00:00Z"}}`)}

		Convey("endpoint 打印两种地址、没有客户端与失效时间;--browser 原样转发", func() {
			stub := stubCDPDaemon(t, idle)
			code, out := runCLI("cdp", "endpoint", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(stub.path, ShouldEqual, control.PathCDPEndpoint)
			So(stub.last.Browser, ShouldEqual, "work")
			So(out, ShouldEqual, "CDP endpoint of browser work (inst-work)\n"+
				"  Playwright connectOverCDP:  http://127.0.0.1:8643/cdp/s\n"+
				"  Puppeteer browserWSEndpoint: ws://127.0.0.1:8643/cdp/s/devtools/browser/b\n"+
				"  client:  none connected\n"+
				"  expires: "+expires.Local().Format(time.RFC3339)+" unless a client connects\n")
		})

		Convey("status 在客户端连着时打印连上的时间,失效从客户端断开起计", func() {
			stub := stubCDPDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"browser":{"id":"inst-work","name":"work"},"endpoint":{` +
				`"httpUrl":"http://h/cdp/s","wsUrl":"ws://h/cdp/s/devtools/browser/b","clientConnected":true,"connectedAt":"2026-10-09T12:30:00Z"}}`)})
			code, out := runCLI("cdp", "status")
			So(code, ShouldEqual, exitOK)
			So(stub.path, ShouldEqual, control.PathCDPStatus)
			So(out, ShouldContainSubstring, "  client:  connected since "+connected.Local().Format(time.RFC3339)+"\n")
			So(out, ShouldContainSubstring, "  expires: 60 minutes after the client disconnects\n")
		})

		Convey("status 在没有端点时如实说明", func() {
			stubCDPDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"browser":{"id":"inst-work","name":"work"},"endpoint":null}`)})
			code, out := runCLI("cdp", "status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "browser work has no CDP endpoint; create one with sctl cdp endpoint\n")
		})

		Convey("close 说明关闭了端点,或者本来就没有", func() {
			stub := stubCDPDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"browser":{"id":"inst-work","name":"work"},"closed":true}`)})
			code, out := runCLI("cdp", "close", "--browser", "work")
			So(code, ShouldEqual, exitOK)
			So(stub.path, ShouldEqual, control.PathCDPClose)
			So(stub.last.Browser, ShouldEqual, "work")
			So(out, ShouldEqual, "closed the CDP endpoint of browser work\n")
			stubCDPDaemon(t, control.CallResult{OK: true, Result: json.RawMessage(`{"browser":{"id":"inst-work","name":"work"},"closed":false}`)})
			code, out = runCLI("cdp", "close")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqual, "browser work has no CDP endpoint\n")
		})

		Convey("-o json 原样输出 daemon 的结果", func() {
			stubCDPDaemon(t, idle)
			code, out := runCLI("cdp", "endpoint", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldEqualJSON, string(idle.Result))
		})

		Convey("浏览器离线时退出码 3 并带上 daemon 的说明;多余参数不联系 daemon", func() {
			stubCDPDaemon(t, pageError("BROWSER_OFFLINE", "browser work is not connected"))
			exit, _, _, err := runCLIResult(strings.NewReader(""), "cdp", "endpoint")
			So(exit, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "browser work is not connected")
			stub := stubCDPDaemon(t, idle)
			for _, sub := range []string{"endpoint", "status", "close"} {
				code, _ := runCLI("cdp", sub, "extra")
				So(code, ShouldEqual, exitError)
			}
			So(stub.calls, ShouldEqual, 0)
		})

		Convey("endpoint 的帮助写明地址就是凭据、连着时 sctl 的页面命令不可用", func() {
			cmd := newCdpCmd()
			endpoint, _, err := cmd.Find([]string{"endpoint"})
			So(err, ShouldBeNil)
			for _, want := range []string{"ENDPOINT_CONNECTED", "sctl cdp close", "60 minutes", "full control"} {
				So(endpoint.Long, ShouldContainSubstring, want)
			}
		})
	})
}
