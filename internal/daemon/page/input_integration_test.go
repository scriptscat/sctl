package page_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// pageEvents 返回页面记录的、type 类型(id 非空时限于该元素)的事件。
func pageEvents(m *page.Manager, tab int, eventType, id string) []map[string]any {
	v, err := eval(m, tab, fmt.Sprintf(`JSON.stringify(window.eventsOf(%q, %q))`, eventType, id))
	So(err, ShouldBeNil)
	var encoded string
	So(json.Unmarshal(v, &encoded), ShouldBeNil)
	var events []map[string]any
	So(json.Unmarshal([]byte(encoded), &events), ShouldBeNil)
	return events
}

// keySequence 把页面记录的键盘事件压成 "keydown:Control" 这样的序列。
func keySequence(m *page.Manager, tab int) []string {
	v, err := eval(m, tab, `JSON.stringify(window.events.filter(e => e.type.startsWith("key")).map(e => e.type + ":" + e.key + ":" + e.modifiers.join("+") + ":" + e.trusted))`)
	So(err, ShouldBeNil)
	var encoded string
	So(json.Unmarshal(v, &encoded), ShouldBeNil)
	var seq []string
	So(json.Unmarshal([]byte(encoded), &seq), ShouldBeNil)
	return seq
}

func valueOf(m *page.Manager, tab int, id string) string {
	v, err := eval(m, tab, fmt.Sprintf(`document.getElementById(%q).value`, id))
	So(err, ShouldBeNil)
	var s string
	So(json.Unmarshal(v, &s), ShouldBeNil)
	return s
}

func focusOn(m *page.Manager, tab int, id string) {
	_, err := eval(m, tab, fmt.Sprintf(`document.getElementById(%q).focus()`, id))
	So(err, ShouldBeNil)
}

func TestInputActionsInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("fill、type、press、select、upload、scroll 在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/input.html")
		fill := func(id, text string) (actionResult, error) {
			return act(m, tab, "fill", map[string]any{"selector": "#" + id, "text": text}, callTimeout)
		}

		Convey("fill 清空后填入文本:input 事件可信、change 触发一次,结果带 tabId 与页面标题", func() {
			res, err := fill("name", "hello")
			So(err, ShouldBeNil)
			So(res, ShouldResemble, actionResult{
				ContentTrust: "untrusted-page-content", TabID: tab, URL: base + "/input.html", Title: "input fixture",
			})
			So(valueOf(m, tab, "name"), ShouldEqual, "hello")
			inputs := pageEvents(m, tab, "input", "name")
			So(inputs, ShouldNotBeEmpty)
			So(inputs[len(inputs)-1]["trusted"], ShouldEqual, true)
			So(inputs[len(inputs)-1]["value"], ShouldEqual, "hello")
			So(pageEvents(m, tab, "change", "name"), ShouldHaveLength, 1)
		})

		Convey("fill 空文本清空输入框并触发 input", func() {
			_, err := fill("name", "")
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "name"), ShouldEqual, "")
			inputs := pageEvents(m, tab, "input", "name")
			So(inputs, ShouldNotBeEmpty)
			So(inputs[len(inputs)-1]["value"], ShouldEqual, "")
		})

		Convey("fill 适用于 textarea、number 与 contenteditable", func() {
			_, err := fill("notes", "line 1\nline 2")
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "notes"), ShouldEqual, "line 1\nline 2")

			_, err = fill("number", "42")
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "number"), ShouldEqual, "42")

			_, err = fill("edit", "new editable")
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `document.getElementById("edit").textContent`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"new editable"`)
			inputs := pageEvents(m, tab, "input", "edit")
			So(inputs, ShouldNotBeEmpty)
			So(inputs[len(inputs)-1]["trusted"], ShouldEqual, true)
		})

		Convey("fill 也接受快照引用", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = act(m, tab, "fill", map[string]any{"ref": refFor(snap, `- textbox "`), "text": "by ref"}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "name"), ShouldEqual, "by ref")
		})

		Convey("fill 拒绝不能填文本的元素:checkbox 与 radio 提示 click,file 提示 upload,其余 INVALID_REQUEST,页面不变", func() {
			for id, hint := range map[string]string{"agree": "click", "pick": "click", "file": "upload", "files": "upload", "range": "", "plain-div": "", "color": ""} {
				_, err := fill(id, "x")
				So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
				if hint != "" {
					So(err.Error(), ShouldContainSubstring, hint)
				}
			}
			So(pageEvents(m, tab, "input", ""), ShouldBeEmpty)
		})

		Convey("fill 等不到可编辑的元素时超时,消息写明 read-only 或 disabled", func() {
			_, err := act(m, tab, "fill", map[string]any{"selector": "#readonly", "text": "x"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "read-only")
			_, err = act(m, tab, "fill", map[string]any{"selector": "#locked", "text": "x"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "disabled")
			So(valueOf(m, tab, "readonly"), ShouldEqual, "fixed")
		})

		Convey("type 向当前焦点元素逐键输入:每个字符有可信的 keydown/keyup 与 input,非键盘字符直接插入", func() {
			focusOn(m, tab, "name")
			_, err := fill("name", "")
			So(err, ShouldBeNil)
			res, err := act(m, tab, "type", map[string]any{"text": "Hi!你"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(valueOf(m, tab, "name"), ShouldEqual, "Hi!你")
			var keys []string
			for _, e := range pageEvents(m, tab, "keydown", "name") {
				keys = append(keys, e["key"].(string))
				So(e["trusted"], ShouldEqual, true)
			}
			So(keys, ShouldResemble, []string{"H", "i", "!"})
			So(pageEvents(m, tab, "keyup", "name"), ShouldHaveLength, 3)
		})

		Convey("type 支持换行(Enter)并拒绝空文本", func() {
			focusOn(m, tab, "notes")
			_, err := fill("notes", "")
			So(err, ShouldBeNil)
			_, err = act(m, tab, "type", map[string]any{"text": "a\nb"}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "notes"), ShouldEqual, "a\nb")
			_, err = act(m, tab, "type", map[string]any{"text": ""}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("press 的组合键先按下修饰键,再按主键,再反序松开,页面看到的修饰状态如实", func() {
			focusOn(m, tab, "name")
			_, err := act(m, tab, "press", map[string]any{"key": "Control+A"}, callTimeout)
			So(err, ShouldBeNil)
			So(keySequence(m, tab), ShouldResemble, []string{
				"keydown:Control:Control:true", "keydown:A:Control:true", "keyup:A:Control:true", "keyup:Control::true",
			})
		})

		Convey("press 单键:Enter、字符与带 Shift 的 Tab(焦点回到前一个元素)", func() {
			focusOn(m, tab, "notes")
			_, err := act(m, tab, "press", map[string]any{"key": "Shift+Tab"}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `document.activeElement.id`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"number"`)

			focusOn(m, tab, "name")
			_, err = fill("name", "")
			So(err, ShouldBeNil)
			_, err = act(m, tab, "press", map[string]any{"key": "x"}, callTimeout)
			So(err, ShouldBeNil)
			_, err = act(m, tab, "press", map[string]any{"key": "Enter"}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "name"), ShouldEqual, "x")
			var keys []string
			for _, e := range pageEvents(m, tab, "keydown", "name") {
				keys = append(keys, e["key"].(string)+"/"+e["code"].(string))
			}
			So(keys, ShouldResemble, []string{"x/KeyX", "Enter/Enter"})
			So(pageEvents(m, tab, "keypress", "name"), ShouldHaveLength, 2)
		})

		Convey("press 拒绝未知键名与畸形组合", func() {
			for _, key := range []string{"", "Foo", "Control+", "Control+Foo", "+A", "Hyper+A", "Control+Control+A", "Enter+A"} {
				_, err := act(m, tab, "press", map[string]any{"key": key}, callTimeout)
				So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
			So(keySequence(m, tab), ShouldBeEmpty)
		})

		Convey("press 全选快捷键选中输入框里的全部文字,随后 type 替换它们(macOS 上是 Meta+A,其余平台是 Control+A)", func() {
			// headless Chrome 与测试进程在同一台机器上,浏览器所在平台就是 runtime.GOOS。
			selectAll := "Control+A"
			if runtime.GOOS == "darwin" {
				selectAll = "Meta+A"
			}
			_, err := fill("name", "hello world")
			So(err, ShouldBeNil)
			focusOn(m, tab, "name")
			_, err = act(m, tab, "press", map[string]any{"key": selectAll}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `[document.getElementById("name").selectionStart, document.getElementById("name").selectionEnd]`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `[0,11]`)
			_, err = act(m, tab, "type", map[string]any{"text": "Typed!"}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "name"), ShouldEqual, "Typed!")
		})

		Convey("select 按 value 或可见文本选择,触发 input 与 change", func() {
			_, err := act(m, tab, "select", map[string]any{"selector": "#color", "values": []string{"g"}}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "color"), ShouldEqual, "g")
			So(pageEvents(m, tab, "input", "color"), ShouldHaveLength, 1)
			So(pageEvents(m, tab, "change", "color"), ShouldHaveLength, 1)

			_, err = act(m, tab, "select", map[string]any{"selector": "#color", "values": []string{"Blue"}}, callTimeout)
			So(err, ShouldBeNil)
			So(valueOf(m, tab, "color"), ShouldEqual, "b")
		})

		Convey("select 多选:选中给出的全部并取消其余", func() {
			_, err := act(m, tab, "select", map[string]any{"selector": "#toppings", "values": []string{"cheese", "Olive"}}, callTimeout)
			So(err, ShouldBeNil)
			selected := func() string {
				v, err := eval(m, tab, `Array.from(document.getElementById("toppings").selectedOptions).map(o => o.value).join(",")`)
				So(err, ShouldBeNil)
				return string(v)
			}
			So(selected(), ShouldEqual, `"cheese,olive"`)
			_, err = act(m, tab, "select", map[string]any{"selector": "#toppings", "values": []string{"ham"}}, callTimeout)
			So(err, ShouldBeNil)
			So(selected(), ShouldEqual, `"ham"`)
		})

		Convey("select 缺失的选项 NOT_FOUND 且不改变选择;非 select 与单选给多个值 INVALID_REQUEST", func() {
			_, err := act(m, tab, "select", map[string]any{"selector": "#color", "values": []string{"Purple"}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
			So(err.Error(), ShouldContainSubstring, "Purple")
			So(valueOf(m, tab, "color"), ShouldEqual, "r")
			So(pageEvents(m, tab, "change", "color"), ShouldBeEmpty)

			_, err = act(m, tab, "select", map[string]any{"selector": "#name", "values": []string{"a"}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			_, err = act(m, tab, "select", map[string]any{"selector": "#color", "values": []string{"g", "b"}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			_, err = act(m, tab, "select", map[string]any{"selector": "#color", "values": []string{}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("upload 为 file input 设置文件,页面看到文件名;多个文件要求 multiple", func() {
			dir := t.TempDir()
			a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.png")
			So(os.WriteFile(a, []byte("aaa"), 0o600), ShouldBeNil)
			So(os.WriteFile(b, []byte("bbb"), 0o600), ShouldBeNil)

			_, err := act(m, tab, "upload", map[string]any{"selector": "#file", "files": []string{a}}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `JSON.stringify(window.picked)`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"[\"a.txt\"]"`)
			So(pageEvents(m, tab, "change", "file"), ShouldHaveLength, 1)

			_, err = act(m, tab, "upload", map[string]any{"selector": "#files", "files": []string{a, b}}, callTimeout)
			So(err, ShouldBeNil)
			v, err = eval(m, tab, `JSON.stringify(window.pickedMany)`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"[\"a.txt\",\"b.png\"]"`)

			_, err = act(m, tab, "upload", map[string]any{"selector": "#file", "files": []string{a, b}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			_, err = act(m, tab, "upload", map[string]any{"selector": "#name", "files": []string{a}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("upload 的路径必须是存在、可读的绝对文件路径,否则 INVALID_REQUEST 且不设置任何文件", func() {
			dir := t.TempDir()
			So(os.WriteFile(filepath.Join(dir, "ok.txt"), []byte("x"), 0o600), ShouldBeNil)
			for _, path := range []string{"ok.txt", filepath.Join(dir, "missing.txt"), dir, ""} {
				_, err := act(m, tab, "upload", map[string]any{"selector": "#file", "files": []string{path}}, callTimeout)
				So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
			_, err := act(m, tab, "upload", map[string]any{"selector": "#file", "files": []string{}}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			v, err := eval(m, tab, `document.getElementById("file").files.length`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "0")
		})

		Convey("scroll 无目标时按像素滚动视口", func() {
			res, err := act(m, tab, "scroll", map[string]any{"dy": 300, "dx": 120}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			waitFor(t, m, tab, `scrollY === 300 && scrollX === 120`)
			_, err = act(m, tab, "scroll", map[string]any{"dy": -100}, callTimeout)
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `scrollY === 200`)
		})

		Convey("scroll 有目标时滚到可视区域内", func() {
			_, err := act(m, tab, "scroll", map[string]any{"selector": "#far"}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `(() => { const r = document.getElementById("far").getBoundingClientRect(); return scrollY > 0 && r.top >= 0 && r.bottom <= innerHeight })()`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "true")
		})

		Convey("scroll 没有目标也没有位移、或两者都给时 INVALID_REQUEST", func() {
			for _, input := range []map[string]any{{}, {"dx": 0, "dy": 0}, {"selector": "#far", "dy": 10}} {
				_, err := act(m, tab, "scroll", input, callTimeout)
				So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		})

		Convey("选择器多于一个时立即 TARGET_AMBIGUOUS,与 click 共用同一套目标规则", func() {
			_, err := act(m, tab, "fill", map[string]any{"selector": "input", "text": "x"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTargetAmbiguous)
			So(strings.Contains(err.Error(), "matches"), ShouldBeTrue)
		})
	})
}

func TestFillInCrossOriginIframeInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("经引用向跨进程 iframe 里的输入框填文本", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := chrome.NewTab(t, base+"/oopif.html")
		waitLoaded(t, m, tab)
		waitFor(t, m, tab, `window.loaded.length === 2 && document.getElementById("cross").contentDocument === null`)

		snap, err := snapshotOf(m, tab, "")
		So(err, ShouldBeNil)
		ref := refFor(snap, `- textbox "Name"`)
		_, err = act(m, tab, "fill", map[string]any{"ref": ref, "text": "typed in frame"}, callTimeout)
		So(err, ShouldBeNil)
		v, err := evalOn(m, tab, `el => [el.value, window.inputEvents.map(e => e.type + ":" + e.trusted + ":" + e.value)]`, ref)
		So(err, ShouldBeNil)
		So(string(v), ShouldEqual, `["typed in frame",["input:true:typed in frame","change:false:typed in frame"]]`)
	})
}
