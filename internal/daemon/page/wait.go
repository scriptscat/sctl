package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// waitInput 是 page wait 的输入,恰好给一个条件。Gone 是文本,选择器写在 SelectorGone:
// 两者都是 spec 里「--gone」的一种,分开写才不必猜一个字符串是文本还是选择器。
type waitInput struct {
	Text         string `json:"text"`
	Gone         string `json:"gone"`
	Selector     string `json:"selector"`
	SelectorGone string `json:"selectorGone"`
	URL          string `json:"url"`
	Load         string `json:"load"`
}

func (in waitInput) validate() error {
	given := 0
	for _, v := range []string{in.Text, in.Gone, in.Selector, in.SelectorGone, in.URL, in.Load} {
		if v != "" {
			given++
		}
	}
	if given != 1 {
		return invalidRequest("page wait takes exactly one condition: text, gone, selector, selectorGone, url or load")
	}
	if in.Load != "" && !slices.Contains(loadStates, in.Load) {
		return invalidRequest(fmt.Sprintf("unknown load state %q: use load, domcontentloaded or networkidle", in.Load))
	}
	return nil
}

// 条件在页面里求值。文本比较前折叠空白:innerText 在块之间放换行,调用方写的文本通常是一行。
// 可见文本取 innerText,display:none 与 visibility:hidden 的内容不在其中。
const (
	visibleTextScript = `((text) => {
  const norm = (s) => s.replace(/\s+/g, " ").trim();
  return { met: norm(document.body ? document.body.innerText : "").includes(norm(text)) };
})(%s)`
	visibleSelectorScript = `((selector) => {
  let elements;
  try { elements = document.querySelectorAll(selector); } catch (e) { return { invalid: String(e.message) }; }
  const visible = [...elements].some((el) => {
    const r = el.getBoundingClientRect();
    return r.width > 0 && r.height > 0 && el.checkVisibility({ visibilityProperty: true });
  });
  return { met: visible };
})(%s)`
	readyStateScript = `({ state: document.readyState })`
)

// evalPage 在标签页顶层会话的主世界里求值一个只读表达式,按值解出结果。
func (t *Tab) evalPage(ctx context.Context, expression string, result any) error {
	var res struct {
		Result           remoteObject      `json:"result"`
		ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
	}
	err := t.send(ctx, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, &res)
	if err != nil {
		return err
	}
	if res.ExceptionDetails != nil {
		return fmt.Errorf("evaluate the wait condition in the page: %s", exceptionMessage(res.ExceptionDetails))
	}
	return json.Unmarshal(res.Result.Value, result)
}

// condition 检查一次:met 为 true 表示成立,否则 reason 写明还在等什么。
type condition func(ctx context.Context, t *Tab) (met bool, reason string, err error)

func scriptCondition(script, arg string, want bool, reason string) condition {
	quoted, err := json.Marshal(arg)
	if err != nil {
		panic(fmt.Sprintf("encode wait argument: %v", err))
	}
	expression := fmt.Sprintf(script, quoted)
	return func(ctx context.Context, t *Tab) (bool, string, error) {
		var res struct {
			Met     bool   `json:"met"`
			Invalid string `json:"invalid"`
		}
		if err := t.evalPage(ctx, expression, &res); err != nil {
			if isCDPError(err) {
				// 文档正在被替换,执行上下文暂时不存在:下一轮再查。
				return false, reason, nil
			}
			return false, reason, err
		}
		if res.Invalid != "" {
			return false, reason, invalidRequest(fmt.Sprintf("invalid selector %q: %s", arg, res.Invalid))
		}
		return res.Met == want, reason, nil
	}
}

func urlCondition(substring string) condition {
	return func(ctx context.Context, t *Tab) (bool, string, error) {
		var history struct {
			CurrentIndex int `json:"currentIndex"`
			Entries      []struct {
				URL string `json:"url"`
			} `json:"entries"`
		}
		if err := t.send(ctx, "Page.getNavigationHistory", nil, &history); err != nil {
			return false, "", err
		}
		current := ""
		if history.CurrentIndex >= 0 && history.CurrentIndex < len(history.Entries) {
			current = history.Entries[history.CurrentIndex].URL
		}
		return strings.Contains(current, substring),
			fmt.Sprintf("waiting for the URL to contain %q (currently %q)", substring, current), nil
	}
}

func loadCondition(state string) condition {
	reason := "waiting for the page to reach " + state
	return func(ctx context.Context, t *Tab) (bool, string, error) {
		var res struct {
			State string `json:"state"`
		}
		if err := t.evalPage(ctx, readyStateScript, &res); err != nil {
			if isCDPError(err) {
				return false, reason, nil
			}
			return false, reason, err
		}
		switch {
		case state == loadStateDOMContentLoaded:
			return res.State != "loading", reason, nil
		case res.State != "complete":
			return false, reason, nil
		case state == loadStateNetworkIdle:
			if err := t.watchFrameNetwork(ctx); err != nil {
				return false, reason, err
			}
			return t.net.quietFor(networkIdleQuiet), reason + " (requests are still in flight or finished less than 500ms ago)", nil
		}
		return true, reason, nil
	}
}

func (in waitInput) condition() condition {
	switch {
	case in.Text != "":
		return scriptCondition(visibleTextScript, in.Text, true, fmt.Sprintf("waiting for text %q to be visible", in.Text))
	case in.Gone != "":
		return scriptCondition(visibleTextScript, in.Gone, false, fmt.Sprintf("waiting for text %q to disappear", in.Gone))
	case in.Selector != "":
		return scriptCondition(visibleSelectorScript, in.Selector, true, fmt.Sprintf("waiting for selector %q to match a visible element", in.Selector))
	case in.SelectorGone != "":
		return scriptCondition(visibleSelectorScript, in.SelectorGone, false, fmt.Sprintf("waiting for selector %q to stop matching a visible element", in.SelectorGone))
	case in.URL != "":
		return urlCondition(in.URL)
	}
	return loadCondition(in.Load)
}

func runWait(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in waitInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	if err := in.validate(); err != nil {
		return nil, err
	}
	if in.Load == loadStateNetworkIdle {
		if err := t.watchNetwork(ctx); err != nil {
			return nil, err
		}
	}
	run, err := beginAction(ctx, t, false)
	if err != nil {
		return nil, err
	}
	defer run.end()
	check := in.condition()
	var poll backoff
	for {
		met, reason, err := check(ctx, t)
		if err != nil {
			return nil, waitFailed(ctx, err, reason)
		}
		if met {
			return run.finish(ctx, false)
		}
		if err := poll.sleep(ctx); err != nil {
			return nil, waitFailed(ctx, err, reason)
		}
	}
}
