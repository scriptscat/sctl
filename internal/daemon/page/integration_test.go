package page_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/daemon/page"
	"github.com/scriptscat/sctl/internal/daemon/page/pagetest"
	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// startPage 启动 headless Chrome(没有时跳过),打开 fixture 页面,返回连着它的 Manager 与标签页 ID。
func startPage(t *testing.T, fixture string) (*page.Manager, int) {
	t.Helper()
	chrome := pagetest.Start(t)
	base := pagetest.Serve(t, "testdata")
	m := page.NewManager(chrome, zap.NewNop())
	chrome.SetListener(m)
	tab := chrome.NewTab(t, base+"/"+fixture)
	waitLoaded(t, m, tab)
	return m, tab
}

// waitLoaded 等 fixture 页面加载完成;新标签页刚创建时文档可能还是 about:blank。
func waitLoaded(t *testing.T, m *page.Manager, tab int) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		v, err := eval(m, tab, `document.readyState === "complete" && location.protocol === "http:"`)
		if err == nil && string(v) == "true" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("fixture page in tab %d did not finish loading", tab)
}

func eval(m *page.Manager, tab int, expression string) (json.RawMessage, error) {
	raw, err := evalRaw(m, page.Request{TabID: &tab}, expression)
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

func evalRaw(m *page.Manager, req page.Request, expression string) (json.RawMessage, error) {
	input, err := json.Marshal(map[string]string{"expression": expression})
	if err != nil {
		return nil, err
	}
	req.Action = "eval"
	req.Input = input
	return m.Do(context.Background(), req)
}

func codeOf(err error) string {
	var pe *page.Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func TestEvalInChrome(t *testing.T) {
	m, tab := startPage(t, "eval.html")

	Convey("page eval 在真 Chrome 的 fixture 页面上", t, func() {
		Convey("结果带实际操作的 tabId,并标记为不可信的页面内容", func() {
			raw, err := evalRaw(m, page.Request{TabID: &tab}, "document.title")
			So(err, ShouldBeNil)
			var res map[string]any
			So(json.Unmarshal(raw, &res), ShouldBeNil)
			So(res, ShouldResemble, map[string]any{"contentTrust": "untrusted-page-content", "tabId": float64(tab), "value": "eval fixture"})
		})

		Convey("能按 JSON 序列化的结果原样返回", func() {
			cases := map[string]string{
				`window.fixtureAnswer`:                            `42`,
				`document.getElementById("greeting").textContent`: `"hello from the fixture"`,
				`({a: [1, "two", null, true], b: {c: 1.5}})`:      `{"a":[1,"two",null,true],"b":{"c":1.5}}`,
				`null`:  `null`,
				`false`: `false`,
				`let doubled = window.fixtureAnswer * 2; doubled + 1`: `85`,
			}
			for expression, want := range cases {
				v, err := eval(m, tab, expression)
				So(err, ShouldBeNil)
				So(string(v), ShouldEqual, want)
			}
		})

		Convey("返回 Promise 时等它 resolve", func() {
			v, err := eval(m, tab, `new Promise(resolve => setTimeout(() => resolve({done: true}), 50))`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `{"done":true}`)
		})

		Convey("不能按 JSON 序列化的结果返回字符串形式", func() {
			cases := map[string]string{
				`undefined`:                       `"undefined"`,
				`NaN`:                             `"NaN"`,
				`(function greet() { return 1 })`: `"function greet() { return 1 }"`,
				`(() => { const o = {}; o.self = o; return o })()`: `"[object Object]"`,
			}
			for expression, want := range cases {
				v, err := eval(m, tab, expression)
				So(err, ShouldBeNil)
				So(string(v), ShouldEqual, want)
			}
		})

		Convey("页面抛出的异常返回 EVAL_ERROR 并带上异常消息", func() {
			_, err := eval(m, tab, `throw new Error("boom from the page")`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeEvalError)
			So(err.Error(), ShouldContainSubstring, "boom from the page")

			_, err = eval(m, tab, `Promise.reject(new TypeError("rejected in the page"))`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeEvalError)
			So(err.Error(), ShouldContainSubstring, "rejected in the page")

			_, err = eval(m, tab, `this is not javascript`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeEvalError)
			So(err.Error(), ShouldContainSubstring, "SyntaxError")
		})

		Convey("永不 resolve 的 Promise 在超时后返回 TIMEOUT,之后同一标签页仍可用", func() {
			_, err := evalRaw(m, page.Request{TabID: &tab, Timeout: 300 * time.Millisecond}, `new Promise(() => {})`)
			So(codeOf(err), ShouldEqual, generated.ErrorCodeTimeout)
			v, err := eval(m, tab, `1 + 1`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `2`)
		})

		Convey("附加期间后台标签页以为自己可见且有焦点", func() {
			v, err := eval(m, tab, `[document.visibilityState, document.hasFocus()]`)
			So(err, ShouldBeNil)
			So(string(v), ShouldEqual, `["visible",true]`)
		})
	})
}
