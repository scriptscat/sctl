package bridge

import (
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"
)

type connectedEvent struct {
	instanceID string
	bundle     string
}

// connectRecorder 是只关心握手完成的监听者。
type connectRecorder struct {
	*recordingListener
	connected chan connectedEvent
}

func (r *connectRecorder) OnInstanceConnected(instanceID, bundle string) {
	r.connected <- connectedEvent{instanceID, bundle}
}

func newConnectRecorder() *connectRecorder {
	return &connectRecorder{recordingListener: newRecordingListener(), connected: make(chan connectedEvent, 4)}
}

func (r *connectRecorder) awaitConnected() connectedEvent {
	select {
	case ev := <-r.connected:
		return ev
	case <-time.After(3 * time.Second):
		So("OnInstanceConnected not called", ShouldBeEmpty)
		return connectedEvent{}
	}
}

func TestInstanceConnectedListener(t *testing.T) {
	Convey("浏览器实例完成握手后,实现了 InstanceConnectedListener 的监听者收到实例 ID 和上报的扩展摘要", t, func() {
		h := startTestServer(t)
		l := newConnectRecorder()
		h.srv.AddBrowserListener(l)
		key := h.registerBrowser(instanceA, "chrome-0123")
		digest := strings.Repeat("ab", 32)

		Convey("上报了 bundle 时原样带上", func() {
			e := h.authenticateBrowser(instanceA, key)
			params := browserCapabilities("chrome-0123")
			params.Peer.Bundle = digest
			So(e.declare(params).Error, ShouldBeNil)
			So(l.awaitConnected(), ShouldResemble, connectedEvent{instanceA, digest})
		})

		Convey("没有上报时为空", func() {
			h.connectBrowser(instanceA, key, "chrome-0123")
			So(l.awaitConnected(), ShouldResemble, connectedEvent{instanceA, ""})
		})

		Convey("bundle 不是 64 位小写十六进制时关闭连接,不通知", func() {
			for _, bad := range []string{strings.Repeat("AB", 32), "abc", strings.Repeat("ab", 33)} {
				e := h.authenticateBrowser(instanceA, key)
				params := browserCapabilities("chrome-0123")
				params.Peer.Bundle = bad
				e.writeRequest(methodCapabilities, uuid.NewString(), params)
				e.closedByDaemon()
			}
			settle()
			So(len(l.connected), ShouldEqual, 0)
		})
	})
}
