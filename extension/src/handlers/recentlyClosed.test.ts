import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from "vitest";
import { registerRecentlyClosedHandlers } from "./recentlyClosed";
import { HandlerRegistry } from "@/background/registry";
import type { RpcOutcome } from "@/shared/messages";

function tab(fields: Partial<chrome.tabs.Tab>): chrome.tabs.Tab {
  return fields as chrome.tabs.Tab;
}

function win(fields: Partial<chrome.windows.Window>): chrome.windows.Window {
  return fields as chrome.windows.Window;
}

// 处理器已按 recent.list 的结果类型返回；分发器只给出 unknown，这里收窄后再断言。
function listItems(outcome: RpcOutcome): Record<string, unknown>[] {
  if (!outcome.ok) throw new Error(`expected ok outcome, got ${outcome.code}`);
  return (outcome.result as { items: Record<string, unknown>[] }).items;
}

type GetRecentlyClosedFn = (filter?: chrome.sessions.Filter) => Promise<chrome.sessions.Session[]>;
type RestoreFn = (sessionId?: string) => Promise<chrome.sessions.Session | undefined>;

interface SessionsMock {
  getRecentlyClosed: Mock<GetRecentlyClosedFn>;
  restore: Mock<RestoreFn>;
}

describe("recently closed handlers", () => {
  let sessions: SessionsMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    sessions = {
      getRecentlyClosed: vi.fn<GetRecentlyClosedFn>().mockResolvedValue([]),
      restore: vi.fn<RestoreFn>().mockResolvedValue(undefined),
    };
    vi.stubGlobal("chrome", { sessions });
    registry = new HandlerRegistry();
    registerRecentlyClosedHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("recent.list", () => {
    it("returns recently closed sessions ordered by closedTime newest first", async () => {
      const mockSessions: chrome.sessions.Session[] = [
        {
          lastModified: 100,
          tab: tab({ sessionId: "tab-1", title: "First Tab", url: "https://example1.com" }),
        },
        {
          lastModified: 50,
          window: win({
            sessionId: "window-1",
            tabs: [
              tab({ sessionId: "tab-2", title: "Tab in Window", url: "https://example2.com" }),
              tab({ sessionId: "tab-3", title: "Another Tab", url: "https://example3.com" }),
            ],
          }),
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      const items = listItems(outcome);
      expect(items).toHaveLength(2);
      expect(outcome).toHaveProperty("result.contentTrust", "untrusted-page-content");
    });

    it("respects limit parameter (1-25)", async () => {
      const mockSessions: chrome.sessions.Session[] = Array.from({ length: 30 }, (_, i) => ({
        lastModified: 1000 - i,
        tab: tab({ sessionId: `tab-${i}`, title: `Tab ${i}`, url: "https://example.com" }),
      }));

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", { limit: 10 });

      expect(outcome.ok).toBe(true);
      const items = listItems(outcome);
      expect(items).toHaveLength(10);
    });

    it("uses default limit of 25 when not specified", async () => {
      const mockSessions: chrome.sessions.Session[] = Array.from({ length: 30 }, (_, i) => ({
        lastModified: 1000 - i,
        tab: tab({ sessionId: `tab-${i}`, title: `Tab ${i}`, url: "https://example.com" }),
      }));

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      const items = listItems(outcome);
      expect(items).toHaveLength(25);
    });

    it("includes tabCount for window sessions", async () => {
      const mockSessions: chrome.sessions.Session[] = [
        {
          lastModified: 100,
          window: win({
            sessionId: "window-1",
            tabs: [
              tab({ sessionId: "tab-1", title: "Tab 1", url: "https://example1.com" }),
              tab({ sessionId: "tab-2", title: "Tab 2", url: "https://example2.com" }),
              tab({ sessionId: "tab-3", title: "Tab 3", url: "https://example3.com" }),
            ],
          }),
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      const items = listItems(outcome);
      const item = items[0];
      expect(item).toMatchObject({ type: "window", tabCount: 3 });
    });

    it("does not include tabCount for tab sessions", async () => {
      const mockSessions: chrome.sessions.Session[] = [
        {
          lastModified: 100,
          tab: tab({ sessionId: "tab-1", title: "Tab", url: "https://example.com" }),
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      const items = listItems(outcome);
      const item = items[0];
      expect(item).toMatchObject({ type: "tab" });
      expect(item).not.toHaveProperty("tabCount");
    });

    it("rejects invalid limit", async () => {
      const result1 = await registry.dispatch("recent.list", { limit: 0 });
      expect(result1).toMatchObject({ ok: false, code: "INVALID_REQUEST" });

      const result2 = await registry.dispatch("recent.list", { limit: 26 });
      expect(result2).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
    });
  });

  describe("recent.restore", () => {
    it("restores a tab session by sessionId", async () => {
      sessions.restore.mockResolvedValue({
        lastModified: 0,
        tab: tab({ id: 42, sessionId: "tab-1", title: "Restored Tab", url: "https://example.com" }),
      });

      const outcome = await registry.dispatch("recent.restore", { sessionId: "tab-1" });

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        expect(outcome.result).toHaveProperty("tabId", 42);
      }
      expect(sessions.restore).toHaveBeenCalledWith("tab-1");
    });

    it("restores a window session by sessionId", async () => {
      sessions.restore.mockResolvedValue({
        lastModified: 0,
        window: win({ id: 99, sessionId: "window-1", tabs: [] }),
      });

      const outcome = await registry.dispatch("recent.restore", { sessionId: "window-1" });

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        expect(outcome.result).toHaveProperty("windowId", 99);
      }
      expect(sessions.restore).toHaveBeenCalledWith("window-1");
    });

    it("restores most recent session when sessionId is not provided", async () => {
      sessions.restore.mockResolvedValue({
        lastModified: 0,
        tab: tab({ id: 42, sessionId: "tab-0", title: "Most Recent", url: "https://example.com" }),
      });

      const outcome = await registry.dispatch("recent.restore", {});

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        expect(outcome.result).toHaveProperty("tabId", 42);
      }
      expect(sessions.restore).toHaveBeenCalledWith();
    });

    it("returns NOT_FOUND when session does not exist", async () => {
      sessions.restore.mockRejectedValue(new Error("Session not found"));

      const outcome = await registry.dispatch("recent.restore", { sessionId: "nonexistent" });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
    });
  });
});
