package page

import (
	"context"
	"encoding/json"
	"slices"
	"sync"
	"testing"
	"time"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol/generated"
)

func (f *fakeCDP) sentCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent)
}

// pageCommands 是交出期间应被拒绝的 page、debug 与 cdp send 命令,每种至少一个。
var pageCommands = []Request{
	{Action: "probe", TabID: tabRef(3)},
	{Action: "probe"},
	{Action: "cdp.send", TabID: tabRef(3), Input: json.RawMessage(`{"method":"Page.getNavigationHistory"}`)},
	{Action: "debug.console", TabID: tabRef(3)},
	{Action: "debug.start", TabID: tabRef(3)},
	{Action: "debug.stop", Input: json.RawMessage(`{"all":true}`)},
	{Action: "debug.status"},
	{Action: "detach", Input: json.RawMessage(`{"all":true}`)},
	{Action: "detach", TabID: tabRef(3)},
}

func TestHandOver(t *testing.T) {
	Convey("端点客户端连上时 sctl 交出这个浏览器的标签页", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)
		_, err = probe(m, Request{TabID: tabRef(4)})
		So(err, ShouldBeNil)

		Convey("先关闭已知的弹框,再断开这个浏览器里 sctl 附加的全部标签页", func() {
			dismissals := recordDismissals(cdp, nil)
			openDialog(m, 4, "alert", "hi")
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			So(dismissals(), ShouldResemble, []dismissal{{tab: 4, params: `{"accept":false}`, detachesBefore: 0}})
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
		})

		Convey("结束录制并清空调试缓存:录制与空闲计时不再触发,收回后没有附加的标签页", func() {
			_, err := debugStart(m, 3)
			So(err, ShouldBeNil)
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			clock.Advance(recordTimeout)
			So(cdp.recordCalls(), ShouldResemble, []recordCall{{tabID: 3, on: true}})
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})

			m.Reclaim(testInstance)
			tabs, err := debugStatus(m, Request{})
			So(err, ShouldBeNil)
			So(tabs, ShouldBeEmpty)
		})

		Convey("交出后 page、debug、cdp send 命令返回 ENDPOINT_CONNECTED,说明怎样恢复,不向浏览器发出任何东西", func() {
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			sent := cdp.sentCount()
			for _, req := range pageCommands {
				_, err := m.Do(context.Background(), req)
				So(errorCode(err), ShouldEqual, generated.ErrorCodeEndpointConnected)
				So(err.Error(), ShouldContainSubstring, "endpoint client")
				So(err.Error(), ShouldContainSubstring, "sctl cdp close")
			}
			So(cdp.sentCount(), ShouldEqual, sent)
			So(cdp.currentHits, ShouldEqual, 0)
			So(cdp.recordCalls(), ShouldBeEmpty)
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
		})

		Convey("输入不合法的命令仍先返回 INVALID_REQUEST", func() {
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			_, err := cdpSend(m, 3, `{"method":"Page.disable"}`)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeInvalidRequest)
		})

		Convey("收回后 sctl 命令恢复,从头重新附加", func() {
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			m.Reclaim(testInstance)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(append(slices.Clone(attachSequence), probeMethod), append(slices.Clone(attachSequence), probeMethod)...))
		})

		Convey("交出另一个浏览器不影响这个浏览器", func() {
			So(m.HandOver(context.Background(), "fedcba9876543210fedcba9876543210"), ShouldBeNil)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			So(cdp.methods(3), ShouldResemble, append(slices.Clone(attachSequence), probeMethod, probeMethod))
		})

		Convey("断开失败时不交出:返回错误,sctl 命令照常执行", func() {
			cdp.mu.Lock()
			cdp.detachErr = &Error{Code: generated.ErrorCodeBrowserOffline, Message: "offline"}
			cdp.mu.Unlock()
			err := m.HandOver(context.Background(), testInstance)
			So(errorCode(err), ShouldEqual, generated.ErrorCodeBrowserOffline)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
		})

		Convey("已交出时再次交出返回错误,不再断开;没交出时收回什么都不做", func() {
			m.Reclaim(testInstance)
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			So(m.HandOver(context.Background(), testInstance), ShouldNotBeNil)
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
			m.Reclaim(testInstance)
			m.Reclaim(testInstance)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
		})

		Convey("浏览器断开不改变交出状态:重新连上后 sctl 命令仍被拒绝,直到收回", func() {
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			m.OnInstanceGone(testInstance)
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(errorCode(err), ShouldEqual, generated.ErrorCodeEndpointConnected)
			m.Reclaim(testInstance)
			_, err = probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
		})
	})
}

func TestHandOverEndsCommandsInFlight(t *testing.T) {
	Convey("交出时正在执行的命令干净地结束,交出完成后 sctl 不再向这个浏览器发命令", t, func() {
		cdp := newFakeCDP()
		m := newTestManager(cdp, &fakeClock{})

		Convey("执行中与排队中的命令返回 ENDPOINT_CONNECTED", func() {
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			entered := make(chan struct{}, 2)
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == probeMethod {
					entered <- struct{}{}
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return json.RawMessage(`{}`), nil
			})
			running := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(3)}); running <- err }()
			<-entered
			waiting := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(3)}); waiting <- err }()
			So(eventually(func() bool { return queued(m, 3) == 2 }), ShouldBeTrue)

			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			sent := cdp.sentCount()
			So(errorCode(<-running), ShouldEqual, generated.ErrorCodeEndpointConnected)
			So(errorCode(<-waiting), ShouldEqual, generated.ErrorCodeEndpointConnected)
			So(cdp.sentCount(), ShouldEqual, sent)
			So(cdp.detachCalls(), ShouldResemble, [][]int{nil})
		})

		Convey("正在附加的命令放弃附加,它的断开在交出完成之前", func() {
			entered := make(chan struct{})
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method == "Runtime.enable" {
					close(entered)
					<-ctx.Done()
					return nil, ctx.Err()
				}
				return json.RawMessage(`{}`), nil
			})
			done := make(chan error, 1)
			go func() { _, err := probe(m, Request{TabID: tabRef(5), Timeout: time.Minute}); done <- err }()
			<-entered
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			So(cdp.detachCalls(), ShouldResemble, [][]int{{5}, nil})
			So(errorCode(<-done), ShouldEqual, generated.ErrorCodeEndpointConnected)
		})

		Convey("不占标签页队列的 page detach --all 也在交出完成之前结束,它的断开不会晚于交出", func() {
			_, err := probe(m, Request{TabID: tabRef(3)})
			So(err, ShouldBeNil)
			openDialog(m, 3, "alert", "hi")
			entered := make(chan struct{})
			var once sync.Once
			cdp.setSend(func(ctx context.Context, cmd Command) (json.RawMessage, error) {
				if cmd.Method != "Page.handleJavaScriptDialog" {
					return json.RawMessage(`{}`), nil
				}
				blocked := false
				once.Do(func() { blocked = true })
				if !blocked {
					return json.RawMessage(`{}`), nil
				}
				close(entered)
				<-ctx.Done()
				// 被取消的 detach --all 慢一步才继续:交出若不等它,它的断开就落在交出之后。
				time.Sleep(50 * time.Millisecond)
				return nil, ctx.Err()
			})
			done := make(chan error, 1)
			go func() {
				_, err := m.Do(context.Background(), Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
				done <- err
			}()
			<-entered
			So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
			sent, detaches := cdp.sentCount(), cdp.detachCalls()
			<-done
			So(cdp.sentCount(), ShouldEqual, sent)
			So(cdp.detachCalls(), ShouldResemble, detaches)
		})
	})
}

func TestHandOverWaitsForIdleDetach(t *testing.T) {
	Convey("正在进行的空闲断开在交出完成之前结束,不会断开交出之后端点附加的标签页", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)
		openDialog(m, 3, "alert", "hi")
		entered, resume := make(chan struct{}), make(chan struct{})
		cdp.setSend(func(_ context.Context, cmd Command) (json.RawMessage, error) {
			if cmd.Method == "Page.handleJavaScriptDialog" {
				close(entered)
				<-resume
			}
			return json.RawMessage(`{}`), nil
		})
		idle := make(chan struct{})
		go func() { clock.Advance(idleTimeout); close(idle) }()
		<-entered

		handedOver := make(chan error, 1)
		go func() { handedOver <- m.HandOver(context.Background(), testInstance) }()
		select {
		case err := <-handedOver:
			t.Fatalf("HandOver returned %v while an idle detach was still running", err)
		case <-time.After(50 * time.Millisecond):
		}
		close(resume)
		So(<-handedOver, ShouldBeNil)
		<-idle
		So(cdp.detachCalls(), ShouldResemble, [][]int{{3}, nil})
	})
}

func TestHandOverRacesWithCommandsAndTimers(t *testing.T) {
	Convey("与交出同时到达的命令和空闲断开:交出完成后 sctl 不再向这个浏览器发出任何东西", t, func() {
		cdp := newFakeCDP()
		clock := &fakeClock{}
		m := newTestManager(cdp, clock)
		_, err := probe(m, Request{TabID: tabRef(3)})
		So(err, ShouldBeNil)

		start := make(chan struct{})
		var wg sync.WaitGroup
		errs := make(chan error, 32)
		run := func(req Request) {
			wg.Go(func() {
				<-start
				_, err := m.Do(context.Background(), req)
				errs <- err
			})
		}
		for range 3 {
			run(Request{Action: "probe", TabID: tabRef(3)})
			run(Request{Action: "probe", TabID: tabRef(4)})
		}
		run(Request{Action: "debug.start", TabID: tabRef(4)})
		run(Request{Action: "debug.status"})
		run(Request{Action: "detach", Input: json.RawMessage(`{"all":true}`)})
		wg.Go(func() {
			<-start
			clock.Advance(idleTimeout)
		})
		close(start)

		So(m.HandOver(context.Background(), testInstance), ShouldBeNil)
		sent, detaches, records := cdp.sentCount(), cdp.detachCalls(), cdp.recordCalls()
		wg.Wait()
		close(errs)
		for err := range errs {
			So([]string{"", generated.ErrorCodeEndpointConnected, generated.ErrorCodeDebuggerDetached}, ShouldContain, errorCode(err))
			if errorCode(err) == "" {
				So(err, ShouldBeNil)
			}
		}
		So(cdp.sentCount(), ShouldEqual, sent)
		So(cdp.detachCalls(), ShouldResemble, detaches)
		So(cdp.recordCalls(), ShouldResemble, records)
		So(detaches[len(detaches)-1], ShouldBeNil)
	})
}
