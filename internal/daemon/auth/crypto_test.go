package auth

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	. "github.com/smartystreets/goconvey/convey"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

func TestSessionHandshake(t *testing.T) {
	p, err := protocol.Load()
	if err != nil {
		t.Fatalf("加载协议失败: %v", err)
	}
	cfg := NewCrypto(p)

	Convey("会话握手(双向 HMAC)", t, func() {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		nonceD, _ := RandomNonceHex(p.Crypto.NonceBytes)
		nonceE, _ := RandomNonceHex(p.Crypto.NonceBytes)

		Convey("扩展应答的 HMAC 由 daemon 用同一密钥验证通过", func() {
			extMAC := cfg.ExtHMAC(ModeSession, key, nonceD, nonceE)
			So(cfg.VerifyExtHMAC(ModeSession, key, nonceD, nonceE, extMAC), ShouldBeTrue)
		})

		Convey("daemon 的 $session.authenticated HMAC 由扩展用同一密钥验证通过", func() {
			okMAC := cfg.DaemonHMAC(ModeSession, key, nonceD, nonceE)
			So(cfg.VerifyDaemonHMAC(ModeSession, key, nonceD, nonceE, okMAC), ShouldBeTrue)
		})

		Convey("ext 与 daemon 方向的 HMAC 不相等(上下文与顺序不同)", func() {
			So(cfg.ExtHMAC(ModeSession, key, nonceD, nonceE),
				ShouldNotEqual, cfg.DaemonHMAC(ModeSession, key, nonceD, nonceE))
		})

		Convey("密钥不符时验证失败", func() {
			wrong := make([]byte, 32)
			_, _ = rand.Read(wrong)
			extMAC := cfg.ExtHMAC(ModeSession, key, nonceD, nonceE)
			So(cfg.VerifyExtHMAC(ModeSession, wrong, nonceD, nonceE, extMAC), ShouldBeFalse)
		})

		Convey("nonce 被篡改时验证失败(抗重放)", func() {
			extMAC := cfg.ExtHMAC(ModeSession, key, nonceD, nonceE)
			other, _ := RandomNonceHex(p.Crypto.NonceBytes)
			So(cfg.VerifyExtHMAC(ModeSession, key, other, nonceE, extMAC), ShouldBeFalse)
		})

		Convey("会话模式与配对模式上下文不同,MAC 不可互换", func() {
			So(cfg.ExtHMAC(ModeSession, key, nonceD, nonceE),
				ShouldNotEqual, cfg.ExtHMAC(ModePairing, key, nonceD, nonceE))
		})

		Convey("HMAC 输出为 64 位小写 hex", func() {
			mac := cfg.ExtHMAC(ModeSession, key, nonceD, nonceE)
			So(len(mac), ShouldEqual, 64)
			So(mac, ShouldEqual, strings.ToLower(mac))
			_, decErr := hex.DecodeString(mac)
			So(decErr, ShouldBeNil)
		})
	})
}

func TestBrowserHandshakeBindsInstanceIdentity(t *testing.T) {
	p, err := protocol.Load()
	if err != nil {
		t.Fatalf("加载协议失败: %v", err)
	}
	cfg := NewCrypto(p)

	Convey("浏览器实例握手 MAC 绑定对端类型与实例 ID", t, func() {
		key := make([]byte, 32)
		_, _ = rand.Read(key)
		nonceD, _ := RandomNonceHex(p.Crypto.NonceBytes)
		nonceE, _ := RandomNonceHex(p.Crypto.NonceBytes)
		const idA = "0123456789abcdef0123456789abcdef"
		const idB = "fedcba9876543210fedcba9876543210"

		Convey("两个方向的 MAC 按线上公式计算:浏览器专用 context || instanceId || 两个 nonce", func() {
			want := func(context, first, second string) string {
				mac := hmac.New(sha256.New, key)
				mac.Write([]byte(context + idA + first + second))
				return hex.EncodeToString(mac.Sum(nil))
			}
			So(cfg.BrowserExtHMAC(ModeSession, idA, key, nonceD, nonceE), ShouldEqual, want("sctl-browser-rpc-v1/ext", nonceD, nonceE))
			So(cfg.BrowserDaemonHMAC(ModeSession, idA, key, nonceD, nonceE), ShouldEqual, want("sctl-browser-rpc-v1/daemon", nonceE, nonceD))
			So(cfg.BrowserExtHMAC(ModePairing, idA, key, nonceD, nonceE), ShouldEqual, want("sctl-browser-rpc-v1/pair-ext", nonceD, nonceE))
			So(cfg.BrowserDaemonHMAC(ModePairing, idA, key, nonceD, nonceE), ShouldEqual, want("sctl-browser-rpc-v1/pair-daemon", nonceE, nonceD))
		})

		Convey("为实例 A 计算的 MAC 不能冒充实例 B 通过校验", func() {
			macA := cfg.BrowserExtHMAC(ModeSession, idA, key, nonceD, nonceE)
			So(cfg.VerifyBrowserExtHMAC(ModeSession, idA, key, nonceD, nonceE, macA), ShouldBeTrue)
			So(cfg.VerifyBrowserExtHMAC(ModeSession, idB, key, nonceD, nonceE, macA), ShouldBeFalse)
			okA := cfg.BrowserDaemonHMAC(ModeSession, idA, key, nonceD, nonceE)
			So(cfg.VerifyBrowserDaemonHMAC(ModeSession, idA, key, nonceD, nonceE, okA), ShouldBeTrue)
			So(cfg.VerifyBrowserDaemonHMAC(ModeSession, idB, key, nonceD, nonceE, okA), ShouldBeFalse)
		})

		Convey("ScriptCat 的 MAC 与浏览器实例的 MAC 互不通用", func() {
			scriptCatMAC := cfg.ExtHMAC(ModeSession, key, nonceD, nonceE)
			So(cfg.VerifyBrowserExtHMAC(ModeSession, idA, key, nonceD, nonceE, scriptCatMAC), ShouldBeFalse)
			browserMAC := cfg.BrowserExtHMAC(ModeSession, idA, key, nonceD, nonceE)
			So(cfg.VerifyExtHMAC(ModeSession, key, nonceD, nonceE, browserMAC), ShouldBeFalse)
		})
	})
}

func TestPairingKeyDelivery(t *testing.T) {
	p, _ := protocol.Load()
	cfg := NewCrypto(p)

	Convey("配对密钥派生与下发", t, func() {
		_, code, _ := NewPairingCode()

		Convey("同一配对码派生出确定且互不相同的 mac / enc 密钥", func() {
			mac1, enc1, err := cfg.DerivePairingKeys(code)
			So(err, ShouldBeNil)
			mac2, enc2, err := cfg.DerivePairingKeys(code)
			So(err, ShouldBeNil)
			So(mac1, ShouldResemble, mac2)
			So(enc1, ShouldResemble, enc2)
			So(mac1, ShouldNotResemble, enc1)
			So(len(mac1), ShouldEqual, 32)
			So(len(enc1), ShouldEqual, 32)
		})

		Convey("配对码大小写/连字符归一化后派生同一密钥", func() {
			mac1, _, _ := cfg.DerivePairingKeys(code)
			mac2, _, _ := cfg.DerivePairingKeys(FormatPairingCode(strings.ToLower(code)))
			So(mac1, ShouldResemble, mac2)
		})

		Convey("AES-256-GCM 下发的长期密钥可被同一 enc 密钥解出原文", func() {
			_, enc, _ := cfg.DerivePairingKeys(code)
			k := make([]byte, 32)
			_, _ = rand.Read(k)
			ct, iv, err := cfg.SealKey(enc, k)
			So(err, ShouldBeNil)
			So(ct, ShouldNotBeBlank)
			So(iv, ShouldNotBeBlank)
			got, err := cfg.OpenKey(enc, ct, iv)
			So(err, ShouldBeNil)
			So(got, ShouldResemble, k)
		})

		Convey("enc 密钥不符时解密失败(GCM 认证)", func() {
			_, enc, _ := cfg.DerivePairingKeys(code)
			k := make([]byte, 32)
			_, _ = rand.Read(k)
			ct, iv, _ := cfg.SealKey(enc, k)
			_, otherCode, _ := NewPairingCode()
			_, wrongEnc, _ := cfg.DerivePairingKeys(otherCode)
			_, err := cfg.OpenKey(wrongEnc, ct, iv)
			So(err, ShouldNotBeNil)
		})
	})
}

func TestPairingCode(t *testing.T) {
	Convey("一次性配对码", t, func() {
		const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

		Convey("规范码为 8 个 crockford-base32 字符,展示为 XXXX-XXXX", func() {
			canonical, display, err := NewPairingCode()
			So(err, ShouldBeNil)
			So(len(canonical), ShouldEqual, 8)
			for _, c := range canonical {
				So(strings.ContainsRune(crockford, c), ShouldBeTrue)
			}
			So(display, ShouldEqual, canonical[:4]+"-"+canonical[4:])
		})

		Convey("连续生成的码不相同(足够随机)", func() {
			seen := map[string]bool{}
			for i := 0; i < 64; i++ {
				c, _, _ := NewPairingCode()
				So(seen[c], ShouldBeFalse)
				seen[c] = true
			}
		})

		Convey("归一化去除连字符/空格并大写,兼容 crockford 混淆字符", func() {
			So(NormalizePairingCode("abcd-efgh"), ShouldEqual, "ABCDEFGH")
			So(NormalizePairingCode(" ab cd efgh "), ShouldEqual, "ABCDEFGH")
			// crockford: o->0, i/l->1
			So(NormalizePairingCode("oil1-2345"), ShouldEqual, "01112345")
		})
	})
}
