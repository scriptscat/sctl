// Package audit 记录守卫侧的安全事件。
//
// 审计的权威存储在扩展侧;本包记录 daemon 本地观察到的握手、配对、Origin 拒绝和限流事件,
// 补足未转发到扩展的安全信号,边界见 docs/threat-model.md。
//
// 事件只有固定字段(类型 / 客户端标识 / 原因分类),没有承载任意载荷的出口,以此保证
// 安全约束见 docs/threat-model.md:token 原文、脚本源码、含凭据的 URL 永不进入审计与日志。
package audit

import (
	"sort"
	"sync"
	"time"

	"go.uber.org/zap"
)

// Type 是安全事件类型。
type Type string

const (
	TypeHandshakeOK        Type = "handshake.ok"
	TypeHandshakeFailed    Type = "handshake.failed"
	TypeOriginRejected     Type = "origin.rejected"
	TypePairingRateLimited Type = "pairing.rate_limited"
	TypePairingFailed      Type = "pairing.failed"
)

// Reason 是失败原因分类。刻意用固定枚举而非原始错误串,避免错误信息把敏感内容带进审计。
type Reason string

const (
	ReasonHMACMismatch  Reason = "hmac_mismatch"
	ReasonTimeout       Reason = "timeout"
	ReasonProtocol      Reason = "protocol_violation"
	ReasonPairExpired   Reason = "pairing_expired"
	ReasonPairExhausted Reason = "pairing_attempts_exhausted"
)

// Event 是一条安全事件。字段集合是封闭的(见包注释)。
type Event struct {
	At     time.Time `json:"at"`
	Type   Type      `json:"type"`
	Client string    `json:"client,omitempty"`
	Reason Reason    `json:"reason,omitempty"`
}

// TypeCount 是按类型聚合的事件计数。
type TypeCount struct {
	Type  Type `json:"type"`
	Count int  `json:"count"`
}

// Recorder 是固定容量的事件环形缓冲,同时把每条事件结构化输出到日志。
// 可查询的这份只驻内存(daemon 重启即清空);日志那份随 <dataDir>/logs 落盘,
// 两个出口都靠上面封闭的字段集合保证不含敏感内容。
type Recorder struct {
	mu     sync.Mutex
	cap    int
	events []Event
	log    *zap.Logger
	clock  func() time.Time
}

// NewRecorder 构造容量为 capacity 的记录器。
func NewRecorder(capacity int, log *zap.Logger) *Recorder {
	if log == nil {
		log = zap.NewNop()
	}
	return &Recorder{
		cap:    capacity,
		events: make([]Event, 0, capacity),
		log:    log,
		clock:  time.Now,
	}
}

// Record 追加一条事件,超出容量时淘汰最旧的一条。At 为零值时由记录器打上时间戳。
func (r *Recorder) Record(ev Event) {
	if ev.At.IsZero() {
		ev.At = r.clock()
	}

	r.mu.Lock()
	if len(r.events) >= r.cap {
		r.events = append(r.events[:0], r.events[len(r.events)-r.cap+1:]...)
	}
	r.events = append(r.events, ev)
	r.mu.Unlock()

	r.log.Warn("security event",
		zap.String("event", string(ev.Type)),
		zap.String("client", ev.Client),
		zap.String("reason", string(ev.Reason)),
	)
}

// Snapshot 返回按发生顺序排列的事件副本。
func (r *Recorder) Snapshot() []Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Event, len(r.events))
	copy(out, r.events)
	return out
}

// Summarize 按类型聚合计数,数量相同时按类型名排序保证输出稳定。
func Summarize(events []Event) []TypeCount {
	if len(events) == 0 {
		return nil
	}
	counts := make(map[Type]int, len(events))
	for _, ev := range events {
		counts[ev.Type]++
	}
	out := make([]TypeCount, 0, len(counts))
	for t, c := range counts {
		out = append(out, TypeCount{Type: t, Count: c})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})
	return out
}
