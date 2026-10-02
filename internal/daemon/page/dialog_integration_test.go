package page_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// promptly 是打开弹框的动作必须返回的期限:远小于动作的默认超时,证明它不是等到超时才返回。
const promptly = 5 * time.Second

func TestDialogsInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("JS 弹框在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/dialog.html")
		clickButton := func(id string) error {
			started := time.Now()
			_, err := act(m, tab, "click", map[string]any{"selector": "#" + id}, callTimeout)
			So(time.Since(started), ShouldBeLessThan, promptly+callTimeout/10)
			return err
		}
		handle := func(input map[string]any) (json.RawMessage, error) {
			raw, err := json.Marshal(input)
			So(err, ShouldBeNil)
			return m.Do(context.Background(), page.Request{Action: "dialog", TabID: &tab, Timeout: callTimeout, Input: raw})
		}

		Convey("没有弹框:page dialog 返回 NOT_FOUND", func() {
			_, err := handle(map[string]any{"action": "accept"})
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("alert:点击立即返回 DIALOG_OPEN,其他命令与截图都被拒,accept 后页面继续", func() {
			err := clickButton("alert")
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "alert")
			So(err.Error(), ShouldContainSubstring, "hello <alert>")

			_, err = eval(m, tab, `1`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err = act(m, tab, "snapshot", map[string]any{}, promptly)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)

			// 截图和其他命令一样立即被拒:弹框卡住渲染,等它超时只会白等 15 秒。
			started := time.Now()
			_, _, err = takeShot(m, tab, map[string]any{})
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "alert")
			So(time.Since(started), ShouldBeLessThan, 2*time.Second)

			raw, err := handle(map[string]any{"action": "accept"})
			So(err, ShouldBeNil)
			var res map[string]any
			So(json.Unmarshal(raw, &res), ShouldBeNil)
			So(res, ShouldResemble, map[string]any{
				"contentTrust": "untrusted-page-content", "tabId": float64(tab), "action": "accept", "dialogType": "alert",
				"url": base + "/dialog.html", "title": "dialog fixture", "navigated": false,
			})
			v, err := eval(m, tab, `window.alerted === true`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "true")
			_, err = handle(map[string]any{"action": "accept"})
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("处理一个弹框后页面立刻打开下一个:处理命令照常返回,下一个弹框同样可以处理", func() {
			So(codeOf(clickButton("twice")), ShouldEqual, generated.ErrorCodeDialogOpen)
			started := time.Now()
			_, err := handle(map[string]any{"action": "accept"})
			So(err, ShouldBeNil)
			So(time.Since(started), ShouldBeLessThan, promptly)
			_, err = eval(m, tab, `1`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "second")
			_, err = handle(map[string]any{"action": "accept"})
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `window.twice === true`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "true")
		})

		Convey("confirm:accept 使页面得到 true,dismiss 得到 false", func() {
			So(codeOf(clickButton("confirm")), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err := handle(map[string]any{"action": "accept"})
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `window.confirmed`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "true")

			So(codeOf(clickButton("confirm")), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err = handle(map[string]any{"action": "dismiss"})
			So(err, ShouldBeNil)
			v, err = eval(m, tab, `window.confirmed`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "false")
		})

		Convey("prompt:--text 成为页面收到的输入", func() {
			So(codeOf(clickButton("prompt")), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err := handle(map[string]any{"action": "accept", "text": "Ada"})
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `window.answer`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"Ada"`)

			So(codeOf(clickButton("prompt")), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err = handle(map[string]any{"action": "dismiss"})
			So(err, ShouldBeNil)
			v, err = eval(m, tab, `window.answer`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, "null")
		})

		Convey("beforeunload:离开页面时弹出,dismiss 留在原页,accept 离开", func() {
			_, err := act(m, tab, "click", map[string]any{"selector": "#arm"}, callTimeout)
			So(err, ShouldBeNil)

			started := time.Now()
			_, err = act(m, tab, "navigate", map[string]any{"action": "goto", "url": base + "/nav-a.html"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			So(err.Error(), ShouldContainSubstring, "beforeunload")
			So(time.Since(started), ShouldBeLessThan, promptly)

			_, err = handle(map[string]any{"action": "dismiss"})
			So(err, ShouldBeNil)
			So(evalString(m, tab, `location.pathname`), ShouldEqual, "/dialog.html")

			_, err = act(m, tab, "navigate", map[string]any{"action": "goto", "url": base + "/nav-a.html"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeDialogOpen)
			_, err = handle(map[string]any{"action": "accept"})
			So(err, ShouldBeNil)
			waitLoaded(t, m, tab)
			So(evalString(m, tab, `location.pathname`), ShouldEqual, "/nav-a.html")
		})
	})
}
