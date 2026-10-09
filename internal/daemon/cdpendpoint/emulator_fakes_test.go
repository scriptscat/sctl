package cdpendpoint

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// 两个已打开的普通标签页,像探针里的「fixture page」与「other user tab」。
var (
	tabFive = generated.DebuggerTabNotification{TabId: 5, TargetId: "T5", Title: "fixture page", URL: "http://127.0.0.1:8711/page.html"}
	tabSix  = generated.DebuggerTabNotification{TabId: 6, TargetId: "T6", Title: "other user tab", URL: "http://127.0.0.1:8711/other.html"}
)

// fakeChrome 像 chrome.debugger 背后的 Chrome 一样回答 debugger.send:标签页会话上的 Target.getTargetInfo 给出
// 自己的目标信息,探针里被 Chrome 拒绝的命令回答 Chrome 原样的错误(经扩展压扁成 INVALID_REQUEST),其余回答 {}。
// block 里的方法一直等到对应的 channel 关闭或 ctx 结束;ctx 结束时记录 "canceled <method>"。
type fakeChrome struct {
	h     *harness
	mu    sync.Mutex
	tabs  map[int]generated.DebuggerTabNotification
	block map[string]chan struct{}
	// before 在回答某个方法之前运行,用来模拟 Chrome 先于应答发出的事件。
	before map[string]func(tabID int)
}

func (c *fakeChrome) answer(ctx context.Context, tabID int, sessionID, method string, params json.RawMessage) (json.RawMessage, bridge.Error) {
	c.mu.Lock()
	tab, known := c.tabs[tabID]
	gate := c.block[method]
	before := c.before[method]
	c.mu.Unlock()
	if !known {
		return nil, bridge.Error{Code: generated.ErrorCodeNotFound, Message: fmt.Sprintf("no tab with id %d", tabID)}
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			c.h.log.add("canceled " + method)
			return nil, bridge.Error{Code: generated.ErrorCodeInternalError, Message: ctx.Err().Error()}
		}
	}
	if before != nil {
		before(tabID)
	}
	switch method {
	case "Target.getTargetInfo":
		if sessionID != "" {
			return json.RawMessage(`{"targetInfo":{"targetId":"FRAME","type":"iframe","title":"","url":"","attached":true,"canAccessOpener":false}}`), bridge.Error{}
		}
		info := map[string]any{"targetInfo": map[string]any{
			"targetId": tab.TargetId, "type": "page", "title": tab.Title, "url": tab.URL, "attached": true,
			"canAccessOpener": false, "browserContextId": "CTX1",
		}}
		raw, _ := json.Marshal(info)
		return raw, bridge.Error{}
	case "WebMCP.enable":
		return nil, bridge.Error{Code: generated.ErrorCodeInvalidRequest, Message: `{"code":-32601,"message":"'WebMCP.enable' wasn't found"}`}
	case "Storage.getCookies":
		return nil, bridge.Error{Code: generated.ErrorCodeInvalidRequest, Message: `{"code":-32000,"message":"Permission denied"}`}
	case "Runtime.evaluate":
		return json.RawMessage(`{"result":{"type":"string","value":"fixture page"}}`), bridge.Error{}
	}
	return json.RawMessage(`{}`), bridge.Error{}
}

func (c *fakeChrome) addTab(tab generated.DebuggerTabNotification) {
	c.mu.Lock()
	c.tabs[tab.TabId] = tab
	c.mu.Unlock()
	c.h.bridge.mu.Lock()
	c.h.bridge.tabs = append(c.h.bridge.tabs, tab)
	c.h.bridge.mu.Unlock()
}

func (c *fakeChrome) setBlock(method string, gate chan struct{}) {
	c.mu.Lock()
	c.block[method] = gate
	c.mu.Unlock()
}

func (c *fakeChrome) setBefore(method string, f func(tabID int)) {
	c.mu.Lock()
	c.before[method] = f
	c.mu.Unlock()
}

// newEmulatorHarness 是装着真实 Emulator 的端点,背后是有 tabs 这些标签页的假 Chrome。
func newEmulatorHarness(t *testing.T, tabs ...generated.DebuggerTabNotification) (*harness, *fakeChrome) {
	t.Helper()
	h := &harness{log: newEventLog(), clock: newFakeClock(), started: make(chan *Browser, 4)}
	h.bridge = newFakeBridge(h.log)
	h.pages = &fakePages{log: h.log}
	chrome := &fakeChrome{h: h, tabs: map[int]generated.DebuggerTabNotification{}, block: map[string]chan struct{}{}, before: map[string]func(int){}}
	h.bridge.tabs = []generated.DebuggerTabNotification{}
	for _, tab := range tabs {
		chrome.addTab(tab)
	}
	h.bridge.cdp = chrome.answer
	h.start(t, Emulator{})
	return h, chrome
}

// notify 像扩展一样送来一条通知。
func (h *harness) notify(method generated.Notification, params any) {
	raw, err := json.Marshal(params)
	if err != nil {
		panic(err)
	}
	h.m.OnNotification("inst-work", string(method), raw)
}

// chromeEvent 像扩展转来的一条 CDP 事件;sessionID 非空表示子会话上的事件。
func (h *harness) chromeEvent(tabID int, sessionID, method, params string) {
	ev := generated.DebuggerEventNotification{TabId: tabID, Method: method, Params: json.RawMessage(params)}
	if sessionID != "" {
		ev.SessionId = &sessionID
	}
	h.notify(generated.NotificationDebuggerEvent, ev)
}

// cdpMessage 是端点发给客户端的一条消息:应答(ID 非空)或事件。
type cdpMessage struct {
	ID        *int            `json:"id"`
	SessionID string          `json:"sessionId"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
	Result    json.RawMessage `json:"result"`
	Error     *cdpError       `json:"error"`
}

// cdpTestClient 是按 Playwright / Puppeteer 的方式说 CDP 的客户端:请求可以连发不等应答,收到的消息按顺序记下。
type cdpTestClient struct {
	t      *testing.T
	conn   *websocket.Conn
	nextID int

	mu      sync.Mutex
	msgs    []cdpMessage
	readErr error
	changed chan struct{}
	done    chan struct{}
}

func dialCDP(t *testing.T, url string) *cdpTestClient {
	t.Helper()
	conn, status, body := dial(t, url, nil)
	if conn == nil {
		t.Fatalf("dial: %d %s", status, body)
	}
	conn.SetReadLimit(-1)
	c := &cdpTestClient{t: t, conn: conn, changed: make(chan struct{}, 1), done: make(chan struct{})}
	go c.readLoop()
	return c
}

func (c *cdpTestClient) readLoop() {
	defer close(c.done)
	for {
		_, data, err := c.conn.Read(context.Background())
		c.mu.Lock()
		if err != nil {
			c.readErr = err
			c.mu.Unlock()
			c.signal()
			return
		}
		var m cdpMessage
		if err := json.Unmarshal(data, &m); err != nil {
			c.readErr = fmt.Errorf("undecodable message %s: %w", data, err)
			c.mu.Unlock()
			c.signal()
			return
		}
		c.msgs = append(c.msgs, m)
		c.mu.Unlock()
		c.signal()
	}
}

func (c *cdpTestClient) signal() {
	select {
	case c.changed <- struct{}{}:
	default:
	}
}

// send 发出一条请求,不等应答,返回它的 id。
func (c *cdpTestClient) send(sessionID, method, params string) int {
	c.t.Helper()
	c.nextID++
	msg := map[string]any{"id": c.nextID, "method": method, "params": json.RawMessage(params)}
	if sessionID != "" {
		msg["sessionId"] = sessionID
	}
	raw, err := json.Marshal(msg)
	if err != nil {
		c.t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.conn.Write(ctx, websocket.MessageText, raw); err != nil {
		c.t.Fatalf("send %s: %v", method, err)
	}
	return c.nextID
}

// waitFor 等到 match 在收到的消息里命中,返回命中的下标;超时或连接断开时测试失败。
func (c *cdpTestClient) waitFor(what string, match func(cdpMessage) bool) int {
	c.t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		c.mu.Lock()
		for i, m := range c.msgs {
			if match(m) {
				c.mu.Unlock()
				return i
			}
		}
		readErr := c.readErr
		c.mu.Unlock()
		if readErr != nil {
			c.t.Fatalf("waiting for %s: the connection ended: %v", what, readErr)
		}
		select {
		case <-c.changed:
		case <-time.After(10 * time.Millisecond):
		case <-deadline:
			c.t.Fatalf("timed out waiting for %s; got %s", what, c.dump())
		}
	}
}

func (c *cdpTestClient) dump() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var b strings.Builder
	for _, m := range c.msgs {
		raw, _ := json.Marshal(m)
		b.Write(raw)
		b.WriteByte('\n')
	}
	return b.String()
}

// response 等到 id 的应答,返回它与它在消息流里的下标。
func (c *cdpTestClient) response(id int) (cdpMessage, int) {
	c.t.Helper()
	i := c.waitFor(fmt.Sprintf("the response to %d", id), func(m cdpMessage) bool { return m.ID != nil && *m.ID == id })
	return c.at(i), i
}

// call 发出请求并等它的应答。
func (c *cdpTestClient) call(sessionID, method, params string) cdpMessage {
	c.t.Helper()
	m, _ := c.response(c.send(sessionID, method, params))
	return m
}

func (c *cdpTestClient) at(i int) cdpMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.msgs[i]
}

// event 等到第一条满足 match 的 method 事件。
func (c *cdpTestClient) event(method string, match func(cdpMessage) bool) (cdpMessage, int) {
	c.t.Helper()
	i := c.waitFor("event "+method, func(m cdpMessage) bool { return m.ID == nil && m.Method == method && (match == nil || match(m)) })
	return c.at(i), i
}

// events 是目前为止收到的 method 事件,只看下标 before 之前的(before < 0 表示全部)。
func (c *cdpTestClient) events(method string, before int) []cdpMessage {
	c.mu.Lock()
	defer c.mu.Unlock()
	var out []cdpMessage
	for i, m := range c.msgs {
		if before >= 0 && i >= before {
			break
		}
		if m.ID == nil && m.Method == method {
			out = append(out, m)
		}
	}
	return out
}

// targetInfo 解出 attachedToTarget / targetCreated 等事件里的 targetInfo。
func targetInfoOf(m cdpMessage) map[string]any {
	var p struct {
		TargetInfo map[string]any `json:"targetInfo"`
	}
	if err := json.Unmarshal(m.Params, &p); err != nil {
		panic(err)
	}
	return p.TargetInfo
}

// attachedSessions 把 attachedToTarget 事件整理成 targetId → sessionId。
func attachedSessions(events []cdpMessage) map[string]string {
	out := map[string]string{}
	for _, ev := range events {
		var p struct {
			SessionID  string `json:"sessionId"`
			TargetInfo struct {
				TargetID string `json:"targetId"`
			} `json:"targetInfo"`
		}
		if err := json.Unmarshal(ev.Params, &p); err != nil {
			panic(err)
		}
		out[p.TargetInfo.TargetID] = p.SessionID
	}
	return out
}

func paramOf(m cdpMessage, key string) any {
	var p map[string]any
	if err := json.Unmarshal(m.Params, &p); err != nil {
		panic(err)
	}
	return p[key]
}

// sentOrder 是假扩展收到的 debugger.send 记录里以 prefix 开头的那些,按到达顺序。
func sentOrder(log *eventLog, prefix string) []string {
	var out []string
	for _, e := range log.all() {
		if strings.HasPrefix(e, prefix) {
			out = append(out, e)
		}
	}
	return out
}

func indexOf(entries []string, entry string) int {
	for i, e := range entries {
		if e == entry {
			return i
		}
	}
	return -1
}
