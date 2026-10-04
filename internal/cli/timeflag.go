package cli

import (
	"fmt"
	"regexp"
	"strconv"
	"time"
)

var relativeTime = regexp.MustCompile(`^(\d+)([dhm])$`)

var relativeUnitMillis = map[string]int64{
	"d": 24 * time.Hour.Milliseconds(),
	"h": time.Hour.Milliseconds(),
	"m": time.Minute.Milliseconds(),
}

// parseTimeFlag 把时间类命令行参数解析成自 epoch 起的毫秒数(浏览器接口用的时间单位)。
// 接受 RFC 3339(如 2026-09-01T08:00:00+08:00),或相对时长 Nd/Nh/Nm,表示「距 now 多久以前」。
// flag 只用于报错时点名参数;now 由调用方传入,时间解析因此可测。命令行参数是不可信输入:
// 无法解析或早于 1970 年时返回退出码 3 的错误,调用方应在发起任何调用之前先解析。
func parseTimeFlag(flag, value string, now time.Time) (int64, error) {
	var ms int64
	if match := relativeTime.FindStringSubmatch(value); match != nil {
		n, err := strconv.ParseInt(match[1], 10, 64)
		unit := relativeUnitMillis[match[2]]
		// 先与 now 比较再相乘:超出 epoch 的时长(含会溢出的数值)一并拒绝,不会绕回成一个未来时间。
		if err != nil || n > now.UnixMilli()/unit {
			return 0, invalidTimeFlag(flag, value, "reaches before 1970")
		}
		ms = now.UnixMilli() - n*unit
	} else {
		t, err := time.Parse(time.RFC3339, value)
		if err != nil {
			return 0, invalidTimeFlag(flag, value, "want RFC 3339 (2026-09-01T08:00:00+08:00) or a duration ago such as 7d, 12h, 30m")
		}
		ms = t.UnixMilli()
		if ms < 0 {
			return 0, invalidTimeFlag(flag, value, "reaches before 1970")
		}
	}
	return ms, nil
}

func invalidTimeFlag(flag, value, reason string) error {
	return &ExitError{Code: exitError, Message: fmt.Sprintf("invalid %s %q: %s", flag, value, reason)}
}
