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
var autoAttachParams = map[string]bool{"autoAttach": true, "waitForDebuggerOnStart": false, "flatten": true}

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
	// ready 表示已在这个会话上开启自动附加与 Page 域。只在标签页队列里读写。
	ready bool
}

func newFrameSessions() *frameSessions {
	return &frameSessions{byFrame: map[string]string{}, sessions: map[string]*frameSession{}}
}

// attached 记录 parent 会话报告的子会话。同一个 frame 换进程时,新会话的附加可能早于旧会话的分离。
func (fs *frameSessions) attached(parent, sessionID, frameID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	fs.sessions[sessionID] = &frameSession{frameID: frameID, parent: parent}
	fs.byFrame[frameID] = sessionID
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

// session 返回 frame 当前的子会话;frame 与父文档同进程或没有被附加时 ok 为 false。
func (fs *frameSessions) session(frameID string) (sessionID string, ready, ok bool) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	sessionID, ok = fs.byFrame[frameID]
	if !ok {
		return "", false, false
	}
	return sessionID, fs.sessions[sessionID].ready, true
}

func (fs *frameSessions) markReady(sessionID string) {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	if s, ok := fs.sessions[sessionID]; ok {
		s.ready = true
	}
}

// autoAttachFrames 在标签页的顶层会话上开启自动附加;嵌套的 OOPIF 由快照在用到外层子会话时开启
// (frameSession),因为事件处理函数不能发命令。
func autoAttachFrames(ctx context.Context, t *Tab) error {
	return t.send(ctx, "Target.setAutoAttach", autoAttachParams, nil)
}

// frameSession 返回 frame 的子会话,第一次用到时在它上面开启自动附加(让嵌套的 OOPIF 也附加上)与
// Page 域(让它的文档替换作废其中的引用)。frame 不是已附加的 OOPIF 时 ok 为 false。
func (t *Tab) frameSession(ctx context.Context, frameID string) (sessionID string, ok bool, err error) {
	sessionID, ready, ok := t.frames.session(frameID)
	if !ok || ready {
		return sessionID, ok, nil
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
	t.frames.attached(sessionID, ev.SessionID, ev.TargetInfo.TargetID)
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
	}
}
