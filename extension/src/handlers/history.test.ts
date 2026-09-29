import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateHistorySearchResult, validateHistoryVisitsResult } from "@/protocol/generated/validators.generated";
import { registerHandlers } from "./index";

type SearchFn = (query: chrome.history.HistoryQuery) => Promise<chrome.history.HistoryItem[]>;
type GetVisitsFn = (details: chrome.history.UrlDetails) => Promise<chrome.history.VisitItem[]>;
type DeleteUrlFn = (details: chrome.history.UrlDetails) => Promise<void>;
type DeleteRangeFn = (range: chrome.history.Range) => Promise<void>;
type DeleteAllFn = () => Promise<void>;

interface HistoryMock {
  search: Mock<SearchFn>;
  getVisits: Mock<GetVisitsFn>;
  deleteUrl: Mock<DeleteUrlFn>;
  deleteRange: Mock<DeleteRangeFn>;
  deleteAll: Mock<DeleteAllFn>;
}

function item(url: string, lastVisitTime: number, overrides: Partial<chrome.history.HistoryItem> = {}) {
  return { id: url, url, title: `title of ${url}`, lastVisitTime, visitCount: 3, typedCount: 0, ...overrides };
}

describe("history handlers", () => {
  let history: HistoryMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    history = {
      search: vi.fn<SearchFn>().mockResolvedValue([]),
      getVisits: vi.fn<GetVisitsFn>().mockResolvedValue([]),
      deleteUrl: vi.fn<DeleteUrlFn>().mockResolvedValue(undefined),
      deleteRange: vi.fn<DeleteRangeFn>().mockResolvedValue(undefined),
      deleteAll: vi.fn<DeleteAllFn>().mockResolvedValue(undefined),
    };
    vi.stubGlobal("chrome", { history });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("history.search", () => {
    it("searches all history when no time range is given and returns items newest first with the untrusted marker", async () => {
      history.search.mockResolvedValue([item("https://old.example/", 100.4), item("https://new.example/", 900.6)]);

      const outcome = await registry.dispatch("history.search", { text: "example" });

      expect(history.search).toHaveBeenCalledWith({ text: "example", startTime: 0, maxResults: 101 });
      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: false,
          items: [
            { url: "https://new.example/", title: "title of https://new.example/", lastVisitTime: 901, visitCount: 3 },
            { url: "https://old.example/", title: "title of https://old.example/", lastVisitTime: 100, visitCount: 3 },
          ],
        },
      });
      expect(validateHistorySearchResult(outcome.ok ? outcome.result : undefined)).toBe(true);
    });

    it("passes the time range to chrome and treats an omitted text as match-all", async () => {
      await registry.dispatch("history.search", { startTime: 10, endTime: 20 });

      expect(history.search).toHaveBeenCalledWith({ text: "", startTime: 10, endTime: 20, maxResults: 101 });
    });

    it("keeps only limit items and reports hasMore when chrome returns one more", async () => {
      history.search.mockResolvedValue([item("https://a/", 3), item("https://b/", 2), item("https://c/", 1)]);

      const outcome = await registry.dispatch("history.search", { limit: 2 });

      expect(history.search).toHaveBeenCalledWith(expect.objectContaining({ maxResults: 3 }));
      expect(outcome.ok && outcome.result).toMatchObject({ hasMore: true });
      expect(outcome.ok && (outcome.result as { items: unknown[] }).items).toHaveLength(2);
    });

    it("rejects a limit above 1000 without querying the browser", async () => {
      const outcome = await registry.dispatch("history.search", { limit: 1001 });

      expect(outcome).toEqual({ ok: false, code: "INVALID_REQUEST", message: expect.any(String) as unknown });
      expect(history.search).not.toHaveBeenCalled();
    });

    it("returns INVALID_REQUEST when startTime is after endTime", async () => {
      const outcome = await registry.dispatch("history.search", { startTime: 20, endTime: 10 });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(history.search).not.toHaveBeenCalled();
    });
  });

  describe("history.visits", () => {
    it("lists visits newest first with the time and transition type", async () => {
      history.getVisits.mockResolvedValue([
        { id: "1", visitId: "11", referringVisitId: "0", visitTime: 100, transition: "typed", isLocal: true },
        { id: "1", visitId: "12", referringVisitId: "11", visitTime: 200.5, transition: "link", isLocal: true },
      ]);

      const outcome = await registry.dispatch("history.visits", { url: "https://a.example/" });

      expect(history.getVisits).toHaveBeenCalledWith({ url: "https://a.example/" });
      expect(outcome).toEqual({
        ok: true,
        result: {
          hasMore: false,
          visits: [
            { visitTime: 201, transition: "link" },
            { visitTime: 100, transition: "typed" },
          ],
        },
      });
      expect(validateHistoryVisitsResult(outcome.ok ? outcome.result : undefined)).toBe(true);
    });

    it("returns INVALID_REQUEST for a string that is not a URL", async () => {
      const outcome = await registry.dispatch("history.visits", { url: "not a url" });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(history.getVisits).not.toHaveBeenCalled();
    });
  });

  describe("history.remove", () => {
    it("deletes every distinct URL", async () => {
      const outcome = await registry.dispatch("history.remove", {
        urls: ["https://a.example/", "https://b.example/", "https://a.example/"],
        confirm: true,
      });

      expect(history.deleteUrl.mock.calls).toEqual([[{ url: "https://a.example/" }], [{ url: "https://b.example/" }]]);
      expect(outcome).toEqual({ ok: true, result: { urls: ["https://a.example/", "https://b.example/"] } });
    });

    it("returns CONFIRMATION_REQUIRED and deletes nothing without confirm", async () => {
      const outcome = await registry.dispatch("history.remove", { urls: ["https://a.example/"] });

      expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
      expect(history.deleteUrl).not.toHaveBeenCalled();
    });

    it("deletes nothing when one URL is not a valid URL", async () => {
      const outcome = await registry.dispatch("history.remove", {
        urls: ["https://a.example/", "nope"],
        confirm: true,
      });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(history.deleteUrl).not.toHaveBeenCalled();
    });
  });

  describe("history.clear", () => {
    it("deletes all history when no range is given", async () => {
      const outcome = await registry.dispatch("history.clear", { confirm: true });

      expect(history.deleteAll).toHaveBeenCalledTimes(1);
      expect(history.deleteRange).not.toHaveBeenCalled();
      expect(outcome).toEqual({ ok: true, result: { all: true } });
    });

    it("deletes only the given range, defaulting an open start to 0", async () => {
      vi.spyOn(Date, "now").mockReturnValue(5000);

      const bounded = await registry.dispatch("history.clear", { startTime: 10, endTime: 20, confirm: true });
      const openStart = await registry.dispatch("history.clear", { endTime: 20, confirm: true });
      const openEnd = await registry.dispatch("history.clear", { startTime: 10, confirm: true });

      expect(history.deleteRange.mock.calls).toEqual([
        [{ startTime: 10, endTime: 20 }],
        [{ startTime: 0, endTime: 20 }],
        [{ startTime: 10, endTime: 5000 }],
      ]);
      expect(history.deleteAll).not.toHaveBeenCalled();
      expect([bounded, openStart, openEnd]).toEqual(Array(3).fill({ ok: true, result: { all: false } }));
    });

    it("returns CONFIRMATION_REQUIRED and deletes nothing without confirm", async () => {
      const outcome = await registry.dispatch("history.clear", {});

      expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
      expect(history.deleteAll).not.toHaveBeenCalled();
    });

    it("returns INVALID_REQUEST without deleting when startTime is after endTime", async () => {
      const outcome = await registry.dispatch("history.clear", { startTime: 20, endTime: 10, confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(history.deleteRange).not.toHaveBeenCalled();
    });
  });
});
