package control

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

// gatedHealthServer 返回一个健康检查服务:up 为 false 时 503(未就绪),true 时 200。
func gatedHealthServer(up *atomic.Bool) *httptest.Server {
	mux := http.NewServeMux()
	mux.HandleFunc(PathHealth, func(w http.ResponseWriter, _ *http.Request) {
		if up.Load() {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
	})
	return httptest.NewServer(mux)
}

func TestEnsureDaemon(t *testing.T) {
	Convey("自动拉起与端口竞态回退", t, func() {
		origSpawn := spawnDaemon
		origTimeout := launchTimeout
		origPoll := pollInterval
		Reset(func() {
			spawnDaemon = origSpawn
			launchTimeout = origTimeout
			pollInterval = origPoll
		})
		launchTimeout = 2 * time.Second
		pollInterval = 5 * time.Millisecond
		ctx := context.Background()

		Convey("daemon 已在运行:直接连通,不拉起", func() {
			var up atomic.Bool
			up.Store(true)
			srv := gatedHealthServer(&up)
			defer srv.Close()
			var spawned atomic.Int32
			spawnDaemon = func() error { spawned.Add(1); return nil }

			c := &Client{http: srv.Client(), base: srv.URL}
			So(c.ensureDaemon(ctx), ShouldBeNil)
			So(spawned.Load(), ShouldEqual, 0)
		})

		Convey("daemon 未运行:拉起后就绪即连通", func() {
			var up atomic.Bool
			srv := gatedHealthServer(&up)
			defer srv.Close()
			var spawned atomic.Int32
			spawnDaemon = func() error {
				spawned.Add(1)
				up.Store(true) // 拉起即就绪
				return nil
			}

			c := &Client{http: srv.Client(), base: srv.URL}
			So(c.ensureDaemon(ctx), ShouldBeNil)
			So(spawned.Load(), ShouldEqual, 1)
		})

		Convey("端口竞态:本进程拉起未建立监听,但轮询连上既有胜出者", func() {
			var up atomic.Bool
			srv := gatedHealthServer(&up)
			defer srv.Close()
			// spawn 自身不建立监听(模拟 serve 绑定失败即退出);胜出者稍后就绪。
			spawnDaemon = func() error {
				go func() {
					time.Sleep(40 * time.Millisecond)
					up.Store(true)
				}()
				return nil
			}

			c := &Client{http: srv.Client(), base: srv.URL}
			So(c.ensureDaemon(ctx), ShouldBeNil)
		})

		Convey("拉起后仍不可达 → 超时错误", func() {
			var up atomic.Bool // 始终 false
			srv := gatedHealthServer(&up)
			defer srv.Close()
			launchTimeout = 150 * time.Millisecond
			spawnDaemon = func() error { return nil }

			c := &Client{http: srv.Client(), base: srv.URL}
			So(c.ensureDaemon(ctx), ShouldEqual, ErrDaemonUnreachable)
		})
	})
}

func TestResolveBaseURL(t *testing.T) {
	Convey("控制端点地址解析", t, func() {
		Convey("SCTL_BRIDGE_ADDR 覆盖默认端口", func() {
			t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:9999")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:9999")
		})

		Convey("未设置时回退协议默认端口 8643", func() {
			os.Unsetenv("SCTL_BRIDGE_ADDR")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:8643")
		})
	})
}
