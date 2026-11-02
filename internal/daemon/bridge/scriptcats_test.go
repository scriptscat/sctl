package bridge

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/audit"
	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

func scriptCatPeer(id string) *authPeer {
	return &authPeer{Kind: string(protocol.PeerScriptCat), InstanceID: id}
}

func scriptCatCapabilities(name string) capabilitiesParams {
	return capabilitiesParams{
		SchemaVersion: "1.0.0",
		Methods:       []string{"scripts.list", "scripts.get"},
		Peer:          &capabilitiesPeer{Name: name, Product: "Chrome", ProductVersion: "129.0", ExtensionVersion: "1.3.0"},
	}
}

// registerScriptCat 直接在 ScriptCat 登记表里放一个已配对实例,返回其密钥。
func (h *testHarness) registerScriptCat(id, name string) []byte {
	key, _ := auth.NewLongTermKey()
	So(h.scriptcats.Pair(store.ScriptCatInstance{ID: id, Name: name, Key: key}), ShouldBeNil)
	return key
}

// authenticateScriptCatInstance 以实例密钥完成新版 ScriptCat 的会话认证并消费 hello;能力声明留给调用方。
func (h *testHarness) authenticateScriptCatInstance(id string, key []byte) *extClient {
	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ScriptCatExtHMAC(auth.ModeSession, id, key, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: scriptCatPeer(id)})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authenticatedParams
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(h.crypto.VerifyScriptCatDaemonHMAC(auth.ModeSession, id, key, nonceD, nonceE, okp.HMAC), ShouldBeTrue)
	So(e.read().Method, ShouldEqual, methodHello)
	return e
}

// connectScriptCatInstance 以实例密钥和名称连接一个已配对的新版 ScriptCat,要求 daemon 接受。
func (h *testHarness) connectScriptCatInstance(id string, key []byte, name string) *extClient {
	e := h.authenticateScriptCatInstance(id, key)
	So(e.declare(scriptCatCapabilities(name)).Error, ShouldBeNil)
	return e
}

// enrollScriptCatInstance 用 sctl connect 打开的配对码为新版 ScriptCat 实例 id 完成配对握手并消费 hello,
// 返回连接与下发的实例密钥;能力声明留给调用方。
func (h *testHarness) enrollScriptCatInstance(id string) (*extClient, []byte) {
	display, err := h.srv.BeginEnrollment()
	So(err, ShouldBeNil)
	kpMac, kpEnc, err := h.crypto.DerivePairingKeys(display)
	So(err, ShouldBeNil)

	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.ScriptCatExtHMAC(auth.ModePairing, id, kpMac, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac, Peer: scriptCatPeer(id)})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authenticatedParams
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(okp.Key, ShouldNotBeNil)
	So(h.crypto.VerifyScriptCatDaemonHMAC(auth.ModePairing, id, kpMac, nonceD, nonceE, okp.HMAC), ShouldBeTrue)
	key, err := h.crypto.OpenKey(kpEnc, okp.Key.Ciphertext, okp.Key.IV)
	So(err, ShouldBeNil)
	So(e.read().Method, ShouldEqual, methodHello)
	return e, key
}

// pairScriptCatInstance 完成新版 ScriptCat 实例的首次配对并以 name 声明能力,要求 daemon 接受。
func (h *testHarness) pairScriptCatInstance(id, name string) (*extClient, []byte) {
	e, key := h.enrollScriptCatInstance(id)
	So(e.declare(scriptCatCapabilities(name)).Error, ShouldBeNil)
	return e, key
}

// closedWithPolicyViolation 断言 daemon 以 1008 关闭了这条连接(握手失败的统一关闭码)。
func (e *extClient) closedWithPolicyViolation() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := e.ws.Read(ctx)
	So(err, ShouldNotBeNil)
	So(errors.Is(err, context.DeadlineExceeded), ShouldBeFalse)
	So(websocket.CloseStatus(err), ShouldEqual, websocket.StatusPolicyViolation)
}

// scriptCatOnline 报告新版 ScriptCat 实例 id 当前是否有可调用的连接;连接断开由 daemon 异步发现,因此等待片刻。
func (h *testHarness) scriptCatOnline(id string, want bool) bool {
	deadline := time.Now().Add(3 * time.Second)
	for {
		h.srv.mu.Lock()
		online := h.srv.scriptCats[id] != nil
		h.srv.mu.Unlock()
		if online == want || time.Now().After(deadline) {
			return online
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestScriptCatInstancesCoexist(t *testing.T) {
	Convey("两个不同实例 ID 的新版 ScriptCat 各自配对后同时在线,同一实例重连只替换它自己", t, func() {
		h := startTestServer(t)
		a, keyA := h.pairScriptCatInstance(instanceA, "work")
		b, keyB := h.pairScriptCatInstance(instanceB, "home")

		a.alive()
		b.alive()
		So(h.srv.ExtConnected(), ShouldBeTrue)
		gotA, ok := h.scriptcats.Get(instanceA)
		So(ok, ShouldBeTrue)
		So(gotA.Name, ShouldEqual, "work")
		So(gotA.Key, ShouldResemble, keyA)
		So(gotA.ExtensionVersion, ShouldEqual, "1.3.0")
		gotB, _ := h.scriptcats.Get(instanceB)
		So(gotB.Key, ShouldResemble, keyB)
		// 新版实例的密钥只进 scriptcats.json:pairing.key 留给老版 ScriptCat。
		_, err := os.Stat(h.keyPath)
		So(os.IsNotExist(err), ShouldBeTrue)

		a2 := h.connectScriptCatInstance(instanceA, keyA, "work")
		a.closedByDaemon()
		a2.alive()
		b.alive()

		Convey("多个 ScriptCat 同时在线时 scripts 调用不随意选一个发出,而是报错", func() {
			_, err := h.srv.Call(context.Background(), Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			var bridgeErr *Error
			So(errors.As(err, &bridgeErr), ShouldBeTrue)
			So(bridgeErr.Code, ShouldEqual, CodeConflict)
		})

		Convey("只剩一个 ScriptCat 在线时 scripts 调用发给它", func() {
			b.ws.CloseNow()
			So(h.scriptCatOnline(instanceB, false), ShouldBeFalse)
			h.scriptsListWorks(a2)
		})
	})
}

func TestScriptCatInstanceIsTheOnlyConnection(t *testing.T) {
	Convey("只有一个新版 ScriptCat 在线(无老版连接)时 ExtConnected 为真,scripts 调用发给它", t, func() {
		h := startTestServer(t)
		So(h.srv.ExtConnected(), ShouldBeFalse)
		key := h.registerScriptCat(instanceA, "work")
		a := h.connectScriptCatInstance(instanceA, key, "work")
		So(h.srv.ExtConnected(), ShouldBeTrue)
		h.scriptsListWorks(a)

		a.ws.CloseNow()
		So(h.scriptCatOnline(instanceA, false), ShouldBeFalse)
		So(h.srv.ExtConnected(), ShouldBeFalse)
		_, err := h.srv.Call(context.Background(), Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
		So(err, ShouldEqual, ErrNotConnected)
	})
}

func TestLegacyScriptCatCoexistsWithInstances(t *testing.T) {
	Convey("不带 peer 的老版 ScriptCat 与新版实例同时在线;老版重连只顶替老版,新版重连不顶替老版", t, func() {
		h := startTestServer(t)
		legacy := h.connectScriptCat()
		key := h.registerScriptCat(instanceA, "work")
		a := h.connectScriptCatInstance(instanceA, key, "work")
		legacy.alive()
		a.alive()

		legacyKey, _, err := h.keys.Load()
		So(err, ShouldBeNil)
		legacy2 := h.doSessionHandshake(legacyKey)
		legacy.closedByDaemon()
		a.alive()

		a2 := h.connectScriptCatInstance(instanceA, key, "work")
		a.closedByDaemon()
		legacy2.alive()
		a2.alive()
	})
}

func TestScriptCatInstanceNameConflict(t *testing.T) {
	Convey("ScriptCat 实例名称在已配对 ScriptCat 实例与老版保留名 scriptcat 之间唯一", t, func() {
		h := startTestServer(t)
		keyA := h.registerScriptCat(instanceA, "work")

		conflicts := func(e *extClient, name string) {
			reply := e.declare(scriptCatCapabilities(name))
			So(reply.Error, ShouldNotBeNil)
			So(reply.Error.Data, ShouldNotBeNil)
			So(reply.Error.Data.Code, ShouldEqual, CodeConflict)
			e.closedByDaemon()
		}

		for _, name := range []string{"work", "scriptcat"} {
			Convey("首次配对声明名称 "+name+":回复 CONFLICT、断开,登记与 pairing.key 都不落盘", func() {
				e, _ := h.enrollScriptCatInstance(instanceB)
				conflicts(e, name)
				_, ok := h.scriptcats.Get(instanceB)
				So(ok, ShouldBeFalse)
				So(h.scriptcats.List(), ShouldHaveLength, 1)
				_, err := os.Stat(h.keyPath)
				So(os.IsNotExist(err), ShouldBeTrue)
				So(h.scriptCatOnline(instanceB, false), ShouldBeFalse)
			})
		}

		Convey("已配对实例改名为保留名 scriptcat:回复 CONFLICT、断开并保留原名", func() {
			conflicts(h.authenticateScriptCatInstance(instanceA, keyA), "scriptcat")
			got, _ := h.scriptcats.Get(instanceA)
			So(got.Name, ShouldEqual, "work")
			So(h.scriptCatOnline(instanceA, false), ShouldBeFalse)
		})

		Convey("与 sctl Browser 实例同名不算冲突", func() {
			h.registerBrowser(instanceB, "home")
			e, _ := h.enrollScriptCatInstance(instanceB)
			So(e.declare(scriptCatCapabilities("home")).Error, ShouldBeNil)
			got, ok := h.scriptcats.Get(instanceB)
			So(ok, ShouldBeTrue)
			So(got.Name, ShouldEqual, "home")
		})
	})
}

func TestScriptCatInstanceCapabilitiesRequirePeer(t *testing.T) {
	Convey("新版 ScriptCat 的能力声明缺失或带非法 peer 时连接不进入可用状态", t, func() {
		h := startTestServer(t)
		key := h.registerScriptCat(instanceA, "work")
		for _, peer := range []*capabilitiesPeer{nil, {Name: "Work"}} {
			e := h.authenticateScriptCatInstance(instanceA, key)
			e.writeRequest(methodCapabilities, uuid.NewString(), capabilitiesParams{SchemaVersion: "1.0.0", Methods: []string{"scripts.list"}, Peer: peer})
			e.closedByDaemon()
		}
		So(h.srv.ExtConnected(), ShouldBeFalse)
	})
}

func TestScriptCatInstanceHandshakeFailures(t *testing.T) {
	Convey("新版 ScriptCat 握手失败以 1008 关闭并记 handshake.failed", t, func() {
		h := startTestServer(t)
		key := h.registerScriptCat(instanceA, "work")

		fail := func(peer *authPeer, mac func(nonceD, nonceE string) string) audit.Event {
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac(nonceD, nonceE), Peer: peer})
			e.closedWithPolicyViolation()
			So(h.srv.ExtConnected(), ShouldBeFalse)
			return waitForAuditEvent(h, audit.TypeHandshakeFailed)
		}
		instanceMAC := func(id string, key []byte) func(string, string) string {
			return func(nonceD, nonceE string) string {
				return h.crypto.ScriptCatExtHMAC(auth.ModeSession, id, key, nonceD, nonceE)
			}
		}

		for _, id := range []string{"0123456789ABCDEF0123456789ABCDEF", "0123", ""} {
			Convey("实例 ID 格式非法("+id+")", func() {
				ev := fail(scriptCatPeer(id), instanceMAC(id, key))
				So(ev.Reason, ShouldEqual, audit.ReasonProtocol)
				So(ev.Client, ShouldBeBlank)
			})
		}

		Convey("kind 未知", func() {
			ev := fail(&authPeer{Kind: "firefox", InstanceID: instanceA}, instanceMAC(instanceA, key))
			So(ev.Reason, ShouldEqual, audit.ReasonProtocol)
			So(ev.Client, ShouldBeBlank)
		})

		Convey("MAC 用错密钥", func() {
			wrong, _ := auth.NewLongTermKey()
			ev := fail(scriptCatPeer(instanceA), instanceMAC(instanceA, wrong))
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.Client, ShouldEqual, "scriptcat:"+instanceA)
		})

		Convey("MAC 为另一实例计算", func() {
			ev := fail(scriptCatPeer(instanceA), instanceMAC(instanceB, key))
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
		})

		Convey("MAC 按老版 ScriptCat 公式计算", func() {
			ev := fail(scriptCatPeer(instanceA), func(nonceD, nonceE string) string {
				return h.crypto.ExtHMAC(auth.ModeSession, key, nonceD, nonceE)
			})
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
		})

		Convey("MAC 按浏览器实例公式计算", func() {
			ev := fail(scriptCatPeer(instanceA), func(nonceD, nonceE string) string {
				return h.crypto.BrowserExtHMAC(auth.ModeSession, instanceA, key, nonceD, nonceE)
			})
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
		})

		Convey("实例未登记且没有 pairing.key", func() {
			ev := fail(scriptCatPeer(instanceB), instanceMAC(instanceB, key))
			So(ev.Reason, ShouldEqual, audit.ReasonUnknownInstance)
			So(ev.Client, ShouldEqual, "scriptcat:"+instanceB)
		})

		Convey("实例的 MAC 去掉身份声明后不能以老版 ScriptCat 身份通过", func() {
			// pairing.key 与已登记实例的密钥相同意味着它已被接管(见 takeover_test.go),这里用只在 pairing.key 里的密钥。
			legacyKey, _ := auth.NewLongTermKey()
			So(h.keys.Save(legacyKey), ShouldBeNil)
			ev := fail(nil, instanceMAC(instanceA, legacyKey))
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.Client, ShouldBeBlank)
		})
	})
}

func TestScriptCatInstanceAudit(t *testing.T) {
	Convey("新版 ScriptCat 成功握手的审计事件标注 scriptcat:<实例 ID>", t, func() {
		h := startTestServer(t)

		Convey("首次配对", func() {
			h.pairScriptCatInstance(instanceA, "work")
			So(waitForAuditEvent(h, audit.TypeHandshakeOK).Client, ShouldEqual, "scriptcat:"+instanceA)
		})

		Convey("会话握手", func() {
			key := h.registerScriptCat(instanceB, "home")
			h.connectScriptCatInstance(instanceB, key, "home")
			So(waitForAuditEvent(h, audit.TypeHandshakeOK).Client, ShouldEqual, "scriptcat:"+instanceB)
		})
	})
}

func TestPairingScriptCatInstanceLeavesOtherKeysAlone(t *testing.T) {
	Convey("配对一个 ScriptCat 实例不改动 pairing.key、其他 ScriptCat 实例或浏览器实例的密钥", t, func() {
		h := startTestServer(t)
		legacy := h.connectScriptCat()
		pairingKeyBefore, err := os.ReadFile(h.keyPath)
		So(err, ShouldBeNil)
		keyA := h.registerScriptCat(instanceA, "work")
		browserKey := h.registerBrowser(instanceA, "chrome-0123")

		h.pairScriptCatInstance(instanceB, "home")

		pairingKeyAfter, err := os.ReadFile(h.keyPath)
		So(err, ShouldBeNil)
		So(pairingKeyAfter, ShouldResemble, pairingKeyBefore)
		gotA, _ := h.scriptcats.Get(instanceA)
		So(gotA.Key, ShouldResemble, keyA)
		gotBrowser, _ := h.browsers.Get(instanceA)
		So(gotBrowser.Key, ShouldResemble, browserKey)
		legacy.alive()
	})
}
