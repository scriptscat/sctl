// Package pagetest 为页面自动化的真 Chrome 集成测试提供 page.CDP 实现:Chrome 直连一个 headless Chrome 的
// 远程调试端口,用扁平化的 Target 会话模拟 sctl Browser 扩展的 CDP 中转(按需附加、子会话、事件与分离通知)。
// 本机找不到 Chrome 时测试跳过;设置了 SCTL_TEST_CHROME 却无法启动时测试失败,CI 据此保证不会悄悄跳过。
package pagetest

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// InstanceID 是 Chrome 扮演的唯一浏览器实例;ResolveBrowser 对空目标与 BrowserName 都解析到它。
const (
	InstanceID  = "c0ffeec0ffeec0ffeec0ffeec0ffee00"
	BrowserName = "pagetest"
)

// EnvChrome 指定 Chrome 可执行文件;CI 装好 Chrome 后设置它。
const EnvChrome = "SCTL_TEST_CHROME"

const (
	startTimeout = 30 * time.Second
	// deadlineMargin 是在 go test 的超时期限之前多久杀掉 Chrome。
	deadlineMargin = 5 * time.Second
	// maxMessageBytes 放宽 coder/websocket 默认 32 KiB 的读上限:CDP 结果(快照、截图)远超这个大小。
	maxMessageBytes = 64 << 20
	// relayEnvelopeBytes 是扩展中转的应答帧里 CDP 结果之外的部分({"jsonrpc":"2.0","id":…,"result":{"result":…}})
	// 的上界。
	relayEnvelopeBytes = 128
)

// Listener 接收 Chrome 产生的 debugger.event / debugger.detached 通知,形状与 bridge.BrowserListener 相同。
type Listener interface {
	OnNotification(instanceID, method string, params json.RawMessage)
	OnInstanceGone(instanceID string)
}

// Chrome 是连到一个 headless Chrome 的 page.CDP。
type Chrome struct {
	// pid 是 Chrome 主进程的 PID,测试据此确认它随测试进程退出。
	pid    int
	ws     *websocket.Conn
	nextID atomic.Int64
	// attachMu 串行化附加,避免同一标签页上并发的第一条命令附加两次。
	attachMu sync.Mutex

	mu        sync.Mutex
	pending   map[int64]*pendingCommand
	targets   map[int]string // tabId → targetId
	sessions  map[int]string // tabId → 顶层会话
	children  map[string]int // 子会话 → tabId
	nextTab   int
	current   int
	listener  Listener
	readError error
}

type pendingCommand struct {
	sessionID string
	done      chan cdpResponse
}

type cdpMessage struct {
	ID        int64           `json:"id,omitempty"`
	SessionID string          `json:"sessionId,omitempty"`
	Method    string          `json:"method,omitempty"`
	Params    json.RawMessage `json:"params,omitempty"`
	Result    json.RawMessage `json:"result,omitempty"`
	Error     *cdpError       `json:"error,omitempty"`
}

type cdpError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type cdpResponse struct {
	result json.RawMessage
	err    error
}

// Start 启动一个隔离配置的 headless Chrome 并连上它的浏览器级调试端点;测试结束时关闭。
func Start(t testing.TB) *Chrome {
	t.Helper()
	bin := findChrome(t)
	dir, err := os.MkdirTemp("", "sctl-pagetest-")
	if err != nil {
		t.Fatalf("create the Chrome profile directory: %v", err)
	}
	args := []string{
		"--headless=new",
		"--remote-debugging-port=0",
		"--user-data-dir=" + dir,
		"--no-first-run",
		"--no-default-browser-check",
		"--disable-gpu",
		"--disable-extensions",
		"about:blank",
	}
	if runtime.GOOS == "linux" {
		// CI 的 Ubuntu 限制非特权用户命名空间,Chrome 的沙箱在那里起不来。
		args = append(args, "--no-sandbox")
	}
	cmd := exec.Command(bin, args...)
	started, release, err := newLifeline(cmd)
	if err != nil {
		t.Fatalf("create the Chrome lifeline: %v", err)
	}
	err = cmd.Start()
	started()
	if err != nil {
		release()
		t.Fatalf("start Chrome %s: %v", bin, err)
	}
	exited := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		release()
		close(exited)
	}()

	endpoint, err := waitForEndpoint(dir, exited)
	if err != nil {
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("Chrome %s did not expose a debugging endpoint: %v", bin, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	ws, resp, err := websocket.Dial(ctx, endpoint, nil)
	// 握手成功时 coder/websocket 已接管连接,resp.Body 为 nil;失败时才有需要关闭的响应体。
	if resp != nil && resp.Body != nil {
		_ = resp.Body.Close()
	}
	if err != nil {
		_ = cmd.Process.Kill()
		<-exited
		t.Fatalf("connect to Chrome at %s: %v", endpoint, err)
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &Chrome{
		pid:      cmd.Process.Pid,
		ws:       ws,
		pending:  map[int64]*pendingCommand{},
		targets:  map[int]string{},
		sessions: map[int]string{},
		children: map[string]int{},
		nextTab:  1,
	}
	go c.readLoop()
	// go test 超时时直接 panic 退出,不运行 Cleanup:在期限前先杀掉 Chrome,不留下孤儿进程。
	var watchdog *time.Timer
	if d, ok := t.(interface{ Deadline() (time.Time, bool) }); ok {
		if deadline, ok := d.Deadline(); ok {
			watchdog = time.AfterFunc(time.Until(deadline)-deadlineMargin, func() { _ = cmd.Process.Kill() })
		}
	}
	t.Cleanup(func() {
		if watchdog != nil {
			watchdog.Stop()
		}
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer closeCancel()
		// 让 Chrome 自己退出并收拾子进程;Windows 上被强杀的子进程会锁住配置目录。
		if _, err := c.call(closeCtx, "", "Browser.close", nil); err != nil {
			t.Logf("Browser.close: %v", err)
		}
		select {
		case <-exited:
		case <-time.After(10 * time.Second):
			_ = cmd.Process.Kill()
			<-exited
		}
		_ = ws.CloseNow()
		if err := os.RemoveAll(dir); err != nil {
			t.Logf("remove the Chrome profile directory: %v", err)
		}
	})
	return c
}

// findChrome 按 SCTL_TEST_CHROME、常见安装路径、PATH 的顺序查找 Chrome;都没有时跳过测试。
func findChrome(t testing.TB) string {
	if bin := os.Getenv(EnvChrome); bin != "" {
		if _, err := os.Stat(bin); err != nil {
			t.Fatalf("%s=%s: %v", EnvChrome, bin, err)
		}
		return bin
	}
	var candidates []string
	switch runtime.GOOS {
	case "darwin":
		candidates = []string{
			"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome",
			"/Applications/Chromium.app/Contents/MacOS/Chromium",
		}
	case "windows":
		for _, base := range []string{os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("LOCALAPPDATA")} {
			if base != "" {
				candidates = append(candidates, filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"))
			}
		}
	}
	for _, c := range candidates {
		if _, err := os.Stat(c); err == nil {
			return c
		}
	}
	for _, name := range []string{"google-chrome", "google-chrome-stable", "chromium", "chromium-browser", "chrome"} {
		if bin, err := exec.LookPath(name); err == nil {
			return bin
		}
	}
	t.Skipf("no Chrome found; set %s to run the real-Chrome integration tests", EnvChrome)
	return ""
}

// waitForEndpoint 读取 Chrome 在配置目录里写下的 DevToolsActivePort(端口与浏览器端点路径)。
func waitForEndpoint(dir string, exited <-chan struct{}) (string, error) {
	deadline := time.Now().Add(startTimeout)
	for time.Now().Before(deadline) {
		select {
		case <-exited:
			return "", errors.New("the Chrome process exited during startup")
		default:
		}
		f, err := os.Open(filepath.Join(dir, "DevToolsActivePort"))
		if err == nil {
			var lines []string
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				lines = append(lines, strings.TrimSpace(scanner.Text()))
			}
			_ = f.Close()
			if len(lines) >= 2 && lines[0] != "" && lines[1] != "" {
				return "ws://127.0.0.1:" + lines[0] + lines[1], nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return "", fmt.Errorf("no DevToolsActivePort after %s", startTimeout)
}

// SetListener 设置通知的接收者,通常是被测的 page.Manager。
func (c *Chrome) SetListener(l Listener) {
	c.mu.Lock()
	c.listener = l
	c.mu.Unlock()
}

// NewTab 在后台打开 url 并返回它的标签页 ID;它同时成为 CurrentTab 的结果。
func (c *Chrome) NewTab(t testing.TB, url string) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), startTimeout)
	defer cancel()
	raw, err := c.call(ctx, "", "Target.createTarget", map[string]any{"url": url, "background": true})
	if err != nil {
		t.Fatalf("open %s: %v", url, err)
	}
	var created struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("decode Target.createTarget: %v", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	tabID := c.nextTab
	c.nextTab++
	c.targets[tabID] = created.TargetID
	c.current = tabID
	return tabID
}

// ResolveBrowser 实现 page.CDP:只有一个名为 BrowserName 的实例。
func (c *Chrome) ResolveBrowser(target string) (string, error) {
	if target == "" || target == BrowserName || strings.HasPrefix(InstanceID, target) {
		return InstanceID, nil
	}
	return "", &page.Error{Code: generated.ErrorCodeBrowserNotFound, Message: "no paired browser matches " + target}
}

// CurrentTab 实现 page.CDP:最近一次 NewTab 或 SelectTab 的标签页。
func (c *Chrome) CurrentTab(ctx context.Context, instanceID string) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.current == 0 {
		return 0, &page.Error{Code: generated.ErrorCodeNotFound, Message: "no tab is open"}
	}
	return c.current, nil
}

// Tabs 实现 page.CDP:浏览器里全部页面目标,页面自己打开的(如 target=_blank)第一次出现时分配标签页 ID。
func (c *Chrome) Tabs(ctx context.Context, instanceID string) ([]int, error) {
	raw, err := c.call(ctx, "", "Target.getTargets", nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		TargetInfos []struct {
			TargetID string `json:"targetId"`
			Type     string `json:"type"`
		} `json:"targetInfos"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("decode Target.getTargets: %w", err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	byTarget := make(map[string]int, len(c.targets))
	for tabID, targetID := range c.targets {
		byTarget[targetID] = tabID
	}
	var tabs []int
	for _, info := range res.TargetInfos {
		if info.Type != "page" {
			continue
		}
		tabID, ok := byTarget[info.TargetID]
		if !ok {
			tabID = c.nextTab
			c.nextTab++
			c.targets[tabID] = info.TargetID
		}
		tabs = append(tabs, tabID)
	}
	slices.Sort(tabs)
	return tabs, nil
}

// SelectTab 实现 page.CDP。
func (c *Chrome) SelectTab(ctx context.Context, instanceID string, tabID int) error {
	targetID, err := c.target(tabID)
	if err != nil {
		return err
	}
	if _, err := c.call(ctx, "", "Target.activateTarget", map[string]string{"targetId": targetID}); err != nil {
		return err
	}
	c.mu.Lock()
	c.current = tabID
	c.mu.Unlock()
	return nil
}

// Send 实现 page.CDP:标签页尚未附加时先用扁平会话附加,与扩展的按需附加一致。
func (c *Chrome) Send(ctx context.Context, instanceID string, cmd page.Command) (json.RawMessage, error) {
	sessionID := cmd.SessionID
	if sessionID == "" {
		var err error
		if sessionID, err = c.attach(ctx, cmd.TabID); err != nil {
			return nil, err
		}
	} else {
		c.mu.Lock()
		owner, ok := c.children[sessionID]
		c.mu.Unlock()
		if !ok || owner != cmd.TabID {
			return nil, &page.Error{Code: generated.ErrorCodeInvalidRequest, Message: fmt.Sprintf("tab %d has no child session %s", cmd.TabID, sessionID)}
		}
	}
	res, err := c.call(ctx, sessionID, cmd.Method, cmd.Params)
	if err != nil {
		return nil, err
	}
	// 扩展中转不发超过单帧上限的应答帧,改答 PAYLOAD_TOO_LARGE(extension/src/offscreen/connection.ts)。
	// 直连 Chrome 没有这个上限,在这里照做,大页面的集成测试才与生产一致。
	if limit := maxFrameBytes(); len(res)+relayEnvelopeBytes > limit {
		return nil, &page.Error{Code: generated.ErrorCodePayloadTooLarge, Message: fmt.Sprintf("result exceeds the %d byte frame limit", limit)}
	}
	return res, nil
}

// maxFrameBytes 是协议的单帧上限。
func maxFrameBytes() int {
	p, err := protocol.Load()
	if err != nil {
		panic("pagetest: load the embedded protocol: " + err.Error())
	}
	return p.Limits.MaxFrameBytes
}

// Detach 实现 page.CDP。调用方发起的断开不产生 debugger.detached 通知。
func (c *Chrome) Detach(ctx context.Context, instanceID string, tabID *int) ([]int, error) {
	c.mu.Lock()
	var tabs []int
	for id := range c.sessions {
		if tabID == nil || *tabID == id {
			tabs = append(tabs, id)
		}
	}
	slices.Sort(tabs)
	sessions := make([]string, len(tabs))
	for i, id := range tabs {
		sessions[i] = c.sessions[id]
		delete(c.sessions, id)
		c.dropChildren(id)
	}
	c.mu.Unlock()
	for _, sessionID := range sessions {
		if _, err := c.call(ctx, "", "Target.detachFromTarget", map[string]string{"sessionId": sessionID}); err != nil {
			return nil, err
		}
	}
	if tabs == nil {
		tabs = []int{}
	}
	return tabs, nil
}

func (c *Chrome) target(tabID int) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	targetID, ok := c.targets[tabID]
	if !ok {
		return "", &page.Error{Code: generated.ErrorCodeNotFound, Message: fmt.Sprintf("no tab %d", tabID)}
	}
	return targetID, nil
}

func (c *Chrome) attach(ctx context.Context, tabID int) (string, error) {
	c.attachMu.Lock()
	defer c.attachMu.Unlock()
	c.mu.Lock()
	sessionID, attached := c.sessions[tabID]
	c.mu.Unlock()
	if attached {
		return sessionID, nil
	}
	targetID, err := c.target(tabID)
	if err != nil {
		return "", err
	}
	raw, err := c.call(ctx, "", "Target.attachToTarget", map[string]any{"targetId": targetID, "flatten": true})
	if err != nil {
		var pe *page.Error
		if errors.As(err, &pe) {
			return "", &page.Error{Code: generated.ErrorCodePageNotAutomatable, Message: pe.Message}
		}
		return "", err
	}
	var res struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("decode Target.attachToTarget: %w", err)
	}
	c.mu.Lock()
	c.sessions[tabID] = res.SessionID
	c.mu.Unlock()
	return res.SessionID, nil
}

// call 发送一条 CDP 命令并等待应答;CDP 错误翻译成与扩展一致的 INVALID_REQUEST。
func (c *Chrome) call(ctx context.Context, sessionID, method string, params any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	msg := cdpMessage{ID: id, SessionID: sessionID, Method: method}
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, err
		}
		msg.Params = raw
	}
	p := &pendingCommand{sessionID: sessionID, done: make(chan cdpResponse, 1)}
	c.mu.Lock()
	if c.readError != nil {
		err := c.readError
		c.mu.Unlock()
		return nil, err
	}
	c.pending[id] = p
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	data, err := json.Marshal(msg)
	if err != nil {
		return nil, err
	}
	if err := c.ws.Write(ctx, websocket.MessageText, data); err != nil {
		return nil, err
	}
	select {
	case res := <-p.done:
		return res.result, res.err
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (c *Chrome) readLoop() {
	for {
		_, data, err := c.ws.Read(context.Background())
		if err != nil {
			c.mu.Lock()
			c.readError = fmt.Errorf("connection to Chrome closed: %w", err)
			for _, p := range c.pending {
				p.done <- cdpResponse{err: c.readError}
			}
			clear(c.pending)
			c.mu.Unlock()
			return
		}
		var msg cdpMessage
		if err := json.Unmarshal(data, &msg); err != nil {
			continue
		}
		if msg.ID != 0 {
			c.deliver(msg)
			continue
		}
		c.handleEvent(msg)
	}
}

func (c *Chrome) deliver(msg cdpMessage) {
	c.mu.Lock()
	p := c.pending[msg.ID]
	delete(c.pending, msg.ID)
	c.mu.Unlock()
	if p == nil {
		return
	}
	if msg.Error != nil {
		p.done <- cdpResponse{err: &page.Error{Code: generated.ErrorCodeInvalidRequest, Message: msg.Error.Message}}
		return
	}
	p.done <- cdpResponse{result: msg.Result}
}

// handleEvent 把会话上的 CDP 事件转成 debugger.event,把顶层会话的被动分离转成 debugger.detached。
func (c *Chrome) handleEvent(msg cdpMessage) {
	if msg.SessionID == "" {
		if msg.Method == "Target.detachedFromTarget" {
			c.handleTopDetach(msg.Params)
		}
		return
	}
	c.mu.Lock()
	tabID, child, ok := c.tabOfSession(msg.SessionID)
	if ok && msg.Method == "Target.attachedToTarget" {
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if json.Unmarshal(msg.Params, &p) == nil && p.SessionID != "" {
			c.children[p.SessionID] = tabID
		}
	}
	l := c.listener
	c.mu.Unlock()
	if !ok || l == nil {
		return
	}
	n := generated.DebuggerEventNotification{Method: msg.Method, Params: msg.Params, TabId: tabID}
	if child {
		n.SessionId = &msg.SessionID
	}
	raw, err := json.Marshal(n)
	if err != nil {
		return
	}
	l.OnNotification(InstanceID, string(generated.NotificationDebuggerEvent), raw)
}

// tabOfSession 找出会话所属的标签页,child 表示它是子会话。调用方持有 c.mu。
func (c *Chrome) tabOfSession(sessionID string) (tabID int, child, ok bool) {
	for id, s := range c.sessions {
		if s == sessionID {
			return id, false, true
		}
	}
	tabID, ok = c.children[sessionID]
	return tabID, true, ok
}

func (c *Chrome) handleTopDetach(params json.RawMessage) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return
	}
	c.mu.Lock()
	tabID, child, ok := c.tabOfSession(p.SessionID)
	if !ok || child {
		c.mu.Unlock()
		return
	}
	delete(c.sessions, tabID)
	c.dropChildren(tabID)
	for id, pc := range c.pending {
		if pc.sessionID == p.SessionID {
			// 移出挂起表:晚到的应答不能再投递到已满的 done。
			delete(c.pending, id)
			pc.done <- cdpResponse{err: &page.Error{Code: generated.ErrorCodeDebuggerDetached, Message: "the debugger detached while the command was running"}}
		}
	}
	l := c.listener
	c.mu.Unlock()
	if l == nil {
		return
	}
	raw, err := json.Marshal(generated.DebuggerDetachedNotification{TabId: tabID, Reason: "target_closed"})
	if err != nil {
		return
	}
	l.OnNotification(InstanceID, string(generated.NotificationDebuggerDetached), raw)
}

// dropChildren 删除标签页的全部子会话。调用方持有 c.mu。
func (c *Chrome) dropChildren(tabID int) {
	for s, owner := range c.children {
		if owner == tabID {
			delete(c.children, s)
		}
	}
}

// Serve 用本地 HTTP 服务提供 dir 下的 fixture 页面,返回基址(不带结尾斜杠)。
func Serve(t testing.TB, dir string) string {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	return srv.URL
}
