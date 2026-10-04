package page

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// mouseButtons 是 CDP Input.dispatchMouseEvent 的按键名与按下时 buttons 位掩码。
var mouseButtons = map[string]int{"left": 1, "right": 2, "middle": 4}

// modifierBits 是 CDP 输入事件的修饰键位掩码。
var modifierBits = map[string]int{"Alt": 1, "Control": 2, "Meta": 4, "Shift": 8}

// maxClickCount 限制 --count:浏览器只区分单击、双击、三击,更大的数没有意义,只会拖长动作。
const maxClickCount = 10

// mouseInput 是鼠标动作共用的修饰参数。
type mouseInput struct {
	Button    string   `json:"button"`
	Count     *int     `json:"count"`
	Modifiers []string `json:"modifiers"`
}

// mouse 是校验过的鼠标参数。
type mouse struct {
	button    string
	buttons   int
	count     int
	modifiers int
}

func (in mouseInput) parse() (mouse, error) {
	m := mouse{button: "left", count: 1}
	if in.Button != "" {
		m.button = in.Button
	}
	bit, ok := mouseButtons[m.button]
	if !ok {
		return mouse{}, invalidRequest(fmt.Sprintf("unknown mouse button %q: use left, right or middle", in.Button))
	}
	m.buttons = bit
	if in.Count != nil {
		if *in.Count < 1 || *in.Count > maxClickCount {
			return mouse{}, invalidRequest(fmt.Sprintf("click count must be between 1 and %d, not %d", maxClickCount, *in.Count))
		}
		m.count = *in.Count
	}
	seen := map[string]bool{}
	for _, name := range in.Modifiers {
		bit, ok := modifierBits[name]
		if !ok {
			return mouse{}, invalidRequest(fmt.Sprintf("unknown modifier %q: use Alt, Control, Meta or Shift", name))
		}
		if seen[name] {
			return mouse{}, invalidRequest(fmt.Sprintf("modifier %s is given twice", name))
		}
		seen[name] = true
		m.modifiers |= bit
	}
	return m, nil
}

type clickInput struct {
	TargetSpec
	mouseInput
}

type hoverInput struct {
	TargetSpec
}

// decodeActionInput 解码嵌入了 TargetSpec 的动作输入并校验目标。
func decodeActionInput(action string, raw json.RawMessage, in any, spec *TargetSpec) error {
	if err := decodeInput(raw, in); err != nil {
		return err
	}
	return spec.validate(action)
}

func runClick(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in clickInput
	if err := decodeActionInput("click", input, &in, &in.TargetSpec); err != nil {
		return nil, err
	}
	m, err := in.parse()
	if err != nil {
		return nil, err
	}
	run, err := beginAction(ctx, t, true)
	if err != nil {
		return nil, err
	}
	defer run.end()
	point, err := actionTarget(ctx, t, in.TargetSpec, clickChecks)
	if err != nil {
		return nil, err
	}
	if err := click(ctx, t, point, m); err != nil {
		return nil, err
	}
	return run.finish(ctx, true)
}

func runHover(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in hoverInput
	if err := decodeActionInput("hover", input, &in, &in.TargetSpec); err != nil {
		return nil, err
	}
	run, err := beginAction(ctx, t, true)
	if err != nil {
		return nil, err
	}
	defer run.end()
	point, err := actionTarget(ctx, t, in.TargetSpec, hoverChecks)
	if err != nil {
		return nil, err
	}
	if err := moveMouse(ctx, t, point, 0); err != nil {
		return nil, err
	}
	return run.finish(ctx, false)
}

// actionTarget 解析目标并等它满足 c,返回鼠标事件的目标点。选择器匹配到的元素在等待期间离开文档
// (例如被框架重新渲染)时重新查询;引用指向的元素离开文档则引用失效。
func actionTarget(ctx context.Context, t *Tab, spec TargetSpec, c checks) (actionPoint, error) {
	_, point, err := actionElement(ctx, t, spec, c, nil)
	return point, err
}

// actionElement 是 actionTarget 的完整形态:同时返回解析出的元素,并在等待条件之前让 pre 检查元素
// (例如元素类型不对时立即以 INVALID_REQUEST 失败,而不是等到超时)。pre 返回 errDetached 与元素在
// 等待期间离开文档同样处理。
func actionElement(ctx context.Context, t *Tab, spec TargetSpec, c checks, pre func(element) error) (element, actionPoint, error) {
	var wait backoff
	for {
		el, err := resolveTarget(ctx, t, spec)
		if err != nil {
			return element{}, actionPoint{}, err
		}
		var point actionPoint
		if pre != nil {
			err = pre(el)
		}
		if err == nil {
			point, err = waitActionable(ctx, t, el, c)
		}
		if !errors.Is(err, errDetached) {
			return el, point, err
		}
		if spec.Ref != "" {
			return element{}, actionPoint{}, staleRef(spec.Ref, t.id)
		}
		if err := wait.sleep(ctx); err != nil {
			return element{}, actionPoint{}, waitFailed(ctx, err, "the element matching the selector keeps leaving the document")
		}
	}
}

// dispatchMouse 发出一个可信的鼠标事件(spec 设计决策 9)。
func dispatchMouse(ctx context.Context, t *Tab, p actionPoint, params map[string]any) error {
	params["x"], params["y"] = p.x, p.y
	return t.sendTo(ctx, p.sessionID, "Input.dispatchMouseEvent", params, nil)
}

func moveMouse(ctx context.Context, t *Tab, p actionPoint, modifiers int) error {
	return dispatchMouse(ctx, t, p, map[string]any{"type": "mouseMoved", "modifiers": modifiers})
}

// click 移到目标点后按下、松开 count 次,clickCount 逐次递增,页面据此产生 dblclick 等事件。
func click(ctx context.Context, t *Tab, p actionPoint, m mouse) error {
	if err := moveMouse(ctx, t, p, m.modifiers); err != nil {
		return err
	}
	for n := 1; n <= m.count; n++ {
		for _, phase := range []struct {
			kind    string
			buttons int
		}{{"mousePressed", m.buttons}, {"mouseReleased", 0}} {
			err := dispatchMouse(ctx, t, p, map[string]any{
				"type": phase.kind, "button": m.button, "buttons": phase.buttons, "clickCount": n, "modifiers": m.modifiers,
			})
			if err != nil {
				return err
			}
		}
	}
	return nil
}
