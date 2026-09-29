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

// netWatch 跟踪标签页顶层会话上进行中的网络请求与主文档的 HTTP 状态。事件在 bridge 读循环里写入,
// 动作在标签页队列里读取,所以用自己的锁。
type netWatch struct {
	mu       sync.Mutex
	inflight map[string]struct{}
	// idleSince 是进行中的请求数最近一次变成 0 的时刻。
	idleSince time.Time
	mainFrame string
	status    int
	changed   chan struct{}
}

func newNetWatch() *netWatch {
	return &netWatch{inflight: map[string]struct{}{}, idleSince: time.Now(), changed: make(chan struct{}, 1)}
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

func (w *netWatch) started(id string) {
	w.mu.Lock()
	w.inflight[id] = struct{}{}
	w.mu.Unlock()
	w.signal()
}

func (w *netWatch) finished(id string) {
	w.mu.Lock()
	if _, ok := w.inflight[id]; ok {
		delete(w.inflight, id)
		if len(w.inflight) == 0 {
			w.idleSince = time.Now()
		}
	}
	w.mu.Unlock()
	w.signal()
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

// watchNetwork 开启 Network 域,之后的请求才会被跟踪。只在需要网络状态的动作里开启,普通动作不付这个代价。
func (t *Tab) watchNetwork(ctx context.Context) error {
	if t.watchingNetwork {
		return nil
	}
	if err := t.send(ctx, "Network.enable", nil, nil); err != nil {
		return err
	}
	t.net.reset("")
	t.watchingNetwork = true
	return nil
}

// waitNetworkIdle 等到至少 networkIdleQuiet 内没有进行中的请求。
func (t *Tab) waitNetworkIdle(ctx context.Context) error {
	for {
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

// onNetworkEvent 返回一个处理顶层会话上 Network 事件的函数;跨进程 iframe 的子会话不开 Network 域。
func onNetworkEvent(f func(w *netWatch, ev networkEvent)) eventHandler {
	return func(t *Tab, sessionID string, params json.RawMessage) {
		var ev networkEvent
		if sessionID != "" || json.Unmarshal(params, &ev) != nil {
			return
		}
		f(t.net, ev)
	}
}

var networkEvents = map[string]eventHandler{
	// EventSource 是永不结束的长连接,算进行中会让 networkidle 永远等不到。
	"Network.requestWillBeSent": onNetworkEvent(func(w *netWatch, ev networkEvent) {
		if ev.Type != "EventSource" {
			w.started(ev.RequestID)
		}
	}),
	"Network.loadingFinished": onNetworkEvent(func(w *netWatch, ev networkEvent) { w.finished(ev.RequestID) }),
	"Network.loadingFailed":   onNetworkEvent(func(w *netWatch, ev networkEvent) { w.finished(ev.RequestID) }),
	"Network.responseReceived": onNetworkEvent(func(w *netWatch, ev networkEvent) {
		if ev.Type == "Document" {
			w.response(ev.FrameID, ev.Response.Status)
		}
	}),
}
