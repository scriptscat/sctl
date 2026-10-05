package page

import (
	"context"
	"encoding/json"
	"sync"

	"go.uber.org/zap"
)

// autoAttachParams 让一个会话自动附加它下面的跨进程 iframe(OOPIF),并以扁平会话(命令与事件带 sessionId)
// 报告。主会话的无障碍树与 DOM 不含 OOPIF 的内容,只能经它自己的子会话读取(spec §真机探针的结论)。
// Chrome 在 setAutoAttach 应答之前就为已有的 OOPIF 发出 Target.attachedToTarget。
// 新出现的 OOPIF 先暂停,子会话准备好(开启记录)之后才放行,它加载时最早的事件也能收到(spec §记录什么,
// 真机探针的结论);已有的 OOPIF 不暂停。每个子会话都由 setupChildSession 放行。
var autoAttachParams = map[string]bool{"autoAttach": true, "waitForDebuggerOnStart": true, "flatten": true}

// frameSessions 记录标签页下 OOPIF 的子会话。iframe 目标的 targetId 就是它的 frameId,所以按 frameId 查找;
// 其他类型的目标(worker 等)也会被自动附加,它们的 targetId 不会与 frameId 相同,记下也不会被查到。
// 事件在 bridge 读循环里更新它,动作在标签页队列里读它,所以用自己的锁。
type frameSessions struct {
	mu       sync.Mutex
	byFrame  map[string]string
	sessions map[string]*frameSession
}

type frameSession struct {
	frameID string
	// parent 是报告这个子会话的会话,顶层会话为空。
	parent string
	// url 是这个 frame 当前文档的 URL,写进来自子会话的调试记录。
	url string
	// iframe 表示目标是跨进程 iframe;worker 等其他目标的网络请求不记录。
	iframe bool
	// setupDone 在 setupChildSession 结束(无论成败)时关闭。
	setupDone chan struct{}
	// ready 表示已在这个会话上开启自动附加与 Page 域。
	ready bool
	// network 表示已在这个会话上开启 Network 域。
	network bool
}

func newFrameSessions() *frameSessions {
	return &frameSessions{byFrame: map[string]string{}, sessions: map[string]*frameSession{}}
}

// attached 记录 parent 会话报告的子会话。同一个 frame 换进程时,新会话的附加可能早于旧会话的分离。
func (fs *frameSessions) attached(parent, sessionID, frameID, url string, iframe bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.sessions[sessionID] = &frameSession{frameID: frameID, parent: parent, url: url, iframe: iframe, setupDone: make(chan struct{})}
	fs.byFrame[frameID] = sessionID
}

// url 返回子会话的 frame 当前的 URL;不是已附加的子会话时为空。
func (fs *frameSessions) url(sessionID string) string {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s, ok := fs.sessions[sessionID]; ok {
		return s.url
	}
	return ""
}

// isIframe 表示 sessionID 是仍然附加着的跨进程 iframe 子会话。
func (fs *frameSessions) isIframe(sessionID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	return ok && s.iframe
}

// navigated 在子会话的 frame 自己导航时更新它的 URL;子会话里嵌套的同进程 iframe 不算。
func (fs *frameSessions) navigated(sessionID, frameID, url string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s, ok := fs.sessions[sessionID]; ok && s.frameID == frameID {
		s.url = url
	}
}

// setupSignal 返回子会话准备结束时关闭的 channel。
func (fs *frameSessions) setupSignal(sessionID string) (chan struct{}, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	if !ok {
		return nil, false
	}
	return s.setupDone, true
}

// detached 删除一个子会话和它下面的全部子会话,返回它的 frame。Chrome 分离一个 OOPIF 时不一定为嵌套在
// 里面的子会话单独报告分离,而它们的文档已随外层一起消失。
func (fs *frameSessions) detached(sessionID string) (string, bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	if !ok {
		return "", false
	}
	fs.remove(sessionID)
	return s.frameID, true
}

// remove 删除 sessionID 及其后代。调用方持有 fs.mu。
func (fs *frameSessions) remove(sessionID string) {
	s := fs.sessions[sessionID]
	delete(fs.sessions, sessionID)
	if fs.byFrame[s.frameID] == sessionID {
		delete(fs.byFrame, s.frameID)
	}
	for id, child := range fs.sessions {
		if child.parent == sessionID {
			fs.remove(id)
		}
	}
}

// session 返回 frame 当前的子会话与它准备结束的信号;frame 与父文档同进程或没有被附加时 ok 为 false。
func (fs *frameSessions) session(frameID string) (sessionID string, setupDone chan struct{}, ok bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	sessionID, ok = fs.byFrame[frameID]
	if !ok {
		return "", nil, false
	}
	return sessionID, fs.sessions[sessionID].setupDone, true
}

// isReady 表示已在子会话上开启自动附加与 Page 域。
func (fs *frameSessions) isReady(sessionID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	return ok && s.ready
}

// sessionRef 是一个子会话与它的 frame。
type sessionRef struct {
	sessionID, frameID string
}

// withoutNetwork 返回还没开启 Network 域的子会话。
func (fs *frameSessions) withoutNetwork() []sessionRef {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	var out []sessionRef
	for id, s := range fs.sessions {
		if !s.network {
			out = append(out, sessionRef{sessionID: id, frameID: s.frameID})
		}
	}
	return out
}

func (fs *frameSessions) hasNetwork(sessionID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	return ok && s.network
}

func (fs *frameSessions) markNetwork(sessionID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s, ok := fs.sessions[sessionID]; ok {
		s.network = true
	}
}

// alive 表示 sessionID 是仍然附加着的子会话。
func (fs *frameSessions) alive(sessionID string) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	_, ok := fs.sessions[sessionID]
	return ok
}

func (fs *frameSessions) markReady(sessionID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s, ok := fs.sessions[sessionID]; ok {
		s.ready = true
	}
}

// autoAttachFrames 在标签页的顶层会话上开启自动附加;嵌套的 OOPIF 由各自外层子会话的准备开启
// (setupChildSession)。
func autoAttachFrames(ctx context.Context, t *Tab) error {
	return t.send(ctx, "Target.setAutoAttach", autoAttachParams, nil)
}

// frameSession 返回 frame 的子会话,先等它的准备(setupChildSession)结束;准备没能开启自动附加(让嵌套的 OOPIF
// 也附加上)与 Page 域(让它的文档替换作废其中的引用)时在这里补上。frame 不是已附加的 OOPIF 时 ok 为 false。
func (t *Tab) frameSession(ctx context.Context, frameID string) (sessionID string, ok bool, err error) {
	sessionID, setupDone, ok := t.frames.session(frameID)
	if !ok {
		return "", false, nil
	}
	select {
	case <-setupDone:
	case <-ctx.Done():
		return "", false, ctx.Err()
	}
	if t.frames.isReady(sessionID) {
		return sessionID, true, nil
	}
	if err := t.sendTo(ctx, sessionID, "Page.enable", nil, nil); err != nil {
		return "", false, err
	}
	if err := t.sendTo(ctx, sessionID, "Target.setAutoAttach", autoAttachParams, nil); err != nil {
		return "", false, err
	}
	t.frames.markReady(sessionID)
	return sessionID, true, nil
}

// targetAttachedEvent 是 Target.attachedToTarget 中用到的字段。
type targetAttachedEvent struct {
	SessionID  string `json:"sessionId"`
	TargetInfo struct {
		TargetID string `json:"targetId"`
		Type     string `json:"type"`
		URL      string `json:"url"`
	} `json:"targetInfo"`
}

type targetDetachedEvent struct {
	SessionID string `json:"sessionId"`
}

func onTargetAttached(t *Tab, sessionID string, params json.RawMessage) {
	var ev targetAttachedEvent
	if err := json.Unmarshal(params, &ev); err != nil || ev.SessionID == "" || ev.TargetInfo.TargetID == "" {
		t.m.log.Debug("ignoring a malformed Target.attachedToTarget event", zap.Int("tabId", t.id), zap.Error(err))
		return
	}
	t.frames.attached(sessionID, ev.SessionID, ev.TargetInfo.TargetID, ev.TargetInfo.URL, ev.TargetInfo.Type == "iframe")
	t.net.frameAttached(ev.TargetInfo.TargetID, ev.SessionID)
}

// onChildSessionAttached 为新附加的子会话安排准备。事件处理函数运行在 bridge 读循环里,不能等命令的应答,
// 所以准备在标签页的后台任务里执行;它与标签页队列里的命令并行,因为暂停的 iframe 可能正拖住队列里的命令
// (导航等 load 事件)。在 onTargetAttached 之后注册,子会话这时已经记下。
func onChildSessionAttached(t *Tab, _ string, params json.RawMessage) {
	var ev targetAttachedEvent
	if json.Unmarshal(params, &ev) != nil {
		return
	}
	done, ok := t.frames.setupSignal(ev.SessionID)
	if !ok {
		return
	}
	iframe := ev.TargetInfo.Type == "iframe"
	if !t.background(func() { t.setupChildSession(ev.SessionID, iframe, done) }) {
		close(done)
	}
}

// setupChildSession 在跨进程 iframe 的子会话上开启网络与控制台记录、Page 域与嵌套 iframe 的自动附加,然后放行它。
// 不是 iframe 的目标(worker)不记录,只放行。无论开启是否成功都放行:自动附加让新目标在启动时暂停,
// 不放行它就一直卡住,第 3 期的导航与 networkidle 也会被拖住。标签页断开时不必放行,调试器分离会让目标继续。
func (t *Tab) setupChildSession(sessionID string, iframe bool, done chan struct{}) {
	defer close(done)
	if iframe {
		ctx, cancel := context.WithTimeout(t.life, t.m.childSetupTimeout)
		err := t.prepareChildSession(ctx, sessionID)
		cancel()
		if err != nil {
			t.m.log.Debug("failed to prepare a cross-process iframe session", zap.Int("tabId", t.id), zap.String("sessionId", sessionID), zap.Error(err))
		}
	}
	ctx, cancel := context.WithTimeout(t.life, t.m.childSetupTimeout)
	defer cancel()
	if err := t.sendTo(ctx, sessionID, "Runtime.runIfWaitingForDebugger", nil, nil); err != nil {
		t.m.log.Debug("failed to resume a cross-process iframe session", zap.Int("tabId", t.id), zap.String("sessionId", sessionID), zap.Error(err))
	}
}

// prepareChildSession 是子会话放行之前开启的全部域;要在 iframe 最早的事件之前开启的新域加在这里。
// Network 最先开启:开启之前的请求不会回放,控制台记录开启失败时网络记录也已在了。
func (t *Tab) prepareChildSession(ctx context.Context, sessionID string) error {
	if err := t.sendTo(ctx, sessionID, "Network.enable", networkEnableParams, nil); err != nil {
		return err
	}
	t.frames.markNetwork(sessionID)
	for _, c := range []struct {
		method string
		params any
	}{
		{"Runtime.enable", nil},
		{"Log.enable", nil},
		{"Page.enable", nil},
		{"Target.setAutoAttach", autoAttachParams},
	} {
		if err := t.sendTo(ctx, sessionID, c.method, c.params, nil); err != nil {
			return err
		}
	}
	t.frames.markReady(sessionID)
	return nil
}

// onTargetDetached 在 OOPIF 的子会话分离(iframe 被移除、换了进程或回到父文档的进程)时作废其中的引用。
func onTargetDetached(t *Tab, _ string, params json.RawMessage) {
	var ev targetDetachedEvent
	if err := json.Unmarshal(params, &ev); err != nil {
		t.m.log.Debug("ignoring a malformed Target.detachedFromTarget event", zap.Int("tabId", t.id), zap.Error(err))
		return
	}
	if frameID, ok := t.frames.detached(ev.SessionID); ok {
		t.refs.replaceFrame(frameID)
		t.net.sessionsGone(t.frames.alive)
	}
}

// owner 返回子会话对应的 frame 与报告它的会话(顶层会话为空);sessionID 不是已附加的子会话时 ok 为 false。
func (fs *frameSessions) owner(sessionID string) (frameID, parent string, ok bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	s, ok := fs.sessions[sessionID]
	if !ok {
		return "", "", false
	}
	return s.frameID, s.parent, true
}
