package bridge

import (
	"os"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

func scriptCatByName(list []InstanceInfo, name string) (InstanceInfo, bool) {
	for _, info := range list {
		if info.Name == name {
			return info, true
		}
	}
	return InstanceInfo{}, false
}

func TestScriptCatInstancesListing(t *testing.T) {
	Convey("ScriptCatInstances 列出已配对的 ScriptCat 实例(在线与离线)", t, func() {
		h := startTestServer(t)
		keyA := h.registerScriptCat(instanceA, "work")
		h.registerScriptCat(instanceB, "home")
		h.connectScriptCatInstance(instanceA, keyA, "work")

		Convey("按名称排序,带名称、ID、在线状态与自报产品版本", func() {
			list, err := h.srv.ScriptCatInstances()
			So(err, ShouldBeNil)
			So(list, ShouldHaveLength, 2)
			So(list[0].Name, ShouldEqual, "home")
			So(list[0].Online, ShouldBeFalse)
			So(list[1].Name, ShouldEqual, "work")
			So(list[1].ID, ShouldEqual, instanceA)
			So(list[1].Online, ShouldBeTrue)
			So(list[1].Product, ShouldEqual, "Chrome")
			So(list[1].ExtensionVersion, ShouldEqual, "1.3.0")
		})

		Convey("pairing.key 存在时多出一行保留名 scriptcat(无 ID);不存在时没有", func() {
			list, err := h.srv.ScriptCatInstances()
			So(err, ShouldBeNil)
			_, found := scriptCatByName(list, "scriptcat")
			So(found, ShouldBeFalse)

			legacy := h.connectScriptCat()
			legacy.alive()
			list, err = h.srv.ScriptCatInstances()
			So(err, ShouldBeNil)
			So(list, ShouldHaveLength, 3)
			row, found := scriptCatByName(list, "scriptcat")
			So(found, ShouldBeTrue)
			So(row.ID, ShouldBeBlank)
			So(row.Online, ShouldBeTrue)
		})
	})

	Convey("老版离线但 pairing.key 仍在时照样列出,标为离线", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)

		list, err := h.srv.ScriptCatInstances()
		So(err, ShouldBeNil)
		So(list, ShouldHaveLength, 1)
		So(list[0].Name, ShouldEqual, "scriptcat")
		So(list[0].Online, ShouldBeFalse)
	})
}

func TestForgetScriptCat(t *testing.T) {
	Convey("忘记 ScriptCat 实例", t, func() {
		h := startTestServer(t)
		keyA := h.registerScriptCat(instanceA, "work")
		h.registerScriptCat(instanceB, "home")
		a := h.connectScriptCatInstance(instanceA, keyA, "work")

		Convey("按名称忘记在线实例:删密钥与登记、断开连接,之后会话握手失败;另一个实例不受影响", func() {
			So(h.srv.ForgetScriptCat("work"), ShouldBeNil)
			a.closedByDaemon()
			_, ok := h.scriptcats.Get(instanceA)
			So(ok, ShouldBeFalse)

			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ScriptCatExtHMAC(auth.ModeSession, instanceA, keyA, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: scriptCatPeer(instanceA)})
			e.closedWithPolicyViolation()
			ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonUnknownInstance)
			So(ev.Client, ShouldEqual, "scriptcat:"+instanceA)

			a2, _ := h.srv.ScriptCatInstances()
			_, kept := scriptCatByName(a2, "home")
			So(kept, ShouldBeTrue)
		})

		Convey("按完整实例 ID 忘记离线实例", func() {
			So(h.srv.ForgetScriptCat(instanceB), ShouldBeNil)
			_, ok := h.scriptcats.Get(instanceB)
			So(ok, ShouldBeFalse)
			a.alive()
		})

		Convey("忘记 scriptcat:删除 pairing.key 并断开老版连接,新版实例不受影响", func() {
			legacy := h.connectScriptCat()
			legacy.alive()

			So(h.srv.ForgetScriptCat("scriptcat"), ShouldBeNil)
			legacy.closedByDaemon()
			_, err := os.Stat(h.keyPath)
			So(os.IsNotExist(err), ShouldBeTrue)
			list, err := h.srv.ScriptCatInstances()
			So(err, ShouldBeNil)
			_, found := scriptCatByName(list, "scriptcat")
			So(found, ShouldBeFalse)
			a.alive()
		})

		Convey("不匹配任何实例(含没有老版时的 scriptcat)→ ErrScriptCatNotFound,什么都不删", func() {
			So(h.srv.ForgetScriptCat("nope"), ShouldEqual, ErrScriptCatNotFound)
			So(h.srv.ForgetScriptCat("scriptcat"), ShouldEqual, ErrScriptCatNotFound)
			list, err := h.srv.ScriptCatInstances()
			So(err, ShouldBeNil)
			So(list, ShouldHaveLength, 2)
		})
	})
}
