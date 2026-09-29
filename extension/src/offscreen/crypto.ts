import { CRYPTO } from "@/protocol/generated/protocol.generated";
import { bytesToHex } from "@/shared/hex";

// WebCrypto 的 BufferSource 要求 ArrayBuffer 后备，所有原语统一使用这个别名。
export type Bytes = Uint8Array<ArrayBuffer>;

// 一次握手使用的 MAC 素材：会话模式用长期密钥 K，配对模式用配对码派生的 MAC 密钥。
export interface MacMaterial {
  mode: "session" | "pairing";
  instanceId: string;
  key: Bytes;
}

export interface KeyDelivery {
  ciphertext: string;
  iv: string;
}

const encoder = new TextEncoder();

function utf8(text: string): Bytes {
  return new Uint8Array(encoder.encode(text));
}

export function hexToBytes(hex: string): Bytes {
  const out = new Uint8Array(hex.length >> 1);
  for (let i = 0; i < out.length; i++) {
    out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  }
  return out;
}

function base64ToBytes(b64: string): Bytes {
  const bin = atob(b64);
  return Uint8Array.from(bin, (c) => c.charCodeAt(0));
}

export function randomNonceHex(): string {
  return bytesToHex(crypto.getRandomValues(new Uint8Array(CRYPTO.nonceBytes)));
}

// 比较两个十六进制 MAC，耗时与内容无关。
export function constantTimeEqualHex(a: string, b: string): boolean {
  if (a.length !== b.length) {
    return false;
  }
  let diff = 0;
  for (let i = 0; i < a.length; i++) {
    diff |= a.charCodeAt(i) ^ b.charCodeAt(i);
  }
  return diff === 0;
}

// HMAC-SHA-256(key, utf8(context || instanceId || first || second))，与 daemon 的 BrowserExtHMAC/BrowserDaemonHMAC 一致；
// browser* 上下文区分对端类型，instanceId 让截获的 MAC 无法冒充另一个实例。
async function handshakeMac(material: MacMaterial, context: string, first: string, second: string): Promise<string> {
  const key = await crypto.subtle.importKey("raw", material.key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  const sig = await crypto.subtle.sign("HMAC", key, utf8(context + material.instanceId + first + second));
  return bytesToHex(new Uint8Array(sig));
}

export function extensionMac(material: MacMaterial, nonceD: string, nonceE: string): Promise<string> {
  const context = material.mode === "session" ? CRYPTO.context.browserSessionExt : CRYPTO.context.browserPairExt;
  return handshakeMac(material, context, nonceD, nonceE);
}

export function daemonMac(material: MacMaterial, nonceD: string, nonceE: string): Promise<string> {
  const context = material.mode === "session" ? CRYPTO.context.browserSessionDaemon : CRYPTO.context.browserPairDaemon;
  return handshakeMac(material, context, nonceE, nonceD);
}

// 从已归一化的配对码经 HKDF-SHA-256 派生 MAC 与 AES-GCM 两把 256 位临时密钥，参数与 ScriptCat 配对相同。
export async function derivePairingKeys(code: string): Promise<{ mac: Bytes; enc: Bytes }> {
  const ikm = await crypto.subtle.importKey("raw", utf8(code), "HKDF", false, ["deriveBits"]);
  const salt = utf8(CRYPTO.context.pairKdfSalt);
  const derive = async (info: string): Promise<Bytes> =>
    new Uint8Array(await crypto.subtle.deriveBits({ name: "HKDF", hash: "SHA-256", salt, info: utf8(info) }, ikm, 256));
  return { mac: await derive(CRYPTO.context.pairKdfInfoMac), enc: await derive(CRYPTO.context.pairKdfInfoEnc) };
}

// 解开 $session.authenticated 下发的长期密钥（AES-256-GCM，密文与 tag 拼接），返回小写 hex。
export async function decryptSessionKey(enc: Bytes, delivery: KeyDelivery): Promise<string> {
  const key = await crypto.subtle.importKey("raw", enc, { name: "AES-GCM" }, false, ["decrypt"]);
  const plain = await crypto.subtle.decrypt(
    { name: "AES-GCM", iv: base64ToBytes(delivery.iv) },
    key,
    base64ToBytes(delivery.ciphertext),
  );
  return bytesToHex(new Uint8Array(plain));
}
