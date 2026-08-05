// Package ratelimit 提供按 key 的滑动窗口限流,用于桥接的配对尝试节流。
// 具体限制由 protocol.json 定义并在构造调用处注入。
package ratelimit

import (
	"sync"
	"time"
)

// Limiter 是按 key 的滑动窗口计数器:每个 key 在 window 内最多放行 limit 次。
type Limiter struct {
	mu     sync.Mutex
	limit  int
	window time.Duration
	events map[string][]time.Time
	clock  func() time.Time
}

// NewLimiter 构造窗口 window 内上限 limit 次的限流器。
func NewLimiter(limit int, window time.Duration) *Limiter {
	return &Limiter{
		limit:  limit,
		window: window,
		events: make(map[string][]time.Time),
		clock:  time.Now,
	}
}

// Allow 记录一次 key 的尝试:窗口内未超上限则记账放行,否则拒绝且不记账。
func (l *Limiter) Allow(key string) bool {
	now := l.clock()
	cutoff := now.Add(-l.window)

	l.mu.Lock()
	defer l.mu.Unlock()

	kept := l.events[key][:0]
	for _, t := range l.events[key] {
		if t.After(cutoff) {
			kept = append(kept, t)
		}
	}
	if len(kept) >= l.limit {
		l.events[key] = kept
		return false
	}
	l.events[key] = append(kept, now)
	return true
}
