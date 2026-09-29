// 实例 ID、nonce、MAC 与密钥在线上都是小写 hex。
export function bytesToHex(bytes: Uint8Array): string {
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}
