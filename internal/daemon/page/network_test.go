package page

import (
	"encoding/json"
	"testing"

	. "github.com/smartystreets/goconvey/convey"
	"go.uber.org/zap"
)

func TestNetworkIdleAcrossFrameSessions(t *testing.T) {
	Convey("进行中的请求按 requestId 跨会话配对,子会话接手或分离后不会永远算作进行中", t, func() {
		tab := &Tab{m: &Manager{log: zap.NewNop()}, id: 7, refs: newRefTable(), frames: newFrameSessions(), net: newNetWatch()}
		deliver := func(sessionID, method, params string) {
			switch method {
			case "Target.attachedToTarget":
				onTargetAttached(tab, sessionID, json.RawMessage(params))
			case "Target.detachedFromTarget":
				onTargetDetached(tab, sessionID, json.RawMessage(params))
			default:
				networkEvents[method](tab, sessionID, json.RawMessage(params))
			}
		}
		inflight := func() int {
			n, _ := tab.net.idle()
			return n
		}

		Convey("iframe 的文档请求在父会话开始、在子会话结束", func() {
			deliver("", "Network.requestWillBeSent", `{"requestId":"doc","type":"Document","frameId":"F"}`)
			deliver("", "Target.attachedToTarget", `{"sessionId":"S","targetInfo":{"targetId":"F"}}`)
			So(inflight(), ShouldEqual, 0)
			deliver("S", "Network.loadingFinished", `{"requestId":"doc"}`)
			So(inflight(), ShouldEqual, 0)
		})

		Convey("子会话里的请求在子会话里结束", func() {
			deliver("", "Target.attachedToTarget", `{"sessionId":"S","targetInfo":{"targetId":"F"}}`)
			deliver("S", "Network.requestWillBeSent", `{"requestId":"x","type":"Fetch","frameId":"F"}`)
			deliver("", "Network.requestWillBeSent", `{"requestId":"top","type":"Fetch","frameId":"main"}`)
			So(inflight(), ShouldEqual, 2)
			deliver("S", "Network.loadingFinished", `{"requestId":"x"}`)
			So(inflight(), ShouldEqual, 1)
		})

		Convey("子会话分离时丢弃它和它下面的会话里进行中的请求,顶层的保留", func() {
			deliver("", "Target.attachedToTarget", `{"sessionId":"S","targetInfo":{"targetId":"F"}}`)
			deliver("S", "Target.attachedToTarget", `{"sessionId":"N","targetInfo":{"targetId":"G"}}`)
			deliver("S", "Network.requestWillBeSent", `{"requestId":"x","type":"Fetch","frameId":"F"}`)
			deliver("N", "Network.requestWillBeSent", `{"requestId":"y","type":"Fetch","frameId":"G"}`)
			deliver("", "Network.requestWillBeSent", `{"requestId":"top","type":"Fetch","frameId":"main"}`)
			deliver("", "Target.detachedFromTarget", `{"sessionId":"S"}`)
			So(inflight(), ShouldEqual, 1)
		})

		Convey("子会话里的文档响应不改变主文档的 HTTP 状态", func() {
			tab.net.reset("main")
			deliver("", "Network.responseReceived", `{"requestId":"doc","type":"Document","frameId":"main","response":{"status":404}}`)
			deliver("S", "Network.responseReceived", `{"requestId":"child","type":"Document","frameId":"main","response":{"status":200}}`)
			So(*tab.net.documentStatus(), ShouldEqual, 404)
		})
	})
}
