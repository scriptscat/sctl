package cli

import (
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestCookiesList(t *testing.T) {
	result := `{"contentTrust":"untrusted-page-content","hasMore":true,"items":[
		{"name":"sid","value":"abc123","domain":"example.com","path":"/","expires":1788251400000,"secure":true,"httpOnly":true,"sameSite":"lax","session":false},
		{"name":"chips\u001b[31m","value":"x","domain":".cdn.example","path":"/","secure":true,"httpOnly":false,"sameSite":"no_restriction","session":true,"partitionTopLevelSite":"https://top.example"}]}`

	Convey("sctl cookies list 列出 Cookie", t, func() {
		Convey("不带参数只下发默认 limit;表格含名称、值、域名、过期、分区站点,值经 terminalSafe,hasMore 提示走 stderr", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out, errOut := runCLICapture("cookies", "list")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "cookies.list")
			So(string(req.Input), ShouldEqual, `{"limit":100}`)
			for _, want := range []string{"NAME", "VALUE", "DOMAIN", "EXPIRES", "PARTITION", "sid", "abc123", "example.com", "https://top.example", "session"} {
				So(out, ShouldContainSubstring, want)
			}
			So(out, ShouldNotContainSubstring, "\x1b")
			So(errOut, ShouldContainSubstring, "--limit")
		})

		Convey("--url、--name、--limit 下发;--domain 单独下发", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ := runCLI("cookies", "list", "--url", "https://example.com/", "--name", "sid", "--limit", "5")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"limit":5,"name":"sid","url":"https://example.com/"}`)
			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, _ = runCLI("cookies", "list", "--domain", "example.com")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"domain":"example.com","limit":100}`)
		})

		Convey("--url 与 --domain 互斥、--limit 越界时退出码 3,且不发起调用", func() {
			for _, args := range [][]string{{"--url", "https://a.example/", "--domain", "a.example"}, {"--limit", "0"}, {"--limit", "1001"}} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
				code, _ := runCLI(append([]string{"cookies", "list"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("-o json 原样输出结果", func() {
			stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(result)})
			code, out := runCLI("cookies", "list", "-o", "json")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, `"contentTrust"`)
		})
	})
}

func TestCookiesGet(t *testing.T) {
	Convey("sctl cookies get --url --name 下发 cookies.get 并输出 Cookie", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"cookie":{"name":"sid","value":"abc123","domain":"example.com","path":"/","secure":false,"httpOnly":false,"sameSite":"unspecified","session":true}}`)})
		code, out := runCLI("cookies", "get", "--url", "https://example.com/", "--name", "sid")
		So(code, ShouldEqual, exitOK)
		So(req.Action, ShouldEqual, "cookies.get")
		So(string(req.Input), ShouldEqual, `{"name":"sid","url":"https://example.com/"}`)
		So(out, ShouldContainSubstring, "abc123")
	})

	Convey("缺少 --url 或 --name 是参数错误,不发起调用;NOT_FOUND 退出码 3", t, func() {
		req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{}`)})
		code, _ := runCLI("cookies", "get", "--url", "https://example.com/")
		So(code, ShouldNotEqual, exitOK)
		So(req.Action, ShouldBeEmpty)
		stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no cookie"}})
		code, _ = runCLI("cookies", "get", "--url", "https://example.com/", "--name", "sid")
		So(code, ShouldEqual, exitError)
	})
}

func TestCookiesSet(t *testing.T) {
	Convey("sctl cookies set 写入 Cookie", t, func() {
		ok := control.CallResult{OK: true, Result: []byte(`{"cookie":{"name":"sid","value":"1","domain":"example.com","path":"/","secure":false,"httpOnly":false,"sameSite":"unspecified","session":true}}`)}

		Convey("只给必填项时是会话 Cookie:不带 expires", func() {
			req := stubDaemonCapturing(t, ok)
			code, _ := runCLI("cookies", "set", "--url", "https://example.com/", "--name", "sid", "--value", "1")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "cookies.set")
			So(string(req.Input), ShouldEqual, `{"name":"sid","url":"https://example.com/","value":"1"}`)
		})

		Convey("全部可选项下发,--expires 按 RFC 3339 解析为毫秒", func() {
			req := stubDaemonCapturing(t, ok)
			code, _ := runCLI("cookies", "set", "--url", "https://example.com/", "--name", "sid", "--value", "1",
				"--domain", "example.com", "--path", "/app", "--secure", "--http-only", "--same-site", "strict",
				"--expires", "2026-09-01T08:00:00+08:00")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual,
				`{"domain":"example.com","expires":1788220800000,"httpOnly":true,"name":"sid","path":"/app","sameSite":"strict","secure":true,"url":"https://example.com/","value":"1"}`)
		})

		Convey("--same-site 取值不合法、--expires 无法解析或是相对时长时退出码 3,且不发起调用", func() {
			for _, extra := range [][]string{{"--same-site", "none"}, {"--expires", "tomorrow"}, {"--expires", "7d"}} {
				req := stubDaemonCapturing(t, ok)
				args := append([]string{"cookies", "set", "--url", "https://example.com/", "--name", "sid", "--value", "1"}, extra...)
				code, _ := runCLI(args...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("Chrome 拒绝写入时 INVALID_REQUEST 的原因透出,退出码 3", func() {
			stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: "INVALID_REQUEST", Message: "Failed to parse or set cookie named sid."}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "cookies", "set", "--url", "https://example.com/", "--name", "sid", "--value", "1")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "Failed to parse or set cookie")
		})
	})
}

func TestCookiesL1Commands(t *testing.T) {
	Convey("cookies rm、clear 是 L1 命令,需要 --yes", t, func() {
		Convey("rm 加 --yes 下发 confirm: true、url、name,并输出删除条数", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"deleted":1}`)})
			code, out := runCLI("cookies", "rm", "--url", "https://example.com/", "--name", "sid", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "cookies.remove")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"name":"sid","url":"https://example.com/"}`)
			So(out, ShouldContainSubstring, "1")
		})

		Convey("clear --domain 与 clear --all 加 --yes 下发并输出删除条数", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"deleted":7}`)})
			code, out := runCLI("cookies", "clear", "--domain", "example.com", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "cookies.clear")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"domain":"example.com"}`)
			So(out, ShouldContainSubstring, "7")
			req = stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"deleted":9}`)})
			code, _ = runCLI("cookies", "clear", "--all", "--yes")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual, `{"all":true,"confirm":true}`)
		})

		Convey("clear 必须恰好给 --domain 或 --all 之一,否则退出码 3,且不发起调用", func() {
			for _, args := range [][]string{{"clear", "--yes"}, {"clear", "--domain", "a.example", "--all", "--yes"}} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"deleted":0}`)})
				code, _ := runCLI(append([]string{"cookies"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("不加 --yes 时不带 confirm,daemon 的 CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			for _, args := range [][]string{
				{"rm", "--url", "https://example.com/", "--name", "sid"},
				{"clear", "--all"},
			} {
				req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
					Code: "CONFIRMATION_REQUIRED", Message: "requires explicit confirmation: pass --yes on the command line or confirm: true in the input",
				}})
				code, _, _, err := runCLIResult(strings.NewReader(""), append([]string{"cookies"}, args...)...)
				So(code, ShouldEqual, exitError)
				So(string(req.Input), ShouldNotContainSubstring, "confirm")
				So(err.Error(), ShouldContainSubstring, "--yes")
			}
		})

		Convey("rm 的 NOT_FOUND 映射为退出码 3", func() {
			stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{Code: "NOT_FOUND", Message: "no cookie"}})
			code, _ := runCLI("cookies", "rm", "--url", "https://example.com/", "--name", "sid", "--yes")
			So(code, ShouldEqual, exitError)
		})
	})
}
