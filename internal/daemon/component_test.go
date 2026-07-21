package daemon

import (
	"testing"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLoopbackOnly(t *testing.T) {
	Convey("仅允许绑定 loopback 地址", t, func() {
		So(validateLoopback("127.0.0.1:8643"), ShouldBeNil)
		So(validateLoopback("[::1]:8643"), ShouldBeNil)
		So(validateLoopback("localhost:8643"), ShouldBeNil)
		So(validateLoopback("0.0.0.0:8643"), ShouldNotBeNil)
		So(validateLoopback("192.168.1.10:8643"), ShouldNotBeNil)
		So(validateLoopback("garbage"), ShouldNotBeNil)
	})
}
