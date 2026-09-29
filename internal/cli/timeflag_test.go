package cli

import (
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
)

func TestParseTimeFlag(t *testing.T) {
	now := time.Date(2026, 9, 29, 12, 0, 0, 0, time.UTC)

	Convey("parseTimeFlag 把 RFC 3339 或相对时长解析成毫秒时间戳", t, func() {
		Convey("RFC 3339 取其绝对时刻,与时区无关", func() {
			ms, err := parseTimeFlag("--since", "2026-09-01T08:00:00+08:00", now)
			So(err, ShouldBeNil)
			So(ms, ShouldEqual, time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC).UnixMilli())
		})

		Convey("Nd、Nh、Nm 表示距现在多久以前", func() {
			for value, ago := range map[string]time.Duration{
				"7d":  7 * 24 * time.Hour,
				"12h": 12 * time.Hour,
				"30m": 30 * time.Minute,
				"0m":  0,
			} {
				ms, err := parseTimeFlag("--since", value, now)
				So(err, ShouldBeNil)
				So(ms, ShouldEqual, now.Add(-ago).UnixMilli())
			}
		})

		Convey("其他写法都是退出码 3 的错误,消息带上参数名和取值", func() {
			for _, value := range []string{"", "yesterday", "7", "d", "-7d", "1.5h", "7w", "7D", "2026-09-01", "99999999999999999999d", "9999999999d", "1969-12-31T23:59:59Z"} {
				ms, err := parseTimeFlag("--until", value, now)
				var exitErr *ExitError
				So(errors.As(err, &exitErr), ShouldBeTrue)
				So(exitErr.Code, ShouldEqual, exitError)
				So(exitErr.Message, ShouldContainSubstring, "--until")
				So(exitErr.Message, ShouldContainSubstring, value)
				So(ms, ShouldEqual, 0)
			}
		})
	})
}
