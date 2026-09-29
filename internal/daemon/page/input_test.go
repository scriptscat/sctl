package page

import (
	"path/filepath"
	"slices"
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
