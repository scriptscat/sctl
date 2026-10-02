import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateReadingListListResult } from "@/protocol/generated/validators.generated";
import { registerAllHandlers } from "./handlers.fixture";

type QueryFn = (info: chrome.readingList.QueryInfo) => Promise<chrome.readingList.ReadingListEntry[]>;
type AddEntryFn = (entry: chrome.readingList.AddEntryOptions) => Promise<void>;
type UpdateEntryFn = (info: chrome.readingList.UpdateEntryOptions) => Promise<void>;
type RemoveEntryFn = (info: chrome.readingList.RemoveOptions) => Promise<void>;

interface ReadingListMock {
  query: Mock<QueryFn>;
  addEntry: Mock<AddEntryFn>;
  updateEntry: Mock<UpdateEntryFn>;
  removeEntry: Mock<RemoveEntryFn>;
}

function entry(url: string, creationTime: number, overrides: Partial<chrome.readingList.ReadingListEntry> = {}) {
  return {
    url,
    title: `title of ${url}`,
    hasBeenRead: false,
    creationTime,
    lastUpdateTime: creationTime + 1,
    ...overrides,
  };
}

// 伪造的阅读列表只按 url 精确匹配，足以驱动「条目是否存在」的判断。
function listing(entries: chrome.readingList.ReadingListEntry[]): QueryFn {
  return (info) => Promise.resolve(entries.filter((e) => info.url === undefined || e.url === info.url));
}

describe("reading list handlers", () => {
  let readingList: ReadingListMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    readingList = {
      query: vi.fn<QueryFn>(),
      addEntry: vi.fn<AddEntryFn>().mockResolvedValue(undefined),
      updateEntry: vi.fn<UpdateEntryFn>().mockResolvedValue(undefined),
      removeEntry: vi.fn<RemoveEntryFn>().mockResolvedValue(undefined),
    };
    vi.stubGlobal("chrome", { readingList });
    registry = new HandlerRegistry();
    registerAllHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns UNSUPPORTED naming the missing API for every reading list method when the browser has no chrome.readingList", async () => {
    vi.stubGlobal("chrome", {});
    const calls = [
      registry.dispatch("readingList.list", {}),
      registry.dispatch("readingList.add", { url: "https://a.example/" }),
      registry.dispatch("readingList.markRead", { urls: ["https://a.example/"] }),
      registry.dispatch("readingList.remove", { urls: ["https://a.example/"], confirm: true }),
    ];
    for (const outcome of await Promise.all(calls)) {
      expect(outcome).toEqual({
        ok: false,
        code: "UNSUPPORTED",
        message: expect.stringContaining("chrome.readingList") as unknown,
      });
    }
  });

  // manifest 的最低版本（Chrome 125）已经提供阅读列表：缺少它的只能是不提供这个 API 的 Chromium 浏览器，升级 Chrome 不是办法。
  it("does not tell a browser without chrome.readingList to upgrade to a Chrome version below the extension's minimum", async () => {
    vi.stubGlobal("chrome", {});

    const outcome = await registry.dispatch("readingList.list", {});

    expect(outcome).toEqual({
      ok: false,
      code: "UNSUPPORTED",
      message: "this browser does not provide chrome.readingList",
    });
  });

  describe("readingList.list", () => {
    it("lists entries newest first with page-controlled titles untouched and reports whether more remain", async () => {
      readingList.query.mockImplementation(
        listing([
          entry("https://old.example/", 100, { hasBeenRead: true }),
          entry("https://new.example/", 300, { title: "<b>new</b>" }),
          entry("https://mid.example/", 200),
        ]),
      );

      const outcome = await registry.dispatch("readingList.list", { limit: 2 });

      expect(readingList.query).toHaveBeenCalledWith({});
      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: true,
          entries: [
            {
              url: "https://new.example/",
              title: "<b>new</b>",
              read: false,
              createdAt: 300,
              updatedAt: 301,
            },
            {
              url: "https://mid.example/",
              title: "title of https://mid.example/",
              read: false,
              createdAt: 200,
              updatedAt: 201,
            },
          ],
        },
      });
      if (outcome.ok) {
        expect(validateReadingListListResult(outcome.result)).toBe(true);
      }
    });

    it("reports no more entries when the limit covers the whole list", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1), entry("https://b.example/", 2)]));

      const outcome = await registry.dispatch("readingList.list", { limit: 2 });

      expect(outcome.ok && (outcome.result as { hasMore: boolean }).hasMore).toBe(false);
    });

    it("returns at most 100 entries when no limit is given", async () => {
      readingList.query.mockImplementation(
        listing(Array.from({ length: 101 }, (_, i) => entry(`https://e${i}.example/`, i))),
      );

      const outcome = await registry.dispatch("readingList.list", {});

      const result = outcome.ok ? (outcome.result as { entries: unknown[]; hasMore: boolean }) : undefined;
      expect(result?.entries).toHaveLength(100);
      expect(result?.hasMore).toBe(true);
    });

    it("filters by read state when asked", async () => {
      readingList.query.mockResolvedValue([]);

      await registry.dispatch("readingList.list", { read: false });

      expect(readingList.query).toHaveBeenCalledWith({ hasBeenRead: false });
    });

    it("rejects a limit above 1000 without querying the browser", async () => {
      const outcome = await registry.dispatch("readingList.list", { limit: 1001 });

      expect(outcome).toEqual({
        ok: false,
        code: "INVALID_REQUEST",
        message: expect.stringContaining("1000") as unknown,
      });
      expect(readingList.query).not.toHaveBeenCalled();
    });
  });

  describe("readingList.add", () => {
    it("adds the URL as unread with the URL as its title when none is given", async () => {
      readingList.query.mockImplementation(listing([]));

      const outcome = await registry.dispatch("readingList.add", { url: "https://a.example/" });

      expect(readingList.addEntry).toHaveBeenCalledWith({
        url: "https://a.example/",
        title: "https://a.example/",
        hasBeenRead: false,
      });
      expect(outcome).toEqual({ ok: true, result: { url: "https://a.example/", title: "https://a.example/" } });
    });

    it("uses the given title", async () => {
      readingList.query.mockImplementation(listing([]));

      const outcome = await registry.dispatch("readingList.add", { url: "https://a.example/", title: "A" });

      expect(readingList.addEntry).toHaveBeenCalledWith({ url: "https://a.example/", title: "A", hasBeenRead: false });
      expect(outcome).toEqual({ ok: true, result: { url: "https://a.example/", title: "A" } });
    });

    it("returns CONFLICT without adding when the URL is already in the list", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      const outcome = await registry.dispatch("readingList.add", { url: "https://a.example/" });

      expect(outcome).toEqual({
        ok: false,
        code: "CONFLICT",
        message: expect.stringContaining("https://a.example/") as unknown,
      });
      expect(readingList.addEntry).not.toHaveBeenCalled();
    });

    it("returns INVALID_REQUEST for a URL the reading list cannot hold", async () => {
      for (const url of ["not a url", "ftp://a.example/file"]) {
        const outcome = await registry.dispatch("readingList.add", { url });

        expect(outcome).toEqual({ ok: false, code: "INVALID_REQUEST", message: expect.any(String) as unknown });
      }
      expect(readingList.addEntry).not.toHaveBeenCalled();
    });
  });

  describe("readingList.markRead", () => {
    it("marks every given URL as read by default", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1), entry("https://b.example/", 2)]));

      const outcome = await registry.dispatch("readingList.markRead", {
        urls: ["https://a.example/", "https://b.example/"],
      });

      expect(readingList.updateEntry.mock.calls).toEqual([
        [{ url: "https://a.example/", hasBeenRead: true }],
        [{ url: "https://b.example/", hasBeenRead: true }],
      ]);
      expect(outcome).toEqual({ ok: true, result: { urls: ["https://a.example/", "https://b.example/"], read: true } });
    });

    it("marks as unread when read is false", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      const outcome = await registry.dispatch("readingList.markRead", { urls: ["https://a.example/"], read: false });

      expect(readingList.updateEntry).toHaveBeenCalledWith({ url: "https://a.example/", hasBeenRead: false });
      expect(outcome).toEqual({ ok: true, result: { urls: ["https://a.example/"], read: false } });
    });

    it("returns NOT_FOUND without changing any entry when one URL is not in the list", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      const outcome = await registry.dispatch("readingList.markRead", {
        urls: ["https://a.example/", "https://x.example/"],
      });

      expect(outcome).toEqual({
        ok: false,
        code: "NOT_FOUND",
        message: expect.stringContaining("https://x.example/") as unknown,
      });
      expect(readingList.updateEntry).not.toHaveBeenCalled();
    });
  });

  describe("readingList.remove", () => {
    it("removes every given URL once, even when a URL is repeated", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1), entry("https://b.example/", 2)]));

      const outcome = await registry.dispatch("readingList.remove", {
        urls: ["https://a.example/", "https://b.example/", "https://a.example/"],
        confirm: true,
      });

      expect(readingList.removeEntry.mock.calls).toEqual([
        [{ url: "https://a.example/" }],
        [{ url: "https://b.example/" }],
      ]);
      expect(outcome).toEqual({ ok: true, result: { urls: ["https://a.example/", "https://b.example/"] } });
    });

    it("returns CONFIRMATION_REQUIRED without removing anything when the call is not confirmed", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      const outcome = await registry.dispatch("readingList.remove", { urls: ["https://a.example/"] });

      expect(outcome).toEqual({ ok: false, code: "CONFIRMATION_REQUIRED", message: expect.any(String) as unknown });
      expect(readingList.removeEntry).not.toHaveBeenCalled();
    });

    it("returns NOT_FOUND without removing any entry when one URL is not in the list", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      const outcome = await registry.dispatch("readingList.remove", {
        urls: ["https://a.example/", "https://x.example/"],
        confirm: true,
      });

      expect(outcome).toEqual({
        ok: false,
        code: "NOT_FOUND",
        message: expect.stringContaining("https://x.example/") as unknown,
      });
      expect(readingList.removeEntry).not.toHaveBeenCalled();
    });

    it("returns NOT_FOUND, not INVALID_REQUEST, for a URL the reading list cannot hold, since it is not in the list", async () => {
      readingList.query.mockImplementation(listing([entry("https://a.example/", 1)]));

      for (const url of ["not a url", "ftp://a.example/file"]) {
        const outcome = await registry.dispatch("readingList.remove", {
          urls: ["https://a.example/", url],
          confirm: true,
        });

        expect(outcome).toEqual({ ok: false, code: "NOT_FOUND", message: expect.stringContaining(url) as unknown });
      }
      expect(readingList.removeEntry).not.toHaveBeenCalled();
    });
  });
});
