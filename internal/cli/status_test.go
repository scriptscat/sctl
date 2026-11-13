package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestStatusDaemonRunningWithoutToken(t *testing.T) {
	Convey("daemon 在运行、但本数据目录里没有它的控制令牌(多半是另一个数据目录)时", t, func() {
		stubDaemon(t, control.CallResult{})
		So(os.Remove(filepath.Join(os.Getenv("SCTL_DATA_DIR"), "control.token")), ShouldBeNil)

		Convey("status 不说 daemon 没在运行,而是退出码 3 并指向 --data-dir", func() {
			code, out, _, err := runCLIResult(strings.NewReader(""), "status")

			So(code, ShouldEqual, exitError)
			So(out, ShouldNotContainSubstring, "not running")
			So(err.Error(), ShouldContainSubstring, "a daemon is running at")
			So(err.Error(), ShouldContainSubstring, "--data-dir")
		})

		Convey("status -o json 同样报错,不输出一份看起来像没在运行的空状态", func() {
			code, out, _ := runCLICapture("-o", "json", "status")

			So(code, ShouldEqual, exitError)
			So(out, ShouldBeEmpty)
		})
	})

	Convey("daemon 确实没在运行时,status 照旧如实报告且退出码 0", t, func() {
		t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:1")
		t.Setenv("SCTL_DATA_DIR", t.TempDir())

		code, out := runCLI("status")

		So(code, ShouldEqual, exitOK)
		So(out, ShouldContainSubstring, "daemon is not running")
	})
}

func TestListenAddressValidation(t *testing.T) {
	Convey("--listen-address 不是 host:port 时,任何命令都在入口拒绝,退出码 3", t, func() {
		t.Setenv("SCTL_BRIDGE_ADDR", "")
		t.Setenv("SCTL_DATA_DIR", t.TempDir())
		for _, address := range []string{"nonsense", "127.0.0.1", "127.0.0.1:http", "127.0.0.1:0", "127.0.0.1:70000"} {
			for _, verb := range []string{"serve", "status"} {
				code, out, _, err := runCLIResult(strings.NewReader(""), "--listen-address", address, verb)

				So(code, ShouldEqual, exitError)
				So(out, ShouldBeEmpty)
				So(err.Error(), ShouldContainSubstring, "invalid --listen-address")
			}
		}
	})
}
