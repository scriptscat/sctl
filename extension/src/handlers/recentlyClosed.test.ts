import { describe, it, expect, beforeEach, afterEach, vi, type Mock } from "vitest";
import { registerRecentlyClosedHandlers } from "./recentlyClosed";
import { HandlerRegistry } from "@/background/registry";

type GetRecentlyClosedFn = (options?: chrome.sessions.GetRecentlyClosedOptions) => Promise<chrome.sessions.Session[]>;
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
          tab: { sessionId: "tab-1", title: "First Tab", url: "https://example1.com" },
        },
        {
          lastModified: 50,
          window: {
            sessionId: "window-1",
            tabs: [
              { sessionId: "tab-2", title: "Tab in Window", url: "https://example2.com" },
              { sessionId: "tab-3", title: "Another Tab", url: "https://example3.com" },
            ],
          },
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        const items = outcome.result.items as unknown[];
        expect(items).toHaveLength(2);
        expect(outcome.result).toHaveProperty("contentTrust", "untrusted-page-content");
      }
    });

    it("respects limit parameter (1-25)", async () => {
      const mockSessions = Array.from({ length: 30 }, (_, i) => ({
        lastModified: 1000 - i,
        tab: { sessionId: `tab-${i}`, title: `Tab ${i}`, url: "https://example.com" },
      }));

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", { limit: 10 });

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        const items = outcome.result.items as unknown[];
        expect(items).toHaveLength(10);
      }
    });

    it("uses default limit of 25 when not specified", async () => {
      const mockSessions = Array.from({ length: 30 }, (_, i) => ({
        lastModified: 1000 - i,
        tab: { sessionId: `tab-${i}`, title: `Tab ${i}`, url: "https://example.com" },
      }));

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        const items = outcome.result.items as unknown[];
        expect(items).toHaveLength(25);
      }
    });

    it("includes tabCount for window sessions", async () => {
      const mockSessions: chrome.sessions.Session[] = [
        {
          lastModified: 100,
          window: {
            sessionId: "window-1",
            tabs: [
              { sessionId: "tab-1", title: "Tab 1", url: "https://example1.com" },
              { sessionId: "tab-2", title: "Tab 2", url: "https://example2.com" },
              { sessionId: "tab-3", title: "Tab 3", url: "https://example3.com" },
            ],
          },
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        const items = outcome.result.items as unknown[];
        const item = items[0] as Record<string, unknown>;
        expect(item).toMatchObject({ type: "window", tabCount: 3 });
      }
    });

    it("does not include tabCount for tab sessions", async () => {
      const mockSessions: chrome.sessions.Session[] = [
        {
          lastModified: 100,
          tab: { sessionId: "tab-1", title: "Tab", url: "https://example.com" },
        },
      ];

      sessions.getRecentlyClosed.mockResolvedValue(mockSessions);

      const outcome = await registry.dispatch("recent.list", {});

      expect(outcome.ok).toBe(true);
      if (outcome.ok) {
        const items = outcome.result.items as unknown[];
        const item = items[0] as Record<string, unknown>;
        expect(item).toMatchObject({ type: "tab" });
        expect(item).not.toHaveProperty("tabCount");
      }
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
        tab: { id: 42, sessionId: "tab-1", title: "Restored Tab", url: "https://example.com" },
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
        window: { id: 99, sessionId: "window-1", tabs: [] },
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
        tab: { id: 42, sessionId: "tab-0", title: "Most Recent", url: "https://example.com" },
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
