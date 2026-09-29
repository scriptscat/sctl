package page

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

// navFake 是只有一条历史记录的假页面;deadlines 记下每个方法收到命令时的 ctx 期限。
type navFake struct {
	deadlines map[string]time.Time
	navigate  string
}

func (p *navFake) send(ctx context.Context, cmd Command) (json.RawMessage, error) {
	if d, ok := ctx.Deadline(); ok {
		p.deadlines[cmd.Method] = d
	}
	switch cmd.Method {
	case "Page.getFrameTree":
		return json.RawMessage(`{"frameTree":{"frame":{"id":"main"}}}`), nil
	case "Page.getNavigationHistory":
		return json.RawMessage(`{"currentIndex":0,"entries":[{"id":1,"url":"https://example.test/","title":"Example"}]}`), nil
	case "Page.navigate":
		return json.RawMessage(p.navigate), nil
	}
	return json.RawMessage(`{}`), nil
}

func newNavManager(navigate string) (*Manager, *navFake, *fakeCDP) {
	p := &navFake{deadlines: map[string]time.Time{}, navigate: navigate}
	cdp := newFakeCDP()
	cdp.setSend(p.send)
	return newTestManager(cdp, &fakeClock{}), p, cdp
}

func TestNavigateInput(t *testing.T) {
	Convey("navigate 与 wait 在边界上校验输入,非法输入不发出导航命令", t, func() {
		m, _, cdp := newNavManager(`{}`)
		cases := map[string][]string{
			"navigate": {
				`{}`,
				`{"action":"jump"}`,
				`{"action":"goto"}`,
				`{"action":"goto","url":""}`,
				`{"action":"back","url":"https://example.test/"}`,
				`{"action":"goto","url":"https://example.test/","wait":"idle"}`,
				`{"action":"reload","extra":1}`,
			},
			"wait": {
				`{}`,
				`{"text":"a","gone":"b"}`,
				`{"text":"a","url":"b"}`,
				`{"selector":"#a","selectorGone":"#b"}`,
				`{"load":"idle"}`,
				`{"text":""}`,
			},
		}
		for action, inputs := range cases {
			for _, input := range inputs {
				_, err := doAction(m, action, input, time.Second)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			}
		}
		for _, method := range cdp.methods(7) {
			So(method, ShouldNotBeIn, "Page.navigate", "Page.reload", "Page.navigateToHistoryEntry")
		}
	})
}

func TestNavigateFailsWithChromesErrorText(t *testing.T) {
	Convey("Page.navigate 报告 errorText 时 goto 返回 NAVIGATION_FAILED 并带上原文", t, func() {
		m, _, _ := newNavManager(`{"frameId":"main","loaderId":"L","errorText":"net::ERR_NAME_NOT_RESOLVED"}`)
		_, err := doAction(m, "navigate", `{"action":"goto","url":"https://nope.invalid/"}`, time.Second)
		So(errorCode(err), ShouldEqual, generated.ErrorCodeNavigationFailed)
		So(err.Error(), ShouldContainSubstring, "net::ERR_NAME_NOT_RESOLVED")
	})
}

func TestNavigateDefaultTimeouts(t *testing.T) {
	remaining := func(p *navFake, method string) time.Duration {
		d, ok := p.deadlines[method]
		So(ok, ShouldBeTrue)
		return time.Until(d)
	}

	Convey("导航默认 30 秒,wait 默认 10 秒,--timeout 覆盖两者", t, func() {
		m, p, _ := newNavManager(`{"frameId":"main","loaderId":"L","errorText":"net::ERR_FAILED"}`)
		_, _ = m.Do(context.Background(), Request{Action: "navigate", TabID: tabRef(7), Input: json.RawMessage(`{"action":"goto","url":"https://example.test/"}`)})
		So(remaining(p, "Page.navigate"), ShouldBeBetween, 29*time.Second, 30*time.Second+time.Millisecond)

		_, err := m.Do(context.Background(), Request{Action: "wait", TabID: tabRef(7), Input: json.RawMessage(`{"url":"example.test"}`)})
		So(err, ShouldBeNil)
		So(remaining(p, "Page.getFrameTree"), ShouldBeBetween, 9*time.Second, 10*time.Second+time.Millisecond)

		_, _ = m.Do(context.Background(), Request{Action: "navigate", TabID: tabRef(7), Timeout: 5 * time.Second, Input: json.RawMessage(`{"action":"goto","url":"https://example.test/"}`)})
		So(remaining(p, "Page.navigate"), ShouldBeBetween, 4*time.Second, 5*time.Second+time.Millisecond)
	})
}

func TestWaitConditionBrokenByThePage(t *testing.T) {
	Convey("页面改坏了求值环境、等待条件的脚本抛异常时,返回带页面异常的 INTERNAL_ERROR 而不是笼统的内部错误", t, func() {
		p := &navFake{deadlines: map[string]time.Time{}}
		cdp := newFakeCDP()
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			if cmd.Method == "Runtime.evaluate" {
				return json.RawMessage(`{"result":{"type":"object"},"exceptionDetails":{"text":"Uncaught","exception":{"type":"object","description":"TypeError: s.replace is not a function"}}}`), nil
			}
			return p.send(ctx, cmd)
		})
		m := newTestManager(cdp, &fakeClock{})
		for _, input := range []string{`{"text":"ready"}`, `{"load":"load"}`} {
			_, err := doAction(m, "wait", input, time.Second)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInternalError)
			So(err.Error(), ShouldContainSubstring, "TypeError: s.replace is not a function")
		}
	})
}
