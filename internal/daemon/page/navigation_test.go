package page

import (
	"context"
	"encoding/json"
	"sync"
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

// orphanedPage 模拟页面上留着一个孤儿弹框(真机探针):新会话上的附加命令一直不回应,只有导航或重新加载能关掉它;
// 关掉之后页面照常回应。recovers 为 false 时导航也不回应。
type orphanedPage struct {
	nav      *navFake
	m        *Manager
	recovers bool
	mu       sync.Mutex
	orphan   bool
}

func (p *orphanedPage) send(ctx context.Context, cmd Command) (json.RawMessage, error) {
	p.mu.Lock()
	orphan := p.orphan
	navigation := cmd.Method == "Page.navigate" || cmd.Method == "Page.reload"
	if orphan && navigation && p.recovers {
		p.orphan = false
		p.mu.Unlock()
		return json.RawMessage(`{}`), nil
	}
	p.mu.Unlock()
	if orphan {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if cmd.Method == "Page.reload" {
		emit(p.m, cmd.TabID, "", "Page.frameNavigated", map[string]any{"frame": map[string]any{"id": "main", "url": "https://example.test/"}})
		emit(p.m, cmd.TabID, "", "Page.loadEventFired", map[string]any{"timestamp": 1})
	}
	return p.nav.send(ctx, cmd)
}

func newOrphanedPage(recovers bool) (*Manager, *fakeCDP) {
	cdp := newFakeCDP()
	m := newTestManager(cdp, &fakeClock{})
	m.attachResponseTimeout = 200 * time.Millisecond
	p := &orphanedPage{nav: &navFake{deadlines: map[string]time.Time{}, navigate: `{"frameId":"main","loaderId":"L","errorText":"net::ERR_ABORTED"}`}, m: m, recovers: recovers, orphan: true}
	cdp.setSend(p.send)
	return m, cdp
}

// navigations 返回发给标签页 7 的导航命令与附加的第一条命令,按发送顺序。
func navigations(cdp *fakeCDP) []string {
	var out []string
	for _, method := range cdp.methods(7) {
		if method == "Page.navigate" || method == "Page.reload" || method == attachSequence[0] {
			out = append(out, method)
		}
	}
	return out
}

func TestNavigationRecoversUnresponsivePage(t *testing.T) {
	Convey("页面留着孤儿弹框、附加没有回应时,page goto 与 page reload 能恢复它(spec §JS 弹框)", t, func() {
		Convey("goto:放弃附加后在新会话上先发出这次导航关掉弹框,再重新附加并照常导航", func() {
			m, cdp := newOrphanedPage(true)
			_, err := doAction(m, "navigate", `{"action":"goto","url":"https://example.test/"}`, 30*time.Second)
			So(err, ShouldBeNil)
			So(navigations(cdp), ShouldResemble, []string{attachSequence[0], "Page.navigate", attachSequence[0], "Page.navigate"})
			So(cdp.detachCalls(), ShouldResemble, [][]int{{7}, {7}})
			_, err = doAction(m, "eval", `{"expression":"1"}`, time.Second)
			So(err, ShouldBeNil)
		})

		Convey("reload:同样先重新加载关掉弹框", func() {
			m, cdp := newOrphanedPage(true)
			_, err := doAction(m, "navigate", `{"action":"reload"}`, 30*time.Second)
			So(err, ShouldBeNil)
			So(navigations(cdp), ShouldResemble, []string{attachSequence[0], "Page.reload", attachSequence[0], "Page.reload"})
		})

		Convey("back、forward 与其他命令不导航,仍返回 PAGE_UNRESPONSIVE", func() {
			m, cdp := newOrphanedPage(true)
			_, err := doAction(m, "navigate", `{"action":"back"}`, 30*time.Second)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageUnresponsive)
			_, err = doAction(m, "eval", `{"expression":"1"}`, 30*time.Second)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageUnresponsive)
			So(navigations(cdp), ShouldResemble, []string{attachSequence[0], attachSequence[0]})
		})

		Convey("输入不合法时返回 INVALID_REQUEST,不发出导航", func() {
			m, cdp := newOrphanedPage(true)
			_, err := doAction(m, "navigate", `{"action":"goto"}`, 30*time.Second)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(navigations(cdp), ShouldResemble, []string{attachSequence[0]})
		})

		Convey("导航也没能让页面回应时返回 PAGE_UNRESPONSIVE,不留下附加", func() {
			m, cdp := newOrphanedPage(false)
			_, err := doAction(m, "navigate", `{"action":"goto","url":"https://example.test/"}`, 30*time.Second)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageUnresponsive)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{7}, {7}})
			cdp.mu.Lock()
			attached := cdp.attached[7]
			cdp.mu.Unlock()
			So(attached, ShouldBeFalse)
		})
	})
}
