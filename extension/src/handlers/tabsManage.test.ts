import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { registerHandlers } from "./index";

// 伪造的浏览器：标签页 1、2 在窗口 10，标签页 3 在窗口 20。
const known = new Map<number, chrome.tabs.Tab>();
const windows = new Set([10, 20]);

function tab(id: number, windowId: number): chrome.tabs.Tab {
  return { id, windowId, index: 0, active: false, pinned: false } as chrome.tabs.Tab;
}

describe("tab and window management handlers", () => {
  const tabs = {
    get: vi.fn(),
    move: vi.fn(),
    update: vi.fn(),
    reload: vi.fn(),
    duplicate: vi.fn(),
  };
  const win = {
    get: vi.fn(),
    create: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
  };
  let registry: HandlerRegistry;

  beforeEach(() => {
    known.clear();
    known.set(1, tab(1, 10));
    known.set(2, tab(2, 10));
    known.set(3, tab(3, 20));
    tabs.get.mockImplementation((id: number) =>
      known.has(id) ? Promise.resolve(known.get(id)) : Promise.reject(new Error("No tab with id")),
    );
    tabs.move.mockResolvedValue([]);
    tabs.update.mockResolvedValue(undefined);
    tabs.reload.mockResolvedValue(undefined);
    tabs.duplicate.mockResolvedValue(tab(99, 10));
    win.get.mockImplementation((id: number) =>
      windows.has(id) ? Promise.resolve({ id }) : Promise.reject(new Error("No window with id")),
    );
    win.create.mockResolvedValue({ id: 77 });
    win.update.mockResolvedValue({});
    win.remove.mockResolvedValue(undefined);
    Object.values(tabs).forEach((fn) => fn.mockClear());
    Object.values(win).forEach((fn) => fn.mockClear());
    vi.stubGlobal("chrome", { tabs, windows: win });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("tabs.move", () => {
    it("moves the tabs to the end of the given window when no index is given", async () => {
      const outcome = await registry.dispatch("tabs.move", { tabIds: [1, 2], windowId: 20 });

      expect(outcome).toEqual({ ok: true, result: { tabIds: [1, 2] } });
      expect(tabs.move).toHaveBeenCalledWith([1, 2], { windowId: 20, index: -1 });
    });

    it("passes an explicit index and keeps the current window when none is given", async () => {
      await registry.dispatch("tabs.move", { tabIds: [3], index: 0 });

      expect(tabs.move).toHaveBeenCalledWith([3], { index: 0 });
    });

    it("moves nothing when any tab is unknown", async () => {
      const outcome = await registry.dispatch("tabs.move", { tabIds: [1, 404], windowId: 20 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.move).not.toHaveBeenCalled();
    });

    it("moves nothing when the target window is unknown", async () => {
      const outcome = await registry.dispatch("tabs.move", { tabIds: [1], windowId: 404 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.move).not.toHaveBeenCalled();
    });
  });

  describe.each([
    ["tabs.pin", { pinned: true }],
    ["tabs.unpin", { pinned: false }],
    ["tabs.mute", { muted: true }],
    ["tabs.unmute", { muted: false }],
  ] as const)("%s", (method, update) => {
    it("updates every tab and returns their IDs", async () => {
      const outcome = await registry.dispatch(method, { tabIds: [1, 3] });

      expect(outcome).toEqual({ ok: true, result: { tabIds: [1, 3] } });
      expect(tabs.update).toHaveBeenCalledWith(1, update);
      expect(tabs.update).toHaveBeenCalledWith(3, update);
    });

    it("changes nothing when any tab is unknown", async () => {
      const outcome = await registry.dispatch(method, { tabIds: [1, 404] });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.update).not.toHaveBeenCalled();
    });
  });

  describe("tabs.reload", () => {
    it("reloads every tab, bypassing the cache only when asked", async () => {
      await registry.dispatch("tabs.reload", { tabIds: [1, 2] });
      await registry.dispatch("tabs.reload", { tabIds: [3], bypassCache: true });

      expect(tabs.reload).toHaveBeenNthCalledWith(1, 1, { bypassCache: false });
      expect(tabs.reload).toHaveBeenNthCalledWith(2, 2, { bypassCache: false });
      expect(tabs.reload).toHaveBeenNthCalledWith(3, 3, { bypassCache: true });
    });

    it("reloads nothing when any tab is unknown", async () => {
      const outcome = await registry.dispatch("tabs.reload", { tabIds: [1, 404] });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.reload).not.toHaveBeenCalled();
    });
  });

  describe("tabs.duplicate", () => {
    it("returns the ID of the new tab", async () => {
      const outcome = await registry.dispatch("tabs.duplicate", { tabId: 1 });

      expect(outcome).toEqual({ ok: true, result: { tabId: 99 } });
      expect(tabs.duplicate).toHaveBeenCalledWith(1);
    });

    it("answers NOT_FOUND for an unknown tab", async () => {
      const outcome = await registry.dispatch("tabs.duplicate", { tabId: 404 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.duplicate).not.toHaveBeenCalled();
    });
  });

  describe("windows.open", () => {
    it("opens a window with the given URLs and state and returns its ID", async () => {
      const outcome = await registry.dispatch("windows.open", {
        urls: ["https://a.example/", "https://b.example/"],
        state: "maximized",
      });

      expect(outcome).toEqual({ ok: true, result: { windowId: 77 } });
      expect(win.create).toHaveBeenCalledWith({
        url: ["https://a.example/", "https://b.example/"],
        state: "maximized",
      });
    });

    it("opens a blank window when no URL or state is given", async () => {
      await registry.dispatch("windows.open", {});

      expect(win.create).toHaveBeenCalledWith({});
    });
  });

  describe("windows.close", () => {
    it("closes every window and returns their IDs", async () => {
      const outcome = await registry.dispatch("windows.close", { windowIds: [10, 20] });

      expect(outcome).toEqual({ ok: true, result: { windowIds: [10, 20] } });
      expect(win.remove).toHaveBeenCalledWith(10);
      expect(win.remove).toHaveBeenCalledWith(20);
    });

    it("closes nothing when any window is unknown", async () => {
      const outcome = await registry.dispatch("windows.close", { windowIds: [10, 404] });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(win.remove).not.toHaveBeenCalled();
    });
  });

  describe("windows.focus", () => {
    it("focuses the window", async () => {
      const outcome = await registry.dispatch("windows.focus", { windowId: 20 });

      expect(outcome).toEqual({ ok: true, result: { windowId: 20 } });
      expect(win.update).toHaveBeenCalledWith(20, { focused: true });
    });

    it("answers NOT_FOUND for an unknown window", async () => {
      const outcome = await registry.dispatch("windows.focus", { windowId: 404 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(win.update).not.toHaveBeenCalled();
    });
  });

  describe("windows.state", () => {
    it("sets the window state", async () => {
      const outcome = await registry.dispatch("windows.state", { windowId: 10, state: "minimized" });

      expect(outcome).toEqual({ ok: true, result: { windowId: 10, state: "minimized" } });
      expect(win.update).toHaveBeenCalledWith(10, { state: "minimized" });
    });

    it("answers NOT_FOUND for an unknown window", async () => {
      const missing = await registry.dispatch("windows.state", { windowId: 404, state: "normal" });

      expect(missing).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(win.update).not.toHaveBeenCalled();
    });
  });
});
