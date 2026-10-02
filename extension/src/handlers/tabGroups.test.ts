import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateTabGroupsListResult, validateTabGroupsCreateResult } from "@/protocol/generated/validators.generated";
import { registerAllHandlers } from "./handlers.fixture";

// 伪造的浏览器：标签页 1、2 在窗口 10（1 在组 5 里），标签页 3 在窗口 20；组 5 在窗口 10。
const tabsById = new Map<number, chrome.tabs.Tab>();
const groups = new Map<number, chrome.tabGroups.TabGroup>();
const windows = new Set([10, 20]);

function tab(id: number, windowId: number, groupId = -1): chrome.tabs.Tab {
  return { id, windowId, groupId, index: 0, active: false, pinned: false } as chrome.tabs.Tab;
}

function group(id: number, windowId: number, title: string): chrome.tabGroups.TabGroup {
  return { id, windowId, title, color: "blue", collapsed: false, shared: false };
}

describe("tab group handlers", () => {
  const tabs = {
    get: vi.fn(),
    query: vi.fn(),
    group: vi.fn(),
    ungroup: vi.fn(),
  };
  const tabGroups = {
    get: vi.fn(),
    query: vi.fn(),
    update: vi.fn(),
  };
  const win = { get: vi.fn() };
  let registry: HandlerRegistry;

  beforeEach(() => {
    tabsById.clear();
    tabsById.set(1, tab(1, 10, 5));
    tabsById.set(2, tab(2, 10));
    tabsById.set(3, tab(3, 20));
    groups.clear();
    groups.set(5, group(5, 10, "<b>Work</b>"));
    tabs.get.mockImplementation((id: number) =>
      tabsById.has(id) ? Promise.resolve(tabsById.get(id)) : Promise.reject(new Error("No tab with id")),
    );
    tabs.query.mockImplementation(() => Promise.resolve([...tabsById.values()]));
    tabs.group.mockResolvedValue(12);
    tabs.ungroup.mockResolvedValue(undefined);
    tabGroups.get.mockImplementation((id: number) =>
      groups.has(id) ? Promise.resolve(groups.get(id)) : Promise.reject(new Error("No group with id")),
    );
    tabGroups.query.mockImplementation(() => Promise.resolve([...groups.values()]));
    tabGroups.update.mockResolvedValue({});
    win.get.mockImplementation((id: number) =>
      windows.has(id) ? Promise.resolve({ id }) : Promise.reject(new Error("No window with id")),
    );
    for (const fn of [...Object.values(tabs), ...Object.values(tabGroups), ...Object.values(win)]) {
      fn.mockClear();
    }
    vi.stubGlobal("chrome", { tabs, tabGroups, windows: win });
    registry = new HandlerRegistry();
    registerAllHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("tabGroups.list", () => {
    it("lists groups with their tab counts and leaves the page-controlled title untouched", async () => {
      const outcome = await registry.dispatch("tabGroups.list", {});

      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          groups: [{ groupId: 5, windowId: 10, title: "<b>Work</b>", color: "blue", collapsed: false, tabCount: 1 }],
        },
      });
      if (outcome.ok) {
        expect(validateTabGroupsListResult(outcome.result)).toBe(true);
      }
    });

    it("restricts the query to one window and rejects an unknown window", async () => {
      await registry.dispatch("tabGroups.list", { windowId: 10 });
      expect(tabGroups.query).toHaveBeenCalledWith({ windowId: 10 });
      expect(tabs.query).toHaveBeenCalledWith({ windowId: 10 });

      const outcome = await registry.dispatch("tabGroups.list", { windowId: 404 });
      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
    });
  });

  describe("tabGroups.create", () => {
    it("groups the tabs inside their window, then applies title and color", async () => {
      const outcome = await registry.dispatch("tabGroups.create", { tabIds: [1, 2], title: "Work", color: "green" });

      expect(outcome).toEqual({ ok: true, result: { groupId: 12 } });
      expect(tabs.group).toHaveBeenCalledWith({ tabIds: [1, 2], createProperties: { windowId: 10 } });
      expect(tabGroups.update).toHaveBeenCalledWith(12, { title: "Work", color: "green" });
      if (outcome.ok) {
        expect(validateTabGroupsCreateResult(outcome.result)).toBe(true);
      }
    });

    it("does not touch the group properties when none are given", async () => {
      await registry.dispatch("tabGroups.create", { tabIds: [2] });

      expect(tabs.group).toHaveBeenCalledOnce();
      expect(tabGroups.update).not.toHaveBeenCalled();
    });

    it("rejects tabs from different windows with INVALID_REQUEST and groups nothing", async () => {
      const outcome = await registry.dispatch("tabGroups.create", { tabIds: [1, 3] });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(tabs.group).not.toHaveBeenCalled();
    });

    it("answers NOT_FOUND and groups nothing when any tab is unknown", async () => {
      const outcome = await registry.dispatch("tabGroups.create", { tabIds: [1, 404] });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.group).not.toHaveBeenCalled();
    });
  });

  describe("tabGroups.add", () => {
    it("adds every tab to the existing group", async () => {
      const outcome = await registry.dispatch("tabGroups.add", { groupId: 5, tabIds: [2, 3] });

      expect(outcome).toEqual({ ok: true, result: { groupId: 5, tabIds: [2, 3] } });
      expect(tabs.group).toHaveBeenCalledWith({ groupId: 5, tabIds: [2, 3] });
    });

    it("answers NOT_FOUND for an unknown group or tab and adds nothing", async () => {
      expect(await registry.dispatch("tabGroups.add", { groupId: 404, tabIds: [2] })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(await registry.dispatch("tabGroups.add", { groupId: 5, tabIds: [2, 404] })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(tabs.group).not.toHaveBeenCalled();
    });
  });

  describe("tabGroups.edit", () => {
    it("updates only the given properties, including expanding with collapsed false", async () => {
      const outcome = await registry.dispatch("tabGroups.edit", { groupId: 5, title: "", collapsed: false });

      expect(outcome).toEqual({ ok: true, result: { groupId: 5 } });
      expect(tabGroups.update).toHaveBeenCalledWith(5, { title: "", collapsed: false });
    });

    it("answers NOT_FOUND for an unknown group", async () => {
      const outcome = await registry.dispatch("tabGroups.edit", { groupId: 404, title: "x" });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabGroups.update).not.toHaveBeenCalled();
    });

    it("rejects an edit that changes nothing", async () => {
      expect(await registry.dispatch("tabGroups.edit", { groupId: 5 })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
      expect(tabGroups.update).not.toHaveBeenCalled();
    });
  });

  describe("tabGroups.ungroup", () => {
    it("removes every tab from its group", async () => {
      const outcome = await registry.dispatch("tabGroups.ungroup", { tabIds: [1, 2] });

      expect(outcome).toEqual({ ok: true, result: { tabIds: [1, 2] } });
      expect(tabs.ungroup).toHaveBeenCalledWith([1, 2]);
    });

    it("ungroups nothing when any tab is unknown", async () => {
      const outcome = await registry.dispatch("tabGroups.ungroup", { tabIds: [1, 404] });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(tabs.ungroup).not.toHaveBeenCalled();
    });
  });
});
