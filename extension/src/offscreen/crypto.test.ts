import { describe, expect, it } from "vitest";
import { daemonMac, decryptSessionKey, derivePairingKeys, extensionMac, hexToBytes } from "./crypto";

// 已知答案由 daemon 的 Go 实现（internal/daemon/auth.Crypto）对同一组输入算出，
// 钉住浏览器实例的 MAC 构成：browser* 上下文 || instanceId || 两个 nonce，以及配对密钥派生与下发。
const KEY = hexToBytes("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
const INSTANCE_ID = "3f2a9c0e5b7d41e8a6f0c2d4e6f80a1b";
const NONCE_D = "aa00112233445566778899aabbccddeeff00112233445566778899aabbccddee";
const NONCE_E = "bbffeeddccbbaa99887766554433221100ffeeddccbbaa998877665544332211";

describe("browser handshake crypto", () => {
  it("computes the session MACs the daemon expects for this instance", async () => {
    const session = { mode: "session" as const, instanceId: INSTANCE_ID, key: KEY };
    expect(await extensionMac(session, NONCE_D, NONCE_E)).toBe(
      "2e345f21f5791d0b707a6f0eae6d02ac2de953de8d45dc8e0c97dd8bcf302c99",
    );
    expect(await daemonMac(session, NONCE_D, NONCE_E)).toBe(
      "ac72627a58d9329f5f77346c22e63049563d767cd690a3cfa6c9b6314d768bb9",
    );
  });

  it("derives pairing keys from a hand-typed code and computes the pairing MACs", async () => {
    const { mac } = await derivePairingKeys("7K3M9QPX");
    const pairing = { mode: "pairing" as const, instanceId: INSTANCE_ID, key: mac };
    expect(await extensionMac(pairing, NONCE_D, NONCE_E)).toBe(
      "b220d935a23f96696a8c3b1d934e9287de32b0eb4d9a5a5bbf19e755fea6a940",
    );
    expect(await daemonMac(pairing, NONCE_D, NONCE_E)).toBe(
      "ea4c7cd883c94406cc8952702e32804fc7099e7f6841e19cbd2762ff6d0e268e",
    );
  });

  it("decrypts the session key the daemon delivers with the pairing encryption key", async () => {
    const { enc } = await derivePairingKeys("7K3M9QPX");
    const key = await decryptSessionKey(enc, {
      ciphertext: "C12Ve2AhzpeCAEKbKBa5vjkfIcZ0dOq9P3CkpYPZ/r3FnApc9KfpT7OP+Y4GoqQ5",
      iv: "ni82RKZHtuASe7ar",
    });
    expect(key).toBe("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
  });

  it("fails to decrypt a delivery sealed with a different pairing code", async () => {
    const { enc } = await derivePairingKeys("7K3M9QPY");
    await expect(
      decryptSessionKey(enc, {
        ciphertext: "C12Ve2AhzpeCAEKbKBa5vjkfIcZ0dOq9P3CkpYPZ/r3FnApc9KfpT7OP+Y4GoqQ5",
        iv: "ni82RKZHtuASe7ar",
      }),
    ).rejects.toThrow();
  });
});
