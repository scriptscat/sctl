package page

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// macEditingCommands 把 macOS 上的编辑快捷键映射为 Chrome 的编辑命令。macOS 上的 Chrome 由系统的
// 按键绑定(NSStandardKeyBindingResponding)把快捷键翻译成编辑命令,经 Input.dispatchKeyEvent 合成的按键
// 不经过这一步:不带 commands 时 Meta+A 只产生 keydown,不会全选。
//
// 键为 "Shift+Control+Alt+Meta+<code>"(修饰键按此顺序,只写按下的)。内容取自 Playwright 的
// macEditingCommands,去掉了两类:插入文字的命令(insertNewline 等,文字由按键自身产生,Playwright 同样
// 不发送)与小键盘按键(sctl 不能按)。
var macEditingCommands = map[string][]string{
	"Backspace":  {"deleteBackward"},
	"Escape":     {"cancelOperation"},
	"ArrowUp":    {"moveUp"},
	"ArrowDown":  {"moveDown"},
	"ArrowLeft":  {"moveLeft"},
	"ArrowRight": {"moveRight"},
	"F5":         {"complete"},
	"Delete":     {"deleteForward"},
	"Home":       {"scrollToBeginningOfDocument"},
	"End":        {"scrollToEndOfDocument"},
	"PageUp":     {"scrollPageUp"},
	"PageDown":   {"scrollPageDown"},

	"Shift+Backspace":  {"deleteBackward"},
	"Shift+Escape":     {"cancelOperation"},
	"Shift+ArrowUp":    {"moveUpAndModifySelection"},
	"Shift+ArrowDown":  {"moveDownAndModifySelection"},
	"Shift+ArrowLeft":  {"moveLeftAndModifySelection"},
	"Shift+ArrowRight": {"moveRightAndModifySelection"},
	"Shift+F5":         {"complete"},
	"Shift+Delete":     {"deleteForward"},
	"Shift+Home":       {"moveToBeginningOfDocumentAndModifySelection"},
	"Shift+End":        {"moveToEndOfDocumentAndModifySelection"},
	"Shift+PageUp":     {"pageUpAndModifySelection"},
	"Shift+PageDown":   {"pageDownAndModifySelection"},

	"Control+Tab":        {"selectNextKeyView"},
	"Control+KeyA":       {"moveToBeginningOfParagraph"},
	"Control+KeyB":       {"moveBackward"},
	"Control+KeyD":       {"deleteForward"},
	"Control+KeyE":       {"moveToEndOfParagraph"},
	"Control+KeyF":       {"moveForward"},
	"Control+KeyH":       {"deleteBackward"},
	"Control+KeyK":       {"deleteToEndOfParagraph"},
	"Control+KeyL":       {"centerSelectionInVisibleArea"},
	"Control+KeyN":       {"moveDown"},
	"Control+KeyO":       {"moveBackward"},
	"Control+KeyP":       {"moveUp"},
	"Control+KeyT":       {"transpose"},
	"Control+KeyV":       {"pageDown"},
	"Control+KeyY":       {"yank"},
	"Control+Backspace":  {"deleteBackwardByDecomposingPreviousCharacter"},
	"Control+ArrowUp":    {"scrollPageUp"},
	"Control+ArrowDown":  {"scrollPageDown"},
	"Control+ArrowLeft":  {"moveToLeftEndOfLine"},
	"Control+ArrowRight": {"moveToRightEndOfLine"},

	"Shift+Control+Tab":        {"selectPreviousKeyView"},
	"Shift+Control+KeyA":       {"moveToBeginningOfParagraphAndModifySelection"},
	"Shift+Control+KeyB":       {"moveBackwardAndModifySelection"},
	"Shift+Control+KeyE":       {"moveToEndOfParagraphAndModifySelection"},
	"Shift+Control+KeyF":       {"moveForwardAndModifySelection"},
	"Shift+Control+KeyN":       {"moveDownAndModifySelection"},
	"Shift+Control+KeyP":       {"moveUpAndModifySelection"},
	"Shift+Control+KeyV":       {"pageDownAndModifySelection"},
	"Shift+Control+Backspace":  {"deleteBackwardByDecomposingPreviousCharacter"},
	"Shift+Control+ArrowUp":    {"scrollPageUp"},
	"Shift+Control+ArrowDown":  {"scrollPageDown"},
	"Shift+Control+ArrowLeft":  {"moveToLeftEndOfLineAndModifySelection"},
	"Shift+Control+ArrowRight": {"moveToRightEndOfLineAndModifySelection"},

	"Alt+Backspace":  {"deleteWordBackward"},
	"Alt+Escape":     {"complete"},
	"Alt+ArrowUp":    {"moveBackward", "moveToBeginningOfParagraph"},
	"Alt+ArrowDown":  {"moveForward", "moveToEndOfParagraph"},
	"Alt+ArrowLeft":  {"moveWordLeft"},
	"Alt+ArrowRight": {"moveWordRight"},
	"Alt+Delete":     {"deleteWordForward"},
	"Alt+PageUp":     {"pageUp"},
	"Alt+PageDown":   {"pageDown"},

	"Shift+Alt+Backspace":  {"deleteWordBackward"},
	"Shift+Alt+Escape":     {"complete"},
	"Shift+Alt+ArrowUp":    {"moveParagraphBackwardAndModifySelection"},
	"Shift+Alt+ArrowDown":  {"moveParagraphForwardAndModifySelection"},
	"Shift+Alt+ArrowLeft":  {"moveWordLeftAndModifySelection"},
	"Shift+Alt+ArrowRight": {"moveWordRightAndModifySelection"},
	"Shift+Alt+Delete":     {"deleteWordForward"},
	"Shift+Alt+PageUp":     {"pageUp"},
	"Shift+Alt+PageDown":   {"pageDown"},

	"Control+Alt+KeyB":            {"moveWordBackward"},
	"Control+Alt+KeyF":            {"moveWordForward"},
	"Control+Alt+Backspace":       {"deleteWordBackward"},
	"Shift+Control+Alt+KeyB":      {"moveWordBackwardAndModifySelection"},
	"Shift+Control+Alt+KeyF":      {"moveWordForwardAndModifySelection"},
	"Shift+Control+Alt+Backspace": {"deleteWordBackward"},

	"Meta+Backspace":  {"deleteToBeginningOfLine"},
	"Meta+ArrowUp":    {"moveToBeginningOfDocument"},
	"Meta+ArrowDown":  {"moveToEndOfDocument"},
	"Meta+ArrowLeft":  {"moveToLeftEndOfLine"},
	"Meta+ArrowRight": {"moveToRightEndOfLine"},

	"Shift+Meta+Backspace":  {"deleteToBeginningOfLine"},
	"Shift+Meta+ArrowUp":    {"moveToBeginningOfDocumentAndModifySelection"},
	"Shift+Meta+ArrowDown":  {"moveToEndOfDocumentAndModifySelection"},
	"Shift+Meta+ArrowLeft":  {"moveToLeftEndOfLineAndModifySelection"},
	"Shift+Meta+ArrowRight": {"moveToRightEndOfLineAndModifySelection"},

	"Meta+KeyA":       {"selectAll"},
	"Meta+KeyC":       {"copy"},
	"Meta+KeyX":       {"cut"},
	"Meta+KeyV":       {"paste"},
	"Meta+KeyZ":       {"undo"},
	"Shift+Meta+KeyZ": {"redo"},
}

// macCommands 返回 macOS 上按下 c 的主键时要带上的编辑命令;c 里的 ControlOrMeta 须已解析。
func (c combo) macCommands() []string {
	if c.key == nil {
		return nil
	}
	var parts []string
	for _, name := range []string{"Shift", "Control", "Alt", "Meta"} {
		if c.hasModifier(name) {
			parts = append(parts, name)
		}
	}
	return macEditingCommands[strings.Join(append(parts, c.key.code), "+")]
}

// forBrowser 按标签页所在浏览器的平台准备 c:ControlOrMeta 在 macOS 上换成 Meta,其他平台换成 Control
// (同 Playwright,但看浏览器而不是 sctl 所在的平台);浏览器在 macOS 上时还返回主键按下时要带的编辑命令。
// 只有需要时才问平台。
func (c combo) forBrowser(ctx context.Context, t *Tab) (combo, []string, error) {
	smart := slices.Index(c.modifiers, controlOrMeta)
	if smart < 0 && len(c.macCommands()) == 0 {
		return c, nil, nil
	}
	onMac, err := t.browserOnMac(ctx)
	if err != nil {
		return combo{}, nil, err
	}
	if smart >= 0 {
		c.modifiers = slices.Clone(c.modifiers)
		c.modifiers[smart] = "Control"
		if onMac {
			c.modifiers[smart] = "Meta"
		}
	}
	if !onMac {
		return c, nil, nil
	}
	return c, c.macCommands(), nil
}

// browserOnMac 报告标签页所在的浏览器是否运行在 macOS 上,一次附加内只问一次。平台取自页面的
// navigator.platform 而不是 daemon 所在的平台:浏览器可以在另一台机器上;chrome.debugger 也不提供
// Browser.getVersion。页面能改写 navigator.platform,但这只会让它自己收到的快捷键多带或少带编辑命令。
// 只在标签页队列里调用。
func (t *Tab) browserOnMac(ctx context.Context) (bool, error) {
	if t.onMac != nil {
		return *t.onMac, nil
	}
	var res struct {
		Result           remoteObject      `json:"result"`
		ExceptionDetails *exceptionDetails `json:"exceptionDetails"`
	}
	if err := t.send(ctx, "Runtime.evaluate", map[string]any{"expression": "navigator.platform", "returnByValue": true}, &res); err != nil {
		return false, err
	}
	if res.ExceptionDetails != nil {
		return false, &Error{Code: generated.ErrorCodeInternalError, Message: "reading the browser platform failed in the page: " + exceptionMessage(res.ExceptionDetails)}
	}
	var platform string
	if err := json.Unmarshal(res.Result.Value, &platform); err != nil {
		return false, fmt.Errorf("decode navigator.platform: %w", err)
	}
	onMac := strings.HasPrefix(platform, "Mac")
	t.onMac = &onMac
	return onMac, nil
}
