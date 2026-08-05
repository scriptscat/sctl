package bridge

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// waitForAuditEvent 轮询审计快照直到出现指定类型的事件。审计记录发生在连接关闭之后,
// 测试观察到断开与记录落地之间存在时序差,故轮询而非直接断言。
func waitForAuditEvent(h *testHarness, typ audit.Type) audit.Event {
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		for _, ev := range h.srv.audit.Snapshot() {
			if ev.Type == typ {
				return ev
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	return audit.Event{}
}

// failSessionHandshake 用错误密钥发起会话握手,并等待连接被断开。
// 返回握手中实际过线的密码学材料,供审计泄露检查比对。
func (h *testHarness) failSessionHandshake() (nonceE, mac string) {
	e := dial(h.url)
	challenge := e.read()
	var cp authChallengeParams
	So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)
	wrong, _ := auth.NewLongTermKey()
	nonceE, _ = auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac = h.crypto.ExtHMAC(auth.ModeSession, wrong, cp.NonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, _ = e.ws.Read(ctx)
	return nonceE, mac
}

func TestAuditRecording(t *testing.T) {
	Convey("守卫在扩展建立会话前拦下的事件会进入审计", t, func() {
		h := startTestServer(t)

		Convey("会话握手 HMAC 失败记录为 handshake.failed", func() {
			key, _ := auth.NewLongTermKey()
			So(h.keys.Save(key), ShouldBeNil)

			h.failSessionHandshake()

			ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
			So(ev.Type, ShouldEqual, audit.TypeHandshakeFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.At.IsZero(), ShouldBeFalse)
		})

		Convey("握手期发送错误的 JSON-RPC 响应记录为协议违规", func() {
			e := dial(h.url)
			e.read()
			e.writeResult(uuid.NewString(), struct{}{})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, _ = e.ws.Read(ctx)

			ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonProtocol)
		})

		Convey("配对握手 HMAC 失败记录为 pairing.failed", func() {
			_, err := h.srv.BeginEnrollment()
			So(err, ShouldBeNil)
			wrongMac, _, _ := h.crypto.DerivePairingKeys("WRONGCODE")

			e := dial(h.url)
			challenge := e.read()
			var cp authChallengeParams
			So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModePairing, wrongMac, cp.NonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac})
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_, _, _ = e.ws.Read(ctx)

			ev := waitForAuditEvent(h, audit.TypePairingFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
		})

		Convey("配对尝试超过限流上限后记录为 pairing.rate_limited", func() {
			_, err := h.srv.BeginEnrollment()
			So(err, ShouldBeNil)
			wrongMac, _, _ := h.crypto.DerivePairingKeys("WRONGCODE")

			// 限流器上限 5/分,第 6 次尝试应被限流挡下。
			for i := 0; i < 6; i++ {
				e := dial(h.url)
				challenge := e.read()
				var cp authChallengeParams
				So(json.Unmarshal(challenge.Params, &cp), ShouldBeNil)
				nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
				mac := h.crypto.ExtHMAC(auth.ModePairing, wrongMac, cp.NonceD, nonceE)
				e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac})
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				_, _, _ = e.ws.Read(ctx)
				cancel()
			}

			ev := waitForAuditEvent(h, audit.TypePairingRateLimited)
			So(ev.Type, ShouldEqual, audit.TypePairingRateLimited)
		})

		Convey("成功握手记录为 handshake.ok", func() {
			key, _ := auth.NewLongTermKey()
			So(h.keys.Save(key), ShouldBeNil)
			h.doSessionHandshake(key)

			ev := waitForAuditEvent(h, audit.TypeHandshakeOK)
			So(ev.Type, ShouldEqual, audit.TypeHandshakeOK)
		})

		Convey("审计不泄露握手中过线的密码学材料", func() {
			key, _ := auth.NewLongTermKey()
			So(h.keys.Save(key), ShouldBeNil)
			nonceE, mac := h.failSessionHandshake()
			waitForAuditEvent(h, audit.TypeHandshakeFailed)

			raw, err := json.Marshal(h.srv.audit.Snapshot())
			So(err, ShouldBeNil)
			So(string(raw), ShouldNotContainSubstring, mac)
			So(string(raw), ShouldNotContainSubstring, nonceE)
		})

		Convey("事件字段集是封闭的,不含审计白名单外的键", func() {
			key, _ := auth.NewLongTermKey()
			So(h.keys.Save(key), ShouldBeNil)
			h.failSessionHandshake()
			waitForAuditEvent(h, audit.TypeHandshakeFailed)

			raw, err := json.Marshal(h.srv.audit.Snapshot())
			So(err, ShouldBeNil)
			var events []map[string]any
			So(json.Unmarshal(raw, &events), ShouldBeNil)
			So(events, ShouldNotBeEmpty)
			allowed := map[string]bool{"at": true, "type": true, "client": true, "reason": true}
			for _, ev := range events {
				for k := range ev {
					So(allowed[k], ShouldBeTrue)
				}
			}
		})
	})
}
