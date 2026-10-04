package cli

import (
	"strconv"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestBrowsingDataClear(t *testing.T) {
	Convey("sctl browsing-data clear 是 L1 命令", t, func() {
		Convey("--types 逗号分隔,加 --yes 时下发 types 与 confirm: true,不带 since 即全部时间", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"types":["cache","cookies"]}`)})
			code, out := runCLI("browsing-data", "clear", "--types", "cache,cookies", "--yes")
			So(code, ShouldEqual, exitOK)
			So(req.Action, ShouldEqual, "browsingData.clear")
			So(string(req.Input), ShouldEqual, `{"confirm":true,"types":["cache","cookies"]}`)
			So(out, ShouldContainSubstring, "cookies")
		})

		Convey("--since 与可重复的 --origin 下发为 since 与 origins", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"types":["cookies"]}`)})
			code, _ := runCLI("browsing-data", "clear", "--types", "cookies", "--since", "2026-09-01T00:00:00Z",
				"--origin", "https://a.example", "--origin", "https://b.example", "--yes")
			So(code, ShouldEqual, exitOK)
			So(string(req.Input), ShouldEqual,
				`{"confirm":true,"origins":["https://a.example","https://b.example"],"since":`+strconv.FormatInt(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli(), 10)+`,"types":["cookies"]}`)
		})

		Convey("不加 --yes 时不带 confirm,CONFIRMATION_REQUIRED 映射为退出码 3", func() {
			req := stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code: "CONFIRMATION_REQUIRED", Message: "browsingData.clear requires explicit confirmation: pass --yes",
			}})
			code, _ := runCLI("browsing-data", "clear", "--types", "cache")
			So(code, ShouldEqual, exitError)
			So(string(req.Input), ShouldEqual, `{"types":["cache"]}`)
		})

		Convey("缺少 --types 或 --since 无法解析时退出码 3,且不发起调用", func() {
			for _, args := range [][]string{
				{"browsing-data", "clear", "--yes"},
				{"browsing-data", "clear", "--types", "cache", "--since", "soon", "--yes"},
			} {
				req := stubDaemonCapturing(t, control.CallResult{OK: true, Result: []byte(`{"types":[]}`)})
				code, _, _, err := runCLIResult(strings.NewReader(""), args...)
				// 缺少必填参数是 cobra 的解析错误(非 ExitError),main 统一映射为退出码 3。
				So(code, ShouldNotEqual, exitOK)
				So(err, ShouldNotBeNil)
				So(req.Action, ShouldBeEmpty)
			}
		})

		Convey("daemon 以 INVALID_REQUEST 拒绝密码类型或不支持来源的类型时退出码 3,消息可见", func() {
			stubDaemonCapturing(t, control.CallResult{OK: false, Error: &control.CallError{
				Code: "INVALID_REQUEST", Message: "origins cannot be combined with history",
			}})
			code, _, _, err := runCLIResult(strings.NewReader(""), "browsing-data", "clear", "--types", "history", "--origin", "https://a.example", "--yes")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "history")
		})
	})
}
