package auth

import (
	"crypto/rand"
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
