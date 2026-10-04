package page

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// TargetSpec 是动作的目标元素:快照引用(给 AI 用)或 CSS 选择器(给脚本用),恰好给一个(spec 设计决策 7)。
// 字段名与动作输入的 JSON 一致,动作把它嵌进自己的输入结构。
type TargetSpec struct {
	Ref      string `json:"ref"`
	Selector string `json:"selector"`
}

// validate 在边界上检查目标恰好给了一个、引用形如 e5。
func (s TargetSpec) validate(action string) error {
	switch {
	case s.Ref == "" && strings.TrimSpace(s.Selector) == "":
		return invalidRequest(fmt.Sprintf("page %s needs a target: a snapshot ref such as e5, or a selector", action))
	case s.Ref != "" && s.Selector != "":
		return invalidRequest(fmt.Sprintf("page %s takes a ref or a selector, not both", action))
	case s.Ref != "" && !refPattern.MatchString(s.Ref):
		return invalidRequest(fmt.Sprintf("page %s takes a snapshot ref such as e5, not %q; give a CSS selector with --selector", action, s.Ref))
	}
	return nil
}

// selectorMatch 是一次严格选择器查询的结果:count 为匹配数量,恰好 1 个时 el 有效。
type selectorMatch struct {
	count int
	el    element
}

// querySelector 在主文档里按 CSS 选择器查询一次,不穿透 iframe。选择器非法返回 INVALID_REQUEST。
func (t *Tab) querySelector(ctx context.Context, selector string) (selectorMatch, error) {
	var doc struct {
		Root struct {
			NodeID int `json:"nodeId"`
		} `json:"root"`
	}
	if err := t.send(ctx, "DOM.getDocument", map[string]int{"depth": 0}, &doc); err != nil {
		return selectorMatch{}, err
	}
	var matched struct {
		NodeIDs []int `json:"nodeIds"`
	}
	err := t.send(ctx, "DOM.querySelectorAll", map[string]any{"nodeId": doc.Root.NodeID, "selector": selector}, &matched)
	if err != nil {
		var pe *Error
		if isCDPError(err) && errors.As(err, &pe) {
			return selectorMatch{}, invalidRequest(fmt.Sprintf("invalid selector %q: %s", selector, pe.Message))
		}
		return selectorMatch{}, err
	}
	if len(matched.NodeIDs) != 1 {
		return selectorMatch{count: len(matched.NodeIDs)}, nil
	}
	var described struct {
		Node struct {
			BackendNodeID int `json:"backendNodeId"`
		} `json:"node"`
	}
	if err := t.send(ctx, "DOM.describeNode", map[string]int{"nodeId": matched.NodeIDs[0]}, &described); err != nil {
		return selectorMatch{}, err
	}
	return selectorMatch{count: 1, el: element{backendNodeID: described.Node.BackendNodeID}}, nil
}

func ambiguousSelector(selector string, count int) *Error {
	return &Error{
		Code:    generated.ErrorCodeTargetAmbiguous,
		Message: fmt.Sprintf("selector %q matches %d elements; it must match exactly one", selector, count),
	}
}

func noMatch(selector string) string {
	return fmt.Sprintf("no element matches selector %q", selector)
}

// resolveTarget 把动作目标解析为页面元素。引用按引用表解析(可以指向跨进程 iframe 里的元素),失效时
// STALE_REF;选择器在主文档里严格匹配:多于一个立即 TARGET_AMBIGUOUS,一个也没有时一直等到出现,
// 超时的 TIMEOUT 写明没有匹配。
func resolveTarget(ctx context.Context, t *Tab, spec TargetSpec) (element, error) {
	if spec.Ref != "" {
		return t.resolveRef(ctx, spec.Ref)
	}
	var wait backoff
	for {
		match, err := t.querySelector(ctx, spec.Selector)
		if err != nil {
			return element{}, waitFailed(ctx, err, noMatch(spec.Selector))
		}
		switch match.count {
		case 1:
			return match.el, nil
		case 0:
			if err := wait.sleep(ctx); err != nil {
				return element{}, waitFailed(ctx, err, noMatch(spec.Selector))
			}
		default:
			return element{}, ambiguousSelector(spec.Selector, match.count)
		}
	}
}
