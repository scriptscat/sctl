package cdpendpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// eventLog 按发生顺序记录假扩展收到的调用与页面组件的交出、收回,断言顺序用。
type eventLog struct {
	mu      sync.Mutex
	entries []string
	changed chan struct{}
}

func newEventLog() *eventLog { return &eventLog{changed: make(chan struct{}, 1)} }

func (l *eventLog) add(entry string) {
	l.mu.Lock()
	l.entries = append(l.entries, entry)
	l.mu.Unlock()
	select {
	case l.changed <- struct{}{}:
	default:
	}
}

func (l *eventLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.entries...)
}

func (l *eventLog) count(entry string) int {
	n := 0
	for _, e := range l.all() {
		if e == entry {
			n++
		}
	}
	return n
}

// waitFor 等到记录里出现 entry 的第 n 次;超时返回 false。
func (l *eventLog) waitFor(entry string, n int) bool {
	deadline := time.After(5 * time.Second)
	for l.count(entry) < n {
		select {
		case <-l.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			return false
		}
	}
	return true
}

// fakeBridge 是扩展中转之前的 bridge:按名称解析实例,对内部浏览器方法给出扩展的典型回答并记录调用。
type fakeBridge struct {
	mu        sync.Mutex
	instances map[string]*fakeInstance // 按名称
	log       *eventLog
	// fail 让某个方法回答一个扩展错误。
	fail map[generated.Method]bridge.Error
	// tabs 是 debugger.targets 列出的标签页;nil 时只有标签页 5。
	tabs []generated.DebuggerTabNotification
	// opened 是 debugger.open 打开的标签页;零值时是标签页 100。
	opened generated.DebuggerOpenResult
	// cdp 回答 debugger.send,像 Chrome 一样:失败时返回 Code 非空的扩展错误;nil 时一律回答 {"ok":true}。它在
	// CallInstance 的 goroutine 里运行,可以阻塞到 ctx 结束,也可以先经 OnNotification 送出事件,模拟 Chrome 在应答之前发出的事件。
	cdp func(ctx context.Context, tabID int, sessionID, method string, params json.RawMessage) (json.RawMessage, bridge.Error)
}

type fakeInstance struct {
	id     string
	online bool
}

func newFakeBridge(log *eventLog) *fakeBridge {
	return &fakeBridge{
		instances: map[string]*fakeInstance{"work": {id: "inst-work", online: true}},
		log:       log,
		fail:      map[generated.Method]bridge.Error{},
	}
}

func (f *fakeBridge) setOnline(name string, online bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.instances[name].online = online
}

func (f *fakeBridge) ResolveBrowser(target string) (bridge.InstanceInfo, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if target == "" {
		for name, inst := range f.instances {
			if inst.online {
				return bridge.InstanceInfo{ID: inst.id, Name: name, Online: true}, nil
			}
		}
		return bridge.InstanceInfo{}, &bridge.Error{Code: generated.ErrorCodeNoBrowserConnected, Message: "no browser instance is connected"}
	}
	inst, ok := f.instances[target]
	switch {
	case !ok:
		return bridge.InstanceInfo{}, &bridge.Error{Code: generated.ErrorCodeBrowserNotFound, Message: "no paired browser instance matches " + target}
	case !inst.online:
		return bridge.InstanceInfo{}, &bridge.Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser " + target + " is not connected"}
	}
	return bridge.InstanceInfo{ID: inst.id, Name: target, Online: true}, nil
}

func (f *fakeBridge) CallInstance(ctx context.Context, instanceID string, req bridge.Request) (bridge.Response, error) {
	f.mu.Lock()
	online := false
	for _, inst := range f.instances {
		if inst.id == instanceID {
			online = inst.online
		}
	}
	failure, failing := f.fail[generated.Method(req.Action)]
	tabs, opened, cdp := f.tabs, f.opened, f.cdp
	f.mu.Unlock()
	if !online {
		return bridge.Response{}, &bridge.Error{Code: generated.ErrorCodeBrowserOffline, Message: "browser " + instanceID + " is not connected"}
	}
	var in struct {
		TabID      *int            `json:"tabId"`
		SessionID  *string         `json:"sessionId"`
		Method     string          `json:"method"`
		Params     json.RawMessage `json:"params"`
		Owned      *bool           `json:"owned"`
		URL        string          `json:"url"`
		Background *bool           `json:"background"`
	}
	if err := json.Unmarshal(req.Input, &in); err != nil {
		return bridge.Response{}, err
	}
	entry := req.Action
	if in.TabID != nil {
		entry += fmt.Sprintf(" %d", *in.TabID)
	}
	if in.SessionID != nil {
		entry += " " + *in.SessionID
	}
	if in.Method != "" {
		entry += " " + in.Method
	}
	if len(in.Params) > 0 {
		entry += " " + string(in.Params)
	}
	if in.Owned != nil {
		entry += fmt.Sprintf(" %t", *in.Owned)
	}
	if in.URL != "" {
		entry += " " + in.URL
	}
	if in.Background != nil && *in.Background {
		entry += " background"
	}
	f.log.add(entry)
	if failing {
		return bridge.Response{OK: false, Error: &failure}, nil
	}
	var result any
	switch generated.Method(req.Action) {
	case generated.MethodDebuggerUserAgent:
		result = generated.DebuggerUserAgentResult{UserAgent: testUserAgent}
	case generated.MethodDebuggerSend:
		if cdp == nil {
			result = generated.DebuggerSendResult{Result: json.RawMessage(`{"ok":true}`)}
			break
		}
		session := ""
		if in.SessionID != nil {
			session = *in.SessionID
		}
		res, failure := cdp(ctx, *in.TabID, session, in.Method, in.Params)
		if failure.Code != "" {
			return bridge.Response{OK: false, Error: &failure}, nil
		}
		result = generated.DebuggerSendResult{Result: res}
	case generated.MethodDebuggerDetach:
		result = generated.DebuggerDetachResult{TabIds: []int{}}
	case generated.MethodDebuggerOwn:
		result = generated.DebuggerOwnResult{Owned: in.Owned != nil && *in.Owned}
	case generated.MethodDebuggerOpen:
		if opened.TargetId == "" {
			opened = generated.DebuggerOpenResult{TabId: 100, TargetId: "T100"}
		}
		result = opened
	case generated.MethodDebuggerClose:
		result = generated.DebuggerCloseResult{TabId: *in.TabID}
	case generated.MethodDebuggerTargets:
		if tabs == nil {
			tabs = []generated.DebuggerTabNotification{{TabId: 5, TargetId: "T5", Title: "Five", URL: "https://five.test/"}}
		}
		result = map[string]any{"targets": tabs}
	case generated.MethodTabsActivate:
		result = generated.TabsActivateResult{TabId: *in.TabID, WindowId: 1}
	default:
		return bridge.Response{}, fmt.Errorf("fake bridge: unexpected method %s", req.Action)
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return bridge.Response{}, err
	}
	return bridge.Response{OK: true, Result: raw}, nil
}

const testUserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/125.0.6422.141 Safari/537.36"

// fakePages 是页面自动化组件的交出与收回。
type fakePages struct {
	log         *eventLog
	mu          sync.Mutex
	handOverErr error
}

func (p *fakePages) HandOver(_ context.Context, instanceID string) error {
	p.log.add("handover " + instanceID)
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.handOverErr
}

func (p *fakePages) Reclaim(instanceID string) {
	p.log.add("reclaim " + instanceID)
}

// fakeClock 只在 Advance 时触发到期的计时器。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	at    time.Time
	f     func()
	done  bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{now: time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now.Add(d), f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	pending := !t.done
	t.done = true
	return pending
}

// Advance 走动时钟并同步执行到期的计时器。
func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	var due []func()
	for _, t := range c.timers {
		if !t.done && !t.at.After(c.now) {
			t.done = true
			due = append(due, t.f)
		}
	}
	c.mu.Unlock()
	for _, f := range due {
		f()
	}
}

// hookFunc 让测试直接写出客户端会话的行为。
type hookFunc func(ctx context.Context, conn *websocket.Conn, b *Browser) error

func (f hookFunc) Serve(ctx context.Context, conn *websocket.Conn, b *Browser) error {
	return f(ctx, conn, b)
}

// harness 是一个挂在 httptest 上的端点管理器,带假扩展、假页面组件与假时钟。
type harness struct {
	m       *Manager
	bridge  *fakeBridge
	pages   *fakePages
	clock   *fakeClock
	log     *eventLog
	srv     *httptest.Server
	host    string
	cancel  context.CancelFunc
	started chan *Browser
}

// newHarness 的默认会话:报告自己的 Browser,然后读到客户端断开或 ctx 结束为止。
func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{log: newEventLog(), clock: newFakeClock(), started: make(chan *Browser, 4)}
	h.bridge = newFakeBridge(h.log)
	h.pages = &fakePages{log: h.log}
	return h.start(t, hookFunc(func(ctx context.Context, conn *websocket.Conn, b *Browser) error {
		h.started <- b
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return err
			}
		}
	}))
}

func newHarnessWithHook(t *testing.T, hook Hook) *harness {
	t.Helper()
	h := &harness{log: newEventLog(), clock: newFakeClock(), started: make(chan *Browser, 4)}
	h.bridge = newFakeBridge(h.log)
	h.pages = &fakePages{log: h.log}
	return h.start(t, hook)
}

func (h *harness) start(t *testing.T, hook Hook) *harness {
	t.Helper()
	return h.startWithPages(t, h.pages, hook)
}

// create 为 work 创建端点并返回它的地址。
func (h *harness) create(t *testing.T) Info {
	t.Helper()
	snap, err := h.m.Create("work", h.host)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return *snap.Endpoint
}

// get 发一个普通 HTTP GET,可改 Host 与 Origin。
func (h *harness) get(t *testing.T, url, host, origin string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, err := resp.Body.Read(buf)
		b.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return resp.StatusCode, b.String()
}

// dial 以 Puppeteer 的方式连上 WS 地址;失败时返回握手响应的状态码与正文。
func dial(t *testing.T, url string, opts *websocket.DialOptions) (*websocket.Conn, int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	conn, resp, err := websocket.Dial(ctx, url, opts)
	if err == nil {
		t.Cleanup(func() { _ = conn.CloseNow() })
		return conn, http.StatusSwitchingProtocols, ""
	}
	if resp == nil {
		t.Fatalf("dial %s: %v", url, err)
	}
	var b strings.Builder
	if resp.Body != nil {
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			b.Write(buf[:n])
			if err != nil {
				break
			}
		}
		resp.Body.Close()
	}
	return nil, resp.StatusCode, b.String()
}

// connect 连上端点并等会话开始。
func (h *harness) connect(t *testing.T, info Info) (*websocket.Conn, *Browser) {
	t.Helper()
	conn, status, body := dial(t, info.WSURL, nil)
	if conn == nil {
		t.Fatalf("connect: %d %s", status, body)
	}
	select {
	case b := <-h.started:
		return conn, b
	case <-time.After(5 * time.Second):
		t.Fatal("the session did not start")
		return nil, nil
	}
}

// waitClosed 等客户端连接被关闭,返回关闭状态。
func waitClosed(conn *websocket.Conn) websocket.StatusCode {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := conn.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

// blockingPages 的 HandOver 一直等到 ctx 结束,像真实的交出在等 sctl 的命令结束;ctx 结束时它回滚并返回 ctx 的错误。
type blockingPages struct {
	fakePages
	entered chan struct{}
}

func (p *blockingPages) HandOver(ctx context.Context, instanceID string) error {
	p.log.add("handover " + instanceID)
	close(p.entered)
	<-ctx.Done()
	return ctx.Err()
}

// startWithPages 与 start 相同,但用给定的页面组件。
func (h *harness) startWithPages(t *testing.T, pages Pages, hook Hook) *harness {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	h.cancel = cancel
	h.m = newManager(ctx, Deps{Bridge: h.bridge, Pages: pages, Hook: hook, MaxMessageBytes: 1 << 20, Log: zap.NewNop()}, h.clock)
	mux := http.NewServeMux()
	mux.Handle(PathPrefix, h.m)
	h.srv = httptest.NewServer(mux)
	h.host = strings.TrimPrefix(h.srv.URL, "http://")
	t.Cleanup(func() {
		cancel()
		h.srv.Close()
	})
	return h
}
