package bridge

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket/wsjson"
	. "github.com/smartystreets/goconvey/convey"
)

func TestApprovalPendingReachesTheWaitingCaller(t *testing.T) {
	Convey("浏览器把请求转入审批时发 $/approvalPending:只有同一连接上这个请求的调用方得到一次 OnPending", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")

		var fired atomic.Int32
		pending := make(chan struct{}, 4)
		out := h.goCall(Request{
			Action:  "bookmarks.remove",
			Browser: "chrome-0123",
			Input:   json.RawMessage(`{"ids":["14"]}`),
			OnPending: func() {
				fired.Add(1)
				pending <- struct{}{}
			},
		})
		req := a.read()

		// 另一条连接冒用这个请求 ID、或本连接报一个不存在的请求,都不触发。
		b.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: req.ID})
		b.alive()
		a.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: "no-such-request"})
		a.alive()
		So(fired.Load(), ShouldEqual, 0)

		a.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: req.ID})
		select {
		case <-pending:
		case <-time.After(callTimeout):
			t.Fatal("the caller was not told the request entered approval")
		}
		a.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: req.ID})
		a.alive()
		a.writeResult(req.ID, json.RawMessage(`{"ids":["14"],"bookmarks":1,"folders":0}`))
		got := <-out
		So(got.err, ShouldBeNil)
		So(got.resp.OK, ShouldBeTrue)
		So(fired.Load(), ShouldEqual, 1)
	})

	Convey("请求在审批前就被答复时调用方不会得到 OnPending", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")

		var fired atomic.Int32
		out := h.goCall(Request{
			Action:    "bookmarks.remove",
			Input:     json.RawMessage(`{"ids":["999999"]}`),
			OnPending: func() { fired.Add(1) },
		})
		req := a.read()
		ctx, cancel := context.WithTimeout(context.Background(), callTimeout)
		defer cancel()
		So(wsjson.Write(ctx, a.ws, newError(req.ID, "NOT_FOUND", "bookmark 999999 not found")), ShouldBeNil)
		got := <-out
		So(got.err, ShouldBeNil)
		So(got.resp.Error.Code, ShouldEqual, "NOT_FOUND")
		So(fired.Load(), ShouldEqual, 0)
	})

	Convey("对不等待人工决定的方法报 $/approvalPending 不触发 OnPending,汇总调用也不会", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")

		var fired atomic.Int32
		out := h.goCall(Request{Action: "tabs.list", Input: json.RawMessage(`{}`), OnPending: func() { fired.Add(1) }})
		reqA := a.read()
		reqB := b.read()
		a.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: reqA.ID})
		b.writeRequest(methodApprovalPending, "", approvalPendingParams{ID: reqB.ID})
		a.alive()
		b.alive()
		a.writeResult(reqA.ID, json.RawMessage(tabsListResult))
		b.writeResult(reqB.ID, json.RawMessage(tabsListResult))
		got := <-out
		So(got.err, ShouldBeNil)
		So(fired.Load(), ShouldEqual, 0)
	})
}
