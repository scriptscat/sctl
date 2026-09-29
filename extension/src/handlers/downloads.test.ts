import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateDownloadsListResult } from "@/protocol/generated/validators.generated";
import { registerHandlers } from "./index";

type SearchFn = (query: chrome.downloads.DownloadQuery) => Promise<chrome.downloads.DownloadItem[]>;
type DownloadFn = (options: chrome.downloads.DownloadOptions) => Promise<number>;
type IdFn = (id: number) => Promise<void>;
type ShowFn = (id: number) => void;

interface DownloadsMock {
  search: Mock<SearchFn>;
  download: Mock<DownloadFn>;
  pause: Mock<IdFn>;
  resume: Mock<IdFn>;
  cancel: Mock<IdFn>;
  erase: Mock<(query: chrome.downloads.DownloadQuery) => Promise<number[]>>;
  removeFile: Mock<IdFn>;
  show: Mock<ShowFn>;
}

function item(id: number, overrides: Partial<chrome.downloads.DownloadItem> = {}): chrome.downloads.DownloadItem {
  return {
    id,
    url: `https://files.example/${id}.bin`,
    finalUrl: `https://files.example/${id}.bin`,
    referrer: "",
    filename: `/home/u/Downloads/${id}.bin`,
    incognito: false,
    danger: "safe",
    mime: "application/octet-stream",
    startTime: "2026-09-01T08:30:00.000Z",
    endTime: undefined,
    state: "complete",
    paused: false,
    canResume: false,
    error: undefined,
    bytesReceived: 10,
    totalBytes: 10,
    fileSize: 10,
    exists: true,
    ...overrides,
  };
}

describe("downloads handlers", () => {
  let downloads: DownloadsMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    downloads = {
      search: vi.fn<SearchFn>().mockResolvedValue([]),
      download: vi.fn<DownloadFn>().mockResolvedValue(42),
      pause: vi.fn<IdFn>().mockResolvedValue(undefined),
      resume: vi.fn<IdFn>().mockResolvedValue(undefined),
      cancel: vi.fn<IdFn>().mockResolvedValue(undefined),
      erase: vi.fn().mockResolvedValue([]),
      removeFile: vi.fn<IdFn>().mockResolvedValue(undefined),
      show: vi.fn<ShowFn>(),
    };
    vi.stubGlobal("chrome", { downloads });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("downloads.list", () => {
    it("asks chrome for newest first and maps each item with the untrusted marker", async () => {
      downloads.search.mockResolvedValue([
        item(2, { state: "in_progress", bytesReceived: 4, totalBytes: -1, exists: false }),
        item(1),
      ]);

      const outcome = await registry.dispatch("downloads.list", {});

      expect(downloads.search).toHaveBeenCalledWith({ orderBy: ["-startTime"], limit: 101 });
      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: false,
          items: [
            {
              id: 2,
              url: "https://files.example/2.bin",
              filename: "/home/u/Downloads/2.bin",
              state: "in_progress",
              bytesReceived: 4,
              totalBytes: -1,
              startTime: Date.parse("2026-09-01T08:30:00.000Z"),
              exists: false,
            },
            {
              id: 1,
              url: "https://files.example/1.bin",
              filename: "/home/u/Downloads/1.bin",
              state: "complete",
              bytesReceived: 10,
              totalBytes: 10,
              startTime: Date.parse("2026-09-01T08:30:00.000Z"),
              exists: true,
            },
          ],
        },
      });
      expect(validateDownloadsListResult(outcome.ok ? outcome.result : undefined)).toBe(true);
    });

    it("passes state and query filters and reports hasMore when chrome returns one more than limit", async () => {
      downloads.search.mockResolvedValue([item(3), item(2), item(1)]);

      const outcome = await registry.dispatch("downloads.list", { state: "complete", query: "report", limit: 2 });

      expect(downloads.search).toHaveBeenCalledWith({
        orderBy: ["-startTime"],
        limit: 3,
        state: "complete",
        query: ["report"],
      });
      expect(outcome.ok && outcome.result).toMatchObject({ hasMore: true });
      expect(outcome.ok && (outcome.result as { items: unknown[] }).items).toHaveLength(2);
    });

    it("rejects a limit above 1000 without querying chrome", async () => {
      const outcome = await registry.dispatch("downloads.list", { limit: 1001 });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(downloads.search).not.toHaveBeenCalled();
    });
  });

  describe("downloads.start", () => {
    it("always uniquifies conflicts and never shows save-as", async () => {
      const outcome = await registry.dispatch("downloads.start", { url: "https://files.example/a.zip" });

      expect(downloads.download).toHaveBeenCalledWith({
        url: "https://files.example/a.zip",
        conflictAction: "uniquify",
        saveAs: false,
      });
      expect(outcome).toEqual({ ok: true, result: { id: 42 } });
    });

    it("passes a relative filename through", async () => {
      await registry.dispatch("downloads.start", { url: "https://files.example/a.zip", filename: "sub/dir/a.zip" });

      expect(downloads.download).toHaveBeenCalledWith({
        url: "https://files.example/a.zip",
        filename: "sub/dir/a.zip",
        conflictAction: "uniquify",
        saveAs: false,
      });
    });

    it.each([
      "/etc/passwd",
      "\\windows\\a.txt",
      "C:\\a.txt",
      "c:/a.txt",
      "../a.txt",
      "a/../b.txt",
      "a\\..\\b.txt",
      "..",
    ])("rejects filename %s with INVALID_REQUEST and starts nothing", async (filename) => {
      const outcome = await registry.dispatch("downloads.start", { url: "https://files.example/a.zip", filename });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(downloads.download).not.toHaveBeenCalled();
    });

    it("accepts a filename that only contains dots inside a segment name", async () => {
      const outcome = await registry.dispatch("downloads.start", {
        url: "https://files.example/a.zip",
        filename: "a..b/c.tar.gz",
      });

      expect(outcome.ok).toBe(true);
    });

    it("rejects an invalid URL with INVALID_REQUEST", async () => {
      const outcome = await registry.dispatch("downloads.start", { url: "not a url" });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(downloads.download).not.toHaveBeenCalled();
    });

    it("surfaces chrome's rejection reason as INVALID_REQUEST", async () => {
      downloads.download.mockRejectedValue(new Error("Invalid URL"));

      const outcome = await registry.dispatch("downloads.start", { url: "https://files.example/a.zip" });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(!outcome.ok && outcome.message).toContain("Invalid URL");
    });
  });

  describe.each(["pause", "resume"] as const)("downloads.%s", (verb) => {
    const method = `downloads.${verb}` as const;

    it(`calls chrome.downloads.${verb} for an existing download`, async () => {
      downloads.search.mockResolvedValue([item(5, { state: "in_progress" })]);

      const outcome = await registry.dispatch(method, { id: 5 });

      expect(downloads.search).toHaveBeenCalledWith({ id: 5 });
      expect(downloads[verb]).toHaveBeenCalledWith(5);
      expect(outcome).toEqual({ ok: true, result: { id: 5 } });
    });

    it("returns NOT_FOUND for an unknown id and does not call chrome", async () => {
      const outcome = await registry.dispatch(method, { id: 99 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(downloads[verb]).not.toHaveBeenCalled();
    });

    it("surfaces chrome's rejection reason as INVALID_REQUEST", async () => {
      downloads.search.mockResolvedValue([item(5)]);
      downloads[verb].mockRejectedValue(new Error("Download must be in progress"));

      const outcome = await registry.dispatch(method, { id: 5 });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(!outcome.ok && outcome.message).toContain("Download must be in progress");
    });
  });

  describe("downloads.show", () => {
    it("shows an existing download in the file manager", async () => {
      downloads.search.mockResolvedValue([item(5)]);

      const outcome = await registry.dispatch("downloads.show", { id: 5 });

      expect(downloads.show).toHaveBeenCalledWith(5);
      expect(outcome).toEqual({ ok: true, result: { id: 5 } });
    });

    it("returns NOT_FOUND for an unknown id", async () => {
      const outcome = await registry.dispatch("downloads.show", { id: 5 });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(downloads.show).not.toHaveBeenCalled();
    });
  });

  describe("downloads.cancel", () => {
    it("cancels an existing download when confirmed", async () => {
      downloads.search.mockResolvedValue([item(5, { state: "in_progress" })]);

      const outcome = await registry.dispatch("downloads.cancel", { id: 5, confirm: true });

      expect(downloads.cancel).toHaveBeenCalledWith(5);
      expect(outcome).toEqual({ ok: true, result: { id: 5 } });
    });

    it("returns CONFIRMATION_REQUIRED and cancels nothing without confirm", async () => {
      const outcome = await registry.dispatch("downloads.cancel", { id: 5 });

      expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
      expect(downloads.cancel).not.toHaveBeenCalled();
    });

    it("returns NOT_FOUND for an unknown id and INVALID_REQUEST for chrome's rejection", async () => {
      expect(await registry.dispatch("downloads.cancel", { id: 9, confirm: true })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });

      downloads.search.mockResolvedValue([item(5)]);
      downloads.cancel.mockRejectedValue(new Error("Download is not in progress"));
      expect(await registry.dispatch("downloads.cancel", { id: 5, confirm: true })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
    });
  });

  describe("downloads.erase", () => {
    it("erases each distinct id once, removing only the record", async () => {
      downloads.search.mockImplementation((query) => Promise.resolve([item(query.id!)]));

      const outcome = await registry.dispatch("downloads.erase", { ids: [1, 2, 1], confirm: true });

      expect(downloads.erase.mock.calls).toEqual([[{ id: 1 }], [{ id: 2 }]]);
      expect(downloads.removeFile).not.toHaveBeenCalled();
      expect(outcome).toEqual({ ok: true, result: { ids: [1, 2] } });
    });

    it("erases nothing when one id is unknown (all-or-nothing)", async () => {
      downloads.search.mockImplementation((query) => Promise.resolve(query.id === 2 ? [] : [item(query.id!)]));

      const outcome = await registry.dispatch("downloads.erase", { ids: [1, 2], confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(downloads.erase).not.toHaveBeenCalled();
    });

    it("returns CONFIRMATION_REQUIRED and erases nothing without confirm", async () => {
      const outcome = await registry.dispatch("downloads.erase", { ids: [1] });

      expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
      expect(downloads.erase).not.toHaveBeenCalled();
    });
  });

  describe("downloads.deleteFile", () => {
    it("removes only the file of a complete download and keeps the record", async () => {
      downloads.search.mockResolvedValue([item(5)]);

      const outcome = await registry.dispatch("downloads.deleteFile", { id: 5, confirm: true });

      expect(downloads.removeFile).toHaveBeenCalledWith(5);
      expect(downloads.erase).not.toHaveBeenCalled();
      expect(outcome).toEqual({ ok: true, result: { id: 5 } });
    });

    it.each(["in_progress", "interrupted"] as const)(
      "returns INVALID_REQUEST for a %s download and removes nothing",
      async (state) => {
        downloads.search.mockResolvedValue([item(5, { state })]);

        const outcome = await registry.dispatch("downloads.deleteFile", { id: 5, confirm: true });

        expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
        expect(downloads.removeFile).not.toHaveBeenCalled();
      },
    );

    it("returns NOT_FOUND for an unknown id and CONFIRMATION_REQUIRED without confirm", async () => {
      expect(await registry.dispatch("downloads.deleteFile", { id: 5, confirm: true })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(await registry.dispatch("downloads.deleteFile", { id: 5 })).toMatchObject({
        ok: false,
        code: "CONFIRMATION_REQUIRED",
      });
      expect(downloads.removeFile).not.toHaveBeenCalled();
    });

    it("surfaces chrome's rejection (for example the file is already gone) as INVALID_REQUEST", async () => {
      downloads.search.mockResolvedValue([item(5)]);
      downloads.removeFile.mockRejectedValue(new Error("Download file already deleted"));

      const outcome = await registry.dispatch("downloads.deleteFile", { id: 5, confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
    });
  });
});
