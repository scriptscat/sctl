import { describe, expect, it } from "vitest";
import { HandlerError, HandlerRegistry } from "./registry";

describe("handler registry", () => {
  it("declares registered methods and answers with the handler's result", async () => {
    const registry = new HandlerRegistry();
    registry.register("windows.list", () => Promise.resolve({ windows: [] }));

    expect(registry.methods()).toEqual(["windows.list"]);
    await expect(registry.dispatch("windows.list", {})).resolves.toEqual({ ok: true, result: { windows: [] } });
  });

  it("maps a handler's domain error to its code and message", async () => {
    const registry = new HandlerRegistry();
    registry.register("tabs.activate", () => Promise.reject(new HandlerError("NOT_FOUND", "no tab 7")));

    await expect(registry.dispatch("tabs.activate", { tabId: 7 })).resolves.toEqual({
      ok: false,
      code: "NOT_FOUND",
      message: "no tab 7",
    });
  });

  it("hides unexpected handler failures behind INTERNAL_ERROR", async () => {
    const registry = new HandlerRegistry();
    registry.register("tabs.close", () => Promise.reject(new Error("chrome exploded at /secret/path")));

    await expect(registry.dispatch("tabs.close", { tabIds: [1] })).resolves.toEqual({
      ok: false,
      code: "INTERNAL_ERROR",
      message: "internal error",
    });
  });

  it("refuses to register the same method twice", () => {
    const registry = new HandlerRegistry();
    registry.register("tabs.list", () => Promise.resolve({ contentTrust: "untrusted-page-content", tabs: [] }));

    expect(() =>
      registry.register("tabs.list", () => Promise.resolve({ contentTrust: "untrusted-page-content", tabs: [] })),
    ).toThrow(/tabs.list/);
  });
});
