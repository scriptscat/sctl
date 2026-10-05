package page

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

const testInstance = "0123456789abcdef0123456789abcdef"

// sentCommand 是假 CDP 收到的一条命令。
type sentCommand struct {
	instanceID string
	Command
}

// fakeCDP 模拟一个在线浏览器实例:记录收到的命令、激活与断开,命令结果由 send 决定。
type fakeCDP struct {
	mu          sync.Mutex
	current     int
	currentErr  error
	currentHits int
	selected    []int
	sent        []sentCommand
	detaches    [][]int // 每次 Detach 的目标;nil 表示全部
	records     []recordCall
	bodies      []BodyQuery
	// body 为 nil 时取体返回空文本。
	body     func(q BodyQuery) (Body, error)
	attached map[int]bool
	// send 为 nil 时每条命令都成功返回 {}。
	send func(ctx context.Context, cmd Command) (json.RawMessage, error)
}

func newFakeCDP() *fakeCDP {
	return &fakeCDP{current: 7, attached: map[int]bool{}}
}

func (f *fakeCDP) ResolveBrowser(target string) (string, error) {
	if target == "" || target == "chrome" {
		return testInstance, nil
	}
	return "", &Error{Code: generated.ErrorCodeBrowserNotFound, Message: "no paired browser matches " + target}
}

func (f *fakeCDP) CurrentTab(ctx context.Context, instanceID string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentHits++
	return f.current, f.currentErr
}

func (f *fakeCDP) Tabs(ctx context.Context, instanceID string) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []int{f.current}, nil
}

func (f *fakeCDP) SelectTab(ctx context.Context, instanceID string, tabID int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.selected = append(f.selected, tabID)
	return nil
}

func (f *fakeCDP) Send(ctx context.Context, instanceID string, cmd Command) (json.RawMessage, error) {
	f.mu.Lock()
	f.sent = append(f.sent, sentCommand{instanceID, cmd})
	send := f.send
	f.mu.Unlock()
	if send != nil {
		res, err := send(ctx, cmd)
		if err == nil {
			f.markAttached(cmd.TabID)
		}
		return res, err
	}
	f.markAttached(cmd.TabID)
	return json.RawMessage(`{}`), nil
}

func (f *fakeCDP) markAttached(tabID int) {
	f.mu.Lock()
	f.attached[tabID] = true
	f.mu.Unlock()
}

type recordCall struct {
	tabID int
	on    bool
}

func (f *fakeCDP) Record(ctx context.Context, instanceID string, tabID int, on bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.records = append(f.records, recordCall{tabID: tabID, on: on})
	return nil
}

func (f *fakeCDP) Body(ctx context.Context, instanceID string, q BodyQuery) (Body, error) {
	f.mu.Lock()
	f.bodies = append(f.bodies, q)
	body := f.body
	f.mu.Unlock()
	if body == nil {
		return Body{}, nil
	}
	return body(q)
}

func (f *fakeCDP) bodyCalls() []BodyQuery {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.bodies)
}

func (f *fakeCDP) Detach(ctx context.Context, instanceID string, tabID *int) ([]int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var detached []int
	if tabID == nil {
		f.detaches = append(f.detaches, nil)
		for id := range f.attached {
			detached = append(detached, id)
		}
		slices.Sort(detached)
		clear(f.attached)
		return detached, nil
	}
	f.detaches = append(f.detaches, []int{*tabID})
	if f.attached[*tabID] {
		delete(f.attached, *tabID)
		detached = []int{*tabID}
	}
	return detached, nil
}

// methods 返回发给 tabID 的 CDP 方法名,按发送顺序。
func (f *fakeCDP) methods(tabID int) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.sent {
		if c.TabID == tabID {
			out = append(out, c.Method)
		}
	}
	return out
}

func (f *fakeCDP) detachCalls() [][]int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.detaches)
}

func (f *fakeCDP) setSend(send func(ctx context.Context, cmd Command) (json.RawMessage, error)) {
	f.mu.Lock()
	f.send = send
	f.mu.Unlock()
}

// fakeClock 只在 Advance 时触发到期的计时器,让空闲断开可以确定地测试。
type fakeClock struct {
	mu     sync.Mutex
	now    time.Duration
	timers []*fakeTimer
}

type fakeTimer struct {
	clock *fakeClock
	at    time.Duration
	f     func()
	done  bool
}

func (c *fakeClock) AfterFunc(d time.Duration, f func()) Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := &fakeTimer{clock: c, at: c.now + d, f: f}
	c.timers = append(c.timers, t)
	return t
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	pending := !t.done
	t.done = true
	return pending
}

// Now 从 baseTime 起按 Advance 走动。
func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return baseTime.Add(c.now)
}

func (c *fakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now += d
	var due []*fakeTimer
	for _, t := range c.timers {
		if !t.done && t.at <= c.now {
			t.done = true
			due = append(due, t)
		}
	}
	c.mu.Unlock()
	for _, t := range due {
		t.f()
	}
}

// attachSequence 是附加钩子在标签页顶层会话上依次发出的命令。
var attachSequence = []string{"Emulation.setFocusEmulationEnabled", "Target.setAutoAttach", "Page.enable", "Page.getFrameTree", "Runtime.enable", "Log.enable", "Network.enable"}

// probeMethod 是测试动作发出的 CDP 命令,与附加钩子发出的命令区分开。
const probeMethod = "Probe.run"

// newTestManager 构造一个带测试动作 probe 的 Manager:它在目标标签页上发一条 probeMethod 并返回 tabId。
func newTestManager(cdp CDP, clock Clock) *Manager {
	m := newManager(cdp, zap.NewNop(), 0)
	m.clock = clock
	m.register("probe", func(ctx context.Context, t *Tab, input json.RawMessage) (any, error) {
		if err := t.send(ctx, probeMethod, nil, nil); err != nil {
			return nil, err
		}
		return map[string]int{"tabId": t.ID()}, nil
	})
	return m
}

func tabRef(id int) *int { return &id }

func errorCode(err error) string {
	var pe *Error
	if errors.As(err, &pe) {
		return pe.Code
	}
	return ""
}

func probe(m *Manager, req Request) (json.RawMessage, error) {
	req.Action = "probe"
	return m.Do(context.Background(), req)
}

func TestManagerTargetSelection(t *testing.T) {
	Convey("页面动作的目标标签页", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})

		Convey("未指定标签页时使用最后获得焦点窗口的激活标签页,结果带实际操作的 tabId", func() {
			res, err := probe(m, Request{})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":7}`)
			So(cdp.methods(7), ShouldContain, probeMethod)
		})

		Convey("默认标签页在命令开始时确定一次:命令执行期间用户切换标签页不改变目标", func() {
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				cdp.mu.Lock()
				cdp.current = 99
				cdp.mu.Unlock()
				return json.RawMessage(`{}`), nil
			})
			_, err := probe(m, Request{})
			So(err, ShouldBeNil)
			So(cdp.currentHits, ShouldEqual, 1)
			So(cdp.methods(99), ShouldBeEmpty)
		})

		Convey("指定标签页时不查询默认标签页", func() {
			res, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":3}`)
			So(cdp.currentHits, ShouldEqual, 0)
		})

		Convey("没有可用的默认标签页时返回 NOT_FOUND", func() {
			cdp.currentErr = &Error{Code: generated.ErrorCodeNotFound, Message: "no normal browser window is open"}
			_, err := probe(m, Request{})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeNotFound)
		})

		Convey("浏览器目标解析失败原样返回目标选择错误", func() {
			_, err := probe(m, Request{Browser: "nope"})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserNotFound)
		})

		Convey("未知动作返回 INVALID_REQUEST", func() {
			_, err := m.Do(context.Background(), Request{Action: "teleport"})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("--activate 先让目标成为窗口内的激活标签页再执行动作", func() {
			_, err := probe(m, Request{TabID: tabRef(3), Activate: true})
			So(err, ShouldBeNil)
			So(cdp.selected, ShouldResemble, []int{3})
		})

		Convey("默认不激活标签页", func() {
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.selected, ShouldBeEmpty)
		})
	})
}

func TestManagerAttachSetup(t *testing.T) {
	Convey("附加后开启焦点模拟与跨进程 iframe 的自动附加", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})

		Convey("标签页上的第一条命令先开启焦点模拟与扁平化的自动附加,之后的命令不再重复", func() {
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(slices.Clone(attachSequence), probeMethod, probeMethod))
			cdp.mu.Lock()
			So(string(cdp.sent[0].Params), ShouldEqual, `{"enabled":true}`)
			So(string(cdp.sent[1].Params), ShouldEqual, `{"autoAttach":true,"flatten":true,"waitForDebuggerOnStart":true}`)
			cdp.mu.Unlock()
		})

		Convey("每个标签页各自开启一次", func() {
			_, _ = probe(m, Request{TabID: tabRef(3)})
			_, _ = probe(m, Request{TabID: tabRef(4)})
			So(cdp.methods(4), ShouldResemble, append(slices.Clone(attachSequence), probeMethod))
		})

		Convey("附加被拒时返回 PAGE_NOT_AUTOMATABLE,下一条命令重新尝试附加", func() {
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				return nil, &Error{Code: generated.ErrorCodePageNotAutomatable, Message: "Cannot access a chrome:// URL"}
			})
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageNotAutomatable)
			So(err.Error(), ShouldContainSubstring, "chrome://")

			cdp.setSend(nil)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append([]string{"Emulation.setFocusEmulationEnabled"}, append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("命令在附加钩子全部成功的同时结束时断开调试器,不留下 daemon 不再计时断开的附加", func() {
			ctx, cancel := context.WithCancel(context.Background())
			cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == attachSequence[len(attachSequence)-1] {
					cancel()
				}
				return json.RawMessage(`{}`), nil
			})
			_, err := m.Do(ctx, Request{Action: "probe", TabID: tabRef(3)})
			So(errors.Is(err, context.Canceled), ShouldBeTrue)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
		})
	})
}

func TestManagerSerializesPerTab(t *testing.T) {
	Convey("同一标签页上的命令按到达顺序串行,不同标签页之间并行", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})
		entered := make(chan int, 4)
		gates := map[int]chan struct{}{3: make(chan struct{}), 4: make(chan struct{})}
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			if cmd.Method == probeMethod {
				entered <- cmd.TabID
				<-gates[cmd.TabID]
			}
			return json.RawMessage(`{}`), nil
		})
		done := make(chan error, 3)
		run := func(tab int) { _, err := probe(m, Request{TabID: tabRef(tab)}); done <- err }

		go run(3)
		So(<-entered, ShouldEqual, 3)
		go run(3)
		go run(4)
		// 标签页 4 不必等标签页 3 上的命令。
		So(<-entered, ShouldEqual, 4)
		select {
		case tab := <-entered:
			t.Fatalf("second command on tab %d started while the first was still running", tab)
		case <-time.After(50 * time.Millisecond):
		}
		gates[3] <- struct{}{}
		So(<-done, ShouldBeNil)
		So(<-entered, ShouldEqual, 3)
		close(gates[3])
		close(gates[4])
		So(<-done, ShouldBeNil)
		So(<-done, ShouldBeNil)
	})
}

func TestManagerIdleDetach(t *testing.T) {
	Convey("最近一条命令结束后空闲 5 分钟断开调试器", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)

		Convey("不到 5 分钟不断开", func() {
			clock.Advance(5*time.Minute - time.Second)
			So(cdp.detachCalls(), ShouldBeEmpty)
		})

		Convey("满 5 分钟断开这个标签页,下一条命令重新附加并开启焦点模拟", func() {
			clock.Advance(5 * time.Minute)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("期间的新命令重新计时", func() {
			clock.Advance(4 * time.Minute)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			clock.Advance(4 * time.Minute)
			So(cdp.detachCalls(), ShouldBeEmpty)
			clock.Advance(time.Minute)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
		})
	})
}

func TestManagerDetachNotifications(t *testing.T) {
	Convey("调试器被动分离与实例断开", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)

		Convey("debugger.detached 让该标签页上正在执行的命令返回 DEBUGGER_DETACHED", func() {
			entered := make(chan struct{})
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			done := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(3)}); done <- err }()
			<-entered
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"canceled_by_user"}`))
			err := <-done
			So(errorCode(err), ShouldEqual, generated.ErrorCodeDebuggerDetached)
			So(err.Error(), ShouldContainSubstring, "canceled_by_user")
		})

		Convey("debugger.detached 清空标签页状态:下一条命令重新开启焦点模拟,旧的空闲计时不再断开", func() {
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"target_closed"}`))
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("其他实例或其他标签页的分离通知不影响这个标签页", func() {
			m.OnNotification("fedcba9876543210fedcba9876543210", "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"target_closed"}`))
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":4,"reason":"target_closed"}`))
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(slices.Clone(attachSequence), probeMethod, probeMethod))
		})

		Convey("实例断开清空它的全部标签页状态,空闲计时不再对它发断开", func() {
			m.OnInstanceGone(testInstance)
			clock.Advance(5 * time.Minute)
			So(cdp.detachCalls(), ShouldBeEmpty)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("实例断开时正在执行的命令返回 DEBUGGER_DETACHED", func() {
			entered := make(chan struct{})
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				close(entered)
				<-ctx.Done()
				return nil, ctx.Err()
			})
			done := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(3)}); done <- err }()
			<-entered
			m.OnInstanceGone(testInstance)
			So(errorCode(<-done), ShouldEqual, generated.ErrorCodeDebuggerDetached)
		})
	})
}

func TestManagerTimeout(t *testing.T) {
	Convey("动作在超时内没有完成返回 TIMEOUT", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})
		cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		})
		_, err := probe(m, Request{TabID: tabRef(3), Timeout: 20 * time.Millisecond})
		So(errorCode(err), ShouldEqual, generated.ErrorCodeTimeout)
	})

	Convey("调用方取消时返回取消错误而不是 TIMEOUT", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})
		ctx, cancel := context.WithCancel(context.Background())
		cdp.setSend(func(sendCtx context.Context, cmd Command) (json.RawMessage, error) {
			cancel()
			<-sendCtx.Done()
			return nil, sendCtx.Err()
		})
		_, err := m.Do(ctx, Request{Action: "probe", TabID: tabRef(3)})
		So(errors.Is(err, context.Canceled), ShouldBeTrue)
	})
}

func TestManagerDetachAction(t *testing.T) {
	Convey("page detach", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)
		_, err = probe(m, Request{TabID: tabRef(4)})
		So(err, ShouldBeNil)

		Convey("断开指定标签页并清空它的状态,不附加调试器", func() {
			res, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":3,"tabIds":[3]}`)
			So(cdp.methods(3), ShouldResemble, append(slices.Clone(attachSequence), probeMethod))

			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("未指定标签页时断开默认标签页", func() {
			cdp.current = 4
			res, err := m.Do(context.Background(), Request{Action: "detach"})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":4,"tabIds":[4]}`)
		})

		Convey("目标没有被附加时也成功,只是什么都不做", func() {
			res, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(9)})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabId":9,"tabIds":[]}`)
			So(cdp.methods(9), ShouldBeEmpty)
		})

		Convey("--all 断开这个浏览器里的全部标签页并清空它们的状态", func() {
			res, err := m.Do(context.Background(), Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
			So(err, ShouldBeNil)
			So(string(res), ShouldEqual, `{"tabIds":[3,4]}`)
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
			So(cdp.currentHits, ShouldEqual, 0)

			clock.Advance(5 * time.Minute)
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
			_, err = probe(m, Request{TabID: tabRef(4)})
			So(err, ShouldBeNil)
			So(cdp.methods(4), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("断开不激活标签页:带 --activate 返回 INVALID_REQUEST", func() {
			_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3), Activate: true})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			So(cdp.selected, ShouldBeEmpty)
		})

		Convey("--all 与 --tab 不能同时给", func() {
			_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3), Input: json.RawMessage(`{"all":true}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})
	})
}
