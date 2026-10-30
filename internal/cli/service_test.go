package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/client/control"
	"github.com/scriptscat/sctl/internal/client/service"
)

// fakeServiceManager 记录安装的命令行;onInstall / onStart 模拟服务启动后 daemon 变得可连。
type fakeServiceManager struct {
	installed bool
	program   *service.Program
	calls     []string
	onStart   func()
}

func (f *fakeServiceManager) Location() string { return "/fake/sctl.plist" }

func (f *fakeServiceManager) Installed(context.Context) (bool, error) { return f.installed, nil }

func (f *fakeServiceManager) Install(_ context.Context, p service.Program) error {
	f.calls = append(f.calls, "install")
	f.installed = true
	f.program = &p
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

func (f *fakeServiceManager) Uninstall(context.Context) (bool, error) {
	f.calls = append(f.calls, "uninstall")
	existed := f.installed
	f.installed = false
	return existed, nil
}

func (f *fakeServiceManager) Start(context.Context) error {
	f.calls = append(f.calls, "start")
	if !f.installed {
		return service.ErrNotInstalled
	}
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

func (f *fakeServiceManager) Stop(context.Context) error {
	f.calls = append(f.calls, "stop")
	if !f.installed {
		return service.ErrNotInstalled
	}
	return nil
}

func (f *fakeServiceManager) Restart(context.Context) error {
	f.calls = append(f.calls, "restart")
	if !f.installed {
		return service.ErrNotInstalled
	}
	if f.onStart != nil {
		f.onStart()
	}
	return nil
}

// stubServiceEnv 换上假的服务管理器与可执行路径,并起一个健康检查可开关的假 daemon。返回管理器和开关。
func stubServiceEnv(t *testing.T) (*fakeServiceManager, *atomic.Bool) {
	t.Helper()
	ready := &atomic.Bool{}
	mux := http.NewServeMux()
	mux.HandleFunc(control.PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		if ready.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	t.Setenv("SCTL_BRIDGE_ADDR", strings.TrimPrefix(srv.URL, "http://"))
	dir := t.TempDir()
	t.Setenv("SCTL_DATA_DIR", dir)
	So(os.WriteFile(filepath.Join(dir, "control.token"), []byte("tok"), 0o600), ShouldBeNil)

	// 真实 daemon 每次启动都重新生成控制令牌;模拟服务起来时同样换一份。
	starts := 0
	m := &fakeServiceManager{onStart: func() {
		starts++
		So(os.WriteFile(filepath.Join(dir, "control.token"), []byte(fmt.Sprintf("tok-%d", starts)), 0o600), ShouldBeNil)
		ready.Store(true)
	}}
	oldNew, oldExe, oldTimeout := newServiceManager, resolveServiceExecutable, serviceReadyTimeout
	newServiceManager = func(string) (service.Manager, error) { return m, nil }
	resolveServiceExecutable = func() (string, error) { return "/opt/bin/sctl", nil }
	serviceReadyTimeout = 300 * time.Millisecond
	t.Cleanup(func() {
		newServiceManager, resolveServiceExecutable, serviceReadyTimeout = oldNew, oldExe, oldTimeout
	})
	return m, ready
}

func TestServiceInstall(t *testing.T) {
	Convey("sctl service install", t, func() {
		m, ready := stubServiceEnv(t)
		address := os.Getenv("SCTL_BRIDGE_ADDR")

		Convey("固化当前生效的数据目录(转成绝对路径)和监听地址,等 daemon 能连上后报告", func() {
			wd := t.TempDir()
			t.Chdir(wd)
			So(os.MkdirAll("rel-data", 0o700), ShouldBeNil)
			m.onStart = func() {
				So(os.WriteFile(filepath.Join(wd, "rel-data", "control.token"), []byte("new"), 0o600), ShouldBeNil)
				ready.Store(true)
			}
			code, out, errOut := runCLICapture("--data-dir", "rel-data", "service", "install")

			So(errOut, ShouldBeEmpty)
			So(code, ShouldEqual, exitOK)
			So(m.program, ShouldNotBeNil)
			So(m.program.Executable, ShouldEqual, "/opt/bin/sctl")
			So(m.program.Args, ShouldResemble, []string{
				"--data-dir", filepath.Join(wd, "rel-data"),
				"--listen-address", address,
				"serve", "--service",
			})
			So(m.program.LogDir, ShouldEqual, filepath.Join(wd, "rel-data", "logs"))
			So(out, ShouldContainSubstring, "/fake/sctl.plist")
			So(out, ShouldContainSubstring, address)
		})

		Convey("非默认的日志级别一并固化", func() {
			code, _ := runCLI("--log-level", "debug", "service", "install")
			So(code, ShouldEqual, exitOK)
			So(m.program.Args, ShouldContain, "--log-level")
			So(m.program.Args, ShouldContain, "debug")
		})

		Convey("还没装过却已有 daemon 能连上时拒绝,不安装", func() {
			ready.Store(true)
			code, _, errOut, err := runCLIResult(strings.NewReader(""), "service", "install")
			So(code, ShouldEqual, exitError)
			So(err.Error()+errOut, ShouldContainSubstring, "already running")
			So(m.calls, ShouldBeEmpty)
		})

		Convey("还没装过、daemon 能连上但令牌不在本数据目录(另一个数据目录的 daemon)时同样拒绝", func() {
			ready.Store(true)
			So(os.Remove(filepath.Join(os.Getenv("SCTL_DATA_DIR"), "control.token")), ShouldBeNil)
			code, _ := runCLI("service", "install")
			So(code, ShouldEqual, exitError)
			So(m.calls, ShouldBeEmpty)
		})

		Convey("已经装过、端口被服务以外的 daemon 占着时:新服务起不来(令牌没变),不能误报成功", func() {
			m.installed = true
			ready.Store(true)
			m.onStart = nil
			code, _, _, err := runCLIResult(strings.NewReader(""), "service", "install")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "not reachable")
		})

		Convey("已经装过时允许覆盖安装,即使 daemon 正在运行", func() {
			m.installed = true
			ready.Store(true)
			code, _ := runCLI("service", "install")
			So(code, ShouldEqual, exitOK)
			So(m.calls, ShouldResemble, []string{"install"})
		})

		Convey("等不到 daemon 时退出码 3,并给出日志目录", func() {
			m.onStart = nil
			code, _, _, err := runCLIResult(strings.NewReader(""), "service", "install")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, filepath.Join(os.Getenv("SCTL_DATA_DIR"), "logs"))
		})
	})
}

func TestServiceLifecycleCommands(t *testing.T) {
	Convey("sctl service start / stop / restart / uninstall", t, func() {
		m, ready := stubServiceEnv(t)

		Convey("未安装时 start 退出码 3,提示先 install", func() {
			code, _, _, err := runCLIResult(strings.NewReader(""), "service", "start")
			So(code, ShouldEqual, exitError)
			So(err.Error(), ShouldContainSubstring, "sctl service install")
		})

		Convey("daemon 已经能连上时 start 不再启动服务,如实说明", func() {
			m.installed = true
			ready.Store(true)
			code, out := runCLI("service", "start")
			So(code, ShouldEqual, exitOK)
			So(m.calls, ShouldBeEmpty)
			So(out, ShouldContainSubstring, "already running")
		})

		Convey("已安装时 restart 后等 daemon 能连上", func() {
			m.installed = true
			code, out := runCLI("service", "restart")
			So(code, ShouldEqual, exitOK)
			So(m.calls, ShouldResemble, []string{"restart"})
			So(out, ShouldContainSubstring, os.Getenv("SCTL_BRIDGE_ADDR"))
		})

		Convey("已安装时 stop 成功", func() {
			m.installed = true
			code, _ := runCLI("service", "stop")
			So(code, ShouldEqual, exitOK)
			So(m.calls, ShouldResemble, []string{"stop"})
		})

		Convey("未安装时 uninstall 成功并如实说明", func() {
			code, out := runCLI("service", "uninstall")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "not installed")
		})

		Convey("已安装时 uninstall 报告已删除", func() {
			m.installed = true
			code, out := runCLI("service", "uninstall")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "removed")
			So(m.installed, ShouldBeFalse)
		})
	})
}

func TestServiceStatus(t *testing.T) {
	Convey("sctl service status", t, func() {
		m, ready := stubServiceEnv(t)
		address := os.Getenv("SCTL_BRIDGE_ADDR")

		Convey("未安装时退出码 0,如实说明", func() {
			code, out := runCLI("service", "status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "not installed")
		})

		Convey("已安装且 daemon 能连上时给出位置和地址", func() {
			m.installed = true
			ready.Store(true)
			code, out := runCLI("service", "status")
			So(code, ShouldEqual, exitOK)
			So(out, ShouldContainSubstring, "/fake/sctl.plist")
			So(out, ShouldContainSubstring, "running at "+address)
		})

		Convey("-o json 输出结构化结果", func() {
			m.installed = true
			code, out := runCLI("service", "status", "-o", "json")
			So(code, ShouldEqual, exitOK)
			var got serviceStatus
			So(json.Unmarshal([]byte(out), &got), ShouldBeNil)
			So(got, ShouldResemble, serviceStatus{Installed: true, Location: "/fake/sctl.plist", Address: address, Running: false})
		})
	})
}
