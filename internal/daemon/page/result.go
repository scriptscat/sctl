package page

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"time"
)

// ActionResult 是每个页面动作的结果(spec §动作的结果):实际操作的标签页、动作结束时的页面 URL 与
// 标题(由网页控制,标记为不可信内容)、是否发生了导航,以及动作打开的新标签页。
type ActionResult struct {
	ContentTrust string `json:"contentTrust"`
	TabID        int    `json:"tabId"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Navigated    bool   `json:"navigated"`
	NewTabID     *int   `json:"newTabId,omitempty"`
}

// navigationStartWindow 是点击之后等主文档开始导航的时间;在这之内开始的导航等到 DOMContentLoaded
// 再返回(spec §动作的结果)。
const navigationStartWindow = 500 * time.Millisecond

// newTabWait 限定点击触发 Page.windowOpen 之后等新标签页出现在浏览器标签页列表里的时间。
// 超过它(例如弹窗被拦截)只是结果里不带新标签页,点击本身已经完成。
const newTabWait = 2 * time.Second

// navWatch 记录一个动作期间标签页主文档的导航事件与打开新窗口的事件。事件在 bridge 读循环里写入,
// 动作在标签页队列里读取;同一标签页的动作串行执行,所以同时最多只有一次观察。
type navWatch struct {
	mu      sync.Mutex
	active  bool
	state   navState
	changed chan struct{}
}

// navState 是 begin 之后观察到的事件。mainFrame 是 begin 时的主 frame,导航不改变它。
type navState struct {
	mainFrame    string
	started      bool
	committed    bool
	sameDocument bool
	loaded       bool
	loadFired    bool
	stopped      bool
	windowOpened bool
}

func newNavWatch() *navWatch {
	return &navWatch{changed: make(chan struct{}, 1)}
}

func (w *navWatch) begin(mainFrame string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.active = true
	w.state = navState{mainFrame: mainFrame}
	select {
	case <-w.changed:
	default:
	}
}

func (w *navWatch) end() {
	w.mu.Lock()
	w.active = false
	w.mu.Unlock()
}

func (w *navWatch) snapshot() navState {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.state
}

// note 在观察期间用 f 更新状态并唤醒等待者。
func (w *navWatch) note(f func(s *navState)) {
	w.mu.Lock()
	if !w.active {
		w.mu.Unlock()
		return
	}
	f(&w.state)
	w.mu.Unlock()
	select {
	case w.changed <- struct{}{}:
	default:
	}
}

// navigated 表示动作期间主文档完成了跨文档导航,或在文档内改变了 URL。
func (s navState) navigated() bool { return s.committed || s.sameDocument }

// frameIDEvent 是只用到 frameId 的 Page 事件。
type frameIDEvent struct {
	FrameID string `json:"frameId"`
}

// onMainFrameEvent 返回一个事件处理函数:事件属于标签页主文档(顶层会话上的主 frame)时用 f 更新观察状态。
func onMainFrameEvent(f func(s *navState)) eventHandler {
	return func(t *Tab, sessionID string, params json.RawMessage) {
		if sessionID != "" {
			return
		}
		var ev frameIDEvent
		if json.Unmarshal(params, &ev) != nil {
			return
		}
		t.nav.note(func(s *navState) {
			if ev.FrameID == s.mainFrame {
				f(s)
			}
		})
	}
}

// onFrameRequestedNavigation 只把在当前标签页里进行的导航算作开始导航;在新标签页或新窗口里打开的
// 由 Page.windowOpen 报告。
func onFrameRequestedNavigation(t *Tab, sessionID string, params json.RawMessage) {
	var ev struct {
		FrameID     string `json:"frameId"`
		Disposition string `json:"disposition"`
	}
	if sessionID != "" || json.Unmarshal(params, &ev) != nil || ev.Disposition != "currentTab" {
		return
	}
	t.nav.note(func(s *navState) {
		if ev.FrameID == s.mainFrame {
			s.started = true
		}
	})
}

func onMainFrameNavigated(t *Tab, sessionID string, params json.RawMessage) {
	var ev frameNavigatedEvent
	if sessionID != "" || json.Unmarshal(params, &ev) != nil || ev.Frame.ParentID != "" {
		return
	}
	t.nav.note(func(s *navState) {
		s.started, s.committed = true, true
	})
}

func onDOMContentLoaded(t *Tab, sessionID string, _ json.RawMessage) {
	if sessionID != "" {
		return
	}
	t.nav.note(func(s *navState) {
		// 只算提交之后的新文档:开始观察之前的加载的 DOMContentLoaded 与这次动作无关。
		if s.committed {
			s.loaded = true
		}
	})
}

func onLoadEventFired(t *Tab, sessionID string, _ json.RawMessage) {
	if sessionID != "" {
		return
	}
	t.nav.note(func(s *navState) {
		if s.committed {
			s.loadFired = true
		}
	})
}

// onWindowOpen 记录标签页(含它的 iframe)要打开新窗口;事件来自这个标签页的会话,所以新标签页的
// 打开者就是它。
func onWindowOpen(t *Tab, _ string, _ json.RawMessage) {
	t.nav.note(func(s *navState) { s.windowOpened = true })
}

var navigationEvents = map[string]eventHandler{
	"Page.frameRequestedNavigation": onFrameRequestedNavigation,
	"Page.frameStartedLoading":      onMainFrameEvent(func(s *navState) { s.started = true }),
	"Page.frameStoppedLoading":      onMainFrameEvent(func(s *navState) { s.stopped = true }),
	"Page.navigatedWithinDocument":  onMainFrameEvent(func(s *navState) { s.sameDocument = true }),
	"Page.frameNavigated":           onMainFrameNavigated,
	"Page.domContentEventFired":     onDOMContentLoaded,
	"Page.loadEventFired":           onLoadEventFired,
	"Page.windowOpen":               onWindowOpen,
}

// actionRun 是一次动作的导航观察:在等待目标之前开始,让命中测试与鼠标事件之间不再夹着额外的往返。
type actionRun struct {
	t *Tab
	// opens 表示动作可能打开新标签页;before 是开始时浏览器里已有的标签页,据此找出新开的那个。
	opens  bool
	before []int
}

// beginAction 开始观察主文档导航与新窗口。代替调用方向页面输入或执行脚本的动作(click、press、eval 等)
// 都可能打开新标签页,opens 为 true;只观察页面(wait、screenshot)或替换整个文档(navigate)的为 false,
// 期间页面自己打开的窗口不算动作打开的。调用方必须在动作结束时调用 end。
func beginAction(ctx context.Context, t *Tab, opens bool) (*actionRun, error) {
	var tree struct {
		FrameTree struct {
			Frame struct {
				ID string `json:"id"`
			} `json:"frame"`
		} `json:"frameTree"`
	}
	if err := t.send(ctx, "Page.getFrameTree", nil, &tree); err != nil {
		return nil, err
	}
	run := &actionRun{t: t, opens: opens}
	if opens {
		before, err := t.m.cdp.Tabs(ctx, t.instanceID)
		if err != nil {
			return nil, err
		}
		run.before = before
	}
	t.nav.begin(tree.FrameTree.Frame.ID)
	return run, nil
}

// beginDialogAction 开始观察但不查询主 frame:弹框卡住渲染进程时 Page.getFrameTree 不会回应,而处理完一个
// 弹框后页面可能立刻打开下一个。不知道主 frame 时,只有顶层 frame 的跨文档导航计为导航。
func beginDialogAction(t *Tab) *actionRun {
	t.nav.begin("")
	return &actionRun{t: t}
}

// end 结束观察;之后到达的事件不再记录。
func (r *actionRun) end() { r.t.nav.end() }

// finish 在输入发出之后组装动作结果。waitNavigation 为 true 时(点击)先等 navigationStartWindow:
// 期间主文档开始导航的,等到 DOMContentLoaded(或导航中止)再返回,受动作的超时限制。
func (r *actionRun) finish(ctx context.Context, waitNavigation bool) (ActionResult, error) {
	if waitNavigation {
		if err := r.waitNavigation(ctx); err != nil {
			return ActionResult{}, err
		}
	}
	state := r.t.nav.snapshot()
	res := ActionResult{ContentTrust: contentTrustPage, TabID: r.t.id, Navigated: state.navigated()}
	if state.windowOpened && r.opens {
		newTab, err := r.findNewTab(ctx)
		if err != nil {
			return ActionResult{}, err
		}
		res.NewTabID = newTab
	}
	// Page.getNavigationHistory 由浏览器进程回答,弹框卡住渲染进程时也能返回。
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			URL   string `json:"url"`
			Title string `json:"title"`
		} `json:"entries"`
	}
	if err := r.t.send(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		return ActionResult{}, err
	}
	if history.CurrentIndex >= 0 && history.CurrentIndex < len(history.Entries) {
		entry := history.Entries[history.CurrentIndex]
		res.URL, res.Title = entry.URL, entry.Title
	}
	return res, nil
}

func (r *actionRun) waitNavigation(ctx context.Context) error {
	window := time.NewTimer(navigationStartWindow)
	defer window.Stop()
	windowOver := false
	for {
		s := r.t.nav.snapshot()
		switch {
		case s.committed && (s.loaded || s.stopped):
			return nil
		case !s.committed && s.sameDocument:
			// 锚点跳转与 pushState 不替换文档,也没有 DOMContentLoaded。
			return nil
		case s.started && !s.committed && s.stopped:
			// 导航开始后又中止(下载、204 响应、被取消):文档没有被替换。
			return nil
		case !s.started && windowOver:
			return nil
		}
		select {
		case <-r.t.nav.changed:
		case <-window.C:
			windowOver = true
		case <-ctx.Done():
			return waitFailed(ctx, ctx.Err(), "the click started a navigation that did not reach DOMContentLoaded")
		}
	}
}

// findNewTab 找出动作期间新出现的标签页。Page.windowOpen 在新窗口创建之前就发出,所以轮询到它出现为止;
// newTabWait 内没有出现(例如被弹窗拦截)时返回 nil。
func (r *actionRun) findNewTab(ctx context.Context) (*int, error) {
	wait, cancel := context.WithTimeout(ctx, newTabWait)
	defer cancel()
	var poll backoff
	for {
		tabs, err := r.t.m.cdp.Tabs(ctx, r.t.instanceID)
		if err != nil {
			return nil, err
		}
		for _, id := range tabs {
			if id != r.t.id && !slices.Contains(r.before, id) {
				return &id, nil
			}
		}
		if err := poll.sleep(wait); err != nil {
			if ctx.Err() != nil {
				return nil, err
			}
			return nil, nil
		}
	}
}
