package page

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

// networkIdleQuiet 是 networkidle 要求的静默时间:至少这么久没有进行中的网络请求(spec 动作列表)。
const networkIdleQuiet = 500 * time.Millisecond

// netWatch 跟踪标签页(顶层会话与跨进程 iframe 的子会话)上进行中的网络请求与主文档的 HTTP 状态。
// 事件在 bridge 读循环里写入,动作在标签页队列里读取,所以用自己的锁。
type netWatch struct {
	mu sync.Mutex
	// inflight 按 requestId 记录:跨进程 iframe 的文档请求在父会话里开始、在 iframe 自己的子会话里结束,
	// 两边报告的是同一个 requestId。
	inflight map[string]request
	// idleSince 是进行中的请求数最近一次变成 0 的时刻。
	idleSince time.Time
	mainFrame string
	status    int
	changed   chan struct{}
}

// request 是一个进行中的请求:报告它开始的会话,以及文档请求所属的 frame(其余请求为空)。
type request struct {
	sessionID     string
	documentFrame string
}

func newNetWatch() *netWatch {
	return &netWatch{inflight: map[string]request{}, idleSince: time.Now(), changed: make(chan struct{}, 1)}
}

// reset 在一次导航开始前丢弃上一个文档遗留的请求(导航会取消它们)与旧状态码;mainFrame 是主 frame。
func (w *netWatch) reset(mainFrame string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	clear(w.inflight)
	w.idleSince = time.Now()
	w.mainFrame = mainFrame
	w.status = 0
}

func (w *netWatch) signal() {
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

func (w *netWatch) started(id string, r request) {
	w.mu.Lock()
	w.inflight[id] = r
	w.mu.Unlock()
	w.signal()
}

func (w *netWatch) finished(id string) {
	w.drop(func(reqID string, _ request) bool { return reqID == id })
}

// drop 删除 match 选中的进行中请求,并唤醒等待者。
func (w *netWatch) drop(match func(id string, r request) bool) {
	w.mu.Lock()
	removed := false
	for id, r := range w.inflight {
		if match(id, r) {
			delete(w.inflight, id)
			removed = true
		}
	}
	if removed && len(w.inflight) == 0 {
		w.idleSince = time.Now()
	}
	w.mu.Unlock()
	w.signal()
}

// frameAttached 在 frame 换成自己的子会话时,不再等父会话里开始的它的文档请求:这个请求在子会话里结束,
// 而子会话的 Network 域要等动作开启,结束事件可能在那之前就已发出,再等就永远等不到。
func (w *netWatch) frameAttached(frameID, sessionID string) {
	w.drop(func(_ string, r request) bool { return r.documentFrame == frameID && r.sessionID != sessionID })
}

// sessionsGone 丢弃已分离的子会话里开始的请求:它们的结束事件不会再来。
func (w *netWatch) sessionsGone(alive func(sessionID string) bool) {
	w.drop(func(_ string, r request) bool { return r.sessionID != "" && !alive(r.sessionID) })
}

func (w *netWatch) response(frameID string, status int) {
	w.mu.Lock()
	if frameID == w.mainFrame {
		w.status = status
	}
	w.mu.Unlock()
}

// idle 返回进行中的请求数,以及为 0 时它持续了多久。
func (w *netWatch) idle() (inflight int, quiet time.Duration) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return len(w.inflight), time.Since(w.idleSince)
}

// quietFor 表示没有进行中的请求,并且已持续至少 d。
func (w *netWatch) quietFor(d time.Duration) bool {
	inflight, quiet := w.idle()
	return inflight == 0 && quiet >= d
}

// documentStatus 返回主文档最近一次响应的 HTTP 状态;没有响应(例如缓存恢复)时为 nil。
func (w *netWatch) documentStatus() *int {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.status == 0 {
		return nil
	}
	status := w.status
	return &status
}

// watchNetwork 从这里开始跟踪进行中的请求。Network 域在附加时已为网络记录开启,但 networkidle 只看这次附加里
// 第一个需要网络状态的动作之后开始的请求:之前开始、永远不结束的请求(页面没读响应体的 fetch、长轮询)
// 不能让它永远等不到空闲。
func (t *Tab) watchNetwork() {
	if t.watchingNetwork {
		return
	}
	t.net.reset("")
	t.watchingNetwork = true
}

// watchFrameNetwork 在还没开启 Network 域的子会话上开启它:跨进程 iframe 的请求只在它自己的子会话里报告。
// 同时让子会话自动附加嵌套的跨进程 iframe。事件处理函数不能发命令,所以由等待网络空闲的动作在每次醒来时调用。
// 子会话的准备(setupChildSession)本身会开启 Network 域,先等它结束,不重复开启。
func (t *Tab) watchFrameNetwork(ctx context.Context) error {
	for _, fs := range t.frames.withoutNetwork() {
		if done, ok := t.frames.setupSignal(fs.sessionID); ok {
			select {
			case <-done:
			case <-ctx.Done():
				return ctx.Err()
			}
			if t.frames.hasNetwork(fs.sessionID) {
				continue
			}
		}
		// 已经分离的子会话与不是 frame 的目标(worker)拒绝这些命令,跳过它们。
		if err := t.sendTo(ctx, fs.sessionID, "Network.enable", networkEnableParams, nil); err != nil && !isCDPError(err) {
			return err
		}
		t.frames.markNetwork(fs.sessionID)
		if _, _, err := t.frameSession(ctx, fs.frameID); err != nil && !isCDPError(err) {
			return err
		}
	}
	return nil
}

// waitNetworkIdle 等到至少 networkIdleQuiet 内没有进行中的请求。
func (t *Tab) waitNetworkIdle(ctx context.Context) error {
	for {
		if err := t.watchFrameNetwork(ctx); err != nil {
			return err
		}
		inflight, quiet := t.net.idle()
		var timer <-chan time.Time
		var tm *time.Timer
		if inflight == 0 {
			if quiet >= networkIdleQuiet {
				return nil
			}
			tm = time.NewTimer(networkIdleQuiet - quiet)
			timer = tm.C
		}
		var err error
		select {
		case <-t.net.changed:
		case <-timer:
		case <-ctx.Done():
			err = waitFailed(ctx, ctx.Err(), fmt.Sprintf("the network did not go idle (%d requests in flight)", inflight))
		}
		if tm != nil {
			tm.Stop()
		}
		if err != nil {
			return err
		}
	}
}

type networkEvent struct {
	RequestID string `json:"requestId"`
	Type      string `json:"type"`
	FrameID   string `json:"frameId"`
	Response  struct {
		Status int `json:"status"`
	} `json:"response"`
}

// onNetworkEvent 返回一个处理 Network 事件的函数,顶层会话与子会话的事件都算。
func onNetworkEvent(f func(w *netWatch, sessionID string, ev networkEvent)) eventHandler {
	return func(t *Tab, sessionID string, params json.RawMessage) {
		var ev networkEvent
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		f(t.net, sessionID, ev)
	}
}

var networkEvents = map[string]eventHandler{
	// EventSource 是永不结束的长连接,算进行中会让 networkidle 永远等不到。
	"Network.requestWillBeSent": onNetworkEvent(func(w *netWatch, sessionID string, ev networkEvent) {
		switch ev.Type {
		case "EventSource":
		case "Document":
			w.started(ev.RequestID, request{sessionID: sessionID, documentFrame: ev.FrameID})
		default:
			w.started(ev.RequestID, request{sessionID: sessionID})
		}
	}),
	"Network.loadingFinished": onNetworkEvent(func(w *netWatch, _ string, ev networkEvent) { w.finished(ev.RequestID) }),
	"Network.loadingFailed":   onNetworkEvent(func(w *netWatch, _ string, ev networkEvent) { w.finished(ev.RequestID) }),
	"Network.responseReceived": onNetworkEvent(func(w *netWatch, sessionID string, ev networkEvent) {
		// 主文档的状态只取顶层会话:子会话里的 frameId 是 iframe 自己的。
		if ev.Type == "Document" && sessionID == "" {
			w.response(ev.FrameID, ev.Response.Status)
		}
	}),
}
