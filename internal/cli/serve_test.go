package cli

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
)

func TestServeAddressInUse(t *testing.T) {
	Convey("sctl serve 绑定不了监听地址", t, func() {
		Convey("占着地址的是另一个 sctl daemon(比如 sctl service 起的那个)时,退出码 3,说明已在运行并指向 status / service", func() {
			stubDaemon(t, control.CallResult{})
			address := os.Getenv("SCTL_BRIDGE_ADDR")

			code, _, errOut, err := runCLIResult(strings.NewReader(""), "serve")

			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "panic")
			So(err.Error(), ShouldContainSubstring, "already running at "+address)
			So(err.Error(), ShouldContainSubstring, "sctl status")
			So(err.Error(), ShouldContainSubstring, "sctl service status")
		})

		Convey("占着地址的不是 sctl 时,退出码 3,给出地址与原始错误,并提示换 --listen-address", func() {
			ln, err := net.Listen("tcp", "127.0.0.1:0")
			So(err, ShouldBeNil)
			t.Cleanup(func() { _ = ln.Close() })
			// 不说 HTTP 的程序:连上就断,健康检查立刻失败而不是等到超时。
			go func() {
				for {
					conn, err := ln.Accept()
					if err != nil {
						return
					}
					_ = conn.Close()
				}
			}()
			t.Setenv("SCTL_BRIDGE_ADDR", ln.Addr().String())
			t.Setenv("SCTL_DATA_DIR", t.TempDir())

			code, _, errOut, err := runCLIResult(strings.NewReader(""), "serve")

			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "panic")
			So(err.Error(), ShouldNotContainSubstring, "already running")
			So(err.Error(), ShouldContainSubstring, ln.Addr().String())
			So(err.Error(), ShouldContainSubstring, "--listen-address")
		})
	})
}

// freeAddress 返回一个此刻没人占用的本机地址。
func freeAddress(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	So(err, ShouldBeNil)
	address := ln.Addr().String()
	So(ln.Close(), ShouldBeNil)
	return address
}

func TestServeStartupFailures(t *testing.T) {
	Convey("sctl serve 绑定地址之后起不来", t, func() {
		dir := t.TempDir()
		t.Setenv("SCTL_DATA_DIR", dir)
		address := freeAddress(t)
		t.Setenv("SCTL_BRIDGE_ADDR", address)

		Convey("browsers.json 损坏时退出码 3,指出是哪个文件,并放开监听地址", func() {
			path := filepath.Join(dir, "browsers.json")
			So(os.WriteFile(path, []byte("{broken"), 0o600), ShouldBeNil)

			code, _, errOut, err := runCLIResult(strings.NewReader(""), "serve")

			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "panic")
			So(err.Error(), ShouldContainSubstring, path)
			ln, lerr := net.Listen("tcp", address)
			So(lerr, ShouldBeNil)
			So(ln.Close(), ShouldBeNil)
		})

		Convey("scriptcats.json 损坏时退出码 3,指出是哪个文件", func() {
			path := filepath.Join(dir, "scriptcats.json")
			So(os.WriteFile(path, []byte("x"), 0o600), ShouldBeNil)

			code, _, errOut, err := runCLIResult(strings.NewReader(""), "serve")

			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "panic")
			So(err.Error(), ShouldContainSubstring, path)
		})

		Convey("控制令牌写不进数据目录时退出码 3,指出令牌文件", func() {
			// 令牌路径被一个目录占着:写入一定失败,root 运行也一样。
			path := filepath.Join(dir, "control.token")
			So(os.MkdirAll(filepath.Join(path, "x"), 0o700), ShouldBeNil)

			code, _, errOut, err := runCLIResult(strings.NewReader(""), "serve")

			So(code, ShouldEqual, exitError)
			So(errOut, ShouldNotContainSubstring, "panic")
			So(err.Error(), ShouldContainSubstring, path)
		})
	})
}
