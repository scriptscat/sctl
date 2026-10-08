package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// 加载状态(spec 动作列表):load 是默认值。
const (
	loadStateLoad             = "load"
	loadStateDOMContentLoaded = "domcontentloaded"
	loadStateNetworkIdle      = "networkidle"
)

var loadStates = []string{loadStateLoad, loadStateDOMContentLoaded, loadStateNetworkIdle}

type navigateInput struct {
	Action string `json:"action"`
	URL    string `json:"url"`
	Wait   string `json:"wait"`
}

// navigateResult 是导航的结果:动作结果加主文档的 HTTP 状态。HTTP 错误状态不是失败,状态码照常报告;
// 没有网络响应(例如从缓存恢复)时不带 httpStatus。
type navigateResult struct {
	ActionResult
	HTTPStatus *int `json:"httpStatus,omitempty"`
}

func (in navigateInput) validate() error {
	switch in.Action {
	case "goto":
		if in.URL == "" {
			return invalidRequest("page goto needs a url")
		}
	case "back", "forward", "reload":
		if in.URL != "" {
			return invalidRequest(fmt.Sprintf("page %s takes no url", in.Action))
		}
	default:
		return invalidRequest(fmt.Sprintf("unknown navigation %q: use goto, back, forward or reload", in.Action))
	}
	if in.Wait != "" && !slices.Contains(loadStates, in.Wait) {
		return invalidRequest(fmt.Sprintf("unknown wait state %q: use load, domcontentloaded or networkidle", in.Wait))
	}
	return nil
}

func runNavigate(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in navigateInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	state := in.Wait
	if state == "" {
		state = loadStateLoad
	}
	t.watchNetwork()
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	t.net.reset(t.nav.snapshot().mainFrame)
	settled, err := startNavigation(ctx, t, in)
	if err != nil {
		return nil, err
	}
	if !settled {
		if err := waitLoadState(ctx, t, state); err != nil {
			return nil, err
		}
	}
	res, err := run.finish(ctx, false)
	if err != nil {
		return nil, err
	}
	return navigateResult{ActionResult: res, HTTPStatus: t.net.documentStatus()}, nil
}

// recoverByNavigating 让留着孤儿弹框、附加没有回应的页面重新回应(spec §JS 弹框):真机探针显示这时新会话上的
// 附加命令全部阻塞,而作为第一条命令发出的 Page.navigate 与 Page.reload 照常回应并关掉弹框。之后动作照常重新附加
// 并导航,所以页面会被加载两次。back 与 forward 要先读导航历史,不能这样恢复。
func recoverByNavigating(ctx context.Context, t *Tab, input json.RawMessage) (bool, error) {
	var in navigateInput
	if err := decodeInput(input, &in); err != nil {
		return false, err
	}
	if err := in.validate(); err != nil {
		return false, err
	}
	switch in.Action {
	case "goto":
		return true, t.send(ctx, "Page.navigate", map[string]string{"url": in.URL}, nil)
	case "reload":
		return true, t.send(ctx, "Page.reload", nil, nil)
	}
	return false, nil
}

// errAborted 是 Chrome 对不替换文档的导航(204 响应、下载)给出的错误文本;它不是网络错误。
const errAborted = "net::ERR_ABORTED"

// startNavigation 发出导航命令。settled 为 true 表示导航已经结束且不会替换文档,没有加载事件可等。
func startNavigation(ctx context.Context, t *Tab, in navigateInput) (settled bool, err error) {
	switch in.Action {
	case "goto":
		var res struct {
			ErrorText string `json:"errorText"`
		}
		if err := t.send(ctx, "Page.navigate", map[string]string{"url": in.URL}, &res); err != nil {
			return false, err
		}
		if res.ErrorText == errAborted {
			return true, nil
		}
		if res.ErrorText != "" {
			return false, &Error{Code: generated.ErrorCodeNavigationFailed, Message: fmt.Sprintf("navigating to %s failed: %s", in.URL, res.ErrorText)}
		}
		return false, nil
	case "reload":
		return false, t.send(ctx, "Page.reload", nil, nil)
	}
	var history struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID int `json:"id"`
		} `json:"entries"`
	}
	if err := t.send(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
		return false, err
	}
	target := history.CurrentIndex - 1
	if in.Action == "forward" {
		target = history.CurrentIndex + 1
	}
	if target < 0 || target >= len(history.Entries) {
		return false, &Error{Code: generated.ErrorCodeNotFound, Message: fmt.Sprintf("the tab has no history entry to go %s to", in.Action)}
	}
	return false, t.send(ctx, "Page.navigateToHistoryEntry", map[string]int{"entryId": history.Entries[target].ID}, nil)
}

// abortGrace 是导航「开始后又停止」却还没看到提交时多等的时间:从 bfcache 恢复的历史记录先报停止再报提交,
// 而下载与 204 响应始终没有提交。
const abortGrace = 250 * time.Millisecond

// waitLoadState 等这次导航到达 state。frameStoppedLoading 表示加载已经结束,从 bfcache 恢复的文档没有
// load 事件,只有它。文档内导航(锚点、pushState)与中止的导航(下载、204)不会提交新文档,观察到它们就算完成。
func waitLoadState(ctx context.Context, t *Tab, state string) error {
	reason := "the page did not reach " + state
	var grace <-chan time.Time
	for {
		s := t.nav.snapshot()
		reached := s.loadFired
		if state == loadStateDOMContentLoaded {
			reached = s.loaded
		}
		switch {
		case s.committed && (reached || s.stopped):
			if state == loadStateNetworkIdle {
				return t.waitNetworkIdle(ctx)
			}
			return nil
		case !s.committed && s.sameDocument:
			return nil
		case s.started && !s.committed && s.stopped && grace == nil:
			timer := time.NewTimer(abortGrace)
			defer timer.Stop()
			grace = timer.C
		}
		select {
		case <-t.nav.changed:
		case <-grace:
			s = t.nav.snapshot()
			if !s.committed {
				return nil
			}
		case <-ctx.Done():
			return waitFailed(ctx, ctx.Err(), reason)
		}
	}
}
