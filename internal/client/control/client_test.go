package control

import (
	"context"
	"errors"
	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDialDoesNotStartDaemon(t *testing.T) {
	Convey("daemon 未运行时只报告不可达,不尝试启动 serve", t, func() {
		t.Setenv("SCTL_BRIDGE_ADDR", "127.0.0.1:1")

		_, err := Dial(context.Background())

		So(errors.Is(err, ErrDaemonUnreachable), ShouldBeTrue)
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
