package page

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func TestParseCombo(t *testing.T) {
	Convey("按键按 Playwright 的写法解析", t, func() {
		Convey("单键、命名键与组合键", func() {
			c, err := parseCombo("Enter")
			So(err, ShouldBeNil)
			So(c.modifiers, ShouldBeEmpty)
			So(c.key.key, ShouldEqual, "Enter")
			So(c.key.text, ShouldEqual, "\r")

			c, err = parseCombo("Shift+Tab")
			So(err, ShouldBeNil)
			So(c.modifiers, ShouldResemble, []string{"Shift"})
			So(c.key.code, ShouldEqual, "Tab")
			So(c.key.vk, ShouldEqual, 9)

			c, err = parseCombo("Control+Shift+ArrowLeft")
			So(err, ShouldBeNil)
			So(c.modifiers, ShouldResemble, []string{"Control", "Shift"})
			So(c.key.key, ShouldEqual, "ArrowLeft")
		})

		Convey("单个字符:Shift 使字母大写,带 Control、Alt、Meta 时没有文本", func() {
			c, err := parseCombo("a")
			So(err, ShouldBeNil)
			So(c.key.text, ShouldEqual, "a")
			So(c.key.code, ShouldEqual, "KeyA")

			c, err = parseCombo("Shift+a")
			So(err, ShouldBeNil)
			So(c.key.key, ShouldEqual, "A")
			So(c.key.text, ShouldEqual, "A")

			c, err = parseCombo("Meta+V")
			So(err, ShouldBeNil)
			So(c.key.code, ShouldEqual, "KeyV")
			So(c.key.text, ShouldEqual, "")
		})

		Convey("加号本身与只按修饰键", func() {
			c, err := parseCombo("+")
			So(err, ShouldBeNil)
			So(c.key.key, ShouldEqual, "+")
			So(c.key.code, ShouldEqual, "Equal")

			c, err = parseCombo("Control++")
			So(err, ShouldBeNil)
			So(c.modifiers, ShouldResemble, []string{"Control"})
			So(c.key.key, ShouldEqual, "+")

			c, err = parseCombo("Shift")
			So(err, ShouldBeNil)
			So(c.modifiers, ShouldResemble, []string{"Shift"})
			So(c.key, ShouldBeNil)
		})

		Convey("未知键名与畸形组合是 INVALID_REQUEST", func() {
			for _, in := range []string{"", "Foo", "enter", "Control+", "+A", "Hyper+A", "Control+Control+A", "Shift+Shift", "Enter+A", "ab", "é"} {
				_, err := parseCombo(in)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})
	})
}

func TestInputActionValidation(t *testing.T) {
	Convey("输入类动作在边界上校验输入,非法输入不向页面发出任何输入", t, func() {
		m, cdp := newActionManager(&renderingPage{rendering: true})
		dir := t.TempDir()
		cases := map[string][]string{
			"fill": {
				`{"selector":"#a"}`,
				`{"text":"x"}`,
				`{"ref":"e5","selector":"#a","text":"x"}`,
				`{"selector":"#a","text":"x","extra":1}`,
			},
			"type":   {`{}`, `{"text":""}`, `{"text":"x","selector":"#a"}`},
			"press":  {`{}`, `{"key":"Foo"}`},
			"select": {`{"selector":"#a"}`, `{"selector":"#a","values":[]}`, `{"values":["x"]}`},
			"upload": {
				`{"selector":"#a"}`,
				`{"selector":"#a","files":[]}`,
				`{"selector":"#a","files":["relative.txt"]}`,
				`{"selector":"#a","files":["` + filepath.ToSlash(filepath.Join(dir, "missing.txt")) + `"]}`,
				`{"selector":"#a","files":["` + filepath.ToSlash(dir) + `"]}`,
				`{"files":["` + filepath.ToSlash(dir) + `"]}`,
			},
			"scroll": {`{}`, `{"dx":0,"dy":0}`, `{"selector":"#a","dy":5}`, `{"ref":"e1","selector":"#a"}`, `{"ref":"#a"}`},
		}
		for action, inputs := range cases {
			for _, input := range inputs {
				_, err := doAction(m, action, input, time.Second)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		}
		for _, method := range cdp.methods(7) {
			So(slices.Contains([]string{"Input.dispatchKeyEvent", "Input.insertText", "Input.dispatchMouseEvent", "DOM.setFileInputFiles"}, method), ShouldBeFalse)
		}
	})
}

// leavingPage 是一个 #target 元素通过自动等待之后、在动作操作它时所在文档已被替换的假页面
// (例如聚焦触发了导航):对它调用的 fill/select 脚本被 Chrome 以执行上下文不存在拒绝。
type leavingPage struct {
	renderingPage
	kind string
}

func (p *leavingPage) send(ctx context.Context, cmd Command) (json.RawMessage, error) {
	if cmd.Method == "Runtime.callFunctionOn" {
		var params struct {
			FunctionDeclaration string `json:"functionDeclaration"`
		}
		if err := json.Unmarshal(cmd.Params, &params); err != nil {
			return nil, err
		}
		switch params.FunctionDeclaration {
		case describeFunction:
			return json.RawMessage(`{"result":{"type":"object","value":{"kind":"` + p.kind + `","type":"","tag":"x","multiple":false}}}`), nil
		case selectContentFunction, selectOptionsFunction:
			return nil, &Error{Code: generated.ErrorCodeInvalidRequest, Message: "Cannot find context with specified id"}
		}
	}
	return p.renderingPage.send(ctx, cmd)
}

func TestInputActionTargetLeavesMidAction(t *testing.T) {
	Convey("元素通过自动等待后、在动作操作它时离开文档,返回领域错误而不是内部错误", t, func() {
		for action, c := range map[string]struct{ kind, input string }{
			"fill":   {"text", `{"selector":"#target","text":"x"}`},
			"select": {"select", `{"selector":"#target","values":["a"]}`},
		} {
			cdp := newFakeCDP()
			cdp.setSend((&leavingPage{renderingPage: renderingPage{rendering: true}, kind: c.kind}).send)
			m := newTestManager(cdp, &fakeClock{})
			_, err := doAction(m, action, c.input, time.Minute)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
			So(err.Error(), ShouldContainSubstring, "#target")
		}
	})
}

// platformPage 是 navigator.platform 为 platform 的假页面。
type platformPage struct {
	renderingPage
	platform string
}

func (p *platformPage) send(ctx context.Context, cmd Command) (json.RawMessage, error) {
	if cmd.Method == "Runtime.evaluate" {
		v, err := json.Marshal(p.platform)
		if err != nil {
			return nil, err
		}
		return json.RawMessage(`{"result":{"type":"string","value":` + string(v) + `}}`), nil
	}
	return p.renderingPage.send(ctx, cmd)
}

// keyDowns 返回发出的按下事件的 "key:commands" 序列,没有 commands 时只有 key。
func keyDowns(f *fakeCDP) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.sent {
		if c.Method != "Input.dispatchKeyEvent" {
			continue
		}
		var p struct {
			Type     string   `json:"type"`
			Key      string   `json:"key"`
			Commands []string `json:"commands"`
		}
		So(json.Unmarshal(c.Params, &p), ShouldBeNil)
		if p.Type == "keyUp" {
			So(p.Commands, ShouldBeEmpty)
			continue
		}
		entry := p.Key
		if len(p.Commands) > 0 {
			entry += ":" + strings.Join(p.Commands, ",")
		}
		out = append(out, entry)
	}
	return out
}

func TestPressEditingCommandsOnMac(t *testing.T) {
	Convey("浏览器在 macOS 上时,press 把编辑快捷键映射为按下主键时的编辑命令", t, func() {
		cdp := newFakeCDP()
		cdp.setSend((&platformPage{renderingPage: renderingPage{rendering: true}, platform: "MacIntel"}).send)
		m := newTestManager(cdp, &fakeClock{})
		for _, key := range []string{"Meta+A", "Meta+Shift+Z", "Alt+ArrowUp", "Shift+ArrowLeft", "Control+O", "Enter", "Shift+Tab", "x"} {
			_, err := doAction(m, "press", `{"key":"`+key+`"}`, time.Minute)
			So(err, ShouldBeNil)
		}
		So(keyDowns(cdp), ShouldResemble, []string{
			"Meta", "A:selectAll",
			"Meta", "Shift", "Z:redo",
			"Alt", "ArrowUp:moveBackward,moveToBeginningOfParagraph",
			"Shift", "ArrowLeft:moveLeftAndModifySelection",
			// 插入文字的命令(Control+O 的 insertNewlineIgnoringFieldEditor、Enter 的 insertNewline)由按键自身完成,不重复发送。
			"Control", "O:moveBackward",
			"Enter",
			"Shift", "Tab",
			"x",
		})
		// 平台在一次附加内只向页面问一次。
		evaluations := 0
		for _, method := range cdp.methods(7) {
			if method == "Runtime.evaluate" {
				evaluations++
			}
		}
		So(evaluations, ShouldEqual, 1)
	})

	Convey("浏览器不在 macOS 上时,同样的快捷键不带编辑命令(Chrome 自己处理)", t, func() {
		for _, platform := range []string{"Win32", "Linux x86_64"} {
			cdp := newFakeCDP()
			cdp.setSend((&platformPage{renderingPage: renderingPage{rendering: true}, platform: platform}).send)
			m := newTestManager(cdp, &fakeClock{})
			for _, key := range []string{"Meta+A", "Control+A", "Shift+ArrowLeft"} {
				_, err := doAction(m, "press", `{"key":"`+key+`"}`, time.Minute)
				So(err, ShouldBeNil)
			}
			So(keyDowns(cdp), ShouldResemble, []string{"Meta", "A", "Control", "A", "Shift", "ArrowLeft"})
		}
	})
}
