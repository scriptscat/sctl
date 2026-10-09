import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import {
  validateTabsActivateResult,
  validateTabsCloseResult,
  validateTabsCurrentResult,
  validateTabsListResult,
  validateTabsOpenResult,
  validateWindowsListResult,
} from "@/protocol/generated/validators.generated";
import { registerAllHandlers } from "./handlers.fixture";

// chrome.tabs / chrome.windows 都是 Promise 化的（manifest 要求 Chrome 125+），伪造对象直接返回/拒绝 Promise。
// 具体函数类型直接抄自 chrome.tabs/chrome.windows 的 Promise 重载，而不是 typeof，避免 vi.fn 泛型解析到回调重载。
type TabsQueryFn = (queryInfo: chrome.tabs.QueryInfo) => Promise<chrome.tabs.Tab[]>;
type TabsGetFn = (tabId: number) => Promise<chrome.tabs.Tab>;
type TabsCreateFn = (createProperties: chrome.tabs.CreateProperties) => Promise<chrome.tabs.Tab>;
type TabsUpdateFn = (
  tabId: number,
  updateProperties: chrome.tabs.UpdateProperties,
) => Promise<chrome.tabs.Tab | undefined>;
type TabsRemoveFn = (tabIds: number | number[]) => Promise<void>;
type WindowsGetFn = (windowId: number, queryOptions?: chrome.windows.QueryOptions) => Promise<chrome.windows.Window>;
type WindowsGetLastFocusedFn = (queryOptions?: chrome.windows.QueryOptions) => Promise<chrome.windows.Window>;
type WindowsUpdateFn = (windowId: number, updateInfo: chrome.windows.UpdateInfo) => Promise<chrome.windows.Window>;
type WindowsGetAllFn = (queryOptions?: chrome.windows.QueryOptions) => Promise<chrome.windows.Window[]>;

interface ChromeMock {
  tabs: {
    query: Mock<TabsQueryFn>;
    get: Mock<TabsGetFn>;
    create: Mock<TabsCreateFn>;
    update: Mock<TabsUpdateFn>;
    remove: Mock<TabsRemoveFn>;
  };
  windows: {
    get: Mock<WindowsGetFn>;
    getLastFocused: Mock<WindowsGetLastFocusedFn>;
    update: Mock<WindowsUpdateFn>;
    getAll: Mock<WindowsGetAllFn>;
  };
}

function fakeTab(overrides: Partial<chrome.tabs.Tab> & { id: number; windowId: number }): chrome.tabs.Tab {
  return {
    active: false,
    pinned: false,
    highlighted: false,
    incognito: false,
    selected: false,
    discarded: false,
    autoDiscardable: true,
    index: 0,
    ...overrides,
  } as chrome.tabs.Tab;
}

function fakeWindow(overrides: Partial<chrome.windows.Window> & { id: number }): chrome.windows.Window {
  return {
    focused: false,
    alwaysOnTop: false,
    incognito: false,
    state: "normal",
    type: "normal",
    ...overrides,
  };
}

function createChromeMock(): ChromeMock {
  return {
    tabs: {
      query: vi.fn<TabsQueryFn>(),
      get: vi.fn<TabsGetFn>(),
      create: vi.fn<TabsCreateFn>(),
      update: vi.fn<TabsUpdateFn>(),
      remove: vi.fn<TabsRemoveFn>(),
    },
    windows: {
      get: vi.fn<WindowsGetFn>(),
      getLastFocused: vi.fn<WindowsGetLastFocusedFn>(),
      update: vi.fn<WindowsUpdateFn>(),
      getAll: vi.fn<WindowsGetAllFn>(),
    },
  };
}

describe("browser method handlers", () => {
  let chromeMock: ChromeMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    chromeMock = createChromeMock();
    vi.stubGlobal("chrome", chromeMock);
    registry = new HandlerRegistry();
    registerAllHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("declares capabilities for exactly the tab, window, page-target helper, debugger, reading list, bookmark, tab management, history, browsing data, recently closed, downloads, cookies and extensions methods", () => {
    expect(new Set(registry.methods())).toEqual(
      new Set([
        "tabs.list",
        "tabs.open",
        "tabs.close",
        "tabs.activate",
        "tabs.current",
        "tabs.select",
        "windows.list",
        "readingList.list",
        "readingList.add",
        "readingList.markRead",
        "readingList.remove",
        "bookmarks.list",
        "bookmarks.search",
        "bookmarks.add",
        "bookmarks.mkdir",
        "bookmarks.move",
        "bookmarks.edit",
        "bookmarks.remove",
        "tabs.move",
        "tabs.pin",
        "tabs.unpin",
        "tabs.mute",
        "tabs.unmute",
        "tabs.reload",
        "tabs.duplicate",
        "windows.open",
        "windows.close",
        "windows.focus",
        "windows.state",
        "tabGroups.list",
        "tabGroups.create",
        "tabGroups.add",
        "tabGroups.edit",
        "tabGroups.ungroup",
        "history.search",
        "history.visits",
        "history.remove",
        "history.clear",
        "browsingData.clear",
        "recent.list",
        "recent.restore",
        "downloads.list",
        "downloads.start",
        "downloads.pause",
        "downloads.resume",
        "downloads.cancel",
        "downloads.erase",
        "downloads.deleteFile",
        "downloads.show",
        "cookies.list",
        "cookies.get",
        "cookies.set",
        "cookies.remove",
        "cookies.clear",
        "extensions.list",
        "extensions.enable",
        "extensions.disable",
        "extensions.uninstall",
        "debugger.send",
        "debugger.detach",
        "debugger.record",
        "debugger.body",
        "debugger.targets",
        "debugger.userAgent",
        "debugger.open",
        "debugger.close",
        "debugger.own",
      ]),
    );
  });

  // 浏览器不提供某个领域的 API 时，这个领域的每个操作都回 UNSUPPORTED 并点名缺少的 API，而不是在 undefined 上崩成
  // INTERNAL_ERROR。伪造的 chrome 只有 tabs 与 windows。
  it("answers UNSUPPORTED naming the missing API for every method of a domain whose chrome namespace is absent", async () => {
    const domains: Record<string, string> = {
      bookmarks: "bookmarks",
      tabGroups: "tabGroups",
      history: "history",
      browsingData: "browsingData",
      recent: "sessions",
      downloads: "downloads",
      cookies: "cookies",
      extensions: "management",
    };
    const checked: string[] = [];
    for (const method of registry.methods()) {
      const namespace = domains[method.split(".")[0]];
      if (namespace === undefined) continue;
      const outcome = registry.requiresApproval(method)
        ? await registry.prepare(method, { ids: ["1"], id: "x" })
        : await registry.dispatch(method, { confirm: true });
      expect(outcome, method).toEqual({
        ok: false,
        code: "UNSUPPORTED",
        message: expect.stringContaining(`chrome.${namespace}`) as unknown,
      });
      checked.push(method);
    }
    expect(checked).toHaveLength(36);
  });

  describe("tabs.list", () => {
    it("lists every tab across all windows with page-controlled title and URL untouched", async () => {
      const tab = fakeTab({
        id: 7,
        windowId: 1,
        active: true,
        pinned: true,
        groupId: 4,
        title: "<script>alert(1)</script>",
        url: "https://example.com/a?b=1",
      });
      chromeMock.tabs.query.mockResolvedValue([tab]);

      const outcome = await registry.dispatch("tabs.list", {});

      expect(chromeMock.tabs.query).toHaveBeenCalledWith({});
      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          tabs: [
            {
              tabId: 7,
              windowId: 1,
              active: true,
              pinned: true,
              groupId: 4,
              title: "<script>alert(1)</script>",
              url: "https://example.com/a?b=1",
            },
          ],
        },
      });
      if (outcome.ok) {
        expect(validateTabsListResult(outcome.result)).toBe(true);
      }
    });

    it("reports groupId -1 for a tab that is in no group", async () => {
      chromeMock.tabs.query.mockResolvedValue([
        fakeTab({ id: 8, windowId: 1, title: "t", url: "https://e.x/", groupId: -1 }),
      ]);

      const outcome = await registry.dispatch("tabs.list", {});

      expect(outcome).toMatchObject({ ok: true, result: { tabs: [{ tabId: 8, groupId: -1 }] } });
    });

    it("filters by window when a window filter is given", async () => {
      chromeMock.windows.get.mockResolvedValue(fakeWindow({ id: 3 }));
      chromeMock.tabs.query.mockResolvedValue([]);

      const outcome = await registry.dispatch("tabs.list", { windowId: 3 });

      expect(chromeMock.windows.get).toHaveBeenCalledWith(3);
      expect(chromeMock.tabs.query).toHaveBeenCalledWith({ windowId: 3 });
      expect(outcome).toEqual({ ok: true, result: { contentTrust: "untrusted-page-content", tabs: [] } });
    });

    it("returns NOT_FOUND when the window filter names a window that does not exist", async () => {
      chromeMock.windows.get.mockRejectedValue(new Error("No window with id: 999."));

      const outcome = await registry.dispatch("tabs.list", { windowId: 999 });

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining("999") as unknown });
      expect(chromeMock.tabs.query).not.toHaveBeenCalled();
    });
  });

  describe("tabs.open", () => {
    it("opens the URL in the last-focused window and activates it by default", async () => {
      chromeMock.windows.getLastFocused.mockResolvedValue(fakeWindow({ id: 5, focused: true }));
      chromeMock.tabs.create.mockResolvedValue(fakeTab({ id: 42, windowId: 5 }));

      const outcome = await registry.dispatch("tabs.open", { url: "https://example.com" });

      expect(chromeMock.windows.getLastFocused).toHaveBeenCalled();
      expect(chromeMock.tabs.create).toHaveBeenCalledWith({ url: "https://example.com", windowId: 5, active: true });
      expect(outcome).toEqual({ ok: true, result: { tabId: 42 } });
      if (outcome.ok) {
        expect(validateTabsOpenResult(outcome.result)).toBe(true);
      }
    });

    it("opens in the requested window in the background when asked", async () => {
      chromeMock.windows.get.mockResolvedValue(fakeWindow({ id: 9 }));
      chromeMock.tabs.create.mockResolvedValue(fakeTab({ id: 43, windowId: 9 }));

      const outcome = await registry.dispatch("tabs.open", {
        url: "https://example.com",
        windowId: 9,
        background: true,
      });

      expect(chromeMock.windows.getLastFocused).not.toHaveBeenCalled();
      expect(chromeMock.tabs.create).toHaveBeenCalledWith({ url: "https://example.com", windowId: 9, active: false });
      expect(outcome).toEqual({ ok: true, result: { tabId: 43 } });
    });

    it("returns NOT_FOUND when the requested window does not exist", async () => {
      chromeMock.windows.get.mockRejectedValue(new Error("No window with id: 404."));

      const outcome = await registry.dispatch("tabs.open", { url: "https://example.com", windowId: 404 });

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining("404") as unknown });
      expect(chromeMock.tabs.create).not.toHaveBeenCalled();
    });
  });

  describe("tabs.close", () => {
    it("closes every given tab and echoes back the closed tab IDs", async () => {
      chromeMock.tabs.get.mockImplementation((tabId: number) => Promise.resolve(fakeTab({ id: tabId, windowId: 1 })));
      chromeMock.tabs.remove.mockResolvedValue(undefined);

      const outcome = await registry.dispatch("tabs.close", { tabIds: [1, 2] });

      expect(chromeMock.tabs.remove).toHaveBeenCalledWith([1, 2]);
      expect(outcome).toEqual({ ok: true, result: { tabIds: [1, 2] } });
      if (outcome.ok) {
        expect(validateTabsCloseResult(outcome.result)).toBe(true);
      }
    });

    it("returns NOT_FOUND without closing any tab when one of the tab IDs does not exist", async () => {
      chromeMock.tabs.get.mockImplementation((tabId: number) =>
        tabId === 2
          ? Promise.reject(new Error("No tab with id: 2."))
          : Promise.resolve(fakeTab({ id: tabId, windowId: 1 })),
      );

      const outcome = await registry.dispatch("tabs.close", { tabIds: [1, 2] });

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining("2") as unknown });
      expect(chromeMock.tabs.remove).not.toHaveBeenCalled();
    });
  });

  describe("tabs.activate", () => {
    it("activates the tab and focuses its window", async () => {
      chromeMock.tabs.get.mockResolvedValue(fakeTab({ id: 11, windowId: 6 }));
      chromeMock.tabs.update.mockResolvedValue(undefined);
      chromeMock.windows.update.mockResolvedValue(fakeWindow({ id: 6, focused: true }));

      const outcome = await registry.dispatch("tabs.activate", { tabId: 11 });

      expect(chromeMock.tabs.update).toHaveBeenCalledWith(11, { active: true });
      expect(chromeMock.windows.update).toHaveBeenCalledWith(6, { focused: true });
      expect(outcome).toEqual({ ok: true, result: { tabId: 11, windowId: 6 } });
      if (outcome.ok) {
        expect(validateTabsActivateResult(outcome.result)).toBe(true);
      }
    });

    it("returns NOT_FOUND when the tab does not exist", async () => {
      chromeMock.tabs.get.mockRejectedValue(new Error("No tab with id: 77."));

      const outcome = await registry.dispatch("tabs.activate", { tabId: 77 });

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining("77") as unknown });
      expect(chromeMock.tabs.update).not.toHaveBeenCalled();
    });
  });

  describe("tabs.current", () => {
    it("returns the active tab of the last-focused normal window", async () => {
      chromeMock.windows.getLastFocused.mockResolvedValue(
        fakeWindow({
          id: 4,
          tabs: [fakeTab({ id: 20, windowId: 4 }), fakeTab({ id: 21, windowId: 4, active: true })],
        }),
      );

      const outcome = await registry.dispatch("tabs.current", {});

      // 只看普通窗口：弹出窗口、开发者工具窗口不是用户正在浏览的页面。
      expect(chromeMock.windows.getLastFocused).toHaveBeenCalledWith({ populate: true, windowTypes: ["normal"] });
      expect(outcome).toEqual({ ok: true, result: { tabId: 21, windowId: 4 } });
      if (outcome.ok) {
        expect(validateTabsCurrentResult(outcome.result)).toBe(true);
      }
    });

    it("returns NOT_FOUND when there is no normal window", async () => {
      chromeMock.windows.getLastFocused.mockRejectedValue(new Error("No last-focused window"));

      const outcome = await registry.dispatch("tabs.current", {});

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.any(String) as unknown });
    });
  });

  describe("tabs.select", () => {
    it("activates the tab inside its window without focusing the window", async () => {
      chromeMock.tabs.get.mockResolvedValue(fakeTab({ id: 11, windowId: 6 }));
      chromeMock.tabs.update.mockResolvedValue(undefined);

      const outcome = await registry.dispatch("tabs.select", { tabId: 11 });

      expect(chromeMock.tabs.update).toHaveBeenCalledWith(11, { active: true });
      expect(chromeMock.windows.update).not.toHaveBeenCalled();
      expect(outcome).toEqual({ ok: true, result: { tabId: 11, windowId: 6 } });
    });

    it("returns NOT_FOUND when the tab does not exist", async () => {
      chromeMock.tabs.get.mockRejectedValue(new Error("No tab with id: 77."));

      const outcome = await registry.dispatch("tabs.select", { tabId: 77 });

      expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining("77") as unknown });
      expect(chromeMock.tabs.update).not.toHaveBeenCalled();
    });
  });

  describe("windows.list", () => {
    it("lists every window with its focus state, lifecycle state and tab count", async () => {
      chromeMock.windows.getAll.mockResolvedValue([
        fakeWindow({
          id: 1,
          focused: true,
          state: "maximized",
          tabs: [fakeTab({ id: 1, windowId: 1 }), fakeTab({ id: 2, windowId: 1 })],
        }),
        fakeWindow({ id: 2, focused: false, state: "minimized", tabs: [] }),
      ]);

      const outcome = await registry.dispatch("windows.list", {});

      expect(chromeMock.windows.getAll).toHaveBeenCalledWith({ populate: true });
      expect(outcome).toEqual({
        ok: true,
        result: {
          windows: [
            { windowId: 1, focused: true, state: "maximized", tabCount: 2 },
            { windowId: 2, focused: false, state: "minimized", tabCount: 0 },
          ],
        },
      });
      if (outcome.ok) {
        expect(validateWindowsListResult(outcome.result)).toBe(true);
      }
    });
  });
});
