import { describe, expect, it, vi } from "vitest";
import { readBundleDigest, type SessionStore } from "./bundle";

const DIGEST = "0123456789abcdef".repeat(4);
const NEWER = "fedcba9876543210".repeat(4);

function response(status: number, body: string): Response {
  return new Response(body, { status });
}

function session(initial: Record<string, unknown> = {}): SessionStore & { items: Record<string, unknown> } {
  const items = { ...initial };
  return {
    items,
    get: (keys: string[]) =>
      Promise.resolve(Object.fromEntries(keys.filter((k) => k in items).map((k) => [k, items[k]]))),
    set: (values: Record<string, unknown>) => {
      Object.assign(items, values);
      return Promise.resolve();
    },
  };
}

describe("readBundleDigest", () => {
  it("returns the digest recorded in the marker sctl writes into its extension directory, and remembers it", async () => {
    const store = session();
    const fetchFn = vi.fn(() => Promise.resolve(response(200, JSON.stringify({ digest: DIGEST }))));
    await expect(readBundleDigest(fetchFn, "chrome-extension://id/sctl-bundle.json", store)).resolves.toBe(DIGEST);
    expect(fetchFn).toHaveBeenCalledWith("chrome-extension://id/sctl-bundle.json");
    expect(store.items).toEqual({ bundle: DIGEST });
  });

  it("keeps reporting the digest of the running code after the directory on disk was rewritten", async () => {
    // service worker 休眠后重启时，磁盘上的标记可能已被换成新版，而运行的仍是旧代码。
    const store = session({ bundle: DIGEST });
    const fetchFn = vi.fn(() => Promise.resolve(response(200, JSON.stringify({ digest: NEWER }))));
    await expect(readBundleDigest(fetchFn, "u", store)).resolves.toBe(DIGEST);
    expect(fetchFn).not.toHaveBeenCalled();
  });

  it("returns null when the extension was loaded without the marker (from the release zip), and remembers that", async () => {
    const store = session();
    const missing = vi.fn(() => Promise.reject(new TypeError("Failed to fetch")));
    await expect(readBundleDigest(missing, "u", store)).resolves.toBeNull();
    expect(store.items).toEqual({ bundle: "" });
    await expect(readBundleDigest(missing, "u", store)).resolves.toBeNull();
    expect(missing).toHaveBeenCalledOnce();

    const notFound = vi.fn(() => Promise.resolve(response(404, "")));
    await expect(readBundleDigest(notFound, "u", session())).resolves.toBeNull();
  });

  it("returns null when the marker does not hold a lowercase sha-256 digest", async () => {
    for (const body of ["not json", JSON.stringify({ digest: DIGEST.toUpperCase() }), JSON.stringify({})]) {
      const fetchFn = vi.fn(() => Promise.resolve(response(200, body)));
      await expect(readBundleDigest(fetchFn, "u", session())).resolves.toBeNull();
    }
  });
});
