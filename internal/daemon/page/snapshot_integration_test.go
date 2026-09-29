package page_test

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// snapshotOf 对标签页做一次快照,root 为空时快照整个文档。
func snapshotOf(m *page.Manager, tab int, root string) (string, error) {
	raw, err := snapshotRaw(m, tab, root)
	if err != nil {
		return "", err
	}
	var res struct {
		Snapshot string `json:"snapshot"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", err
	}
	return res.Snapshot, nil
}

func snapshotRaw(m *page.Manager, tab int, root string) (json.RawMessage, error) {
	input := map[string]string{}
	if root != "" {
		input["root"] = root
	}
	raw, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	return m.Do(context.Background(), page.Request{Action: "snapshot", TabID: &tab, Timeout: callTimeout, Input: raw})
}

// refFor 取快照中第一行包含 line 的节点的引用。
func refFor(snapshot, line string) string {
	for _, l := range strings.Split(snapshot, "\n") {
		if strings.Contains(l, line) {
			if m := regexp.MustCompile(`\[ref=(e\d+)\]`).FindStringSubmatch(l); m != nil {
				return m[1]
			}
		}
	}
	return ""
}

// waitFor 轮询表达式直到它为 true。
func waitFor(t *testing.T, m *page.Manager, tab int, expression string) {
	t.Helper()
	deadline := time.Now().Add(callTimeout)
	for time.Now().Before(deadline) {
		v, err := eval(m, tab, expression)
		if err == nil && string(v) == "true" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("condition %s never held in tab %d", expression, tab)
}

func TestSnapshotInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")

	Convey("page snapshot 在真 Chrome 的 fixture 页面上", t, func() {
		// 每个分支用新的 Manager 与新的标签页:引用编号从 e1 开始,页面状态互不影响。
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := chrome.NewTab(t, base+"/snapshot.html")
		waitLoaded(t, m, tab)
		waitFor(t, m, tab, `document.querySelector("iframe").contentDocument?.readyState === "complete"`)

		Convey("按层级输出角色、名称、状态、值、链接与文本,排除不可见元素并展开布局容器与同进程 iframe", func() {
			raw, err := snapshotRaw(m, tab, "")
			So(err, ShouldBeNil)
			var res map[string]any
			So(json.Unmarshal(raw, &res), ShouldBeNil)
			So(res["contentTrust"], ShouldEqual, "untrusted-page-content")
			So(res["tabId"], ShouldEqual, float64(tab))
			So(res["snapshot"], ShouldEqual, strings.Join([]string{
				`- heading "Fixture title" [level=1] [ref=e1]`,
				`- paragraph`,
				`  - text: Plain inline text with`,
				`  - link "a link" [ref=e2]`,
				`    - /url: ` + base + `/next.html`,
				`  - text: inside.`,
				`- form`,
				`  - text: Email`,
				`  - textbox "Email" [required] [ref=e3]: "a@example.com"`,
				`  - text: Notes`,
				`  - textbox "Notes" [ref=e4]: "line one"`,
				`  - text: Color`,
				`  - combobox "Color" [ref=e5]: "Green"`,
				`    - option "Red" [ref=e6]`,
				`    - option "Green" [selected] [ref=e7]`,
				`  - checkbox "Subscribe" [checked] [ref=e8]`,
				`  - button "Disabled action" [disabled] [ref=e9]`,
				`  - button "Menu" [expanded] [ref=e10]`,
				`  - button "Bold" [pressed] [ref=e11]`,
				`- generic [ref=e12]`,
				`  - text: Focusable div`,
				`- link "Card title" [ref=e13]`,
				`  - /url: ` + base + `/card.html`,
				`- button "Floated" [ref=e14]`,
				`- iframe "Inner frame" [ref=e15]`,
				`  - button "Inside frame" [ref=e16]`,
			}, "\n"))
		})

		Convey("获得焦点的元素带 focused", func() {
			_, err := eval(m, tab, `document.querySelector("textarea").focus()`)
			So(err, ShouldBeNil)
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			So(snap, ShouldContainSubstring, `- textbox "Notes" [focused] [ref=`)
		})

		Convey("新快照取代旧引用:旧引用返回 STALE_REF,新引用可用", func() {
			first, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			second, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			oldRef, newRef := refFor(first, `- button "Menu"`), refFor(second, `- button "Menu"`)
			So(newRef, ShouldNotEqual, oldRef)

			_, err = snapshotOf(m, tab, oldRef)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			sub, err := snapshotOf(m, tab, newRef)
			So(err, ShouldBeNil)
			So(sub, ShouldStartWith, `- button "Menu" [expanded] [ref=`)
		})

		Convey("--root 引用只输出该子树,子树外的引用不再生成", func() {
			full, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			sub, err := snapshotOf(m, tab, refFor(full, `- combobox "Color"`))
			So(err, ShouldBeNil)
			So(sub, ShouldEqual, strings.Join([]string{
				`- combobox "Color" [ref=e17]: "Green"`,
				`  - option "Red" [ref=e18]`,
				`  - option "Green" [selected] [ref=e19]`,
			}, "\n"))
			_, err = snapshotOf(m, tab, refFor(full, `- heading`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("--root 引用可以指向 iframe 里的元素", func() {
			full, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			sub, err := snapshotOf(m, tab, refFor(full, `- button "Inside frame"`))
			So(err, ShouldBeNil)
			So(sub, ShouldEqual, `- button "Inside frame" [ref=e17]`)
		})

		Convey("--root 选择器在主文档里严格匹配", func() {
			sub, err := snapshotOf(m, tab, "form")
			So(err, ShouldBeNil)
			So(sub, ShouldStartWith, "- form\n  - text: Email\n")
			So(sub, ShouldNotContainSubstring, "heading")

			_, err = snapshotOf(m, tab, "#missing")
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)

			_, err = snapshotOf(m, tab, "button")
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTargetAmbiguous)
			So(err.Error(), ShouldContainSubstring, "8 elements")

			_, err = snapshotOf(m, tab, "[[")
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("主文档被替换后全部引用返回 STALE_REF", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = eval(m, tab, `window.beforeReload = true; location.reload()`)
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `document.readyState === "complete" && window.beforeReload === undefined`)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Menu"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("元素被页面移除后它的引用返回 STALE_REF,其余引用仍可用", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			// 页面仍持有被移除的节点,节点在内存里还活着,也必须判为失效。
			_, err = eval(m, tab, `window.kept = document.querySelector("form"); window.kept.remove()`)
			So(err, ShouldBeNil)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Menu"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Floated"`))
			So(err, ShouldBeNil)
		})

		Convey("iframe 的文档被替换后只有它里面的引用失效", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = eval(m, tab, `const w = document.querySelector("iframe").contentWindow; w.beforeReload = true; w.location.reload()`)
			So(err, ShouldBeNil)
			waitFor(t, m, tab, `(() => { const w = document.querySelector("iframe").contentWindow; return w.document.readyState === "complete" && w.beforeReload === undefined })()`)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Inside frame"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Floated"`))
			So(err, ShouldBeNil)
		})

		Convey("调试器断开后引用返回 STALE_REF", func() {
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = m.Do(context.Background(), page.Request{Action: "detach", TabID: &tab, Timeout: callTimeout})
			So(err, ShouldBeNil)
			_, err = snapshotOf(m, tab, refFor(snap, `- button "Menu"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})

		Convey("另一个标签页快照里的引用不在这个标签页上解析", func() {
			other := chrome.NewTab(t, base+"/snapshot.html")
			waitLoaded(t, m, other)
			snap, err := snapshotOf(m, tab, "")
			So(err, ShouldBeNil)
			_, err = snapshotOf(m, other, "")
			So(err, ShouldBeNil)
			_, err = snapshotOf(m, other, refFor(snap, `- button "Menu"`))
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})
	})
}
