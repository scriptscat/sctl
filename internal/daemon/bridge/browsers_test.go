package bridge

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/daemon/auth"
	"github.com/scriptscat/sctl/internal/daemon/store"
	"github.com/scriptscat/sctl/internal/pkg/audit"
)

const (
	instanceA = "0123456789abcdef0123456789abcdef"
	instanceB = "fedcba9876543210fedcba9876543210"
)

func browserPeer(id string) *authPeer {
	return &authPeer{Kind: peerKindBrowser, InstanceID: id}
}

func browserCapabilities(name string) capabilitiesParams {
	return capabilitiesParams{
		SchemaVersion: "1.0.0",
		Methods:       []string{"tabs.list", "tabs.open", "tabs.close", "tabs.activate", "windows.list"},
		Peer:          &capabilitiesPeer{Name: name, Product: "Chrome", ProductVersion: "129.0", ExtensionVersion: "0.1.0"},
	}
}

// challenge 拨号并读取 $session.authenticate 的 nonceD。
func (h *testHarness) challenge() (*extClient, Message, string) {
	e := dial(h.url)
	msg := e.read()
	So(msg.Method, ShouldEqual, methodAuthenticate)
	var cp authChallengeParams
	So(json.Unmarshal(msg.Params, &cp), ShouldBeNil)
	return e, msg, cp.NonceD
}

// authenticateBrowser 以实例密钥完成浏览器实例的会话认证并消费 hello;能力声明留给调用方。
func (h *testHarness) authenticateBrowser(id string, key []byte) *extClient {
	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.BrowserExtHMAC(auth.ModeSession, id, key, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: browserPeer(id)})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authenticatedParams
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(h.crypto.VerifyBrowserDaemonHMAC(auth.ModeSession, id, key, nonceD, nonceE, okp.HMAC), ShouldBeTrue)
	So(e.read().Method, ShouldEqual, methodHello)
	return e
}

// declare 发出能力声明并返回 daemon 的回复。
func (e *extClient) declare(params capabilitiesParams) Message {
	id := uuid.NewString()
	e.writeRequest(methodCapabilities, id, params)
	reply := e.read()
	So(reply.ID, ShouldEqual, id)
	return reply
}

// connectBrowser 以实例密钥和名称连接一个已配对的浏览器实例,要求 daemon 接受。
func (h *testHarness) connectBrowser(id string, key []byte, name string) *extClient {
	e := h.authenticateBrowser(id, key)
	So(e.declare(browserCapabilities(name)).Error, ShouldBeNil)
	return e
}

// pairBrowser 用 sctl connect 打开的配对码为实例 id 完成首次配对,以 name 声明能力,返回连接与下发的实例密钥。
func (h *testHarness) pairBrowser(id, name string) (*extClient, []byte) {
	display, err := h.srv.BeginEnrollment()
	So(err, ShouldBeNil)
	kpMac, kpEnc, err := h.crypto.DerivePairingKeys(display)
	So(err, ShouldBeNil)

	e, challenge, nonceD := h.challenge()
	nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
	mac := h.crypto.BrowserExtHMAC(auth.ModePairing, id, kpMac, nonceD, nonceE)
	e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac, Peer: browserPeer(id)})

	ok := e.read()
	So(ok.Method, ShouldEqual, methodAuthenticated)
	var okp authenticatedParams
	So(json.Unmarshal(ok.Params, &okp), ShouldBeNil)
	So(okp.Key, ShouldNotBeNil)
	So(h.crypto.VerifyBrowserDaemonHMAC(auth.ModePairing, id, kpMac, nonceD, nonceE, okp.HMAC), ShouldBeTrue)
	key, err := h.crypto.OpenKey(kpEnc, okp.Key.Ciphertext, okp.Key.IV)
	So(err, ShouldBeNil)
	So(e.read().Method, ShouldEqual, methodHello)
	So(e.declare(browserCapabilities(name)).Error, ShouldBeNil)
	return e, key
}

// registerBrowser 直接在登记表里放一个已配对实例,返回其密钥。
func (h *testHarness) registerBrowser(id, name string) []byte {
	key, _ := auth.NewLongTermKey()
	So(h.browsers.Pair(store.BrowserInstance{ID: id, Name: name, Key: key}), ShouldBeNil)
	return key
}

// connectScriptCat 以 pairing.key 中的密钥接入 ScriptCat。
func (h *testHarness) connectScriptCat() *extClient {
	key, _ := auth.NewLongTermKey()
	So(h.keys.Save(key), ShouldBeNil)
	return h.doSessionHandshake(key)
}

// scriptsListWorks 经 Call 发起 scripts.list,由 ScriptCat 连接应答,断言调用成功。
func (h *testHarness) scriptsListWorks(sc *extClient) {
	result := make(chan error, 1)
	go func() {
		resp, err := h.srv.Call(context.Background(), Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
		if err == nil && !resp.OK {
			err = resp.Error
		}
		result <- err
	}()
	req := sc.read()
	So(req.Method, ShouldEqual, "scripts.list")
	sc.writeResult(req.ID, json.RawMessage(`{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`))
	So(<-result, ShouldBeNil)
}

// alive 用对端发起的 $session.ping 证明连接仍在服务。
func (e *extClient) alive() {
	id := uuid.NewString()
	e.writeRequest(methodPing, id, struct{}{})
	So(e.read().ID, ShouldEqual, id)
}

// closedByDaemon 断言 daemon 已关闭这条连接。
func (e *extClient) closedByDaemon() {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err := e.ws.Read(ctx)
	So(err, ShouldNotBeNil)
}

func (h *testHarness) instance(ref string) (InstanceInfo, bool) {
	for _, info := range h.srv.Instances() {
		if info.ID == ref || info.Name == ref {
			return info, true
		}
	}
	return InstanceInfo{}, false
}

func TestBrowserPairing(t *testing.T) {
	Convey("配对浏览器实例签发实例密钥并以默认名称登记,ScriptCat 的密钥与连接不受影响", t, func() {
		h := startTestServer(t)
		sc := h.connectScriptCat()
		pairingKeyBefore, err := os.ReadFile(h.keyPath)
		So(err, ShouldBeNil)

		e, key := h.pairBrowser(instanceA, "chrome-0123")

		registered, ok := h.browsers.Get(instanceA)
		So(ok, ShouldBeTrue)
		So(registered.Name, ShouldEqual, "chrome-0123")
		So(registered.Key, ShouldResemble, key)
		So(registered.Product, ShouldEqual, "Chrome")

		pairingKeyAfter, err := os.ReadFile(h.keyPath)
		So(err, ShouldBeNil)
		So(pairingKeyAfter, ShouldResemble, pairingKeyBefore)
		So(h.srv.ExtConnected(), ShouldBeTrue)
		h.scriptsListWorks(sc)

		info, ok := h.instance(instanceA)
		So(ok, ShouldBeTrue)
		So(info.Online, ShouldBeTrue)
		So(info.Name, ShouldEqual, "chrome-0123")
		So(info.ProductVersion, ShouldEqual, "129.0")
		So(info.ExtensionVersion, ShouldEqual, "0.1.0")
		So(info.ConnectedAt.IsZero(), ShouldBeFalse)

		Convey("下发的实例密钥可用于之后的会话握手", func() {
			e.ws.CloseNow()
			again := h.connectBrowser(instanceA, key, "chrome-0123")
			again.alive()
		})
	})
}

func TestBrowserInstancesCoexist(t *testing.T) {
	Convey("ScriptCat 与两个浏览器实例同时在线;同一实例重连只替换它自己", t, func() {
		h := startTestServer(t)
		sc := h.connectScriptCat()
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		keyB := h.registerBrowser(instanceB, "edge-fedc")
		a1 := h.connectBrowser(instanceA, keyA, "chrome-0123")
		b := h.connectBrowser(instanceB, keyB, "edge-fedc")

		infoA, _ := h.instance(instanceA)
		infoB, _ := h.instance(instanceB)
		So(infoA.Online, ShouldBeTrue)
		So(infoB.Online, ShouldBeTrue)
		So(h.srv.ExtConnected(), ShouldBeTrue)
		h.scriptsListWorks(sc)
		a1.alive()
		b.alive()

		a2 := h.connectBrowser(instanceA, keyA, "chrome-0123")
		a1.closedByDaemon()
		a2.alive()
		b.alive()
		h.scriptsListWorks(sc)
		So(h.srv.Instances(), ShouldHaveLength, 2)

		Convey("新的 ScriptCat 连接只替换旧的 ScriptCat 连接", func() {
			key, _, err := h.keys.Load()
			So(err, ShouldBeNil)
			sc2 := h.doSessionHandshake(key)
			sc.closedByDaemon()
			a2.alive()
			b.alive()
			h.scriptsListWorks(sc2)
		})

		Convey("浏览器实例断开后仍列为已配对但离线", func() {
			b.ws.CloseNow()
			deadline := time.Now().Add(3 * time.Second)
			for time.Now().Before(deadline) {
				if info, _ := h.instance(instanceB); !info.Online {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			info, ok := h.instance(instanceB)
			So(ok, ShouldBeTrue)
			So(info.Online, ShouldBeFalse)
			So(info.ConnectedAt.IsZero(), ShouldBeTrue)
		})
	})
}

func TestBrowserIsRegisteredBeforeCapabilitiesReply(t *testing.T) {
	Convey("浏览器实例收到 capabilities 回复时已登记为在线", t, func() {
		h := startTestServer(t)
		key := h.registerBrowser(instanceA, "chrome-0123")
		e := h.authenticateBrowser(instanceA, key)

		early, reply := h.declareWithRegistrationFrozen(e, browserCapabilities("chrome-0123"),
			func() bool { return h.srv.online[instanceA] != nil })
		So(early, ShouldBeFalse)
		So(reply.Error, ShouldBeNil)
		info, _ := h.instance(instanceA)
		So(info.Online, ShouldBeTrue)
	})
}

func TestBrowserResponsesCannotAnswerScriptCatCalls(t *testing.T) {
	Convey("浏览器实例不能冒名应答发给 ScriptCat 的调用", t, func() {
		h := startTestServer(t)
		sc := h.connectScriptCat()
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")

		result := make(chan Response, 1)
		go func() {
			resp, _ := h.srv.Call(context.Background(), Request{Action: "scripts.list", Input: json.RawMessage(`{}`)})
			result <- resp
		}()
		req := sc.read()
		a.writeResult(req.ID, json.RawMessage(`{"scripts":[{"forged":true}],"contentTrust":"untrusted-user-script-metadata"}`))
		a.alive()
		sc.writeResult(req.ID, json.RawMessage(`{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`))
		So(string((<-result).Result), ShouldEqual, `{"scripts":[],"contentTrust":"untrusted-user-script-metadata"}`)
	})
}

func TestBrowserHandshakeBindsInstanceIdentity(t *testing.T) {
	Convey("握手 MAC 绑定对端类型与实例 ID", t, func() {
		h := startTestServer(t)
		// 两个实例刻意共用同一把密钥:只有身份参与 MAC 计算时,冒充才会失败。
		key := h.registerBrowser(instanceA, "chrome-0123")
		So(h.browsers.Pair(store.BrowserInstance{ID: instanceB, Name: "edge-fedc", Key: key}), ShouldBeNil)

		Convey("为实例 A 计算的 MAC 声明为实例 B 时握手失败并进入审计", func() {
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			macA := h.crypto.BrowserExtHMAC(auth.ModeSession, instanceA, key, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: macA, Peer: browserPeer(instanceB)})
			e.closedByDaemon()

			ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.Client, ShouldEqual, "browser:"+instanceB)
			info, _ := h.instance(instanceB)
			So(info.Online, ShouldBeFalse)
		})

		Convey("ScriptCat 公式的 MAC 不能以浏览器实例身份通过", func() {
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.ExtHMAC(auth.ModeSession, key, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: browserPeer(instanceA)})
			e.closedByDaemon()
		})

		Convey("浏览器实例的 MAC 去掉身份声明后不能以 ScriptCat 身份通过", func() {
			So(h.keys.Save(key), ShouldBeNil)
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.BrowserExtHMAC(auth.ModeSession, instanceA, key, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac})
			e.closedByDaemon()
			So(h.srv.ExtConnected(), ShouldBeFalse)
		})
	})
}

func TestBrowserRename(t *testing.T) {
	Convey("重命名通过以新名称重连完成,名称在已配对实例中唯一", t, func() {
		h := startTestServer(t)
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		h.registerBrowser(instanceB, "edge-fedc")

		Convey("新名称未被占用:登记表更新", func() {
			a := h.connectBrowser(instanceA, keyA, "work")
			a.alive()
			got, _ := h.browsers.Get(instanceA)
			So(got.Name, ShouldEqual, "work")
			info, _ := h.instance("work")
			So(info.ID, ShouldEqual, instanceA)
		})

		Convey("名称被另一个已配对(即便离线)的实例占用:回复 CONFLICT、断开并保留原名称", func() {
			e := h.authenticateBrowser(instanceA, keyA)
			reply := e.declare(browserCapabilities("edge-fedc"))
			So(reply.Error, ShouldNotBeNil)
			So(reply.Error.Code, ShouldEqual, -32000)
			So(reply.Error.Data, ShouldNotBeNil)
			So(reply.Error.Data.Code, ShouldEqual, "CONFLICT")
			e.closedByDaemon()

			got, _ := h.browsers.Get(instanceA)
			So(got.Name, ShouldEqual, "chrome-0123")
			info, _ := h.instance(instanceA)
			So(info.Online, ShouldBeFalse)
		})

		Convey("名称不符合规则时连接不进入可用状态", func() {
			for _, name := range []string{"", "Chrome", "has space", "under_score", "a23456789012345678901234567890123"} {
				e := h.authenticateBrowser(instanceA, keyA)
				e.writeRequest(methodCapabilities, uuid.NewString(), browserCapabilities(name))
				e.closedByDaemon()
			}
			got, _ := h.browsers.Get(instanceA)
			So(got.Name, ShouldEqual, "chrome-0123")
		})

		Convey("浏览器实例不声明 peer 时连接不进入可用状态", func() {
			e := h.authenticateBrowser(instanceA, keyA)
			e.writeRequest(methodCapabilities, uuid.NewString(), capabilitiesParams{SchemaVersion: "1.0.0", Methods: []string{"tabs.list"}})
			e.closedByDaemon()
			info, _ := h.instance(instanceA)
			So(info.Online, ShouldBeFalse)
		})
	})
}

func TestForgetInstance(t *testing.T) {
	Convey("从 daemon 侧忘记浏览器实例", t, func() {
		h := startTestServer(t)
		sc := h.connectScriptCat()
		keyA := h.registerBrowser(instanceA, "chrome-0123")
		h.registerBrowser(instanceB, "edge-fedc")
		a := h.connectBrowser(instanceA, keyA, "chrome-0123")

		Convey("按名称忘记在线实例:删除密钥与登记、断开连接,之后握手失败;ScriptCat 不受影响", func() {
			So(h.srv.ForgetInstance("chrome-0123"), ShouldBeNil)
			a.closedByDaemon()
			_, ok := h.browsers.Get(instanceA)
			So(ok, ShouldBeFalse)
			_, listed := h.instance(instanceA)
			So(listed, ShouldBeFalse)

			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.BrowserExtHMAC(auth.ModeSession, instanceA, keyA, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: browserPeer(instanceA)})
			e.closedByDaemon()
			ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonUnknownInstance)
			So(ev.Client, ShouldEqual, "browser:"+instanceA)

			h.scriptsListWorks(sc)
		})

		Convey("按实例 ID 忘记离线实例", func() {
			So(h.srv.ForgetInstance(instanceB), ShouldBeNil)
			_, ok := h.browsers.Get(instanceB)
			So(ok, ShouldBeFalse)
			a.alive()
		})

		Convey("不匹配任何已配对实例时返回 ErrInstanceNotFound", func() {
			So(h.srv.ForgetInstance("nope"), ShouldEqual, ErrInstanceNotFound)
			So(h.srv.Instances(), ShouldHaveLength, 2)
		})
	})
}

func TestBrowserPeerValidation(t *testing.T) {
	Convey("认证响应里的实例身份在边界校验", t, func() {
		h := startTestServer(t)
		key := h.registerBrowser(instanceA, "chrome-0123")

		for _, peer := range []*authPeer{
			{Kind: peerKindBrowser, InstanceID: "0123456789ABCDEF0123456789ABCDEF"},
			{Kind: peerKindBrowser, InstanceID: "0123"},
			{Kind: "scriptcat", InstanceID: instanceA},
		} {
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.BrowserExtHMAC(auth.ModeSession, peer.InstanceID, key, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modeSession, NonceE: nonceE, HMAC: mac, Peer: peer})
			e.closedByDaemon()
		}
		ev := waitForAuditEvent(h, audit.TypeHandshakeFailed)
		So(ev.Reason, ShouldEqual, audit.ReasonProtocol)
		So(ev.Client, ShouldBeBlank)
	})
}

func TestBrowserAudit(t *testing.T) {
	Convey("浏览器实例的配对与握手进入 daemon 审计,并标注实例", t, func() {
		h := startTestServer(t)

		Convey("配对码错误记录为 pairing.failed", func() {
			_, err := h.srv.BeginEnrollment()
			So(err, ShouldBeNil)
			wrongMac, _, _ := h.crypto.DerivePairingKeys("WRONGCODE")
			e, challenge, nonceD := h.challenge()
			nonceE, _ := auth.RandomNonceHex(h.proto.Crypto.NonceBytes)
			mac := h.crypto.BrowserExtHMAC(auth.ModePairing, instanceA, wrongMac, nonceD, nonceE)
			e.writeResult(challenge.ID, authResponseResult{Mode: modePairing, NonceE: nonceE, HMAC: mac, Peer: browserPeer(instanceA)})
			e.closedByDaemon()

			ev := waitForAuditEvent(h, audit.TypePairingFailed)
			So(ev.Reason, ShouldEqual, audit.ReasonHMACMismatch)
			So(ev.Client, ShouldEqual, "browser:"+instanceA)
			_, ok := h.browsers.Get(instanceA)
			So(ok, ShouldBeFalse)
		})

		Convey("成功握手记录为 handshake.ok", func() {
			key := h.registerBrowser(instanceA, "chrome-0123")
			h.connectBrowser(instanceA, key, "chrome-0123")
			ev := waitForAuditEvent(h, audit.TypeHandshakeOK)
			So(ev.Client, ShouldEqual, "browser:"+instanceA)
		})
	})
}
