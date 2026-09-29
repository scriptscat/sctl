package page

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

const (
	// defaultTimeout 是动作的默认超时(spec 设计决策 8),包含在同一标签页上排队等待的时间。
	defaultTimeout = 10 * time.Second
	// idleTimeout 是调试器的空闲断开时间(spec 设计决策 3)。daemon 是这个计时的权威,
	// 扩展侧更长的兜底计时只防 daemon 失联后提示条一直挂着。
	idleTimeout = 5 * time.Minute
	// cleanupTimeout 限定 daemon 自行发起的断开(空闲、附加准备失败),它们不属于任何调用方的命令。
	cleanupTimeout = 10 * time.Second
)

// Request 是一次页面动作。TabID 为 nil 时使用默认标签页;Timeout 为 0 时使用动作的默认超时。
type Request struct {
	Action   string
	Browser  string
	TabID    *int
	Activate bool
	Timeout  time.Duration
	Input    json.RawMessage
}

// Tab 是一个标签页在一次附加期间的自动化状态,交给动作处理函数使用;调试器分离后整个 Tab 作废,
// 下一次附加得到新的 Tab。
type Tab struct {
	m          *Manager
	instanceID string
	id         int
}

// ID 返回标签页 ID。
func (t *Tab) ID() int { return t.id }

// send 在标签页的顶层会话上执行一条 CDP 命令,result 非 nil 时把结果解到其中。
func (t *Tab) send(ctx context.Context, method string, params, result any) error {
	// 已结束的命令不能再发命令:调试器分离后的任何一条命令都会让扩展悄悄重新附加。
	if err := ctx.Err(); err != nil {
		return err
	}
	var raw json.RawMessage
	if params != nil {
		encoded, err := json.Marshal(params)
		if err != nil {
			return fmt.Errorf("encode %s params: %w", method, err)
		}
		raw = encoded
	}
	res, err := t.m.cdp.Send(ctx, t.instanceID, Command{TabID: t.id, Method: method, Params: raw})
	if err != nil {
		return err
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(res, result); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}

// handler 在已解析、已附加并按标签页串行化的目标上执行一个动作,返回值编码为动作结果。
type handler func(ctx context.Context, t *Tab, input json.RawMessage) (any, error)

// browserHandler 执行不附加调试器、作用于整个浏览器实例的动作(page detach)。
type browserHandler func(ctx context.Context, instanceID string, req Request) (any, error)

// action 是一个已注册的页面动作,tab 与 browser 恰有一个非 nil。
type action struct {
	tab     handler
	browser browserHandler
}

// attachHook 在标签页每次附加后、第一个动作执行前按注册顺序运行,为这次附加准备页面状态。
type attachHook func(ctx context.Context, t *Tab) error

// Clock 提供空闲断开计时;测试注入假时钟。
type Clock interface {
	AfterFunc(d time.Duration, f func()) Timer
}

// Timer 是 Clock.AfterFunc 返回的计时器。
type Timer interface {
	Stop() bool
}

type realClock struct{}

func (realClock) AfterFunc(d time.Duration, f func()) Timer { return time.AfterFunc(d, f) }

type tabKey struct {
	instanceID string
	tabID      int
}

// slot 是一个标签页的串行队列与附加状态。
type slot struct {
	// turn 容量为 1:持有者即当前在这个标签页上执行的命令;channel 的发送等待队列按到达顺序放行。
	turn chan struct{}
	// refs 是持有或等待 turn 的数量;为 0 且未附加时从表中删除。
	refs int
	// tab 非 nil 表示已附加并完成附加钩子。
	tab *Tab
	// idle 与 idleSeq:每次开始命令或重新计时都递增 idleSeq,已触发但晚到的旧计时器据此放弃。
	idle    Timer
	idleSeq uint64
	// cancel 取消正在执行的命令,cause 是交给调用方的错误。
	cancel context.CancelCauseFunc
}

// Manager 持有全部浏览器实例上的页面自动化状态。
type Manager struct {
	cdp     CDP
	log     *zap.Logger
	clock   Clock
	actions map[string]action
	hooks   []attachHook

	mu    sync.Mutex
	slots map[tabKey]*slot
}

// NewManager 构造页面自动化组件并注册全部动作。
func NewManager(cdp CDP, log *zap.Logger) *Manager {
	m := &Manager{
		cdp:     cdp,
		log:     log,
		clock:   realClock{},
		actions: map[string]action{},
		slots:   map[tabKey]*slot{},
	}
	m.addAttachHook(enableFocusEmulation)
	m.register("eval", runEval)
	m.registerBrowser("detach", m.detach)
	return m
}

func (m *Manager) register(name string, h handler) {
	m.addAction(name, action{tab: h})
}

func (m *Manager) registerBrowser(name string, h browserHandler) {
	m.addAction(name, action{browser: h})
}

func (m *Manager) addAction(name string, a action) {
	if _, dup := m.actions[name]; dup {
		panic("page: action " + name + " registered twice")
	}
	m.actions[name] = a
}

func (m *Manager) addAttachHook(h attachHook) {
	m.hooks = append(m.hooks, h)
}

// enableFocusEmulation 让附加期间的页面以为自己可见且有焦点。真机探针显示,不这样做时 Chrome 丢弃发往
// 后台标签页的鼠标与按键事件、截图在 Chrome 125 上不返回、requestAnimationFrame 永不触发。
func enableFocusEmulation(ctx context.Context, t *Tab) error {
	return t.send(ctx, "Emulation.setFocusEmulationEnabled", map[string]bool{"enabled": true}, nil)
}

// Do 执行一次页面动作,返回 JSON 结果。调用方取消时返回 ctx 的错误,其余失败都是 *Error。
func (m *Manager) Do(ctx context.Context, req Request) (json.RawMessage, error) {
	a, ok := m.actions[req.Action]
	if !ok {
		return nil, invalidRequest("unknown page action " + req.Action)
	}
	timeout := defaultTimeout
	if req.Timeout > 0 {
		timeout = req.Timeout
	}
	ctx, cancel := context.WithTimeoutCause(ctx, timeout, &Error{
		Code:    generated.ErrorCodeTimeout,
		Message: fmt.Sprintf("page %s did not finish within %s", req.Action, timeout),
	})
	defer cancel()

	result, err := m.dispatch(ctx, a, req)
	if err != nil {
		return nil, causeOf(ctx, err)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("encode page %s result: %w", req.Action, err)
	}
	return raw, nil
}

func (m *Manager) dispatch(ctx context.Context, a action, req Request) (any, error) {
	instanceID, err := m.cdp.ResolveBrowser(req.Browser)
	if err != nil {
		return nil, err
	}
	if a.browser != nil {
		return a.browser(ctx, instanceID, req)
	}
	tabID, err := m.targetTab(ctx, instanceID, req.TabID)
	if err != nil {
		return nil, err
	}
	return m.onTab(ctx, tabKey{instanceID, tabID}, func(ctx context.Context, s *slot) (any, error) {
		if req.Activate {
			if err := m.cdp.SelectTab(ctx, instanceID, tabID); err != nil {
				return nil, err
			}
		}
		t, err := m.attach(ctx, s, instanceID, tabID)
		if err != nil {
			return nil, err
		}
		return a.tab(ctx, t, req.Input)
	})
}

// targetTab 返回显式指定的标签页,否则在此刻查询一次默认标签页:之后用户切换标签页不改变本次命令的目标。
func (m *Manager) targetTab(ctx context.Context, instanceID string, explicit *int) (int, error) {
	if explicit != nil {
		return *explicit, nil
	}
	return m.cdp.CurrentTab(ctx, instanceID)
}

// causeOf 在 ctx 已结束时用结束原因替换 err:超时、调试器分离都以 cause 携带领域错误,
// 调用方取消则是 context.Canceled。
func causeOf(ctx context.Context, err error) error {
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	return err
}

// onTab 在 key 的串行队列里执行 fn,并在结束后为已附加的标签页重新开始空闲计时。
func (m *Manager) onTab(ctx context.Context, key tabKey, fn func(ctx context.Context, s *slot) (any, error)) (any, error) {
	s, err := m.acquire(ctx, key)
	if err != nil {
		return nil, err
	}
	runCtx, cancel := context.WithCancelCause(ctx)
	m.mu.Lock()
	m.stopIdle(s)
	s.cancel = cancel
	m.mu.Unlock()
	defer func() {
		cancel(nil)
		m.finish(key, s)
	}()
	result, err := fn(runCtx, s)
	if err != nil {
		return nil, causeOf(runCtx, err)
	}
	return result, nil
}

func (m *Manager) acquire(ctx context.Context, key tabKey) (*slot, error) {
	m.mu.Lock()
	s := m.slots[key]
	if s == nil {
		s = &slot{turn: make(chan struct{}, 1)}
		m.slots[key] = s
	}
	s.refs++
	m.mu.Unlock()
	select {
	case s.turn <- struct{}{}:
		return s, nil
	case <-ctx.Done():
		m.mu.Lock()
		s.refs--
		m.dropIfUnused(key, s)
		m.mu.Unlock()
		return nil, ctx.Err()
	}
}

// finish 结束一条命令:标签页仍附加时重新开始空闲计时,然后放出 turn。
func (m *Manager) finish(key tabKey, s *slot) {
	m.mu.Lock()
	s.cancel = nil
	if s.tab != nil {
		s.idleSeq++
		seq := s.idleSeq
		s.idle = m.clock.AfterFunc(idleTimeout, func() { m.idleExpired(key, s, seq) })
	}
	m.mu.Unlock()
	m.release(key, s)
}

// release 放出 turn,没有其他命令持有或等待且未附加时删除 slot。
func (m *Manager) release(key tabKey, s *slot) {
	m.mu.Lock()
	s.refs--
	m.dropIfUnused(key, s)
	m.mu.Unlock()
	<-s.turn
}

// dropIfUnused 在没有命令持有或等待、也没有附加时删除 slot。调用方持有 m.mu。
func (m *Manager) dropIfUnused(key tabKey, s *slot) {
	if s.refs == 0 && s.tab == nil && m.slots[key] == s {
		delete(m.slots, key)
	}
}

// stopIdle 取消空闲计时;已经触发的旧计时器看到 idleSeq 变化后放弃。调用方持有 m.mu。
func (m *Manager) stopIdle(s *slot) {
	s.idleSeq++
	if s.idle != nil {
		s.idle.Stop()
		s.idle = nil
	}
}

// clearTab 作废标签页的附加状态并以 cause 取消其正在执行的命令。调用方持有 m.mu。
func (m *Manager) clearTab(key tabKey, s *slot, cause error) {
	m.stopIdle(s)
	s.tab = nil
	if s.cancel != nil {
		s.cancel(cause)
	}
	m.dropIfUnused(key, s)
}

// attach 返回标签页当前的附加状态;尚未附加时由第一条 CDP 命令触发扩展附加,并运行全部附加钩子。
func (m *Manager) attach(ctx context.Context, s *slot, instanceID string, tabID int) (*Tab, error) {
	m.mu.Lock()
	t := s.tab
	m.mu.Unlock()
	if t != nil {
		return t, nil
	}
	t = &Tab{m: m, instanceID: instanceID, id: tabID}
	for _, hook := range m.hooks {
		if err := hook(ctx, t); err != nil {
			m.abandonAttach(instanceID, tabID, err)
			return nil, err
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	// 钩子运行期间收到的分离通知已取消 ctx;此时附加已经失效,不能记为已附加。
	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	s.tab = t
	return t, nil
}

// abandonAttach 在附加钩子失败后断开标签页:扩展可能已经附加,daemon 不记录它就不会再为它计时断开。
func (m *Manager) abandonAttach(instanceID string, tabID int, cause error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	if _, err := m.cdp.Detach(ctx, instanceID, &tabID); err != nil {
		m.log.Debug("failed to detach after a failed attach setup", zap.Int("tabId", tabID), zap.NamedError("setupError", cause), zap.Error(err))
	}
}

// idleExpired 在空闲计时到期时断开标签页。它像命令一样排进标签页的队列,所以不会打断正在执行的命令;
// 排到时若期间有过新命令(idleSeq 变化)或已经断开,就什么都不做。
func (m *Manager) idleExpired(key tabKey, armed *slot, seq uint64) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	s, err := m.acquire(ctx, key)
	if err != nil {
		return
	}
	defer m.release(key, s)
	m.mu.Lock()
	expired := s == armed && s.idleSeq == seq && s.tab != nil
	if expired {
		s.tab = nil
		s.idle = nil
	}
	m.mu.Unlock()
	if expired {
		if _, err := m.cdp.Detach(ctx, key.instanceID, &key.tabID); err != nil {
			m.log.Warn("failed to detach an idle tab", zap.String("instanceId", key.instanceID), zap.Int("tabId", key.tabID), zap.Error(err))
		}
	}
}

// OnNotification 接收浏览器实例的通知。它运行在 bridge 的读循环里,只做内存状态更新。
func (m *Manager) OnNotification(instanceID, method string, params json.RawMessage) {
	if method != string(generated.NotificationDebuggerDetached) {
		return
	}
	var n generated.DebuggerDetachedNotification
	if err := json.Unmarshal(params, &n); err != nil {
		// bridge 已按 schema 校验过通知,解不开说明生成类型与 schema 不一致。
		m.log.Error("failed to decode a debugger.detached notification", zap.Error(err))
		return
	}
	key := tabKey{instanceID, n.TabId}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[key]; s != nil {
		m.clearTab(key, s, detachedError(fmt.Sprintf("the debugger detached from tab %d (%s) while the command was running", n.TabId, n.Reason)))
	}
}

// OnInstanceGone 清空一个断开连接的浏览器实例上的全部页面状态;扩展在连接断开时自行断开全部调试器。
func (m *Manager) OnInstanceGone(instanceID string) {
	m.clearInstance(instanceID, detachedError("the browser disconnected while the command was running"))
}

func (m *Manager) clearInstance(instanceID string, cause error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for key, s := range m.slots {
		if key.instanceID == instanceID {
			m.clearTab(key, s, cause)
		}
	}
}

func detachedError(message string) *Error {
	return &Error{Code: generated.ErrorCodeDebuggerDetached, Message: message}
}

// detachInput 是 page detach 的输入:all 断开这个浏览器里的全部标签页。
type detachInput struct {
	All bool `json:"all"`
}

// detachResult 是 page detach 的结果:TabID 是单标签页模式下的目标,TabIDs 是实际断开的标签页。
type detachResult struct {
	TabID  *int  `json:"tabId,omitempty"`
	TabIDs []int `json:"tabIds"`
}

// detach 断开一个标签页或全部标签页并清空它们的状态;目标没有附加时也成功。它从不附加调试器。
func (m *Manager) detach(ctx context.Context, instanceID string, req Request) (any, error) {
	var in detachInput
	if err := decodeInput(req.Input, &in); err != nil {
		return nil, err
	}
	if req.Activate {
		return nil, invalidRequest("--activate does not apply to page detach")
	}
	if in.All {
		if req.TabID != nil {
			return nil, invalidRequest("give either a tab or all, not both")
		}
		tabIDs, err := m.cdp.Detach(ctx, instanceID, nil)
		if err != nil {
			return nil, err
		}
		m.clearInstance(instanceID, detachedError("page detach --all detached the debugger while the command was running"))
		return detachResult{TabIDs: nonNil(tabIDs)}, nil
	}
	tabID, err := m.targetTab(ctx, instanceID, req.TabID)
	if err != nil {
		return nil, err
	}
	key := tabKey{instanceID, tabID}
	return m.onTab(ctx, key, func(ctx context.Context, s *slot) (any, error) {
		tabIDs, err := m.cdp.Detach(ctx, instanceID, &tabID)
		if err != nil {
			return nil, err
		}
		m.mu.Lock()
		m.stopIdle(s)
		s.tab = nil
		m.mu.Unlock()
		return detachResult{TabID: &tabID, TabIDs: nonNil(tabIDs)}, nil
	})
}

func nonNil(ids []int) []int {
	if ids == nil {
		return []int{}
	}
	return ids
}

// decodeInput 严格解码动作输入:未知字段是调用方的错误,不静默忽略。空输入等同 {}。
func decodeInput(raw json.RawMessage, v any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return invalidRequest("invalid page action input: " + err.Error())
	}
	if dec.More() {
		return invalidRequest("invalid page action input: trailing data")
	}
	return nil
}
