package control

import (
	"context"
	"errors"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestDialDoesNotStartDaemon(t *testing.T) {
	Convey("daemon 未运行时只报告不可达,不尝试启动 serve", t, func() {
		SetAddress("127.0.0.1:1")
		t.Cleanup(func() { SetAddress("") })

		_, err := Dial(context.Background())

		So(errors.Is(err, ErrDaemonUnreachable), ShouldBeTrue)
	})
}

func TestResolveBaseURL(t *testing.T) {
	Convey("控制端点地址解析", t, func() {
		Convey("显式地址覆盖默认端口", func() {
			SetAddress("127.0.0.1:9999")
			defer SetAddress("")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:9999")
		})

		Convey("未设置时回退协议默认端口 8643", func() {
			SetAddress("")
			base, err := resolveBaseURL()
			So(err, ShouldBeNil)
			So(base, ShouldEqual, "http://127.0.0.1:8643")
		})
	})
}
