package page_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// evalOn 以引用为参数执行函数表达式。
func evalOn(m *page.Manager, tab int, expression, ref string) (json.RawMessage, error) {
	input, err := json.Marshal(map[string]string{"expression": expression, "ref": ref})
	if err != nil {
		return nil, err
	}
	raw, err := m.Do(context.Background(), page.Request{Action: "eval", TabID: &tab, Timeout: callTimeout, Input: input})
	if err != nil {
		return nil, err
	}
	var res struct {
		Value json.RawMessage `json:"value"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, err
	}
	return res.Value, nil
}

// whereIs 是在元素所在 frame 里执行、报告元素文本与 frame 身份的函数。
const whereIs = `el => [el.textContent, document.title, location.host]`

func TestCrossOriginIframesInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	topHost, childHost := u.Host, "localhost:"+u.Port()

	Convey("跨进程 iframe 在真 Chrome 上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		// oopif.html(127.0.0.1)嵌入 localhost 的 oopif-child.html,它再嵌入 127.0.0.1 的 oopif-nested.html:
		// 每一层都与父 frame 跨站,都是独立进程的 iframe。
		tab := chrome.NewTab(t, base+"/oopif.html")
		waitLoaded(t, m, tab)
		waitFor(t, m, tab, `window.loaded.length === 2 && document.getElementById("cross").contentDocument === null`)

		Convey("快照把跨进程 iframe 的内容展开在 iframe 节点下,嵌套的也展开,引用跨 frame 唯一", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			So(snap, ShouldEqual, strings.Join([]string{
				`- button "Top button" [ref=e1]`,
				`- iframe "Cross frame" [ref=e2]`,
				`  - text: Name`,
				`  - textbox "Name" [ref=e3]: "child value"`,
				`  - button "Child submit" [ref=e4]`,
				`  - iframe "Nested frame" [ref=e5]`,
				`    - button "Nested button" [ref=e6]`,
			}, "\n"))
		})

		Convey("--root 引用可以指向跨进程 iframe 里的元素", func() {
			full, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			sub, err := snapshotOf(m, tab, refFor(full, `- iframe "Nested frame"`))
			So(err, ShouldBeNil)
			So(sub, ShouldEqual, strings.Join([]string{
				`- iframe "Nested frame" [ref=e7]`,
				`  - button "Nested button" [ref=e8]`,
			}, "\n"))
		})

		Convey("eval 带引用时在元素自己的 frame 里执行", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			cases := map[string][]string{
				`- button "Top button"`:    {"Top button", "oopif fixture", topHost},
				`- button "Child submit"`:  {"Child submit", "child", childHost},
				`- button "Nested button"`: {"Nested button", "nested", topHost},
			}
			for line, want := range cases {
				v, err := evalOn(m, tab, whereIs, refFor(snap, line))
				So(err, ShouldBeNil)
				var got []string
				So(json.Unmarshal(v, &got), ShouldBeNil)
				So(got, ShouldResemble, want)
			}

			v, err := evalOn(m, tab, `async el => el.value`, refFor(snap, `- textbox "Name"`))
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"child value"`)

			_, err = evalOn(m, tab, `el => { throw new Error("thrown in " + document.title) }`, refFor(snap, `- button "Child submit"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeEvalError)
			So(err.Error(), ShouldContainSubstring, "thrown in child")

			_, err = evalOn(m, tab, `document.title`, refFor(snap, `- button "Child submit"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeEvalError)
			So(err.Error(), ShouldContainSubstring, "function")
		})

		Convey("跨进程 iframe 在同站内导航后只有它和嵌套 iframe 里的引用失效", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = eval(m, tab, `document.getElementById("cross").src += "?again"`)
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `window.loaded.length === 4`)

			for _, line := range []string{`- button "Child submit"`, `- button "Nested button"`} {
				_, err = evalOn(m, tab, whereIs, refFor(snap, line))
				So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			}
			_, err = evalOn(m, tab, whereIs, refFor(snap, `- button "Top button"`))
			So(err, ShouldBeNil)
			_, err = evalOn(m, tab, whereIs, refFor(snap, `- iframe "Cross frame"`))
			So(err, ShouldBeNil)

			again, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			v, err := evalOn(m, tab, whereIs, refFor(again, `- button "Nested button"`))
			So(err, ShouldBeNil)
			So(string(v), ShouldContainSubstring, `"nested"`)
		})

		Convey("嵌套 iframe 导航只使它里面的引用失效,外层 iframe 的引用仍可用", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = evalOn(m, tab, `el => { document.getElementById("nested").src += "?again" }`, refFor(snap, `- button "Child submit"`))
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `window.loaded.length === 3`)

			_, err = evalOn(m, tab, whereIs, refFor(snap, `- button "Nested button"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			_, err = evalOn(m, tab, whereIs, refFor(snap, `- button "Child submit"`))
			So(err, ShouldBeNil)
		})

		Convey("iframe 换到另一个进程后它里面的引用失效,新快照照常展开", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			// 换成与顶层同站的地址:iframe 回到顶层页面的进程,原来的子会话随之断开。
			_, err = eval(m, tab, `document.getElementById("cross").src = location.origin + "/oopif-child.html"`)
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `window.loaded.length === 4 && document.getElementById("cross").contentDocument !== null`)

			_, err = evalOn(m, tab, whereIs, refFor(snap, `- button "Child submit"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			_, err = evalOn(m, tab, whereIs, refFor(snap, `- button "Top button"`))
			So(err, ShouldBeNil)

			again, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			v, err := evalOn(m, tab, whereIs, refFor(again, `- button "Child submit"`))
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `["Child submit","child","`+topHost+`"]`)
			v, err = evalOn(m, tab, whereIs, refFor(again, `- button "Nested button"`))
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `["Nested button","nested","`+topHost+`"]`)
		})
	})
}
