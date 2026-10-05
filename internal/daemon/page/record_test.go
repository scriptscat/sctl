package page

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func (f *fakeCDP) recordCalls() []recordCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.records)
}

// statusView 是 debug status 与 debug start 结果里一个标签页的 JSON 形状,测试据此钉住字段名。
type statusView struct {
	TabID       int        `json:"tabId"`
	AttachedAt  time.Time  `json:"attachedAt"`
	Recording   bool       `json:"recording"`
	RemainingMs *int64     `json:"remainingMs"`
	Console     countsView `json:"console"`
	Network     countsView `json:"network"`
}

type countsView struct {
	Records int    `json:"records"`
	Dropped uint64 `json:"dropped"`
}

type stopView struct {
	TabID  *int  `json:"tabId"`
	TabIDs []int `json:"tabIds"`
}

func debugStart(m *Manager, tab int) (statusView, error) {
	raw, err := m.Do(context.Background(), Request{Action: "debug.start", TabID: &tab})
	if err != nil {
		return statusView{}, err
	}
	var v statusView
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v, nil
}

func debugStop(m *Manager, req Request) (stopView, error) {
	req.Action = "debug.stop"
	raw, err := m.Do(context.Background(), req)
	if err != nil {
		return stopView{}, err
	}
	var v stopView
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v, nil
}

func debugStatus(m *Manager, req Request) ([]statusView, error) {
	req.Action = "debug.status"
	raw, err := m.Do(context.Background(), req)
	if err != nil {
		return nil, err
	}
	var v struct {
		Tabs []statusView `json:"tabs"`
	}
	So(json.Unmarshal(raw, &v), ShouldBeNil)
	return v.Tabs, nil
}

// queued 返回持有或等待 tab 队列的数量。
func queued(m *Manager, tab int) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s := m.slots[tabKey{testInstance, tab}]; s != nil {
		return s.refs
	}
	return 0
}

func TestDebugRecording(t *testing.T) {
	Convey("debug start/stop 的录制", t, func() {
		m, _, cdp, clock := newDebugManager()

		Convey("debug start 附加未附加的标签页并让扩展开始录制,结果写明录制中与剩余 60 分钟", func() {
			v, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, attachSequence)
			So(cdp.recordCalls(), ShouldResemble, []recordCall{{tabID: 3, on: true}})
			So(v.TabID, ShouldEqual, 3)
			So(v.Recording, ShouldBeTrue)
			So(v.AttachedAt.IsZero(), ShouldBeFalse)
			So(*v.RemainingMs, ShouldEqual, time.Hour.Milliseconds())
		})

		Convey("录制中的标签页空闲超过 5 分钟也不断开,其他命令之后同样不布置空闲断开", func() {
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			clock.Advance(idleTimeout)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			clock.Advance(30 * time.Minute)
			So(cdp.detachCalls(), ShouldBeEmpty)
		})

		Convey("录制期间的查询结果带 recording 为 true", func() {
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Recording, ShouldBeTrue)
		})

		Convey("对已在录制的标签页再次 debug start 也成功,不清空缓存、不重新附加", func() {
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("kept")))
			v, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			So(v.Recording, ShouldBeTrue)
			So(v.Console.Records, ShouldEqual, 1)
			So(cdp.methods(3), ShouldResemble, attachSequence)
		})

		Convey("无法附加的页面返回 PAGE_NOT_AUTOMATABLE,不开始录制", func() {
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				return nil, &Error{Code: generated.ErrorCodePageNotAutomatable, Message: "cannot attach to chrome:// pages"}
			})
			_, err := debugStart(m, 3)
			So(errorCode(err), ShouldEqual, generated.ErrorCodePageNotAutomatable)
			So(cdp.recordCalls(), ShouldBeEmpty)
		})

		Convey("JS 弹框打开时 debug start 照常执行", func() {
			openDialog(m, 3, "alert", "hi")
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
		})

		Convey("debug stop 结束录制、保留缓存,之后空闲 5 分钟断开", func() {
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("kept")))
			res, err := debugStop(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(*res.TabID, ShouldEqual, 3)
			So(res.TabIDs, ShouldResemble, []int{3})
			So(cdp.recordCalls(), ShouldResemble, []recordCall{{tabID: 3, on: true}, {tabID: 3, on: false}})
			v, err := debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			So(v.Recording, ShouldBeFalse)
			So(texts(v), ShouldResemble, []string{"kept"})
			clock.Advance(idleTimeout - time.Second)
			So(cdp.detachCalls(), ShouldBeEmpty)
			clock.Advance(time.Second)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
		})

		Convey("debug stop 不附加调试器:目标未附加或不在录制时也成功,只是什么都不做", func() {
			res, err := debugStop(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(*res.TabID, ShouldEqual, 3)
			So(res.TabIDs, ShouldBeEmpty)
			So(cdp.methods(3), ShouldBeEmpty)
			_, err = debugConsole(m, 3, `{}`)
			So(err, ShouldBeNil)
			res, err = debugStop(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(res.TabIDs, ShouldBeEmpty)
			So(cdp.methods(3), ShouldResemble, attachSequence)
			res, err = debugStop(m, Request{})
			So(err, ShouldBeNil)
			So(*res.TabID, ShouldEqual, 7)
			So(res.TabIDs, ShouldBeEmpty)
			So(cdp.recordCalls(), ShouldBeEmpty)
			So(cdp.methods(7), ShouldBeEmpty)
		})

		Convey("debug stop --all 结束这个浏览器里全部录制中的标签页,不附加其他标签页", func() {
			for _, tab := range []int{3, 5} {
				_, err := debugStart(m, tab)
				So(err, ShouldBeNil)
			}
			_, err := debugConsole(m, 4, `{}`)
			So(err, ShouldBeNil)
			res, err := debugStop(m, Request{Input: json.RawMessage(`{"all":true}`)})
			So(err, ShouldBeNil)
			So(res.TabID, ShouldBeNil)
			So(res.TabIDs, ShouldResemble, []int{3, 5})
			So(cdp.recordCalls()[2:], ShouldResemble, []recordCall{{tabID: 3, on: false}, {tabID: 5, on: false}})
			So(cdp.methods(7), ShouldBeEmpty)
			clock.Advance(idleTimeout)
			So(cdp.detachCalls(), ShouldHaveLength, 3)
		})

		Convey("debug stop 的 --all 与 --tab 不能同时给,也不接受 --activate", func() {
			_, err := debugStop(m, Request{TabID: tabRef(3), Input: json.RawMessage(`{"all":true}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			_, err = debugStop(m, Request{TabID: tabRef(3), Activate: true})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})
	})
}

func TestDebugRecordingAutoEnd(t *testing.T) {
	Convey("录制中的标签页连续 60 分钟没有 debug 命令时自动结束录制", t, func() {
		m, pg, cdp, clock := newDebugManager()
		_, err := debugStart(m, 3)
		So(err, ShouldBeNil)

		Convey("满 60 分钟结束录制并让扩展停止录制,之后同 debug stop:空闲 5 分钟断开", func() {
			clock.Advance(time.Hour - time.Second)
			So(cdp.recordCalls(), ShouldHaveLength, 1)
			clock.Advance(time.Second)
			So(cdp.recordCalls(), ShouldResemble, []recordCall{{tabID: 3, on: true}, {tabID: 3, on: false}})
			So(cdp.detachCalls(), ShouldBeEmpty)
			clock.Advance(idleTimeout)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{3}})
		})

		Convey("每条 debug 命令重新计时,页面命令不算", func() {
			for _, query := range []func() error{
				func() error { _, err := debugConsole(m, 3, `{}`); return err },
				func() error {
					_, err := m.Do(context.Background(), Request{Action: "debug.network", TabID: tabRef(3)})
					return err
				},
				func() error {
					_, err := m.Do(context.Background(), Request{Action: "debug.clear", TabID: tabRef(3)})
					return err
				},
				func() error { _, err := debugStart(m, 3); return err },
			} {
				clock.Advance(59 * time.Minute)
				So(query(), ShouldBeNil)
			}
			clock.Advance(59 * time.Minute)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.recordCalls(), ShouldHaveLength, 2)
			clock.Advance(time.Minute)
			So(cdp.recordCalls(), ShouldHaveLength, 3)
			So(cdp.recordCalls()[2], ShouldResemble, recordCall{tabID: 3, on: false})
		})

		Convey("debug status 报告剩余时间,本身不重新计时", func() {
			clock.Advance(20 * time.Minute)
			tabs, err := debugStatus(m, Request{})
			So(err, ShouldBeNil)
			So(*tabs[0].RemainingMs, ShouldEqual, (40 * time.Minute).Milliseconds())
			clock.Advance(40 * time.Minute)
			tabs, err = debugStatus(m, Request{})
			So(err, ShouldBeNil)
			So(tabs[0].Recording, ShouldBeFalse)
			So(tabs[0].RemainingMs, ShouldBeNil)
		})

		Convey("到期时已排队的 debug 命令先执行并重新计时:晚到的到期作废,录制继续", func() {
			gate := make(chan struct{})
			pg.onCommand = func(cmd Command) {
				if cmd.Method == probeMethod {
					<-gate
				}
			}
			probeDone := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(3)}); probeDone <- err }()
			So(eventually(func() bool { return queued(m, 3) == 1 }), ShouldBeTrue)
			queryDone := make(chan error, 1)
			go func() {
				_, err := m.Do(context.Background(), Request{Action: "debug.console", TabID: tabRef(3)})
				queryDone <- err
			}()
			So(eventually(func() bool { return queued(m, 3) == 2 }), ShouldBeTrue)
			advanced := make(chan struct{})
			go func() { clock.Advance(time.Hour); close(advanced) }()
			So(eventually(func() bool { return queued(m, 3) == 3 }), ShouldBeTrue)
			close(gate)
			So(<-probeDone, ShouldBeNil)
			So(<-queryDone, ShouldBeNil)
			<-advanced
			So(cdp.recordCalls(), ShouldHaveLength, 1)
			tabs, err := debugStatus(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(tabs[0].Recording, ShouldBeTrue)
			clock.Advance(time.Hour)
			So(cdp.recordCalls(), ShouldHaveLength, 2)
		})
	})
}

func TestDebugRecordingEndsOnDetach(t *testing.T) {
	Convey("调试器断开时录制随之结束", t, func() {
		m, _, cdp, clock := newDebugManager()
		detaches := map[string]func(){
			"debugger.detached": func() {
				m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"canceled_by_user"}`))
			},
			"page detach": func() {
				_, err := m.Do(context.Background(), Request{Action: "detach", TabID: tabRef(3)})
				So(err, ShouldBeNil)
			},
			"page detach --all": func() {
				_, err := m.Do(context.Background(), Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
				So(err, ShouldBeNil)
			},
			"浏览器实例断开": func() { m.OnInstanceGone(testInstance) },
		}
		for name, detach := range detaches {
			Convey(name+":重新附加后不在录制,空闲 5 分钟断开;旧的 60 分钟计时不再对它生效", func() {
				_, err := debugStart(m, 3)
				So(err, ShouldBeNil)
				clock.Advance(10 * time.Minute)
				detach()
				detachesBefore := len(cdp.detachCalls())
				v, err := debugConsole(m, 3, `{}`)
				So(err, ShouldBeNil)
				So(v.Recording, ShouldBeFalse)
				clock.Advance(idleTimeout)
				So(cdp.detachCalls(), ShouldHaveLength, detachesBefore+1)
				_, err = debugStart(m, 3)
				So(err, ShouldBeNil)
				clock.Advance(50 * time.Minute)
				tabs, err := debugStatus(m, Request{TabID: tabRef(3)})
				So(err, ShouldBeNil)
				So(tabs[0].Recording, ShouldBeTrue)
				So(cdp.recordCalls(), ShouldResemble, []recordCall{{tabID: 3, on: true}, {tabID: 3, on: true}})
			})
		}
	})
}

func TestDebugStatus(t *testing.T) {
	Convey("debug status", t, func() {
		m, _, cdp, _ := newDebugManager()
		_, err := debugStart(m, 3)
		So(err, ShouldBeNil)
		for range 3 {
			emit(m, 3, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("x")))
		}
		emit(m, 3, "", "Network.requestWillBeSent", sent("r1", "GET", "https://app.test/api", "Fetch", 0))
		_, err = debugConsole(m, 5, `{}`)
		So(err, ShouldBeNil)
		for range debugBufferSize + 2 {
			emit(m, 5, "", "Runtime.consoleAPICalled", consoleCall("log", 0, str("y")))
		}

		Convey("列出这个浏览器里被附加的标签页:录制状态、附加时间、各缓存条数与丢弃数,按标签页排列", func() {
			tabs, err := debugStatus(m, Request{})
			So(err, ShouldBeNil)
			So(tabs, ShouldHaveLength, 2)
			So(tabs[0].TabID, ShouldEqual, 3)
			So(tabs[0].Recording, ShouldBeTrue)
			So(tabs[0].AttachedAt.IsZero(), ShouldBeFalse)
			So(tabs[0].Console, ShouldResemble, countsView{Records: 3})
			So(tabs[0].Network, ShouldResemble, countsView{Records: 1})
			So(tabs[1].TabID, ShouldEqual, 5)
			So(tabs[1].Recording, ShouldBeFalse)
			So(tabs[1].RemainingMs, ShouldBeNil)
			So(tabs[1].Console, ShouldResemble, countsView{Records: debugBufferSize, Dropped: 2})
		})

		Convey("--tab 只列这一个;它没有被附加时列表为空,也不附加它", func() {
			tabs, err := debugStatus(m, Request{TabID: tabRef(5)})
			So(err, ShouldBeNil)
			So(tabs, ShouldHaveLength, 1)
			So(tabs[0].TabID, ShouldEqual, 5)
			tabs, err = debugStatus(m, Request{TabID: tabRef(9)})
			So(err, ShouldBeNil)
			So(tabs, ShouldBeEmpty)
			So(cdp.methods(9), ShouldBeEmpty)
		})

		Convey("断开的标签页不再列出", func() {
			m.OnNotification(testInstance, "debugger.detached", json.RawMessage(`{"tabId":3,"reason":"target_closed"}`))
			tabs, err := debugStatus(m, Request{})
			So(err, ShouldBeNil)
			So(tabs, ShouldHaveLength, 1)
			So(tabs[0].TabID, ShouldEqual, 5)
		})

		Convey("不接受 --activate 与输入字段", func() {
			_, err := debugStatus(m, Request{Activate: true})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
			_, err = debugStatus(m, Request{Input: json.RawMessage(`{"all":true}`)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})
	})
}
