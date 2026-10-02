import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateCookiesListResult } from "@/protocol/generated/validators.generated";
import { registerAllHandlers } from "./handlers.fixture";

type GetAllFn = (details: chrome.cookies.GetAllDetails) => Promise<chrome.cookies.Cookie[]>;
type SetFn = (details: chrome.cookies.SetDetails) => Promise<chrome.cookies.Cookie | null>;
type RemoveFn = (details: chrome.cookies.CookieDetails) => Promise<unknown>;

interface CookiesMock {
  getAll: Mock<GetAllFn>;
  set: Mock<SetFn>;
  remove: Mock<RemoveFn>;
}

function cookie(name: string, overrides: Partial<chrome.cookies.Cookie> = {}): chrome.cookies.Cookie {
  return {
    name,
    value: `v-${name}`,
    domain: "example.com",
    path: "/",
    storeId: "0",
    session: true,
    hostOnly: false,
    secure: false,
    httpOnly: false,
    sameSite: "unspecified",
    ...overrides,
  };
}

const partitioned = (name: string): chrome.cookies.Cookie =>
  cookie(name, {
    domain: ".cdn.example",
    secure: true,
    partitionKey: { topLevelSite: "https://top.example", hasCrossSiteAncestor: false },
  });

describe("cookies handlers", () => {
  let cookies: CookiesMock;
  let registry: HandlerRegistry;

  beforeEach(() => {
    cookies = {
      getAll: vi.fn<GetAllFn>().mockResolvedValue([]),
      set: vi.fn<SetFn>().mockResolvedValue(cookie("a")),
      remove: vi.fn<RemoveFn>().mockResolvedValue({}),
    };
    vi.stubGlobal("chrome", { cookies });
    registry = new HandlerRegistry();
    registerAllHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("cookies.list", () => {
    it("asks for all partitions, maps every field and the partition's top-level site, and marks the result untrusted", async () => {
      cookies.getAll.mockResolvedValue([
        cookie("sid", { expirationDate: 1788251400.5, session: false, secure: true, httpOnly: true, sameSite: "lax" }),
        partitioned("chips"),
      ]);

      const outcome = await registry.dispatch("cookies.list", {});

      expect(cookies.getAll).toHaveBeenCalledWith({ partitionKey: {} });
      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: false,
          items: [
            {
              name: "sid",
              value: "v-sid",
              domain: "example.com",
              path: "/",
              expires: 1788251400500,
              secure: true,
              httpOnly: true,
              sameSite: "lax",
              session: false,
            },
            {
              name: "chips",
              value: "v-chips",
              domain: ".cdn.example",
              path: "/",
              secure: true,
              httpOnly: false,
              sameSite: "unspecified",
              session: true,
              partitionTopLevelSite: "https://top.example",
            },
          ],
        },
      });
      expect(validateCookiesListResult(outcome.ok ? outcome.result : undefined)).toBe(true);
    });

    it("passes url or domain and name filters through with the partition wildcard", async () => {
      await registry.dispatch("cookies.list", { url: "https://a.example/x", name: "sid" });
      await registry.dispatch("cookies.list", { domain: "a.example" });

      expect(cookies.getAll).toHaveBeenNthCalledWith(1, {
        partitionKey: {},
        url: "https://a.example/x",
        name: "sid",
      });
      expect(cookies.getAll).toHaveBeenNthCalledWith(2, { partitionKey: {}, domain: "a.example" });
    });

    it("truncates to limit and reports hasMore", async () => {
      cookies.getAll.mockResolvedValue([cookie("a"), cookie("b"), cookie("c")]);

      const outcome = await registry.dispatch("cookies.list", { limit: 2 });

      expect(outcome.ok && outcome.result).toMatchObject({ hasMore: true });
      expect(outcome.ok && (outcome.result as { items: unknown[] }).items).toHaveLength(2);
    });

    it("rejects url together with domain, and a limit above 1000, without reading cookies", async () => {
      expect(await registry.dispatch("cookies.list", { url: "https://a.example/", domain: "a.example" })).toMatchObject(
        {
          ok: false,
          code: "INVALID_REQUEST",
        },
      );
      expect(await registry.dispatch("cookies.list", { limit: 1001 })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
      expect(cookies.getAll).not.toHaveBeenCalled();
    });
  });

  describe("cookies.get", () => {
    it("returns the matching cookie, preferring a non-partitioned one", async () => {
      cookies.getAll.mockResolvedValue([partitioned("sid"), cookie("sid")]);

      const outcome = await registry.dispatch("cookies.get", { url: "https://example.com/", name: "sid" });

      expect(cookies.getAll).toHaveBeenCalledWith({ partitionKey: {}, url: "https://example.com/", name: "sid" });
      expect(outcome).toMatchObject({ ok: true, result: { cookie: { name: "sid", domain: "example.com" } } });
      expect(outcome.ok && (outcome.result as { cookie: object }).cookie).not.toHaveProperty("partitionTopLevelSite");
    });

    it("returns NOT_FOUND when there is no such cookie", async () => {
      expect(await registry.dispatch("cookies.get", { url: "https://example.com/", name: "nope" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
    });
  });

  describe("cookies.set", () => {
    it("sets a session cookie when no expires is given, mapping only the given options", async () => {
      cookies.set.mockResolvedValue(cookie("sid", { value: "1" }));

      const outcome = await registry.dispatch("cookies.set", { url: "https://example.com/", name: "sid", value: "1" });

      expect(cookies.set).toHaveBeenCalledWith({ url: "https://example.com/", name: "sid", value: "1" });
      expect(outcome).toMatchObject({ ok: true, result: { cookie: { name: "sid", value: "1", session: true } } });
    });

    it("maps domain, path, flags, sameSite and converts expires from milliseconds to seconds", async () => {
      await registry.dispatch("cookies.set", {
        url: "https://example.com/",
        name: "sid",
        value: "1",
        domain: "example.com",
        path: "/app",
        secure: true,
        httpOnly: true,
        sameSite: "strict",
        expires: 1788251400500,
      });

      expect(cookies.set).toHaveBeenCalledWith({
        url: "https://example.com/",
        name: "sid",
        value: "1",
        domain: "example.com",
        path: "/app",
        secure: true,
        httpOnly: true,
        sameSite: "strict",
        expirationDate: 1788251400.5,
      });
    });

    it("surfaces Chrome's rejection reason as INVALID_REQUEST, whether it throws or resolves null", async () => {
      cookies.set.mockRejectedValueOnce(new Error("Failed to parse or set cookie named sid."));
      const thrown = await registry.dispatch("cookies.set", { url: "https://example.com/", name: "sid", value: "1" });
      expect(thrown).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(!thrown.ok && thrown.message).toContain("Failed to parse or set cookie named sid.");

      cookies.set.mockResolvedValueOnce(null);
      expect(
        await registry.dispatch("cookies.set", { url: "https://example.com/", name: "sid", value: "1" }),
      ).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
    });
  });

  describe("cookies.remove", () => {
    it("removes the cookie by url and name and reports the count", async () => {
      cookies.getAll.mockResolvedValue([cookie("sid")]);

      const outcome = await registry.dispatch("cookies.remove", {
        url: "https://example.com/",
        name: "sid",
        confirm: true,
      });

      expect(cookies.remove).toHaveBeenCalledWith({ url: "https://example.com/", name: "sid" });
      expect(outcome).toEqual({ ok: true, result: { deleted: 1 } });
    });

    it("removes a partitioned match with its partition key", async () => {
      cookies.getAll.mockResolvedValue([partitioned("sid")]);

      await registry.dispatch("cookies.remove", { url: "https://cdn.example/", name: "sid", confirm: true });

      expect(cookies.remove).toHaveBeenCalledWith({
        url: "https://cdn.example/",
        name: "sid",
        partitionKey: { topLevelSite: "https://top.example", hasCrossSiteAncestor: false },
      });
    });

    it("removes only the single cookie that get would return when partitioned cookies share the name", async () => {
      cookies.getAll.mockResolvedValue([partitioned("sid"), cookie("sid")]);

      const outcome = await registry.dispatch("cookies.remove", {
        url: "https://example.com/",
        name: "sid",
        confirm: true,
      });

      expect(cookies.remove.mock.calls.map(([details]) => details)).toEqual([
        { url: "https://example.com/", name: "sid" },
      ]);
      expect(outcome).toEqual({ ok: true, result: { deleted: 1 } });
    });

    it("returns NOT_FOUND when absent and CONFIRMATION_REQUIRED without confirm, removing nothing", async () => {
      expect(
        await registry.dispatch("cookies.remove", { url: "https://example.com/", name: "sid", confirm: true }),
      ).toMatchObject({ ok: false, code: "NOT_FOUND" });
      cookies.getAll.mockResolvedValue([cookie("sid")]);
      expect(await registry.dispatch("cookies.remove", { url: "https://example.com/", name: "sid" })).toMatchObject({
        ok: false,
        code: "CONFIRMATION_REQUIRED",
      });
      expect(cookies.remove).not.toHaveBeenCalled();
    });
  });

  describe("cookies.clear", () => {
    it("deletes every cookie of the domain (subdomains included by Chrome), partitioned ones with their key, and returns the count", async () => {
      cookies.getAll.mockResolvedValue([
        cookie("a", { domain: ".example.com", path: "/p", secure: true }),
        cookie("b", { domain: "sub.example.com" }),
        partitioned("c"),
      ]);

      const outcome = await registry.dispatch("cookies.clear", { domain: "example.com", confirm: true });

      expect(cookies.getAll).toHaveBeenCalledWith({ partitionKey: {}, domain: "example.com" });
      expect(cookies.remove.mock.calls.map(([details]) => details)).toEqual([
        { url: "https://example.com/p", name: "a" },
        { url: "http://sub.example.com/", name: "b" },
        {
          url: "https://cdn.example/",
          name: "c",
          partitionKey: { topLevelSite: "https://top.example", hasCrossSiteAncestor: false },
        },
      ]);
      expect(outcome).toEqual({ ok: true, result: { deleted: 3 } });
    });

    it("clears everything with all, counting only cookies Chrome actually removed", async () => {
      cookies.getAll.mockResolvedValue([cookie("a"), cookie("b")]);
      cookies.remove.mockResolvedValueOnce(null);

      const outcome = await registry.dispatch("cookies.clear", { all: true, confirm: true });

      expect(cookies.getAll).toHaveBeenCalledWith({ partitionKey: {} });
      expect(outcome).toEqual({ ok: true, result: { deleted: 1 } });
    });

    it("needs exactly one of domain and all, and confirm, otherwise deletes nothing", async () => {
      expect(await registry.dispatch("cookies.clear", { confirm: true })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
      expect(await registry.dispatch("cookies.clear", { domain: "a.example", all: true, confirm: true })).toMatchObject(
        {
          ok: false,
          code: "INVALID_REQUEST",
        },
      );
      expect(await registry.dispatch("cookies.clear", { all: true })).toMatchObject({
        ok: false,
        code: "CONFIRMATION_REQUIRED",
      });
      expect(cookies.remove).not.toHaveBeenCalled();
    });
  });
});
