package page_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// navResult 是 navigate 的结果:动作结果加主文档的 HTTP 状态。
type navResult struct {
	actionResult
	HTTPStatus *int `json:"httpStatus"`
}

// serveNav 提供 testdata,并在 /slow 上放一个迟迟才响应的端点(图片 2.5 秒,fetch 1.5 秒):让 load 与
// networkidle 有可观察的差别,并在负载下留足余量。
func serveNav(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/slow", func(w http.ResponseWriter, r *http.Request) {
		delay := 1500 * time.Millisecond
		if r.URL.RawQuery == "img" {
			delay = 2500 * time.Millisecond
		}
		select {
		case <-time.After(delay):
		case <-r.Context().Done():
			return
		}
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("slow"))
	})
	mux.HandleFunc("/nocontent", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/", http.FileServer(http.Dir("testdata")))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func navigate(m *page.Manager, tab int, input map[string]any, timeout time.Duration) (navResult, error) {
	return doJSON[navResult](m, tab, "navigate", input, timeout)
}

func doJSON[T any](m *page.Manager, tab int, action string, input map[string]any, timeout time.Duration) (T, error) {
	var res T
	raw, err := json.Marshal(input)
	if err != nil {
		return res, err
	}
	out, err := m.Do(context.Background(), page.Request{Action: action, TabID: &tab, Timeout: timeout, Input: raw})
	if err != nil {
		return res, err
	}
	err = json.Unmarshal(out, &res)
	return res, err
}

func evalString(m *page.Manager, tab int, expression string) string {
	v, err := eval(m, tab, expression)
	So(err, ShouldBeNil)
	var s string
	So(json.Unmarshal(v, &s), ShouldBeNil)
	return s
}

func TestNavigateInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := serveNav(t)

	Convey("navigate 在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/nav-a.html")
		goTo := func(path string, wait string) (navResult, error) {
			input := map[string]any{"action": "goto", "url": base + path}
			if wait != "" {
				input["wait"] = wait
			}
			return navigate(m, tab, input, callTimeout)
		}

		Convey("goto 默认等 load:结果带 URL、标题、导航标记与 HTTP 状态", func() {
			res, err := goTo("/nav-b.html", "")
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(res.URL, ShouldEqual, base+"/nav-b.html")
			So(res.Title, ShouldEqual, "nav b")
			So(res.Navigated, ShouldBeTrue)
			So(res.ContentTrust, ShouldEqual, "untrusted-page-content")
			So(res.HTTPStatus, ShouldNotBeNil)
			So(*res.HTTPStatus, ShouldEqual, 200)
		})

		Convey("load 等到图片加载完,domcontentloaded 在此之前返回", func() {
			_, err := goTo("/nav-load.html", "domcontentloaded")
			So(err, ShouldBeNil)
			So(evalString(m, tab, `document.readyState`), ShouldEqual, "interactive")
			_, err = goTo("/nav-a.html", "")
			So(err, ShouldBeNil)
			_, err = goTo("/nav-load.html?again", "load")
			So(err, ShouldBeNil)
			So(evalString(m, tab, `document.readyState`), ShouldEqual, "complete")
		})

		Convey("networkidle 等 load 之后才发出的慢请求结束,load 不等它", func() {
			_, err := goTo("/nav-idle.html", "load")
			So(err, ShouldBeNil)
			So(evalString(m, tab, `String(window.fetchDone)`), ShouldEqual, "false")

			started := time.Now()
			res, err := goTo("/nav-idle.html?again", "networkidle")
			So(err, ShouldBeNil)
			So(evalString(m, tab, `String(window.fetchDone)`), ShouldEqual, "true")
			So(time.Since(started), ShouldBeGreaterThan, 1600*time.Millisecond)
			So(res.URL, ShouldEqual, base+"/nav-idle.html?again")
		})

		Convey("networkidle 把跨进程 iframe 里的请求也算进去,iframe 的文档请求不会被当成永远进行中", func() {
			started := time.Now()
			res, err := goTo("/nav-idle-oopif.html", "networkidle")
			So(err, ShouldBeNil)
			So(evalString(m, tab, `String(window.childFetchDone)`), ShouldEqual, "true")
			So(time.Since(started), ShouldBeGreaterThan, 1600*time.Millisecond)
			So(res.URL, ShouldEqual, base+"/nav-idle-oopif.html")

			_, err = doJSON[actionResult](m, tab, "wait", map[string]any{"load": "networkidle"}, expectTimeout)
			So(err, ShouldBeNil)
		})

		Convey("连接被拒返回 NAVIGATION_FAILED 并带 Chrome 的错误文本", func() {
			dead := httptest.NewServer(http.NotFoundHandler())
			deadURL := dead.URL
			dead.Close()
			_, err := navigate(m, tab, map[string]any{"action": "goto", "url": deadURL + "/"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNavigationFailed)
			So(err.Error(), ShouldContainSubstring, "ERR_CONNECTION_REFUSED")
		})

		Convey("HTTP 404 不是失败,结果带状态码", func() {
			res, err := goTo("/missing.html", "")
			So(err, ShouldBeNil)
			So(res.HTTPStatus, ShouldNotBeNil)
			So(*res.HTTPStatus, ShouldEqual, 404)
			So(res.URL, ShouldEqual, base+"/missing.html")
		})

		Convey("204 响应不替换文档:goto 照常返回,没有导航", func() {
			res, err := goTo("/nocontent", "")
			So(err, ShouldBeNil)
			So(res.Navigated, ShouldBeFalse)
			So(res.URL, ShouldEqual, base+"/nav-a.html")
		})

		Convey("只改锚点的 goto 是文档内导航:立即返回,引用保持有效", func() {
			res, err := goTo("/nav-a.html#section", "")
			So(err, ShouldBeNil)
			So(res.Navigated, ShouldBeTrue)
			So(res.URL, ShouldEqual, base+"/nav-a.html#section")
		})

		Convey("没有历史时 back 与 forward 返回 NOT_FOUND", func() {
			fresh := chrome.NewTab(t, "about:blank")
			_, err := navigate(m, fresh, map[string]any{"action": "back"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
			_, err = navigate(m, fresh, map[string]any{"action": "forward"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("back 与 forward 在历史里前后移动", func() {
			_, err := goTo("/nav-b.html", "")
			So(err, ShouldBeNil)
			res, err := navigate(m, tab, map[string]any{"action": "back"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.URL, ShouldEqual, base+"/nav-a.html")
			So(res.Navigated, ShouldBeTrue)
			So(evalString(m, tab, `document.title`), ShouldEqual, "nav a")

			res, err = navigate(m, tab, map[string]any{"action": "forward"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.URL, ShouldEqual, base+"/nav-b.html")
			_, err = navigate(m, tab, map[string]any{"action": "forward"}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("reload 重新加载当前文档", func() {
			_, err := eval(m, tab, `window.marker = 1`)
			So(err, ShouldBeNil)
			res, err := navigate(m, tab, map[string]any{"action": "reload"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.URL, ShouldEqual, base+"/nav-a.html")
			So(res.Navigated, ShouldBeTrue)
			So(res.HTTPStatus, ShouldNotBeNil)
			So(evalString(m, tab, `typeof window.marker`), ShouldEqual, "undefined")
		})

		Convey("导航之后旧快照的引用返回 STALE_REF", func() {
			raw, err := m.Do(context.Background(), page.Request{Action: "snapshot", TabID: &tab, Timeout: callTimeout, Input: json.RawMessage(`{}`)})
			So(err, ShouldBeNil)
			found := regexp.MustCompile(`ref=(e\d+)`).FindStringSubmatch(string(raw))
			So(found, ShouldHaveLength, 2)
			_, err = goTo("/nav-b.html", "")
			So(err, ShouldBeNil)
			_, err = act(m, tab, "click", map[string]any{"ref": found[1]}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeStaleRef)
		})
	})
}

func TestWaitInChrome(t *testing.T) {
	chrome := pagetest.Start(t)
	base := serveNav(t)

	Convey("wait 在真 Chrome 的后台标签页上", t, func() {
		m := page.NewManager(chrome, zap.NewNop())
		chrome.SetListener(m)
		tab := openInBackground(t, chrome, m, base+"/wait.html")
		// 从这里开始计时:fixture 在 400 ms 后才改变页面,wait 返回时页面必须已经改变。
		wait := func(input map[string]any, timeout time.Duration) (actionResult, error) {
			return act(m, tab, "wait", input, timeout)
		}
		settled := func() string { return evalString(m, tab, `String(!!document.getElementById("late"))`) }

		Convey("text 等可见文本出现,不把隐藏文本算作出现", func() {
			res, err := wait(map[string]any{"text": "Late arrival"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.TabID, ShouldEqual, tab)
			So(settled(), ShouldEqual, "true")

			_, err = wait(map[string]any{"text": "Never shown"}, 1500*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `text "Never shown"`)
		})

		Convey("gone 等文本消失(移除或隐藏)", func() {
			_, err := wait(map[string]any{"gone": "Going soon"}, callTimeout)
			So(err, ShouldBeNil)
			So(settled(), ShouldEqual, "true")
			_, err = wait(map[string]any{"gone": "Hides itself"}, callTimeout)
			So(err, ShouldBeNil)

			_, err = wait(map[string]any{"gone": "Late arrival"}, 1500*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `text "Late arrival"`)
		})

		Convey("selectorGone 等选择器对应的元素消失", func() {
			_, err := wait(map[string]any{"selectorGone": "#going"}, callTimeout)
			So(err, ShouldBeNil)
			So(settled(), ShouldEqual, "true")

			_, err = wait(map[string]any{"selectorGone": "#late"}, 1500*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `selector "#late"`)
		})

		Convey("selector 等元素可见,隐藏的元素不算", func() {
			_, err := wait(map[string]any{"selector": "#reveal"}, callTimeout)
			So(err, ShouldBeNil)
			So(settled(), ShouldEqual, "true")

			_, err = wait(map[string]any{"selector": "#nothing-here"}, 1500*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `selector "#nothing-here"`)
		})

		Convey("非法选择器返回 INVALID_REQUEST 而不是等到超时", func() {
			_, err := wait(map[string]any{"selector": "#["}, callTimeout)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("url 等地址包含子串,超时消息带当前地址", func() {
			res, err := wait(map[string]any{"url": "done=1"}, callTimeout)
			So(err, ShouldBeNil)
			So(res.URL, ShouldEqual, base+"/wait.html?done=1")

			_, err = wait(map[string]any{"url": "never-in-url"}, 1500*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, `URL to contain "never-in-url"`)
			So(err.Error(), ShouldContainSubstring, "wait.html")
		})

		Convey("load 各状态在页面已经到达时立即返回", func() {
			for _, state := range []string{"domcontentloaded", "load", "networkidle"} {
				_, err := wait(map[string]any{"load": state}, callTimeout)
				So(err, ShouldBeNil)
			}
		})

		Convey("load 等到页面到达状态,超时消息写明状态", func() {
			_, err := navigate(m, tab, map[string]any{"action": "goto", "url": base + "/nav-load.html", "wait": "domcontentloaded"}, callTimeout)
			So(err, ShouldBeNil)
			_, err = wait(map[string]any{"load": "load"}, 300*time.Millisecond)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			So(err.Error(), ShouldContainSubstring, "load")
			_, err = wait(map[string]any{"load": "load"}, callTimeout)
			So(err, ShouldBeNil)
			So(evalString(m, tab, `document.readyState`), ShouldEqual, "complete")
		})
	})
}
