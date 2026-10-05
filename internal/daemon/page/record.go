package page

import (
	"cmp"
	"context"
	"encoding/json"
	"slices"
	"time"

	"go.uber.org/zap"
)

// recordTimeout 是录制的自动结束时间(spec 设计决策 4):录制中的标签页这么久没有 debug 命令就结束录制,
// 忘了停止录制的调用方不会让提示条一直挂着。
const recordTimeout = 60 * time.Minute

// recording 是标签页在一次附加里的录制状态。录制期间 daemon 不布置空闲断开,扩展也不做兜底断开。
type recording struct {
	on bool
	// ends 是自动结束的时间。timer 与 seq:每次重新计时、结束录制都递增 seq,已触发但晚到的旧计时器据此放弃。
	ends  time.Time
	timer Timer
	seq   uint64
}

// armRecording 为录制中的标签页重新开始自动结束计时。调用方持有 m.mu。
func (m *Manager) armRecording(t *Tab) {
	m.stopRecordTimer(t)
	seq := t.rec.seq
	t.rec.ends = m.clock.Now().Add(recordTimeout)
	t.rec.timer = m.clock.AfterFunc(recordTimeout, func() { m.recordExpired(t, seq) })
}

// stopRecordTimer 取消自动结束计时。调用方持有 m.mu。
func (m *Manager) stopRecordTimer(t *Tab) {
	t.rec.seq++
	if t.rec.timer != nil {
		t.rec.timer.Stop()
		t.rec.timer = nil
	}
}

// endRecording 结束标签页的录制;不通知扩展。调用方持有 m.mu。
func (m *Manager) endRecording(t *Tab) {
	m.stopRecordTimer(t)
	t.rec.on = false
}

// recordExpired 在自动结束计时到期时结束录制,之后同 debug stop。它像命令一样排进标签页的队列,排到时若期间
// 有过 debug 命令、停止录制或断开(seq 都会变化)就什么都不做。等待队列不设时限:到期一旦放弃,录制就再也不会自动结束,
// 而队列里的命令都受各自的超时约束。
func (m *Manager) recordExpired(armed *Tab, seq uint64) {
	key := tabKey{armed.instanceID, armed.id}
	s, err := m.acquire(context.Background(), key)
	if err != nil {
		panic("page: acquire without a deadline failed: " + err.Error())
	}
	m.mu.Lock()
	expired := armed.rec.seq == seq
	if expired {
		m.endRecording(armed)
	}
	m.mu.Unlock()
	if !expired {
		m.release(key, s)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if err := m.cdp.Record(ctx, key.instanceID, key.tabID, false); err != nil {
		// daemon 接着为它布置空闲断开,断开时扩展清除录制标记。
		m.log.Warn("failed to stop recording an expired tab", zap.String("instanceId", key.instanceID), zap.Int("tabId", key.tabID), zap.Error(err))
	}
	m.finish(key, s)
}

// runDebugStart 让标签页进入录制:已在录制时重新计时,不清空缓存。
func runDebugStart(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	if err := decodeInput(input, &struct{}{}); err != nil {
		return nil, err
	}
	m := t.m
	if err := m.cdp.Record(ctx, t.instanceID, t.id, true); err != nil {
		return nil, err
	}
	m.mu.Lock()
	// clearTab 在 m.mu 下取消 ctx:此刻 ctx 未结束,Tab 就仍是附加着的那个,不会为作废的 Tab 开始录制。
	err := ctx.Err()
	if err == nil {
		t.rec.on = true
		m.armRecording(t)
	}
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return m.tabStatus(t), nil
}

// debugStop 结束一个标签页或这个浏览器里全部标签页的录制,缓存保留,之后照常空闲断开。它从不附加调试器;
// 目标不在录制时也成功。
func (m *Manager) debugStop(ctx context.Context, instanceID string, req Request) (any, error) {
	var in detachInput
	if err := decodeInput(req.Input, &in); err != nil {
		return nil, err
	}
	if req.Activate {
		return nil, invalidRequest("--activate does not apply to debug stop")
	}
	if in.All {
		if req.TabID != nil {
			return nil, invalidRequest("give either a tab or all, not both")
		}
		var keys []tabKey
		m.mu.Lock()
		for key, s := range m.slots {
			if key.instanceID == instanceID && s.tab != nil && s.tab.rec.on {
				keys = append(keys, key)
			}
		}
		m.mu.Unlock()
		slices.SortFunc(keys, func(a, b tabKey) int { return cmp.Compare(a.tabID, b.tabID) })
		stopped := []int{}
		for _, key := range keys {
			ok, err := m.stopTab(ctx, key)
			if err != nil {
				return nil, err
			}
			if ok {
				stopped = append(stopped, key.tabID)
			}
		}
		return detachResult{TabIDs: stopped}, nil
	}
	tabID, err := m.targetTab(ctx, instanceID, req.TabID)
	if err != nil {
		return nil, err
	}
	ok, err := m.stopTab(ctx, tabKey{instanceID, tabID})
	if err != nil {
		return nil, err
	}
	res := detachResult{TabID: &tabID, TabIDs: []int{}}
	if ok {
		res.TabIDs = []int{tabID}
	}
	return res, nil
}

// stopTab 在标签页的队列里结束它的录制并通知扩展,返回它此前是否在录制。finish 随后为它恢复空闲计时。
func (m *Manager) stopTab(ctx context.Context, key tabKey) (bool, error) {
	var stopped bool
	_, err := m.onTab(ctx, key, true, func(ctx context.Context, s *slot) (any, error) {
		m.mu.Lock()
		t := s.tab
		stopped = t != nil && t.rec.on
		if stopped {
			m.endRecording(t)
		}
		m.mu.Unlock()
		if !stopped {
			return nil, nil
		}
		return nil, m.cdp.Record(ctx, key.instanceID, key.tabID, false)
	})
	return stopped, err
}

// tabStatus 是 debug status 里的一个标签页,也是 debug start 的结果。
type tabStatus struct {
	TabID      int       `json:"tabId"`
	AttachedAt time.Time `json:"attachedAt"`
	Recording  bool      `json:"recording"`
	// RemainingMs 是录制还剩多久自动结束,只在录制时给出。
	RemainingMs *int64      `json:"remainingMs,omitempty"`
	Console     bufferStats `json:"console"`
	Network     bufferStats `json:"network"`
}

func (m *Manager) tabStatus(t *Tab) tabStatus {
	st := tabStatus{TabID: t.id, AttachedAt: t.attachedAt, Console: t.console.stats(), Network: t.network.stats()}
	m.mu.Lock()
	defer m.mu.Unlock()
	if t.rec.on {
		st.Recording = true
		// 计时器已到期、结束录制还在队列里等待时,剩余时间是 0 而不是负数。
		remaining := max(t.rec.ends.Sub(m.clock.Now()), 0).Milliseconds()
		st.RemainingMs = &remaining
	}
	return st
}

type statusResult struct {
	Tabs []tabStatus `json:"tabs"`
}

// debugStatus 列出这个浏览器里被附加的标签页(给出 tab 时只列它)。它只读 daemon 的内存,不附加调试器,
// 也不为录制重新计时:否则报告的剩余时间永远是 60 分钟。
func (m *Manager) debugStatus(_ context.Context, instanceID string, req Request) (any, error) {
	if err := decodeInput(req.Input, &struct{}{}); err != nil {
		return nil, err
	}
	if req.Activate {
		return nil, invalidRequest("--activate does not apply to debug status")
	}
	var tabs []*Tab
	m.mu.Lock()
	for key, s := range m.slots {
		if key.instanceID == instanceID && s.tab != nil && (req.TabID == nil || key.tabID == *req.TabID) {
			tabs = append(tabs, s.tab)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(tabs, func(a, b *Tab) int { return cmp.Compare(a.id, b.id) })
	res := statusResult{Tabs: make([]tabStatus, len(tabs))}
	for i, t := range tabs {
		res.Tabs[i] = m.tabStatus(t)
	}
	return res, nil
}
