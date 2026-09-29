package page_test

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// expectTimeout 是期望超时的动作的期限:留出几轮等待的时间,让超时消息报出被测条件而不是第一轮还没做完。
const expectTimeout = 5 * time.Second

// actionResult 是 click/hover 等动作的结果。
type actionResult struct {
	ContentTrust string `json:"contentTrust"`
	TabID        int    `json:"tabId"`
	URL          string `json:"url"`
	Title        string `json:"title"`
	Navigated    bool   `json:"navigated"`
	NewTabID     *int   `json:"newTabId"`
}

func act(m *page.Manager, tab int, action string, input map[string]any, timeout time.Duration) (actionResult, error) {
	raw, err := json.Marshal(input)
	if err != nil {
		return actionResult{}, err
	}
	out, err := m.Do(context.Background(), page.Request{Action: action, TabID: &tab, Timeout: timeout, Input: raw})
	if err != nil {
		return actionResult{}, err
	}
	var res actionResult
	err = json.Unmarshal(out, &res)
	return res, err
}

// openInBackground 打开 fixture 页面,再打开并激活另一个标签页,让 fixture 所在的标签页处于后台。
func openInBackground(t *testing.T, chrome *pagetest.Chrome, m *page.Manager, pageURL string) int {
	t.Helper()
	tab := chrome.NewTab(t, pageURL)
	other := chrome.NewTab(t, "about:blank")
	ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
	defer cancel()
	if err := chrome.SelectTab(ctx, pagetest.InstanceID, other); err != nil {
		t.Fatalf("activate the foreground tab: %v", err)
	}
	waitLoaded(t, m, tab)
	return tab
}

// clickEvents 返回页面记录的、目标为 id 的 type 事件。
func clickEvents(m *page.Manager, tab int, eventType, id string) []map[string]any {
	v, err := eval(m, tab, `JSON.stringify(window.events.filter(e => e.type === "`+eventType+`" && e.id === "`+id+`"))`)
	So(err, ShouldBeNil)
	var encoded string
	So(json.Unmarshal(v, &encoded), ShouldBeNil)
	var events []map[string]any
	So(json.Unmarshal([]byte(encoded), &events), ShouldBeNil)
	return events
}

func TestClickAndHoverInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("click 与 hover 在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/actions.html")
		click := func(input map[string]any, timeout time.Duration) (actionResult, error) {
			return act(m, tab, "click", input, timeout)
		}

		Convey("点击发出可信事件,结果带 tabId、页面 URL 与标题,没有导航", func() {
			res, err := click(map[string]any{"selector": "#plain"}, callTimeout)
			So(err, ShouldBeNil)
			So(res, ShouldResemble, actionResult{
				ContentTrust: "untrusted-page-content", TabID: tab, URL: base + "/actions.html", Title: "actions fixture",
			})
			So(clickEvents(m, tab, "click", "plain"), ShouldResemble, []map[string]any{
				{"type": "click", "id": "plain", "trusted": true, "button": float64(0), "detail": float64(1), "modifiers": []any{}},
			})
		})

		Convey("--button、--count、--modifiers 如实送达页面", func() {
			_, err := click(map[string]any{"selector": "#plain", "button": "right"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "contextmenu", "plain"), ShouldHaveLength, 1)
			So(clickEvents(m, tab, "contextmenu", "plain")[0]["button"], ShouldEqual, 2)

			_, err = click(map[string]any{"selector": "#plain", "button": "middle"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "auxclick", "plain"), ShouldHaveLength, 2) // 右键与中键各一次
			So(clickEvents(m, tab, "auxclick", "plain")[1]["button"], ShouldEqual, 1)
			So(clickEvents(m, tab, "click", "plain"), ShouldBeEmpty)

			_, err = click(map[string]any{"selector": "#plain", "count": 2}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "dblclick", "plain"), ShouldHaveLength, 1)
			clicks := clickEvents(m, tab, "click", "plain")
			So(len(clicks), ShouldEqual, 2)
			So(clicks[1]["detail"], ShouldEqual, 2)

			_, err = click(map[string]any{"selector": "#plain", "modifiers": []string{"Alt", "Shift"}}, callTimeout)
			So(err, ShouldBeNil)
			clicks = clickEvents(m, tab, "click", "plain")
			So(clicks[len(clicks)-1]["modifiers"], ShouldResemble, []any{"Alt", "Shift"})
			So(clicks[len(clicks)-1]["trusted"], ShouldEqual, true)
		})

		Convey("hover 把鼠标移到元素上:页面收到 mouseover,:hover 样式生效,不产生点击", func() {
			res, err := act(m, tab, "hover", map[string]any{"selector": "#hover-box"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(res.Navigated, ShouldBeFalse)
			So(clickEvents(m, tab, "mouseover", "hover-box"), ShouldNotBeEmpty)
			v, err := eval(m, tab, `[document.getElementById("hover-box").matches(":hover"), getComputedStyle(document.getElementById("hover-box")).backgroundColor]`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `[true,"rgb(255, 0, 0)"]`)
			So(clickEvents(m, tab, "click", "hover-box"), ShouldBeEmpty)
		})

		Convey("点击点被其他元素遮挡时一直等,超时消息写明遮挡元素;遮挡消失后点击成功", func() {
			_, err := eval(m, tab, `document.body.insertAdjacentHTML("beforeend", '<div class="modal-backdrop"></div>')`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#plain"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "obscured by div.modal-backdrop")
			So(clickEvents(m, tab, "click", "plain"), ShouldBeEmpty)

			_, err = eval(m, tab, `setTimeout(() => document.querySelector(".modal-backdrop").remove(), 300)`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#plain"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "click", "plain"), ShouldHaveLength, 1)
		})

		Convey("disabled 的元素等到可用再点击;一直不可用时超时消息写明 disabled", func() {
			_, err := click(map[string]any{"selector": "#late"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "disabled")

			_, err = eval(m, tab, `setTimeout(() => { document.getElementById("late").disabled = false }, 300)`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#late"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "click", "late"), ShouldHaveLength, 1)
		})

		Convey("不可见的元素等到可见再点击;一直不可见时超时消息写明 not visible", func() {
			_, err := click(map[string]any{"selector": "#hidden"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "not visible")

			_, err = eval(m, tab, `setTimeout(() => { document.getElementById("hidden").style.display = "" }, 300)`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#hidden"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "click", "hidden"), ShouldHaveLength, 1)
		})

		Convey("移动中的元素等位置稳定后在最终位置点击;一直移动时超时消息写明 not stable", func() {
			_, err := eval(m, tab, `document.getElementById("mover").classList.add("forever")`)
			So(err, ShouldBeNil)
			_, err = act(m, tab, "hover", map[string]any{"selector": "#mover"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "not stable")

			// 在点击发生的那一刻记下元素的位置:点在动画途中就会记下中间位置。
			_, err = eval(m, tab, `const mover = document.getElementById("mover");
				mover.addEventListener("click", () => { window.clickedAt = getComputedStyle(mover).transform });
				mover.className = "sliding"`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#mover"}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `[window.clicksOn("mover"), window.clickedAt]`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `[1,"matrix(1, 0, 0, 1, 240, 0)"]`)
		})

		Convey("选择器匹配 0 个时等它出现,一直没有时超时消息写明选择器;匹配多个时立即 TARGET_AMBIGUOUS 带数量", func() {
			_, err := click(map[string]any{"selector": "#later"}, expectTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `no element matches selector "#later"`)

			_, err = eval(m, tab, `setTimeout(() => document.body.insertAdjacentHTML("afterbegin", '<button id="later">Later</button>'), 300)`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"selector": "#later"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "click", "later"), ShouldHaveLength, 1)

			started := time.Now()
			_, err = click(map[string]any{"selector": ".item"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTargetAmbiguous)
			So(err.Error(), ShouldContainSubstring, "matches 2 elements")
			So(time.Since(started), ShouldBeLessThan, callTimeout/2)

			_, err = click(map[string]any{"selector": "button["}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("视口外的元素先滚动到可视区域内再点击", func() {
			_, err := click(map[string]any{"selector": "#far"}, callTimeout)
			So(err, ShouldBeNil)
			So(clickEvents(m, tab, "click", "far"), ShouldHaveLength, 1)
			v, err := eval(m, tab, `window.scrollY > 1000`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `true`)
		})

		Convey("引用指向同进程 iframe 里的元素时按它在顶层视口里的位置点击", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			ref := refFor(snap, `- button "Inner button"`)
			So(ref, ShouldNotBeEmpty)
			_, err = click(map[string]any{"ref": ref}, callTimeout)
			So(err, ShouldBeNil)
			v, err := eval(m, tab, `window.innerClicks`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `1`)
		})

		Convey("引用指向的元素已被移出文档:STALE_REF", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			ref := refFor(snap, `- button "Plain"`)
			_, err = eval(m, tab, `document.getElementById("plain").remove()`)
			So(err, ShouldBeNil)
			_, err = click(map[string]any{"ref": ref}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("点击触发主文档导航时等到 DOMContentLoaded,结果报告导航后的 URL 与标题", func() {
			res, err := click(map[string]any{"selector": "#nav"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.Navigated, ShouldBeTrue)
			So(res.URL, ShouldEqual, base+"/actions-next.html")
			So(res.Title, ShouldEqual, "next fixture")
			So(res.NewTabID, ShouldBeNil)
			v, err := eval(m, tab, `document.readyState !== "loading" && location.pathname`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `"/actions-next.html"`)
		})

		Convey("点击锚点只在文档内导航:不等待,报告 navigated 与新 URL,引用仍然有效", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			res, err := click(map[string]any{"ref": refFor(snap, `- link "Jump down"`)}, expectTimeout)
			So(err, ShouldBeNil)
			So(res.Navigated, ShouldBeTrue)
			So(res.URL, ShouldEqual, base+"/actions.html#far")
			_, err = click(map[string]any{"ref": refFor(snap, `- button "Plain"`)}, callTimeout)
			So(err, ShouldBeNil)
		})

		Convey("点击导航到另一个站点(换渲染进程)时同样等到新文档,之前的引用失效", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			u, err := url.Parse(base)
			So(err, ShouldBeNil)
			crossSite := "http://localhost:" + u.Port() + "/actions-next.html"
			_, err = eval(m, tab, `document.getElementById("nav").href = "`+crossSite+`"`)
			So(err, ShouldBeNil)
			res, err := click(map[string]any{"ref": refFor(snap, `- link "Next page"`)}, callTimeout)
			So(err, ShouldBeNil)
			So(res.Navigated, ShouldBeTrue)
			So(res.URL, ShouldEqual, crossSite)
			So(res.Title, ShouldEqual, "next fixture")
			_, err = click(map[string]any{"ref": refFor(snap, `- button "Plain"`)}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("点击打开新标签页时结果带新标签页的 tabId,但仍报告原标签页且不导航", func() {
			res, err := click(map[string]any{"selector": "#blank"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(res.Navigated, ShouldBeFalse)
			So(res.URL, ShouldEqual, base+"/actions.html")
			So(res.NewTabID, ShouldNotBeNil)
			So(*res.NewTabID, ShouldNotEqual, tab)
			waitFor(t, m, *res.NewTabID, `location.href === "`+base+`/actions-next.html?new"`)
		})

		Convey("按键打开新标签页时结果同样带新标签页的 tabId", func() {
			_, err := eval(m, tab, `document.getElementById("blank").href = "/actions-next.html?pressed"; document.getElementById("blank").focus()`)
			So(err, ShouldBeNil)
			res, err := act(m, tab, "press", map[string]any{"key": "Enter"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(res.NewTabID, ShouldNotBeNil)
			So(*res.NewTabID, ShouldNotEqual, tab)
			waitFor(t, m, *res.NewTabID, `location.href === "`+base+`/actions-next.html?pressed"`)
		})
	})
}

func TestClickInCrossOriginIframeInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}

	Convey("click 按引用点击跨进程 iframe 里的元素", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/oopif.html")
		waitFor(t, m, tab, `window.loaded.length === 2 && document.getElementById("cross").contentDocument === null`)
		snap, err := snapshotOf(m, tab, "")
		So(err, ShouldBeNil)

		for _, line := range []string{`- button "Child submit"`, `- button "Nested button"`} {
			ref := refFor(snap, line)
			So(ref, ShouldNotBeEmpty)
			_, err = evalOn(m, tab, `el => { window.clicked = []; el.addEventListener("click", e => window.clicked.push([e.isTrusted, location.host])) }`, ref)
			So(err, ShouldBeNil)
			res, err := act(m, tab, "click", map[string]any{"ref": ref}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(res.URL, ShouldEqual, base+"/oopif.html")
			v, err := evalOn(m, tab, `el => window.clicked`, ref)
			So(err, ShouldBeNil)
			host := u.Host
			if strings.Contains(line, "Child") {
				host = "localhost:" + u.Port()
			}
			So(string(v), ShouldEqual, `[[true,"`+host+`"]]`)
		}
	})
}
