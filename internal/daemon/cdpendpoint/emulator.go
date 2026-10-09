package cdpendpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/bridge"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// Emulator 是端点的 Hook:chrome.debugger 只能附加标签页,浏览器级的 Browser.* 与 Target.* 在标签页会话上被 Chrome
// 拒绝,所以由这里在根会话上模拟一个浏览器;页面会话与跨进程 iframe 子会话上的命令和事件原样转发(spec 设计决策 2)。
// 页面会话的 sessionId 由这里合成,子会话的 sessionId 是 Chrome 报告的原值。
type Emulator struct{}

// Serve 实现 Hook。
func (Emulator) Serve(ctx context.Context, conn *websocket.Conn, b *Browser) error {
	return newEmulation(ctx, conn, b).serve()
}

// CDP 的错误码,与 Chrome 相同。
const (
	cdpServerError     = -32000
	cdpSessionNotFound = -32001
	cdpInvalidRequest  = -32600
	cdpInvalidParams   = -32602
)

// pipelineGrace 是同一个 Chrome 会话上一条命令开始执行后、下一条命令可以越过它开始的时间。客户端连发命令不等应答
// (Playwright 附加页面后连发十几条,最后一条 Runtime.runIfWaitingForDebugger 必须在其余之后),Chrome 按到达顺序
// 执行,而 bridge 不告诉调用方请求何时写给了扩展,所以同一会话的命令依次开始:上一条得到应答,或者开始了这么久之后——
// 那时它早已写出,且可能正被 Chrome 挂着等后面的命令(弹框下的 Runtime.evaluate 要等 Page.handleJavaScriptDialog,
// 被 Fetch 拦住的 Page.navigate 要等 Fetch.continueRequest),再等它就会死锁。
const pipelineGrace = 20 * time.Millisecond

var (
	errNotCDP   = errors.New("cdpendpoint: the client sent a message that is not a CDP request")
	errNoTarget = &cdpError{Code: cdpInvalidParams, Message: "No target with given id found"}
)

// unsupportedFeatures 是 sctl 的端点不支持的命令及其所属功能(spec「不支持的功能」):chrome.debugger 的标签页会话
// 做不到,而由 sctl 模拟又会对客户端谎报结果。ServiceWorker.* 整个域另见 unsupportedFeature。
var unsupportedFeatures = map[string]string{
	"Target.createBrowserContext":           "new browser contexts",
	"Target.disposeBrowserContext":          "new browser contexts",
	"Browser.grantPermissions":              "permissions",
	"Browser.resetPermissions":              "permissions",
	"Browser.setPermission":                 "permissions",
	"Browser.getWindowForTarget":            "window size and position",
	"Browser.getWindowBounds":               "window size and position",
	"Browser.setWindowBounds":               "window size and position",
	"Browser.setContentsSize":               "window size and position",
	"Security.setIgnoreCertificateErrors":   "ignoring certificate errors",
	"Security.setOverrideCertificateErrors": "ignoring certificate errors",
}

func unsupportedFeature(method string) (string, bool) {
	if feature, ok := unsupportedFeatures[method]; ok {
		return feature, true
	}
	if strings.HasPrefix(method, "ServiceWorker.") {
		return "Service Worker targets", true
	}
	return "", false
}

func notSupported(method, feature string) *cdpError {
	if feature == "" {
		feature = "this command"
	}
	return &cdpError{Code: cdpServerError, Message: method + ": sctl's CDP endpoint does not support " + feature}
}

func (e *cdpError) Error() string { return e.Message }

// toCDPError 把一次失败变成回给客户端的 CDP 错误。扩展把 Chrome 拒绝命令的错误压扁成 INVALID_REQUEST,
// 正文是 Chrome 错误的 JSON 文本,这里还原成 Chrome 的 {code, message, data}。
func toCDPError(err error) cdpError {
	var ce *cdpError
	if errors.As(err, &ce) {
		return *ce
	}
	if errors.Is(err, bridge.ErrDisconnected) {
		return cdpError{Code: cdpServerError, Message: "the browser disconnected from sctl"}
	}
	var be *bridge.Error
	if !errors.As(err, &be) {
		return cdpError{Code: cdpServerError, Message: err.Error()}
	}
	switch be.Code {
	case generated.ErrorCodeInvalidRequest:
		if chrome, ok := parseChromeError(be.Message); ok {
			return chrome
		}
		return cdpError{Code: cdpServerError, Message: be.Message}
	case generated.ErrorCodePayloadTooLarge:
		return cdpError{Code: cdpServerError, Message: "the result is too large for sctl to relay: " + be.Message}
	case generated.ErrorCodeDebuggerDetached:
		return cdpError{Code: cdpServerError, Message: "the debugger detached from the tab: " + be.Message}
	case generated.ErrorCodeNotFound:
		return cdpError{Code: cdpInvalidParams, Message: be.Message}
	case generated.ErrorCodePageNotAutomatable:
		return cdpError{Code: cdpServerError, Message: "Chrome does not let sctl debug this tab: " + be.Message}
	case generated.ErrorCodeBrowserOffline:
		return cdpError{Code: cdpServerError, Message: "the browser disconnected from sctl"}
	case generated.ErrorCodeMethodNotFound:
		return cdpError{Code: cdpMethodNotFound, Message: be.Message}
	}
	return cdpError{Code: cdpServerError, Message: be.Code + ": " + be.Message}
}

// parseChromeError 解析 chrome.debugger.sendCommand 失败时的错误文本,形如 {"code":-32000,"message":"Not allowed"}。
// 其他文本(如 "Detached while handling command.")不是 Chrome 的错误对象。
func parseChromeError(text string) (cdpError, bool) {
	if !strings.HasPrefix(strings.TrimSpace(text), "{") {
		return cdpError{}, false
	}
	var ce cdpError
	if err := json.Unmarshal([]byte(text), &ce); err != nil || ce.Code == 0 || ce.Message == "" {
		return cdpError{}, false
	}
	return ce, true
}

// chromeProduct 从 User-Agent 里取出 Chrome 的产品与版本,如 Chrome/125.0.6422.141 或 HeadlessChrome/125.0.0.0。
func chromeProduct(userAgent string) string {
	if product := chromeProductPattern.FindString(userAgent); product != "" {
		return product
	}
	return "Chrome"
}

type routeKind int

const (
	rootRoute routeKind = iota
	tabRoute
	childRoute
)

// route 是一条客户端命令的去处:根会话、某个标签页的顶层会话,或某个标签页里 Chrome 的子会话。
type route struct {
	kind          routeKind
	tab           int
	chromeSession string
}

// gateKey 标识命令要排队的 Chrome 会话:同一标签页上的几个合成会话共用 Chrome 的同一个顶层会话。
func (r route) gateKey() string {
	switch r.kind {
	case rootRoute:
		return "root"
	case tabRoute:
		return "tab:" + strconv.Itoa(r.tab)
	}
	return "child:" + r.chromeSession
}

// tabState 是客户端看到的一个页面目标。
type tabState struct {
	id         int
	targetID   string
	title, url string
	// chromeInfo 是附加后 Chrome 自己报告的目标信息(带 openerId、browserContextId 等,客户端据此认出 popup),附加前为 nil。
	chromeInfo map[string]json.RawMessage
	// attach 是这次调试器附加(开焦点模拟、读目标信息);nil 表示调试器没有附加。
	attach *pending
	// autoAttach 是为自动附加的客户端附加这个标签页的那一次,只做一次:用户关掉调试提示条后不再自动附加回去。
	autoAttach *pending
	// announced 表示已向发现目标的客户端报告过 targetCreated。
	announced bool
}

// pending 是一次进行中或已完成的操作,done 关闭后 err 可读。
type pending struct {
	done chan struct{}
	err  error
}

type childSession struct {
	tab int
	// parent 是父会话的 Chrome 会话 ID,父会话是标签页顶层时为空。
	parent string
}

type cdpResponse struct {
	ID        json.RawMessage `json:"id"`
	SessionID string          `json:"sessionId,omitempty"`
	Result    any             `json:"result"`
}

type cdpEvent struct {
	SessionID string `json:"sessionId,omitempty"`
	Method    string `json:"method"`
	Params    any    `json:"params"`
}

// closeAfterReply 是 Browser.close 的结果:回答后断开客户端,浏览器与标签页保留(spec 设计决策 7)。
type closeAfterReply struct{}

// outbound 是一条要写给客户端的消息。
type outbound struct {
	msg any
	// register 在写协程里、写出 msg 之前运行,返回 false 时不写:新会话在它的 attachedToTarget 写出时才开始接收事件,
	// 事件就不会先于它到达客户端;标签页在此之前已经断开或关闭时,这次附加不再报告。
	register   func() bool
	closeAfter bool
}

// emulation 是一个客户端连接的状态。读协程(serve 所在的 goroutine)解析请求并为每条命令起一个 goroutine;
// 写协程是唯一写连接、也是唯一处理浏览器通知的地方,所以事件与应答按一个顺序到达客户端。
type emulation struct {
	conn   *websocket.Conn
	b      *Browser
	log    *zap.Logger
	ctx    context.Context
	cancel context.CancelFunc
	out    chan outbound
	wg     sync.WaitGroup
	// browserClosed 在 Browser.close 的应答写出、连接以 1000 关闭后设置,Serve 随之正常返回。
	browserClosed atomic.Bool

	mu         sync.Mutex
	discover   bool
	autoAttach bool
	tabs       map[int]*tabState
	// sessions 是合成的页面会话 → 标签页。
	sessions map[string]int
	// children 是 Chrome 报告的子会话(跨进程 iframe、worker)→ 所在标签页与父会话。
	children map[string]childSession
	// gates 是每个 Chrome 会话上最后一条命令「可以被越过」时关闭的 channel,见 pipelineGrace。
	gates       map[string]chan struct{}
	nextSession int
}

func newEmulation(ctx context.Context, conn *websocket.Conn, b *Browser) *emulation {
	ctx, cancel := context.WithCancel(ctx)
	return &emulation{
		conn:     conn,
		b:        b,
		log:      b.m.log,
		ctx:      ctx,
		cancel:   cancel,
		out:      make(chan outbound),
		tabs:     map[int]*tabState{},
		sessions: map[string]int{},
		children: map[string]childSession{},
		gates:    map[string]chan struct{}{},
	}
}

// serve 读到连接结束,然后取消并等待全部命令与写协程:返回时这个客户端的 goroutine 都已结束。
func (e *emulation) serve() error {
	e.wg.Add(1)
	go e.writeLoop()
	err := e.readLoop()
	e.cancel()
	e.wg.Wait()
	if e.browserClosed.Load() {
		return nil
	}
	return err
}

func (e *emulation) readLoop() error {
	for {
		_, data, err := e.conn.Read(e.ctx)
		if err != nil {
			return err
		}
		var req cdpRequest
		if err := json.Unmarshal(data, &req); err != nil || len(req.ID) == 0 {
			_ = e.conn.Close(websocket.StatusUnsupportedData, "not a CDP request")
			return errNotCDP
		}
		e.dispatch(req)
	}
}

func (e *emulation) dispatch(req cdpRequest) {
	if req.Method == "" {
		e.enqueue(outbound{msg: errorResponse(req, cdpError{Code: cdpInvalidRequest, Message: "Message must have string 'method' property"})})
		return
	}
	r, ok := e.route(req.SessionID)
	if !ok {
		e.enqueue(outbound{msg: errorResponse(req, cdpError{Code: cdpSessionNotFound, Message: "Session with given id not found: " + req.SessionID})})
		return
	}
	key := r.gateKey()
	ready := make(chan struct{})
	e.mu.Lock()
	prev := e.gates[key]
	e.gates[key] = ready
	e.mu.Unlock()
	e.wg.Add(1)
	go e.run(req, r, key, prev, ready)
}

func (e *emulation) route(sessionID string) (route, bool) {
	if sessionID == "" {
		return route{kind: rootRoute}, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if tab, ok := e.sessions[sessionID]; ok {
		return route{kind: tabRoute, tab: tab}, true
	}
	if child, ok := e.children[sessionID]; ok {
		return route{kind: childRoute, tab: child.tab, chromeSession: sessionID}, true
	}
	return route{}, false
}

// run 等同一会话上的上一条命令开始满 pipelineGrace 或得到应答,然后执行这条命令并排队它的应答。
func (e *emulation) run(req cdpRequest, r route, key string, prev <-chan struct{}, ready chan struct{}) {
	defer e.wg.Done()
	var once sync.Once
	release := func() { once.Do(func() { close(ready) }) }
	defer func() {
		release()
		e.mu.Lock()
		if e.gates[key] == ready {
			delete(e.gates, key)
		}
		e.mu.Unlock()
	}()
	if prev != nil {
		select {
		case <-prev:
		case <-e.ctx.Done():
			return
		}
	}
	timer := time.AfterFunc(pipelineGrace, release)
	defer timer.Stop()

	result, err := e.execute(req, r)
	if e.ctx.Err() != nil {
		// 客户端已经断开或正在被断开,应答无处可送。
		return
	}
	if err != nil {
		e.enqueue(outbound{msg: errorResponse(req, toCDPError(err))})
		return
	}
	_, closeAfter := result.(closeAfterReply)
	if closeAfter {
		result = struct{}{}
	}
	e.enqueue(outbound{msg: cdpResponse{ID: req.ID, SessionID: req.SessionID, Result: result}, closeAfter: closeAfter})
}

func errorResponse(req cdpRequest, ce cdpError) cdpErrorResponse {
	return cdpErrorResponse{ID: req.ID, SessionID: req.SessionID, Error: ce}
}

// execute 回答一条命令:不支持的功能直接拒绝;根会话与页面会话上的浏览器级命令由这里模拟;其余转发给标签页。
// 根会话上不属于浏览器级的命令(如 Storage.getCookies)经一个已附加的标签页发出,Chrome 的结果与错误原样返回。
func (e *emulation) execute(req cdpRequest, r route) (any, error) {
	if feature, ok := unsupportedFeature(req.Method); ok {
		return nil, notSupported(req.Method, feature)
	}
	if r.kind != childRoute {
		domain, _, _ := strings.Cut(req.Method, ".")
		switch domain {
		case "Browser":
			return e.browserCommand(req)
		case "Target":
			if res, err, handled := e.targetCommand(req, r); handled {
				return res, err
			}
			if r.kind == rootRoute {
				return nil, notSupported(req.Method, "")
			}
		}
	}
	tab := r.tab
	if r.kind == rootRoute {
		var ok bool
		if tab, ok = e.anyAttachedTab(); !ok {
			return nil, &cdpError{Code: cdpServerError, Message: req.Method + ": sctl's CDP endpoint sends browser-wide commands through an attached tab, and no tab is attached"}
		}
	}
	return e.b.Send(e.ctx, tab, r.chromeSession, req.Method, req.Params)
}

func (e *emulation) browserCommand(req cdpRequest) (any, error) {
	switch req.Method {
	case "Browser.getVersion":
		ua, err := e.b.UserAgent(e.ctx)
		if err != nil {
			return nil, err
		}
		return map[string]string{"protocolVersion": protocolVersion, "product": chromeProduct(ua), "revision": "", "userAgent": ua, "jsVersion": ""}, nil
	case "Browser.setDownloadBehavior":
		// Playwright 连接时一定会发,失败会让 connectOverCDP 失败;下载照浏览器自己的设置进行,客户端收不到下载事件。
		return struct{}{}, nil
	case "Browser.close":
		return closeAfterReply{}, nil
	}
	return nil, notSupported(req.Method, "")
}

// targetCommand 模拟浏览器级的 Target.* 命令;handled 为 false 时这条命令在页面会话上转发给 Chrome
// (页面自己的 setAutoAttach、getTargetInfo 与它的子会话的 detachFromTarget 由 Chrome 回答)。
func (e *emulation) targetCommand(req cdpRequest, r route) (res any, err error, handled bool) {
	switch req.Method {
	case "Target.setAutoAttach":
		if r.kind != rootRoute {
			return nil, nil, false
		}
		res, err = e.setAutoAttach(req)
	case "Target.setDiscoverTargets":
		res, err = e.setDiscoverTargets(req)
	case "Target.getTargets":
		res, err = e.getTargets()
	case "Target.getBrowserContexts":
		// 只有默认上下文:客户端把没有 browserContextId 或 ID 不在这个列表里的目标都归入默认上下文。
		res = map[string][]string{"browserContextIds": {}}
	case "Target.getTargetInfo":
		var p struct {
			TargetID string `json:"targetId"`
		}
		if err := decodeParams(req.Params, &p); err != nil {
			return nil, err, true
		}
		if p.TargetID == "" && r.kind != rootRoute {
			return nil, nil, false
		}
		res, err = e.getTargetInfo(p.TargetID)
	case "Target.createTarget":
		res, err = e.createTarget(req)
	case "Target.closeTarget":
		res, err = e.closeTarget(req)
	case "Target.activateTarget":
		res, err = e.activateTarget(req)
	case "Target.attachToTarget":
		res, err = e.attachToTarget(req)
	case "Target.detachFromTarget":
		return e.detachFromTarget(req, r)
	default:
		return nil, nil, false
	}
	return res, err, true
}

func decodeParams(raw json.RawMessage, v any) error {
	if len(raw) == 0 {
		return nil
	}
	if err := json.Unmarshal(raw, v); err != nil {
		data, _ := json.Marshal(err.Error())
		return &cdpError{Code: cdpInvalidParams, Message: "Invalid parameters", Data: data}
	}
	return nil
}

func requireFlatten(method string, flatten *bool) error {
	if flatten == nil || !*flatten {
		return &cdpError{Code: cdpServerError, Message: method + ": sctl's CDP endpoint supports only flat sessions; pass flatten: true"}
	}
	return nil
}

// setAutoAttach 打开自动附加时,在应答之前为每个已有的标签页报告 attachedToTarget,像 Chrome 一样:Playwright 在
// connectOverCDP 返回前就据此列出已有页面。已在运行的页面不会停在调试器上,所以 waitingForDebugger 为 false;
// 客户端随后照常发的 Runtime.runIfWaitingForDebugger 转发给 Chrome,无害。filter 不起作用:Puppeteer 要的是标签页
// 目标(type tab)而这里只有页面目标,探针中它照样使用这些页面。
func (e *emulation) setAutoAttach(req cdpRequest) (any, error) {
	var p struct {
		AutoAttach bool  `json:"autoAttach"`
		Flatten    *bool `json:"flatten"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.AutoAttach {
		if err := requireFlatten(req.Method, p.Flatten); err != nil {
			return nil, err
		}
	}
	e.mu.Lock()
	e.autoAttach = p.AutoAttach
	e.mu.Unlock()
	if !p.AutoAttach {
		return struct{}{}, nil
	}
	tabs, err := e.learnTargets()
	if err != nil {
		return nil, err
	}
	var wg sync.WaitGroup
	for _, tab := range tabs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			e.autoAttachTab(tab)
		}()
	}
	wg.Wait()
	return struct{}{}, nil
}

// setDiscoverTargets 打开发现时,在应答之前为每个还没报告过的标签页报告 targetCreated。
func (e *emulation) setDiscoverTargets(req cdpRequest) (any, error) {
	var p struct {
		Discover bool `json:"discover"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	e.mu.Lock()
	e.discover = p.Discover
	if !p.Discover {
		for _, ts := range e.tabs {
			ts.announced = false
		}
	}
	e.mu.Unlock()
	if p.Discover {
		if _, err := e.learnTargets(); err != nil {
			return nil, err
		}
	}
	return struct{}{}, nil
}

func (e *emulation) getTargets() (any, error) {
	tabs, err := e.learnTargets()
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	infos := make([]map[string]any, 0, len(tabs))
	for _, tab := range tabs {
		if ts := e.tabs[tab]; ts != nil {
			infos = append(infos, e.targetInfoLocked(ts))
		}
	}
	return map[string]any{"targetInfos": infos}, nil
}

func (e *emulation) getTargetInfo(targetID string) (any, error) {
	if targetID == "" || targetID == e.b.BrowserID() {
		return map[string]any{"targetInfo": map[string]any{
			"targetId": e.b.BrowserID(), "type": "browser", "title": "", "url": "", "attached": true, "canAccessOpener": false,
		}}, nil
	}
	tab, err := e.findTab(targetID)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	ts := e.tabs[tab]
	if ts == nil {
		return nil, errNoTarget
	}
	return map[string]any{"targetInfo": e.targetInfoLocked(ts)}, nil
}

// createTarget 在这个浏览器里打开标签页;客户端自动附加时,应答之前已报告它被附加——Playwright 拿到 targetId 就去找
// 那个页面。只有默认浏览器上下文。
func (e *emulation) createTarget(req cdpRequest) (any, error) {
	var p struct {
		URL              string `json:"url"`
		Background       bool   `json:"background"`
		BrowserContextID string `json:"browserContextId"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if p.BrowserContextID != "" {
		return nil, notSupported(req.Method, "new browser contexts")
	}
	url := p.URL
	if url == "" {
		url = "about:blank"
	}
	opened, err := e.b.Open(e.ctx, url, p.Background)
	if err != nil {
		return nil, err
	}
	e.mu.Lock()
	ts := e.tabs[opened.TabID]
	if ts == nil {
		ts = &tabState{id: opened.TabID, targetID: opened.TargetID, url: url}
		e.tabs[opened.TabID] = ts
	}
	created := e.announceLocked(ts)
	e.mu.Unlock()
	if created != nil && !e.enqueue(outbound{msg: *created}) {
		return nil, e.ctx.Err()
	}
	e.autoAttachTab(opened.TabID)
	return map[string]string{"targetId": opened.TargetID}, nil
}

func (e *emulation) closeTarget(req cdpRequest) (any, error) {
	tab, err := e.targetParam(req)
	if err != nil {
		return nil, err
	}
	if err := e.b.CloseTab(e.ctx, tab); err != nil {
		return nil, err
	}
	return map[string]bool{"success": true}, nil
}

func (e *emulation) activateTarget(req cdpRequest) (any, error) {
	tab, err := e.targetParam(req)
	if err != nil {
		return nil, err
	}
	if err := e.b.ActivateTab(e.ctx, tab); err != nil {
		return nil, err
	}
	return struct{}{}, nil
}

func (e *emulation) attachToTarget(req cdpRequest) (any, error) {
	var p struct {
		Flatten *bool `json:"flatten"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err
	}
	if err := requireFlatten(req.Method, p.Flatten); err != nil {
		return nil, err
	}
	tab, err := e.targetParam(req)
	if err != nil {
		return nil, err
	}
	sessionID, err := e.attachSession(tab)
	if err != nil {
		return nil, err
	}
	return map[string]string{"sessionId": sessionID}, nil
}

// detachFromTarget 分离一个合成的页面会话;分离的是标签页上最后一个会话时断开它的调试器,提示条随之消失。
// 子会话的分离发给它的父会话,由 Chrome 完成。
func (e *emulation) detachFromTarget(req cdpRequest, r route) (any, error, bool) {
	var p struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return nil, err, true
	}
	e.mu.Lock()
	tab, synthetic := e.sessions[p.SessionID]
	child, isChild := e.children[p.SessionID]
	e.mu.Unlock()
	switch {
	case synthetic:
		res, err := e.detachSession(p.SessionID, tab)
		return res, err, true
	case isChild && r.kind == rootRoute:
		res, err := e.b.Send(e.ctx, child.tab, child.parent, req.Method, req.Params)
		return res, err, true
	case r.kind == rootRoute:
		return nil, &cdpError{Code: cdpInvalidParams, Message: "No session with given id"}, true
	}
	return nil, nil, false
}

func (e *emulation) detachSession(sessionID string, tab int) (any, error) {
	e.mu.Lock()
	delete(e.sessions, sessionID)
	targetID := ""
	last := len(e.sessionsOfLocked(tab)) == 0
	if ts := e.tabs[tab]; ts != nil {
		targetID = ts.targetID
		if last {
			ts.attach = nil
			e.dropChildrenLocked(tab)
		}
	}
	e.mu.Unlock()
	detached := cdpEvent{Method: "Target.detachedFromTarget", Params: map[string]string{"sessionId": sessionID, "targetId": targetID}}
	if !e.enqueue(outbound{msg: detached}) {
		return nil, e.ctx.Err()
	}
	if last {
		if err := e.b.DetachTab(e.ctx, tab); err != nil {
			return nil, err
		}
	}
	return struct{}{}, nil
}

func (e *emulation) targetParam(req cdpRequest) (int, error) {
	var p struct {
		TargetID string `json:"targetId"`
	}
	if err := decodeParams(req.Params, &p); err != nil {
		return 0, err
	}
	return e.findTab(p.TargetID)
}

// findTab 按 targetId 找标签页,不认识时重新列出一次浏览器的标签页。
func (e *emulation) findTab(targetID string) (int, error) {
	if tab, ok := e.tabByTarget(targetID); ok {
		return tab, nil
	}
	if _, err := e.learnTargets(); err != nil {
		return 0, err
	}
	if tab, ok := e.tabByTarget(targetID); ok {
		return tab, nil
	}
	return 0, errNoTarget
}

func (e *emulation) tabByTarget(targetID string) (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	for id, ts := range e.tabs {
		if targetID != "" && ts.targetID == targetID {
			return id, true
		}
	}
	return 0, false
}

// learnTargets 列出浏览器里能附加的标签页并记下它们,向发现目标的客户端报告还没报告过的;返回标签页 ID,按扩展给的顺序。
func (e *emulation) learnTargets() ([]int, error) {
	targets, err := e.b.Targets(e.ctx)
	if err != nil {
		return nil, err
	}
	ids := make([]int, 0, len(targets))
	var created []cdpEvent
	e.mu.Lock()
	for _, t := range targets {
		ts := e.tabs[t.TabID]
		if ts == nil {
			ts = &tabState{id: t.TabID}
			e.tabs[t.TabID] = ts
		}
		ts.targetID, ts.title, ts.url = t.TargetID, t.Title, t.URL
		if ev := e.announceLocked(ts); ev != nil {
			created = append(created, *ev)
		}
		ids = append(ids, t.TabID)
	}
	e.mu.Unlock()
	for _, ev := range created {
		if !e.enqueue(outbound{msg: ev}) {
			return nil, e.ctx.Err()
		}
	}
	return ids, nil
}

// announceLocked 在客户端发现目标且还没报告过这个标签页时给出它的 targetCreated。调用方持有 e.mu。
func (e *emulation) announceLocked(ts *tabState) *cdpEvent {
	if !e.discover || ts.announced {
		return nil
	}
	ts.announced = true
	return &cdpEvent{Method: "Target.targetCreated", Params: map[string]any{"targetInfo": e.targetInfoLocked(ts)}}
}

// targetInfoLocked 是标签页的 CDP 目标信息:附加过时以 Chrome 自己的为底,标题、地址与 attached 取当前值。调用方持有 e.mu。
func (e *emulation) targetInfoLocked(ts *tabState) map[string]any {
	info := make(map[string]any, len(ts.chromeInfo)+6)
	for k, v := range ts.chromeInfo {
		info[k] = v
	}
	info["targetId"] = ts.targetID
	info["title"] = ts.title
	info["url"] = ts.url
	info["attached"] = len(e.sessionsOfLocked(ts.id)) > 0
	if _, ok := info["type"]; !ok {
		info["type"] = "page"
	}
	if _, ok := info["canAccessOpener"]; !ok {
		info["canAccessOpener"] = false
	}
	return info
}

// sessionsOfLocked 是标签页上的合成会话,按 ID 排序。调用方持有 e.mu。
func (e *emulation) sessionsOfLocked(tab int) []string {
	var out []string
	for sid, t := range e.sessions {
		if t == tab {
			out = append(out, sid)
		}
	}
	slices.Sort(out)
	return out
}

func (e *emulation) dropChildrenLocked(tab int) {
	for sid, child := range e.children {
		if child.tab == tab {
			delete(e.children, sid)
		}
	}
}

// anyAttachedTab 是调试器已附加的标签页里 ID 最小的一个,根会话上的非浏览器级命令经它发出。
func (e *emulation) anyAttachedTab() (int, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	best, found := 0, false
	for id, ts := range e.tabs {
		if ts.attach == nil || !ts.attach.succeeded() {
			continue
		}
		if !found || id < best {
			best, found = id, true
		}
	}
	return best, found
}

func (p *pending) succeeded() bool {
	select {
	case <-p.done:
		return p.err == nil
	default:
		return false
	}
}

// autoAttachTab 为自动附加的客户端附加标签页,每个标签页只做一次;另一次正在进行时等它报告完附加再返回,
// 这样 createTarget 的应答不会先于它的 attachedToTarget。
func (e *emulation) autoAttachTab(tab int) {
	e.mu.Lock()
	ts := e.tabs[tab]
	if ts == nil || !e.autoAttach {
		e.mu.Unlock()
		return
	}
	if p := ts.autoAttach; p != nil {
		e.mu.Unlock()
		select {
		case <-p.done:
		case <-e.ctx.Done():
		}
		return
	}
	p := &pending{done: make(chan struct{})}
	ts.autoAttach = p
	e.mu.Unlock()
	defer close(p.done)
	if _, err := e.attachSession(tab); err != nil {
		p.err = err
		e.log.Debug("could not auto-attach a tab for the CDP endpoint client", zap.Int("tabId", tab), zap.Error(err))
	}
}

// attachSession 为客户端附加标签页并开一个合成会话,报告 attachedToTarget,返回会话 ID。
func (e *emulation) attachSession(tab int) (string, error) {
	if err := e.attachTab(tab); err != nil {
		return "", err
	}
	e.mu.Lock()
	ts := e.tabs[tab]
	if ts == nil {
		e.mu.Unlock()
		return "", errNoTarget
	}
	e.nextSession++
	sessionID := fmt.Sprintf("sctl-session-%d", e.nextSession)
	info := e.targetInfoLocked(ts)
	info["attached"] = true
	e.mu.Unlock()
	// 写协程决定是否报告附加;没报告时这个会话不存在,不能把它的 ID 交给客户端。
	registered := make(chan bool, 1)
	attached := outbound{
		msg: cdpEvent{Method: "Target.attachedToTarget", Params: map[string]any{"sessionId": sessionID, "targetInfo": info, "waitingForDebugger": false}},
		register: func() bool {
			e.mu.Lock()
			defer e.mu.Unlock()
			ok := e.tabs[tab] == ts && ts.attach != nil
			if ok {
				e.sessions[sessionID] = tab
			}
			registered <- ok
			return ok
		},
	}
	if !e.enqueue(attached) {
		return "", e.ctx.Err()
	}
	select {
	case ok := <-registered:
		if !ok {
			return "", &cdpError{Code: cdpServerError, Message: "the debugger detached from the tab while sctl was attaching it"}
		}
		return sessionID, nil
	case <-e.ctx.Done():
		return "", e.ctx.Err()
	}
}

// attachTab 附加标签页的调试器并做好准备,同一标签页上同时只做一次:先开焦点模拟,再读 Chrome 自己的目标信息。
// 焦点模拟在客户端的任何命令之前开启:不开时 Puppeteer 在后台标签页上的点击、输入、弹框与截图会超时(spec「焦点模拟」)。
func (e *emulation) attachTab(tab int) error {
	e.mu.Lock()
	ts := e.tabs[tab]
	if ts == nil {
		e.mu.Unlock()
		return errNoTarget
	}
	if p := ts.attach; p != nil {
		e.mu.Unlock()
		select {
		case <-p.done:
			return p.err
		case <-e.ctx.Done():
			return e.ctx.Err()
		}
	}
	p := &pending{done: make(chan struct{})}
	ts.attach = p
	e.mu.Unlock()

	info, err := e.prepareTab(tab)
	e.mu.Lock()
	defer e.mu.Unlock()
	p.err = err
	close(p.done)
	if ts.attach != p {
		// 准备期间 Chrome 断开了调试器或标签页已关闭。
		return err
	}
	if err != nil {
		ts.attach = nil
		return err
	}
	ts.chromeInfo = info
	var s string
	if json.Unmarshal(info["title"], &s) == nil {
		ts.title = s
	}
	if json.Unmarshal(info["url"], &s) == nil {
		ts.url = s
	}
	return nil
}

func (e *emulation) prepareTab(tab int) (map[string]json.RawMessage, error) {
	if _, err := e.b.Send(e.ctx, tab, "", "Emulation.setFocusEmulationEnabled", json.RawMessage(`{"enabled":true}`)); err != nil {
		return nil, err
	}
	raw, err := e.b.Send(e.ctx, tab, "", "Target.getTargetInfo", nil)
	if err != nil {
		return nil, err
	}
	var res struct {
		TargetInfo map[string]json.RawMessage `json:"targetInfo"`
	}
	if err := json.Unmarshal(raw, &res); err != nil || res.TargetInfo == nil {
		return nil, fmt.Errorf("chrome answered Target.getTargetInfo for tab %d without a target: %s", tab, raw)
	}
	return res.TargetInfo, nil
}

func (e *emulation) enqueue(o outbound) bool {
	select {
	case e.out <- o:
		return true
	case <-e.ctx.Done():
		return false
	}
}

// writeLoop 是唯一写连接、处理浏览器通知的 goroutine。写出一条排队的消息之前,先处理此刻已经到达的通知:扩展按 Chrome
// 发出的顺序送来事件与应答,bridge 在交出应答之前已把它前面的事件放进通知队列,这样 Chrome 先于应答发出的事件
// (如 Runtime.enable 之前的 executionContextCreated)也先于应答到达客户端。
func (e *emulation) writeLoop() {
	defer e.wg.Done()
	// 写协程结束后没有人再取排队的消息,还在排队的读协程与命令要靠 ctx 结束才能返回。
	defer e.cancel()
	events := e.b.Notifications()
	for {
		select {
		case <-e.ctx.Done():
			return
		case n := <-events:
			if !e.onNotification(n) {
				return
			}
		case o := <-e.out:
			for range len(events) {
				if !e.onNotification(<-events) {
					return
				}
			}
			if o.register != nil && !o.register() {
				continue
			}
			if !e.write(o.msg) {
				return
			}
			if o.closeAfter {
				e.browserClosed.Store(true)
				_ = e.conn.Close(websocket.StatusNormalClosure, "Browser.close: the client disconnected; the browser keeps running")
				return
			}
		}
	}
}

// write 写出一条消息;失败时连接已不可用,结束整个会话。
func (e *emulation) write(msg any) bool {
	data, err := json.Marshal(msg)
	if err == nil {
		err = e.conn.Write(e.ctx, websocket.MessageText, data)
	}
	if err != nil {
		e.log.Debug("could not write to the CDP endpoint client", zap.Error(err))
		e.cancel()
		return false
	}
	return true
}

func (e *emulation) writeAll(msgs []cdpEvent) bool {
	for _, m := range msgs {
		if !e.write(m) {
			return false
		}
	}
	return true
}

// onNotification 把浏览器的一条通知转成客户端的事件。返回 false 表示连接已不可用。
func (e *emulation) onNotification(n Notification) bool {
	switch generated.Notification(n.Method) {
	case generated.NotificationDebuggerEvent:
		var ev generated.DebuggerEventNotification
		if err := json.Unmarshal(n.Params, &ev); err != nil {
			e.log.Debug("ignoring a malformed debugger.event", zap.Error(err))
			return true
		}
		return e.writeAll(e.chromeEvent(ev))
	case generated.NotificationDebuggerDetached:
		var d generated.DebuggerDetachedNotification
		if err := json.Unmarshal(n.Params, &d); err != nil {
			e.log.Debug("ignoring a malformed debugger.detached", zap.Error(err))
			return true
		}
		e.mu.Lock()
		msgs := e.detachTabLocked(d.TabId)
		if d.Reason == "target_closed" {
			msgs = append(msgs, e.removeTabLocked(d.TabId)...)
		}
		e.mu.Unlock()
		return e.writeAll(msgs)
	case generated.NotificationDebuggerTabRemoved:
		var r generated.DebuggerTabRemovedNotification
		if err := json.Unmarshal(n.Params, &r); err != nil {
			e.log.Debug("ignoring a malformed debugger.tabRemoved", zap.Error(err))
			return true
		}
		e.mu.Lock()
		msgs := append(e.detachTabLocked(r.TabId), e.removeTabLocked(r.TabId)...)
		e.mu.Unlock()
		return e.writeAll(msgs)
	case generated.NotificationDebuggerTabCreated, generated.NotificationDebuggerTabUpdated:
		var t generated.DebuggerTabNotification
		if err := json.Unmarshal(n.Params, &t); err != nil {
			e.log.Debug("ignoring a malformed tab notification", zap.String("method", n.Method), zap.Error(err))
			return true
		}
		return e.onTab(t)
	}
	return true
}

// chromeEvent 把标签页上的一条 CDP 事件交给客户端:顶层会话的事件发给这个标签页上的每个合成会话,子会话的事件带着
// Chrome 的子会话 ID 原样转发。没有客户端会话的标签页上的事件丢弃。顺带跟踪子会话的附加与分离。
func (e *emulation) chromeEvent(ev generated.DebuggerEventNotification) []cdpEvent {
	params := ev.Params
	if len(params) == 0 {
		params = json.RawMessage(`{}`)
	}
	parent := ""
	if ev.SessionId != nil {
		parent = *ev.SessionId
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	var targets []string
	if parent == "" {
		targets = e.sessionsOfLocked(ev.TabId)
	} else if child, ok := e.children[parent]; ok && child.tab == ev.TabId {
		targets = []string{parent}
	}
	if len(targets) == 0 {
		return nil
	}
	switch ev.Method {
	case "Target.attachedToTarget", "Target.detachedFromTarget":
		var p struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(params, &p); err != nil || p.SessionID == "" {
			break
		}
		if ev.Method == "Target.attachedToTarget" {
			e.children[p.SessionID] = childSession{tab: ev.TabId, parent: parent}
		} else {
			delete(e.children, p.SessionID)
		}
	}
	msgs := make([]cdpEvent, len(targets))
	for i, sid := range targets {
		msgs[i] = cdpEvent{SessionID: sid, Method: ev.Method, Params: params}
	}
	return msgs
}

// detachTabLocked 处理标签页的调试器被断开:它的会话都报告分离,子会话作废,下次附加重新准备。调用方持有 e.mu。
func (e *emulation) detachTabLocked(tab int) []cdpEvent {
	targetID := ""
	if ts := e.tabs[tab]; ts != nil {
		targetID = ts.targetID
		ts.attach = nil
	}
	var msgs []cdpEvent
	for _, sid := range e.sessionsOfLocked(tab) {
		delete(e.sessions, sid)
		msgs = append(msgs, cdpEvent{Method: "Target.detachedFromTarget", Params: map[string]string{"sessionId": sid, "targetId": targetID}})
	}
	e.dropChildrenLocked(tab)
	return msgs
}

// removeTabLocked 处理标签页关闭:向发现目标的客户端报告 targetDestroyed。调用方持有 e.mu。
func (e *emulation) removeTabLocked(tab int) []cdpEvent {
	ts := e.tabs[tab]
	if ts == nil {
		return nil
	}
	delete(e.tabs, tab)
	if !ts.announced {
		return nil
	}
	return []cdpEvent{{Method: "Target.targetDestroyed", Params: map[string]string{"targetId": ts.targetID}}}
}

// onTab 处理标签页的创建与变化。还不认识的标签页(包括先以变化通知出现的)按新目标报告,客户端自动附加时附加它;
// 认识的标签页只在标题、地址或目标 ID 变化时报告 targetInfoChanged。
func (e *emulation) onTab(t generated.DebuggerTabNotification) bool {
	e.mu.Lock()
	var msgs []cdpEvent
	attach := false
	ts := e.tabs[t.TabId]
	switch {
	case ts == nil:
		ts = &tabState{id: t.TabId, targetID: t.TargetId, title: t.Title, url: t.URL}
		e.tabs[t.TabId] = ts
		if ev := e.announceLocked(ts); ev != nil {
			msgs = append(msgs, *ev)
		}
		attach = e.autoAttach
	case ts.targetID != t.TargetId || ts.title != t.Title || ts.url != t.URL:
		ts.targetID, ts.title, ts.url = t.TargetId, t.Title, t.URL
		if ts.announced {
			msgs = append(msgs, cdpEvent{Method: "Target.targetInfoChanged", Params: map[string]any{"targetInfo": e.targetInfoLocked(ts)}})
		}
	}
	e.mu.Unlock()
	if !e.writeAll(msgs) {
		return false
	}
	if attach {
		e.wg.Add(1)
		go func() {
			defer e.wg.Done()
			e.autoAttachTab(t.TabId)
		}()
	}
	return true
}
