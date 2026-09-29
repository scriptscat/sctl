package page

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

// keySpec 是一次按键的 CDP 参数:key/code/windowsVirtualKeyCode 与产生的文本。
type keySpec struct {
	key  string
	code string
	vk   int
	// text 是按下时插入的字符;没有文本的键(方向键、带 Control 的组合)为空。
	text string
}

// namedKeys 是按名称按的键,写法与 Playwright 一致(区分大小写)。
var namedKeys = func() map[string]keySpec {
	keys := map[string]keySpec{
		"Enter":      {key: "Enter", code: "Enter", vk: 13, text: "\r"},
		"Tab":        {key: "Tab", code: "Tab", vk: 9},
		"Backspace":  {key: "Backspace", code: "Backspace", vk: 8},
		"Delete":     {key: "Delete", code: "Delete", vk: 46},
		"Escape":     {key: "Escape", code: "Escape", vk: 27},
		"Insert":     {key: "Insert", code: "Insert", vk: 45},
		"Home":       {key: "Home", code: "Home", vk: 36},
		"End":        {key: "End", code: "End", vk: 35},
		"PageUp":     {key: "PageUp", code: "PageUp", vk: 33},
		"PageDown":   {key: "PageDown", code: "PageDown", vk: 34},
		"ArrowLeft":  {key: "ArrowLeft", code: "ArrowLeft", vk: 37},
		"ArrowUp":    {key: "ArrowUp", code: "ArrowUp", vk: 38},
		"ArrowRight": {key: "ArrowRight", code: "ArrowRight", vk: 39},
		"ArrowDown":  {key: "ArrowDown", code: "ArrowDown", vk: 40},
		"Space":      {key: " ", code: "Space", vk: 32, text: " "},
	}
	for n := 1; n <= 12; n++ {
		name := fmt.Sprintf("F%d", n)
		keys[name] = keySpec{key: name, code: name, vk: 111 + n}
	}
	return keys
}()

// modifierKeys 是修饰键自身作为按键时的参数。
var modifierKeys = map[string]keySpec{
	"Alt":     {key: "Alt", code: "AltLeft", vk: 18},
	"Control": {key: "Control", code: "ControlLeft", vk: 17},
	"Meta":    {key: "Meta", code: "MetaLeft", vk: 91},
	"Shift":   {key: "Shift", code: "ShiftLeft", vk: 16},
}

// charKey 是美式键盘上产生某个字符的物理键。
type charKey struct {
	code string
	vk   int
	// shifted 表示这个字符要按住 Shift 才能打出。
	shifted bool
}

// shiftedPairs 是非字母键上未按 Shift 与按住 Shift 的字符。
var shiftedPairs = []struct {
	base, shifted rune
	code          string
	vk            int
}{
	{'`', '~', "Backquote", 192}, {'-', '_', "Minus", 189}, {'=', '+', "Equal", 187},
	{'[', '{', "BracketLeft", 219}, {']', '}', "BracketRight", 221}, {'\\', '|', "Backslash", 220},
	{';', ':', "Semicolon", 186}, {'\'', '"', "Quote", 222}, {',', '<', "Comma", 188},
	{'.', '>', "Period", 190}, {'/', '?', "Slash", 191},
}

// usLayout 与 shiftOf 描述美式键盘的可打印 ASCII 字符:字符到物理键,以及字符按住 Shift 后的字符。
var usLayout, shiftOf = func() (map[rune]charKey, map[rune]rune) {
	layout := map[rune]charKey{' ': {code: "Space", vk: 32}}
	shift := map[rune]rune{}
	for c := 'a'; c <= 'z'; c++ {
		code, vk := "Key"+strings.ToUpper(string(c)), int(c-'a')+65
		layout[c] = charKey{code: code, vk: vk}
		layout[c-'a'+'A'] = charKey{code: code, vk: vk, shifted: true}
		shift[c] = c - 'a' + 'A'
	}
	const digitShifts = ")!@#$%^&*("
	for d := 0; d < 10; d++ {
		code, vk := fmt.Sprintf("Digit%d", d), 48+d
		layout[rune('0'+d)] = charKey{code: code, vk: vk}
		layout[rune(digitShifts[d])] = charKey{code: code, vk: vk, shifted: true}
		shift[rune('0'+d)] = rune(digitShifts[d])
	}
	for _, p := range shiftedPairs {
		layout[p.base] = charKey{code: p.code, vk: p.vk}
		layout[p.shifted] = charKey{code: p.code, vk: p.vk, shifted: true}
		shift[p.base] = p.shifted
	}
	return layout, shift
}()

// charSpec 返回按字符 r 的参数;r 不在美式键盘上时 ok 为 false。holdShift 表示按键时按着 Shift,
// 此时字符取它的 Shift 形态(Shift+a 产生 "A")。
func charSpec(r rune, holdShift bool) (keySpec, bool) {
	if holdShift {
		if s, ok := shiftOf[r]; ok {
			r = s
		}
	}
	ck, ok := usLayout[r]
	if !ok {
		return keySpec{}, false
	}
	return keySpec{key: string(r), code: ck.code, vk: ck.vk, text: string(r)}, true
}

// combo 是解析后的按键组合:依次按下的修饰键与最后的主键。只按修饰键(如 Shift)时没有主键。
type combo struct {
	modifiers []string
	key       *keySpec
}

// parseCombo 解析 Playwright 写法的按键:`Enter`、`a`、`Control+A`、`Shift+Tab`、`Control++`。
// 修饰键是 Alt、Control、Meta、Shift;主键是命名键(区分大小写)、单个字符,或修饰键自身。
func parseCombo(input string) (combo, error) {
	var names []string
	key := input
	switch {
	case input == "+":
	case strings.HasSuffix(input, "++"):
		names = strings.Split(strings.TrimSuffix(input, "++"), "+")
		key = "+"
	case strings.Contains(input, "+"):
		names = strings.Split(input, "+")
		key = names[len(names)-1]
		names = names[:len(names)-1]
	}
	modifierOnly := false
	if _, ok := modifierBits[key]; ok {
		names, key, modifierOnly = append(names, key), "", true
	}
	var c combo
	seen := map[string]bool{}
	for _, name := range names {
		if _, ok := modifierBits[name]; !ok {
			return combo{}, invalidRequest(fmt.Sprintf("invalid key %q: %q is not a modifier; use Alt, Control, Meta or Shift before the last +", input, name))
		}
		if seen[name] {
			return combo{}, invalidRequest(fmt.Sprintf("invalid key %q: modifier %s is given twice", input, name))
		}
		seen[name] = true
		c.modifiers = append(c.modifiers, name)
	}
	if modifierOnly {
		return c, nil
	}
	spec, err := resolveKey(input, key, seen["Shift"])
	if err != nil {
		return combo{}, err
	}
	if seen["Control"] || seen["Alt"] || seen["Meta"] {
		// 带 Control、Alt、Meta 的组合是快捷键,不是输入文字。
		spec.text = ""
	}
	c.key = &spec
	return c, nil
}

func resolveKey(input, key string, holdShift bool) (keySpec, error) {
	if spec, ok := namedKeys[key]; ok {
		return spec, nil
	}
	if r, size := utf8.DecodeRuneInString(key); key != "" && size == len(key) {
		if spec, ok := charSpec(r, holdShift); ok {
			return spec, nil
		}
	}
	return keySpec{}, invalidRequest(fmt.Sprintf("invalid key %q: unknown key %q; use a name such as Enter, Tab, Escape, ArrowDown, F5, or one character, optionally with Alt, Control, Meta, Shift and + before it (Control+A)", input, key))
}

func (k keySpec) params(kind string, modifiers int) map[string]any {
	p := map[string]any{
		"type": kind, "modifiers": modifiers, "key": k.key, "code": k.code,
		"windowsVirtualKeyCode": k.vk, "nativeVirtualKeyCode": k.vk,
	}
	if kind == "keyDown" {
		if k.text == "" {
			p["type"] = "rawKeyDown"
		} else {
			p["text"], p["unmodifiedText"] = k.text, k.text
		}
	}
	return p
}

// pressCombo 按下修饰键与主键,再按相反顺序松开,全部是可信的键盘事件。修饰键按下时事件的
// modifiers 已含它自己,松开时不含。
func pressCombo(ctx context.Context, t *Tab, c combo) error {
	send := func(k keySpec, kind string, mods int) error {
		return t.send(ctx, "Input.dispatchKeyEvent", k.params(kind, mods), nil)
	}
	mods := 0
	for _, name := range c.modifiers {
		mods |= modifierBits[name]
		if err := send(modifierKeys[name], "keyDown", mods); err != nil {
			return err
		}
	}
	if c.key != nil {
		if err := send(*c.key, "keyDown", mods); err != nil {
			return err
		}
		if err := send(*c.key, "keyUp", mods); err != nil {
			return err
		}
	}
	for i := len(c.modifiers) - 1; i >= 0; i-- {
		mods &^= modifierBits[c.modifiers[i]]
		if err := send(modifierKeys[c.modifiers[i]], "keyUp", mods); err != nil {
			return err
		}
	}
	return nil
}

func keyRef(name string) *keySpec {
	spec := namedKeys[name]
	return &spec
}

type typeInput struct {
	Text string `json:"text"`
}

type pressInput struct {
	Key string `json:"key"`
}

// runType 在当前焦点元素上逐字输入:美式键盘上有的字符是完整的按键事件,换行是 Enter,其余字符
// (中文等)用 Input.insertText 直接插入。
func runType(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in typeInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	if in.Text == "" {
		return nil, invalidRequest("page type needs the text to type")
	}
	run, err := beginAction(ctx, t, true)
	if err != nil {
		return nil, err
	}
	defer run.end()
	enter, tab := combo{key: keyRef("Enter")}, combo{key: keyRef("Tab")}
	runes := []rune(in.Text)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		switch r {
		case '\r', '\n':
			if r == '\r' && i+1 < len(runes) && runes[i+1] == '\n' {
				i++
			}
			err = pressCombo(ctx, t, enter)
		case '\t':
			err = pressCombo(ctx, t, tab)
		default:
			if spec, ok := charSpec(r, false); ok {
				err = pressCombo(ctx, t, combo{key: &spec})
			} else {
				err = t.send(ctx, "Input.insertText", map[string]string{"text": string(r)}, nil)
			}
		}
		if err != nil {
			return nil, err
		}
	}
	return run.finish(ctx, false)
}

func runPress(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
	var in pressInput
	if err := decodeInput(input, &in); err != nil {
		return nil, err
	}
	c, err := parseCombo(in.Key)
	if err != nil {
		return nil, err
	}
	run, err := beginAction(ctx, t, true)
	if err != nil {
		return nil, err
	}
	defer run.end()
	if err := pressCombo(ctx, t, c); err != nil {
		return nil, err
	}
	return run.finish(ctx, false)
}
