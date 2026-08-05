// Package auth 实现桥接协议的认证原语:双向 HMAC 挑战应答握手(会话/配对两种模式)、
// 配对码派生(HKDF)与长期密钥下发(AES-256-GCM)。长期密钥的落盘存储见
// internal/daemon/store。全部常量取自内嵌 protocol.json(见 internal/pkg/protocol),
// 不在此硬编码。
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/scriptscat/sctl/internal/pkg/protocol"
)

// Mode 区分握手所用的上下文与密钥来源。
type Mode int

const (
	// ModeSession 使用长期共享密钥 K 的会话握手。
	ModeSession Mode = iota
	// ModePairing 使用配对码派生密钥 Kp_mac 的首次配对握手。
	ModePairing
)

// crockfordAlphabet 是 Crockford base32 字母表(去掉 I L O U 以避免混淆)。
const crockfordAlphabet = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// Crypto 承载从 protocol.json 读出的上下文字符串,提供握手与配对的密码学操作。
type Crypto struct {
	ctx map[string]string
}

// NewCrypto 用协议常量构造密码学助手。
func NewCrypto(p *protocol.Protocol) *Crypto {
	return &Crypto{ctx: p.Crypto.Context}
}

func (c *Crypto) extContext(mode Mode) string {
	if mode == ModePairing {
		return c.ctx["pairExt"]
	}
	return c.ctx["sessionExt"]
}

func (c *Crypto) daemonContext(mode Mode) string {
	if mode == ModePairing {
		return c.ctx["pairDaemon"]
	}
	return c.ctx["sessionDaemon"]
}

// handshakeHMAC 计算 HMAC-SHA256( key, context || first || second ) 并返回小写 hex。
// nonce 以其小写 hex 字符串形式参与拼接(与扩展侧 WebCrypto 实现一致,免去 hex 解码)。
func handshakeHMAC(key []byte, context, first, second string) string {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(context + first + second))
	return hex.EncodeToString(mac.Sum(nil))
}

// ExtHMAC 计算扩展对 $session.authenticate 的响应应携带的 HMAC:context=ctx.*Ext,顺序 nonceD||nonceE。
func (c *Crypto) ExtHMAC(mode Mode, key []byte, nonceD, nonceE string) string {
	return handshakeHMAC(key, c.extContext(mode), nonceD, nonceE)
}

// DaemonHMAC 计算 daemon $session.authenticated 通知应携带的 HMAC:context=ctx.*Daemon,顺序 nonceE||nonceD。
func (c *Crypto) DaemonHMAC(mode Mode, key []byte, nonceD, nonceE string) string {
	return handshakeHMAC(key, c.daemonContext(mode), nonceE, nonceD)
}

// VerifyExtHMAC 恒定时间校验扩展应答的 HMAC。
func (c *Crypto) VerifyExtHMAC(mode Mode, key []byte, nonceD, nonceE, got string) bool {
	return constantTimeHexEqual(c.ExtHMAC(mode, key, nonceD, nonceE), got)
}

// VerifyDaemonHMAC 恒定时间校验 daemon $session.authenticated 的 HMAC(供扩展/测试使用)。
func (c *Crypto) VerifyDaemonHMAC(mode Mode, key []byte, nonceD, nonceE, got string) bool {
	return constantTimeHexEqual(c.DaemonHMAC(mode, key, nonceD, nonceE), got)
}

// constantTimeHexEqual 解码两个 hex 字符串后做恒定时间比较,任一非法 hex 即判否。
func constantTimeHexEqual(want, got string) bool {
	wb, err := hex.DecodeString(want)
	if err != nil {
		return false
	}
	gb, err := hex.DecodeString(got)
	if err != nil {
		return false
	}
	return hmac.Equal(wb, gb)
}

// DerivePairingKeys 从配对码派生 (Kp_mac, Kp_enc),各 32 字节。
func (c *Crypto) DerivePairingKeys(code string) (kpMac, kpEnc []byte, err error) {
	ikm := []byte(NormalizePairingCode(code))
	salt := []byte(c.ctx["pairKdfSalt"])
	kpMac, err = hkdf.Key(sha256.New, ikm, salt, c.ctx["pairKdfInfoMac"], 32)
	if err != nil {
		return nil, nil, fmt.Errorf("derive Kp_mac: %w", err)
	}
	kpEnc, err = hkdf.Key(sha256.New, ikm, salt, c.ctx["pairKdfInfoEnc"], 32)
	if err != nil {
		return nil, nil, fmt.Errorf("derive Kp_enc: %w", err)
	}
	return kpMac, kpEnc, nil
}

// SealKey 用 Kp_enc 以 AES-256-GCM 加密长期密钥 K,返回 base64(密文+tag) 与 base64(iv)。
func (c *Crypto) SealKey(kpEnc, k []byte) (ctB64, ivB64 string, err error) {
	block, err := aes.NewCipher(kpEnc)
	if err != nil {
		return "", "", fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", "", fmt.Errorf("create GCM: %w", err)
	}
	iv := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(iv); err != nil {
		return "", "", fmt.Errorf("generate iv: %w", err)
	}
	ct := gcm.Seal(nil, iv, k, nil)
	return base64.StdEncoding.EncodeToString(ct), base64.StdEncoding.EncodeToString(iv), nil
}

// OpenKey 用 Kp_enc 解出下发的长期密钥 K(扩展侧逻辑,供测试对拍)。
func (c *Crypto) OpenKey(kpEnc []byte, ctB64, ivB64 string) ([]byte, error) {
	ct, err := base64.StdEncoding.DecodeString(ctB64)
	if err != nil {
		return nil, fmt.Errorf("decode ciphertext: %w", err)
	}
	iv, err := base64.StdEncoding.DecodeString(ivB64)
	if err != nil {
		return nil, fmt.Errorf("decode iv: %w", err)
	}
	block, err := aes.NewCipher(kpEnc)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create GCM: %w", err)
	}
	k, err := gcm.Open(nil, iv, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("GCM decryption failed: %w", err)
	}
	return k, nil
}

// RandomNonceHex 生成 n 字节随机数并返回小写 hex 表示。
func RandomNonceHex(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// NewLongTermKey 生成 256-bit 长期共享密钥 K。
func NewLongTermKey() ([]byte, error) {
	k := make([]byte, 32)
	if _, err := rand.Read(k); err != nil {
		return nil, fmt.Errorf("generate long-term key: %w", err)
	}
	return k, nil
}

// NewPairingCode 生成一次性配对码,返回规范形(8 字符)与展示形(XXXX-XXXX)。
func NewPairingCode() (canonical, display string, err error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", "", fmt.Errorf("generate pairing code: %w", err)
	}
	var sb strings.Builder
	for _, v := range b {
		// 取每字节低 5 位映射到 32 字母表,分布均匀无模偏差。
		sb.WriteByte(crockfordAlphabet[v&0x1f])
	}
	canonical = sb.String()
	return canonical, FormatPairingCode(canonical), nil
}

// FormatPairingCode 把规范配对码格式化为 XXXX-XXXX 展示形。
func FormatPairingCode(canonical string) string {
	c := NormalizePairingCode(canonical)
	if len(c) != 8 {
		return c
	}
	return c[:4] + "-" + c[4:]
}

// NormalizePairingCode 归一化用户输入:去连字符/空格、大写,并按 Crockford 规则消歧混淆字符。
func NormalizePairingCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		switch r {
		case '-', ' ', '\t':
			continue
		case 'O':
			sb.WriteRune('0')
		case 'I', 'L':
			sb.WriteRune('1')
		default:
			sb.WriteRune(r)
		}
	}
	return sb.String()
}
