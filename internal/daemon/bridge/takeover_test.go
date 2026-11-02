package bridge

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

// legacyKey 读出当前 pairing.key 中的老版密钥。
func (h *testHarness) legacyKey() []byte {
	key, ok, err := h.keys.Load()
	So(err, ShouldBeNil)
	So(ok, ShouldBeTrue)
	return key
}

// pairingKeyRemoved 报告 pairing.key 是否已不在磁盘上。
func (h *testHarness) pairingKeyRemoved() bool {
	_, err := os.Stat(h.keyPath)
	return os.IsNotExist(err)
}

// legacyHandshakeFails 断言不带 peer 的老版会话握手以 key 被拒(1008 关闭)。
func (h *testHarness) legacyHandshakeFails(key []byte) {
	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ExtHMAC(auth.ModeSession, key, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})
	e.closedWithPolicyViolation()
}

// takeoverFails 以未登记的实例 id 和按 key 计算的 ScriptCat 实例 MAC 发起会话握手,断言握手失败并返回审计事件。
func (h *testHarness) takeoverFails(id string, key []byte) audit.Event {
	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ScriptCatExtHMAC(auth.ModeSession, id, key, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: scriptCatPeer(id)})
	e.closedWithPolicyViolation()
	return waitForAuditEvent(h, audit.TypeHandshakeFailed)
}

func TestUpgradedScriptCatTakesOverPairingKey(t *testing.T) {
	Convey("未登记的新版 ScriptCat 以 pairing.key 的密钥会话握手:密钥归到该实例、pairing.key 删除、在线老版连接断开", t, func() {
		h := startTestServer(t)
		legacy := h.connectScriptCat()
		key := h.legacyKey()

		a := h.connectScriptCatInstance(instanceA, key, "work")

		got, ok := h.scriptcats.Get(instanceA)
		So(ok, ShouldBeTrue)
		So(got.Name, ShouldEqual, "work")
		So(got.Key, ShouldResemble, key)
		So(h.pairingKeyRemoved(), ShouldBeTrue)
		legacy.closedByDaemon()
		h.scriptsListWorks(a)

		Convey("此后老版握手不再成立", func() {
			h.legacyHandshakeFails(key)
			So(h.scriptCatOnline(instanceA, true), ShouldBeTrue)
		})

		Convey("接管后的实例按登记的密钥重连", func() {
			a2 := h.connectScriptCatInstance(instanceA, key, "work")
			a.closedByDaemon()
			a2.alive()
		})
	})

	Convey("没有老版连接在线时接管同样登记密钥并删除 pairing.key", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)

		h.connectScriptCatInstance(instanceA, key, "work")

		got, ok := h.scriptcats.Get(instanceA)
		So(ok, ShouldBeTrue)
		So(got.Key, ShouldResemble, key)
		So(h.pairingKeyRemoved(), ShouldBeTrue)
	})
}

func TestScriptCatTakeoverFailuresKeepPairingKey(t *testing.T) {
	Convey("接管失败时 pairing.key 不变、不登记,老版连接照常在线", t, func() {
		h := startTestServer(t)
		legacy := h.connectScriptCat()
		before, err := os.ReadFile(h.keyPath)
		So(err, ShouldBeNil)
		unchanged := func() {
			after, err := os.ReadFile(h.keyPath)
			So(err, ShouldBeNil)
			So(after, ShouldResemble, before)
			_, ok := h.scriptcats.Get(instanceA)
			So(ok, ShouldBeFalse)
			legacy.alive()
		}

		Convey("MAC 不是用 pairing.key 的密钥计算的:按握手失败处理", func() {
			wrong, _ := auth.NewLongTermKey()
			ev := h.takeoverFails(instanceA, wrong)
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.Client, ShouldEqual, "scriptcat:"+instanceA)
			unchanged()
		})

		Convey("MAC 用 pairing.key 的密钥但按老版公式计算:按握手失败处理", func() {
			key := h.legacyKey()
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModeSession, key, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: scriptCatPeer(instanceA)})
			e.closedWithPolicyViolation()
			unchanged()
		})

		for _, name := range []string{"home", "scriptcat"} {
			Convey("能力声明的名称 "+name+" 冲突:回复 CONFLICT,不登记、不删除 pairing.key", func() {
				h.registerScriptCat(instanceB, "home")
				e := h.authenticateScriptCatInstance(instanceA, h.legacyKey())
				reply := e.declare(scriptCatCapabilities(name))
				So(reply.Error, ShouldNotBeNil)
				So(reply.Error.Data, ShouldNotBeNil)
				So(reply.Error.Data.Code, ShouldEqual, CodeConflict)
				e.closedByDaemon()
				unchanged()
				So(h.scriptCatOnline(instanceA, false), ShouldBeFalse)
			})
		}
	})
}

func TestLegacyConnectionCannotComeOnlineAfterTakeover(t *testing.T) {
	Convey("握手已通过但尚未声明能力的老版连接,在接管之后声明能力时被断开且不上线", t, func() {
		h := startTestServer(t)
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)
		legacy := h.authenticateSession(key)

		h.connectScriptCatInstance(instanceA, key, "work")
		legacy.writeRequest(methodCapabilities, uuid.NewString(), capabilitiesParams{SchemaVersion: "1.0.0", Methods: h.allMethods()})
		legacy.closedWithPolicyViolation()

		h.srv.mu.Lock()
		online := h.srv.legacyScriptCat
		h.srv.mu.Unlock()
		So(online, ShouldBeNil)
	})
}

func TestFailedPairingKeyDeleteIsRetried(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("relies on a read-only POSIX directory blocking file removal")
	}
	Convey("接管登记成功但删除 pairing.key 失败:登记保留,老版握手不成立,删除在启动或下次接管检查时重试", t, func() {
		dir := t.TempDir()
		keyDir := filepath.Join(dir, "keys")
		So(os.Mkdir(keyDir, 0o700), ShouldBeNil)
		h := startTestServerAt(t, dir, filepath.Join(keyDir, "pairing.key"))
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)
		legacy := h.authenticateSession(key)
		So(legacy.declare(capabilitiesParams{SchemaVersion: "1.0.0", Methods: h.allMethods()}).Error, ShouldBeNil)

		So(os.Chmod(keyDir, 0o500), ShouldBeNil)
		defer func() { _ = os.Chmod(keyDir, 0o700) }()
		a := h.connectScriptCatInstance(instanceA, key, "work")

		got, ok := h.scriptcats.Get(instanceA)
		So(ok, ShouldBeTrue)
		So(got.Key, ShouldResemble, key)
		So(h.pairingKeyRemoved(), ShouldBeFalse)
		legacy.closedByDaemon()
		h.legacyHandshakeFails(key)
		h.scriptsListWorks(a)

		So(os.Chmod(keyDir, 0o700), ShouldBeNil)

		Convey("daemon 启动时删除", func() {
			So(h.srv.RetireTakenOverPairingKey(), ShouldBeNil)
			So(h.pairingKeyRemoved(), ShouldBeTrue)
		})

		Convey("下次接管检查时删除:另一个未登记实例拿这把密钥也接管不了", func() {
			ev := h.takeoverFails(instanceB, key)
			So(ev.Reason, ShouldEqual, audit.ReasonUnknownInstance)
			So(h.pairingKeyRemoved(), ShouldBeTrue)
			_, ok := h.scriptcats.Get(instanceB)
			So(ok, ShouldBeFalse)
		})

		Convey("下次老版握手检查时删除", func() {
			h.legacyHandshakeFails(key)
			So(h.pairingKeyRemoved(), ShouldBeTrue)
		})
	})
}

func TestStartupKeepsAnUntakenPairingKey(t *testing.T) {
	Convey("pairing.key 未被任何实例接管时,启动检查保留它,老版 ScriptCat 照常连接", t, func() {
		h := startTestServer(t)
		h.registerScriptCat(instanceA, "work")
		key, _ := auth.NewLongTermKey()
		So(h.keys.Save(key), ShouldBeNil)

		So(h.srv.RetireTakenOverPairingKey(), ShouldBeNil)

		So(h.pairingKeyRemoved(), ShouldBeFalse)
		h.doSessionHandshake(key).alive()
	})
}
