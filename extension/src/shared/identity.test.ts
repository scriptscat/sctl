import { describe, expect, it } from "vitest";
import { detectProduct, defaultName, isValidName, newInstanceId } from "./identity";
import { DEFAULT_ADDRESS, parseAddress } from "./address";
import { normalizePairingCode } from "./pairing-code";

describe("instance identity", () => {
  it("generates a fresh 32-digit lowercase hex instance ID each time", () => {
    const first = newInstanceId();
    expect(first).toMatch(/^[0-9a-f]{32}$/);
    expect(newInstanceId()).not.toBe(first);
  });

  it("names an instance by its browser brand and the first four hex digits of its ID", () => {
    const id = "3f2a9c0e5b7d41e8a6f0c2d4e6f80a1b";
    const edge = detectProduct([
      { brand: "Not.A/Brand", version: "99" },
      { brand: "Microsoft Edge", version: "129" },
    ]);
    const chrome = detectProduct([
      { brand: "Chromium", version: "129" },
      { brand: "Google Chrome", version: "129" },
    ]);
    expect(defaultName(edge, id)).toBe("edge-3f2a");
    expect(defaultName(chrome, id)).toBe("chrome-3f2a");
    expect(chrome.product).toBe("Chrome");
  });

  it("falls back to chromium when the browser reports no known brand", () => {
    expect(defaultName(detectProduct(undefined), "91c0aaaaaaaaaaaaaaaaaaaaaaaaaaaa")).toBe("chromium-91c0");
  });

  it("accepts only 1-32 lowercase letters, digits and hyphens as a name", () => {
    expect(isValidName("chrome-3f2a")).toBe(true);
    expect(isValidName("a".repeat(32))).toBe(true);
    expect(isValidName("a".repeat(33))).toBe(false);
    expect(isValidName("")).toBe(false);
    expect(isValidName("Chrome")).toBe(false);
    expect(isValidName("work laptop")).toBe(false);
  });
});

describe("daemon address", () => {
  it("defaults to the daemon's default listen address", () => {
    expect(DEFAULT_ADDRESS).toBe("127.0.0.1:8643");
  });

  it("accepts host:port forms and trims surrounding space", () => {
    expect(parseAddress(" 127.0.0.1:9000 ")).toBe("127.0.0.1:9000");
    expect(parseAddress("localhost:8643")).toBe("localhost:8643");
    expect(parseAddress("[::1]:8643")).toBe("[::1]:8643");
  });

  it("rejects an address without a port, with a scheme or path, or with an out-of-range port", () => {
    expect(parseAddress("127.0.0.1")).toBeNull();
    expect(parseAddress("ws://127.0.0.1:8643")).toBeNull();
    expect(parseAddress("127.0.0.1:8643/x")).toBeNull();
    expect(parseAddress("127.0.0.1:65536")).toBeNull();
    expect(parseAddress("")).toBeNull();
  });

  // 这类地址形似 host:port，但 WebSocket 构造时直接抛错；存下来后每次连接都会失败，且再也改不回来。
  it("rejects a host:port that no WebSocket URL can be built from", () => {
    expect(parseAddress("999.1.1.1:8643")).toBeNull();
    expect(parseAddress("[1.2.3.4]:8643")).toBeNull();
    expect(parseAddress("[:::]:8643")).toBeNull();
  });
});

describe("pairing code", () => {
  it("normalizes the displayed form and ambiguous characters the way the daemon does", () => {
    expect(normalizePairingCode("7k3m-9qpx")).toBe("7K3M9QPX");
    expect(normalizePairingCode(" O1IL-abcd ")).toBe("0111ABCD");
  });

  it("rejects input that cannot be an 8-character Crockford code", () => {
    expect(normalizePairingCode("7K3M-9QP")).toBeNull();
    expect(normalizePairingCode("7K3M-9QPU")).toBeNull();
  });
});
