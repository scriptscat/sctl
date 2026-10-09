package cdpendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// cleanupTimeout 限定客户端断开后关弹框、断开标签页、清除标记的总时长;浏览器已断开时这些调用立刻失败。
const cleanupTimeout = 10 * time.Second

// notificationBuffer 是一个会话里尚未被 Hook 取走的通知上限。以 var 暴露仅为便于测试。
var notificationBuffer = 4096

var (
	// errSessionOverflow 结束一个跟不上浏览器事件的会话:静默丢掉事件会让客户端的状态悄悄出错。
	errSessionOverflow = errors.New("the client fell too far behind the browser's events")
	errSessionClosed   = errors.New("cdpendpoint: the client session has ended")
)

// Target 是端点能附加的一个标签页。
type Target struct {
	TabID    int
	TargetID string
	Title    string
	URL      string
}

// OpenedTab 是为客户端打开的标签页。
type OpenedTab struct {
	TabID    int
	TargetID string
}

// Notification 是浏览器实例发来的一条通知:Method 是 generated.NotificationDebugger* 之一,Params 是通知对象本身。
type Notification struct {
	Method string
	Params json.RawMessage
}

// Browser 是一个客户端会话能对浏览器做的事,交给 Hook。它记下会话附加与标记过的标签页和其中已知未处理的弹框,
// 会话结束时据此关弹框、断开、清除标记(spec「断开」)。会话结束后它的方法都返回错误。
type Browser struct {
	m          *Manager
	instanceID string
	ctx        context.Context
	cancel     context.CancelCauseFunc
	events     chan Notification

	mu       sync.Mutex
	closed   bool
	inflight sync.WaitGroup
	// owned 是会话标为端点所有的标签页:附加过的与为客户端打开的。
	owned map[int]bool
	// attached 是会话发过命令、因而附加了调试器的标签页。
	attached map[int]bool
	// dialogs 是有已知未处理 JS 弹框的标签页,值是弹框所在的会话 ID(顶层为空)。
	dialogs map[int]string
}

func newBrowser(m *Manager, instanceID string, c *client) *Browser {
	return &Browser{
		m:          m,
		instanceID: instanceID,
		ctx:        c.ctx,
		cancel:     c.cancel,
		events:     make(chan Notification, notificationBuffer),
		owned:      map[int]bool{},
		attached:   map[int]bool{},
		dialogs:    map[int]string{},
	}
}

// InstanceID 是端点所属浏览器实例的 ID。
func (b *Browser) InstanceID() string { return b.instanceID }

// Notifications 送达这个浏览器的 debugger.event、debugger.detached 与 debugger.tabCreated / tabUpdated / tabRemoved,
// 按到达顺序。Hook 要及时取走:积压超过上限时会话被结束。channel 不会关闭,会话结束看 Serve 的 ctx。
func (b *Browser) Notifications() <-chan Notification { return b.events }

// Targets 列出浏览器里能附加的标签页(debugger.targets)。
func (b *Browser) Targets(ctx context.Context) ([]Target, error) {
	var res generated.DebuggerTargetsResult
	if err := b.call(ctx, generated.MethodDebuggerTargets, generated.DebuggerTargetsParams{}, &res); err != nil {
		return nil, err
	}
	targets := make([]Target, len(res.Targets))
	for i, t := range res.Targets {
		targets[i] = Target{TabID: t.TabId, TargetID: t.TargetId, Title: t.Title, URL: t.URL}
	}
	return targets, nil
}

// UserAgent 读浏览器真实的 User-Agent(debugger.userAgent)。
func (b *Browser) UserAgent(ctx context.Context) (string, error) {
	var res generated.DebuggerUserAgentResult
	if err := b.call(ctx, generated.MethodDebuggerUserAgent, generated.DebuggerUserAgentParams{}, &res); err != nil {
		return "", err
	}
	return res.UserAgent, nil
}

// Open 为客户端打开一个标签页(debugger.open);扩展已把它标为端点所有,会话结束时清除标记,标签页保留。
func (b *Browser) Open(ctx context.Context, url string, background bool) (OpenedTab, error) {
	params := generated.DebuggerOpenParams{URL: url}
	if background {
		params.Background = &background
	}
	var res generated.DebuggerOpenResult
	if err := b.call(ctx, generated.MethodDebuggerOpen, params, &res); err != nil {
		return OpenedTab{}, err
	}
	b.mu.Lock()
	b.owned[res.TabId] = true
	b.mu.Unlock()
	return OpenedTab{TabID: res.TabId, TargetID: res.TargetId}, nil
}

// CloseTab 关闭一个标签页(debugger.close),客户端关闭目标时用。
func (b *Browser) CloseTab(ctx context.Context, tabID int) error {
	if err := b.call(ctx, generated.MethodDebuggerClose, generated.DebuggerCloseParams{TabId: tabID}, nil); err != nil {
		return err
	}
	b.forget(tabID)
	return nil
}

// DetachTab 断开一个标签页的调试器(debugger.detach),客户端分离目标时用;扩展同时清除端点所有的标记。
func (b *Browser) DetachTab(ctx context.Context, tabID int) error {
	if err := b.call(ctx, generated.MethodDebuggerDetach, generated.DebuggerDetachParams{TabId: &tabID}, nil); err != nil {
		return err
	}
	b.forget(tabID)
	return nil
}

// Send 把一条 CDP 命令发给标签页的顶层会话,或 sessionID 指定的子会话(debugger.send),返回 Chrome 的结果。
// 第一次向一个标签页发命令前先把它标为端点所有,扩展的 10 分钟兜底空闲断开就不会断开它。
// Chrome 拒绝命令时返回 *bridge.Error{Code: INVALID_REQUEST},Message 是 Chrome 错误的 JSON 文本。
func (b *Browser) Send(ctx context.Context, tabID int, sessionID, method string, params json.RawMessage) (json.RawMessage, error) {
	if err := b.own(ctx, tabID); err != nil {
		return nil, err
	}
	in := generated.DebuggerSendParams{TabId: tabID, Method: method, Params: params}
	if sessionID != "" {
		in.SessionId = &sessionID
	}
	var res generated.DebuggerSendResult
	if err := b.call(ctx, generated.MethodDebuggerSend, in, &res); err != nil {
		return nil, err
	}
	return res.Result, nil
}

func (b *Browser) own(ctx context.Context, tabID int) error {
	b.mu.Lock()
	owned := b.owned[tabID]
	b.mu.Unlock()
	if !owned {
		if err := b.call(ctx, generated.MethodDebuggerOwn, generated.DebuggerOwnParams{TabId: tabID, Owned: true}, nil); err != nil {
			return err
		}
	}
	b.mu.Lock()
	b.owned[tabID] = true
	// 记在发送之前:命令失败时调试器也可能已经附加,多断开一次没有害处。
	b.attached[tabID] = true
	b.mu.Unlock()
	return nil
}

func (b *Browser) forget(tabID int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	delete(b.owned, tabID)
	delete(b.attached, tabID)
	delete(b.dialogs, tabID)
}

// call 在会话期间调用浏览器的一个内部方法;会话结束时调用随之取消。
func (b *Browser) call(ctx context.Context, method generated.Method, input, result any) error {
	b.mu.Lock()
	if b.closed {
		b.mu.Unlock()
		return errSessionClosed
	}
	b.inflight.Add(1)
	b.mu.Unlock()
	defer b.inflight.Done()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	defer context.AfterFunc(b.ctx, cancel)()
	return call(ctx, b.m.deps.Bridge, b.instanceID, method, input, result)
}

// deliver 在 bridge 读循环里处理一条通知:先更新会话记下的标签页状态,再排给 Hook。不阻塞。
func (b *Browser) deliver(method string, params json.RawMessage) {
	b.track(method, params)
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	select {
	case b.events <- Notification{Method: method, Params: params}:
	default:
		b.cancel(errSessionOverflow)
	}
}

// track 记下弹框的开关与 Chrome 自己断开、关闭的标签页:后者已经没有调试器,也不再带端点所有的标记。
// 弹框事件在弹框所在的子会话里打开时也带着那个会话 ID。
func (b *Browser) track(method string, params json.RawMessage) {
	switch generated.Notification(method) {
	case generated.NotificationDebuggerEvent:
		var ev generated.DebuggerEventNotification
		if err := json.Unmarshal(params, &ev); err != nil {
			b.m.log.Debug("ignoring a malformed debugger.event", zap.Error(err))
			return
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		switch ev.Method {
		case "Page.javascriptDialogOpening":
			sessionID := ""
			if ev.SessionId != nil {
				sessionID = *ev.SessionId
			}
			b.dialogs[ev.TabId] = sessionID
		case "Page.javascriptDialogClosed":
			delete(b.dialogs, ev.TabId)
		}
	case generated.NotificationDebuggerDetached:
		var n generated.DebuggerDetachedNotification
		if err := json.Unmarshal(params, &n); err != nil {
			b.m.log.Debug("ignoring a malformed debugger.detached", zap.Error(err))
			return
		}
		b.forget(n.TabId)
	case generated.NotificationDebuggerTabRemoved:
		var n generated.DebuggerTabRemovedNotification
		if err := json.Unmarshal(params, &n); err != nil {
			b.m.log.Debug("ignoring a malformed debugger.tabRemoved", zap.Error(err))
			return
		}
		b.forget(n.TabId)
	}
}

// end 结束会话:之后的调用一律失败,并等正在进行的调用返回,清理才不会被它们重新附加的标签页打乱。
func (b *Browser) end() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.inflight.Wait()
}

// OnNotification 实现 bridge.BrowserListener:把浏览器的通知交给连着的客户端会话。运行在 bridge 读循环里,不阻塞。
func (m *Manager) OnNotification(instanceID, method string, params json.RawMessage) {
	m.mu.Lock()
	var b *Browser
	if ep := m.byInstance[instanceID]; ep != nil && ep.client != nil {
		b = ep.client.browser
	}
	m.mu.Unlock()
	if b != nil {
		b.deliver(method, params)
	}
}

// runSession 运行一个已交出标签页的客户端,直到它断开、Hook 返回或 ctx 结束,然后清理并收回标签页。
func (m *Manager) runSession(ep *endpoint, c *client, conn *websocket.Conn) {
	b := newBrowser(m, ep.instanceID, c)
	m.mu.Lock()
	c.browser = b
	m.mu.Unlock()
	m.log.Info("a CDP endpoint client connected", zap.String("browser", ep.instanceID))
	// 客户端的 ctx 结束(close、失效、浏览器断开、daemon 退出、会话返回)时由这里以关闭帧关闭连接,之后才结束 Hook 的 ctx:
	// coder/websocket 在读写的 ctx 结束时直接断开 TCP,先结束它客户端就收不到关闭原因。
	hookCtx, stopHook := context.WithCancel(context.WithoutCancel(c.ctx))
	defer stopHook()
	context.AfterFunc(c.ctx, func() {
		code, reason := closeReason(c.ctx)
		_ = conn.Close(code, reason)
		stopHook()
	})
	err := m.deps.Hook.Serve(hookCtx, conn, b)
	c.cancel(errClientGone)
	m.log.Info("a CDP endpoint client disconnected", zap.String("browser", ep.instanceID), zap.NamedError("reason", err))
	b.end()
	m.cleanup(b)
	m.deps.Pages.Reclaim(ep.instanceID)
	m.release(ep, c)
}

// cleanup 先关闭端点标签页上已知的 JS 弹框(调试器断开后留下的弹框会挡住之后的每一次附加),再断开会话附加的标签页,
// 最后清除全部端点所有的标记;标签页本身都保留(spec 设计决策 7)。失败只记日志:浏览器可能已经断开,扩展断开时会自己
// 断开全部调试器。
func (m *Manager) cleanup(b *Browser) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(b.ctx), cleanupTimeout)
	defer cancel()
	b.mu.Lock()
	dialogs := make(map[int]string, len(b.dialogs))
	for tab, session := range b.dialogs {
		if b.attached[tab] {
			dialogs[tab] = session
		}
	}
	attached := sortedTabs(b.attached)
	owned := sortedTabs(b.owned)
	b.mu.Unlock()

	for _, tab := range sortedTabs(dialogs) {
		in := generated.DebuggerSendParams{TabId: tab, Method: "Page.handleJavaScriptDialog", Params: json.RawMessage(`{"accept":false}`)}
		if session := dialogs[tab]; session != "" {
			in.SessionId = &session
		}
		m.logCleanup("dismiss the JS dialog", tab, call(ctx, m.deps.Bridge, b.instanceID, generated.MethodDebuggerSend, in, nil))
	}
	for _, tab := range attached {
		m.logCleanup("detach", tab, call(ctx, m.deps.Bridge, b.instanceID, generated.MethodDebuggerDetach, generated.DebuggerDetachParams{TabId: &tab}, nil))
	}
	for _, tab := range owned {
		m.logCleanup("clear the endpoint mark", tab, call(ctx, m.deps.Bridge, b.instanceID, generated.MethodDebuggerOwn, generated.DebuggerOwnParams{TabId: tab}, nil))
	}
}

func (m *Manager) logCleanup(step string, tabID int, err error) {
	if err != nil {
		m.log.Debug("CDP endpoint cleanup step failed", zap.String("step", step), zap.Int("tabId", tabID), zap.Error(err))
	}
}

func sortedTabs[V any](tabs map[int]V) []int {
	out := make([]int, 0, len(tabs))
	for tab := range tabs {
		out = append(out, tab)
	}
	slices.Sort(out)
	return out
}

// call 调用实例上的一个内部浏览器方法;扩展的失败统一为 *bridge.Error。结果已由 bridge 按 schema 校验过。
func call(ctx context.Context, b Bridge, instanceID string, method generated.Method, input, result any) error {
	raw, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("encode %s input: %w", method, err)
	}
	resp, err := b.CallInstance(ctx, instanceID, bridge.Request{Action: string(method), Input: raw})
	if err != nil {
		return err
	}
	if !resp.OK {
		if resp.Error == nil {
			return fmt.Errorf("%s failed without an error", method)
		}
		return resp.Error
	}
	if result == nil {
		return nil
	}
	if err := json.Unmarshal(resp.Result, result); err != nil {
		return fmt.Errorf("decode %s result: %w", method, err)
	}
	return nil
}
