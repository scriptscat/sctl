package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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

// modifierKeys 是修饰键自身作为按键时的参数,键是 Playwright 的写法:Shift 等不分左右的名称按左侧的键,
// ShiftLeft、ShiftRight 等指定一侧。keySpec.key 是修饰键的名称(modifierBits 的键)。
var modifierKeys = func() map[string]keySpec {
	keys := map[string]keySpec{}
	for name, vk := range map[string][2]int{"Alt": {18, 18}, "Control": {17, 17}, "Meta": {91, 92}, "Shift": {16, 16}} {
		left := keySpec{key: name, code: name + "Left", vk: vk[0]}
		keys[name], keys[name+"Left"] = left, left
		keys[name+"Right"] = keySpec{key: name, code: name + "Right", vk: vk[1]}
	}
	return keys
}()

// controlOrMeta 是 Playwright 的跨平台修饰键:浏览器在 macOS 上时是 Meta,其他平台是 Control。它在按下时
// 才按浏览器的平台解析(combo.forBrowser)。
const controlOrMeta = "ControlOrMeta"

// isModifier 报告 name 是不是修饰键的写法。
func isModifier(name string) bool {
	_, ok := modifierKeys[name]
	return ok || name == controlOrMeta
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

// codeKeys 是 Playwright 按物理键码写的可打印键(KeyA、Digit1、Minus 等)到它未按 Shift 时的字符。
var codeKeys = func() map[string]rune {
	keys := map[string]rune{}
	for r, ck := range usLayout {
		if !ck.shifted {
			keys[ck.code] = r
		}
	}
	return keys
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

// combo 是解析后的按键组合:依次按下的修饰键(按输入的写法,可以是 ControlOrMeta)与最后的主键。
// 只按修饰键(如 Shift)时没有主键。
type combo struct {
	modifiers []string
	key       *keySpec
}

// hasModifier 报告 c 按着名为 name(Alt、Control、Meta、Shift)的修饰键;ControlOrMeta 解析前不算。
func (c combo) hasModifier(name string) bool {
	return slices.ContainsFunc(c.modifiers, func(m string) bool { return modifierKeys[m].key == name })
}

// parseCombo 解析 Playwright 写法的按键:`Enter`、`a`、`KeyA`、`Control+A`、`Shift+Tab`、`Control++`、
// `ControlOrMeta+A`。修饰键是 Alt、Control、Meta、Shift、它们的 Left/Right 写法与 ControlOrMeta;主键是
// 命名键(区分大小写)、可打印键的键码、单个字符,或修饰键自身。
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
	if isModifier(key) {
		names, key, modifierOnly = append(names, key), "", true
	}
	var c combo
	// held 按修饰键的名称记录已给出的修饰键;ControlOrMeta 同时占用 Control 与 Meta。
	held := map[string]bool{}
	for _, name := range names {
		if !isModifier(name) {
			return combo{}, invalidRequest(fmt.Sprintf("invalid key %q: %q is not a modifier; use Alt, Control, Meta, Shift (or their Left and Right forms such as ShiftLeft) or ControlOrMeta before the last +", input, name))
		}
		covers := []string{modifierKeys[name].key}
		if name == controlOrMeta {
			covers = []string{"Control", "Meta"}
		}
		for _, m := range covers {
			if held[m] {
				return combo{}, invalidRequest(fmt.Sprintf("invalid key %q: modifier %s is given twice", input, m))
			}
		}
		for _, m := range covers {
			held[m] = true
		}
		c.modifiers = append(c.modifiers, name)
	}
	if modifierOnly {
		return c, nil
	}
	spec, err := resolveKey(input, key, held["Shift"])
	if err != nil {
		return combo{}, err
	}
	if held["Control"] || held["Alt"] || held["Meta"] {
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
	if r, ok := codeKeys[key]; ok {
		spec, _ := charSpec(r, holdShift)
		return spec, nil
	}
	if r, size := utf8.DecodeRuneInString(key); key != "" && size == len(key) {
		if spec, ok := charSpec(r, holdShift); ok {
			return spec, nil
		}
	}
	return keySpec{}, invalidRequest(fmt.Sprintf("invalid key %q: unknown key %q; use a name such as Enter, Tab, Escape, ArrowDown, F5, a key code such as KeyA or Digit1, or one character, optionally with modifiers such as Control, Shift or ControlOrMeta and + before it (Control+A)", input, key))
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
// modifiers 已含它自己,松开时不含。按浏览器的平台解析 ControlOrMeta;浏览器在 macOS 上时,主键的按下
// 事件带上快捷键对应的编辑命令。
func pressCombo(ctx context.Context, t *Tab, c combo) error {
	c, commands, err := c.forBrowser(ctx, t)
	if err != nil {
		return err
	}
	send := func(k keySpec, kind string, mods int) error {
		return t.send(ctx, "Input.dispatchKeyEvent", k.params(kind, mods), nil)
	}
	mods := 0
	for _, name := range c.modifiers {
		mods |= modifierBits[modifierKeys[name].key]
		if err := send(modifierKeys[name], "keyDown", mods); err != nil {
			return err
		}
	}
	if c.key != nil {
		down := c.key.params("keyDown", mods)
		if len(commands) > 0 {
			down["commands"] = commands
		}
		if err := t.send(ctx, "Input.dispatchKeyEvent", down, nil); err != nil {
			return err
		}
		if err := send(*c.key, "keyUp", mods); err != nil {
			return err
		}
	}
	for i := len(c.modifiers) - 1; i >= 0; i-- {
		mods &^= modifierBits[modifierKeys[c.modifiers[i]].key]
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
