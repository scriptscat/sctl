import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateBrowsingDataClearParams } from "@/protocol/generated/validators.generated";
import { registerHandlers } from "./index";

type RemoveFn = (options: chrome.browsingData.RemovalOptions, data: chrome.browsingData.DataTypeSet) => Promise<void>;

describe("browsingData.clear", () => {
  let remove: Mock<RemoveFn>;
  let registry: HandlerRegistry;

  beforeEach(() => {
    remove = vi.fn<RemoveFn>().mockResolvedValue(undefined);
    vi.stubGlobal("chrome", { browsingData: { remove } });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("clears the given types over all time when no since is given", async () => {
    const outcome = await registry.dispatch("browsingData.clear", { types: ["cache", "cookies"], confirm: true });

    expect(remove).toHaveBeenCalledWith({ since: 0 }, { cache: true, cookies: true });
    expect(outcome).toEqual({ ok: true, result: { types: ["cache", "cookies"] } });
  });

  it("passes since and origins through for types that support origin filtering", async () => {
    await registry.dispatch("browsingData.clear", {
      types: [
        "localStorage",
        "indexedDB",
        "cacheStorage",
        "fileSystems",
        "serviceWorkers",
        "webSQL",
        "cache",
        "cookies",
      ],
      since: 1234,
      origins: ["https://a.example", "http://b.example:8080"],
      confirm: true,
    });

    expect(remove).toHaveBeenCalledWith(
      { since: 1234, origins: ["https://a.example", "http://b.example:8080"] },
      {
        localStorage: true,
        indexedDB: true,
        cacheStorage: true,
        fileSystems: true,
        serviceWorkers: true,
        webSQL: true,
        cache: true,
        cookies: true,
      },
    );
  });

  it.each(["history", "downloads", "formData"])(
    "returns INVALID_REQUEST naming %s when origins is combined with a type that cannot be filtered by origin",
    async (type) => {
      const outcome = await registry.dispatch("browsingData.clear", {
        types: ["cookies", type],
        origins: ["https://a.example"],
        confirm: true,
      });

      expect(outcome).toEqual({
        ok: false,
        code: "INVALID_REQUEST",
        message: expect.stringContaining(type) as unknown,
      });
      expect(remove).not.toHaveBeenCalled();
    },
  );

  it("allows types that cannot be filtered by origin when no origins are given", async () => {
    await registry.dispatch("browsingData.clear", { types: ["history", "downloads", "formData"], confirm: true });

    expect(remove).toHaveBeenCalledWith({ since: 0 }, { history: true, downloads: true, formData: true });
  });

  it("returns INVALID_REQUEST for an origin that is not a bare http or https origin", async () => {
    for (const origin of ["a.example", "https://a.example/path", "ftp://a.example"]) {
      const outcome = await registry.dispatch("browsingData.clear", {
        types: ["cookies"],
        origins: [origin],
        confirm: true,
      });
      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
    }
    expect(remove).not.toHaveBeenCalled();
  });

  it("returns CONFIRMATION_REQUIRED and clears nothing without confirm", async () => {
    const outcome = await registry.dispatch("browsingData.clear", { types: ["cache"] });

    expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
    expect(remove).not.toHaveBeenCalled();
  });

  it("the protocol validator rejects passwords and unknown types", () => {
    expect(validateBrowsingDataClearParams({ types: ["cache"], confirm: true })).toBe(true);
    expect(validateBrowsingDataClearParams({ types: ["passwords"], confirm: true })).toBe(false);
    expect(validateBrowsingDataClearParams({ types: ["appcache"] })).toBe(false);
    expect(validateBrowsingDataClearParams({ types: [] })).toBe(false);
  });
});
