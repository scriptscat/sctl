package ratelimit

import (
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestLimiter(t *testing.T) {
	Convey("滑动窗口限流", t, func() {
		now := time.Unix(0, 0)
		l := NewLimiter(3, time.Minute)
		l.clock = func() time.Time { return now }

		Convey("窗口内放行至上限,超出即拒绝", func() {
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeFalse)
		})

		Convey("不同 key 各自独立计数", func() {
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeFalse)
			So(l.Allow("c2"), ShouldBeTrue)
		})

		Convey("窗口滑过后旧事件过期,重新放行", func() {
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeTrue)
			So(l.Allow("c1"), ShouldBeFalse)
			now = now.Add(61 * time.Second)
			So(l.Allow("c1"), ShouldBeTrue)
		})
	})
}
