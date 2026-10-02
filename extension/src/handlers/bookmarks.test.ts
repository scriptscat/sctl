import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import type { BookmarksListResult } from "@/protocol/generated/protocol.generated";
import {
  validateBookmarksListResult,
  validateBookmarksRemoveResult,
  validateBookmarksSearchResult,
} from "@/protocol/generated/validators.generated";
import { registerHandlers } from "./index";

interface FakeNode {
  id: string;
  title: string;
  url?: string;
  parentId?: string;
  index?: number;
  dateAdded?: number;
  children?: FakeNode[];
}

// 伪造的书签树：0 是根；1/2/3 是浏览器内置的顶层文件夹。
function buildTree(): FakeNode {
  const tree: FakeNode = {
    id: "0",
    title: "",
    children: [
      {
        id: "1",
        title: "Bookmarks bar",
        children: [
          {
            id: "10",
            title: "Dev",
            children: [
              { id: "11", title: "MDN", url: "https://developer.mozilla.org/", dateAdded: 1000 },
              { id: "12", title: "Go", children: [{ id: "13", title: "Go Blog", url: "https://go.dev/blog/" }] },
            ],
          },
          { id: "14", title: "<b>News</b>", url: "https://news.example/", dateAdded: 2000 },
        ],
      },
      { id: "2", title: "Other bookmarks", children: [{ id: "20", title: "Other", url: "https://other.example/" }] },
      { id: "3", title: "Mobile bookmarks", children: [] },
    ],
  };
  const link = (node: FakeNode) => {
    (node.children ?? []).forEach((child, index) => {
      child.parentId = node.id;
      child.index = index;
      link(child);
    });
  };
  link(tree);
  return tree;
}

type Outcome = Awaited<ReturnType<HandlerRegistry["dispatch"]>>;

// dispatch 的结果类型是 unknown；成功时取出 nodes，失败时给空数组（断言另有 toMatchObject 负责）。
function nodesOf(outcome: Outcome): (BookmarksListResult["nodes"][number] & { path?: string[] })[] {
  return outcome.ok ? (outcome.result as BookmarksListResult).nodes : [];
}

function flatten(node: FakeNode, into = new Map<string, FakeNode>()): Map<string, FakeNode> {
  into.set(node.id, node);
  (node.children ?? []).forEach((child) => flatten(child, into));
  return into;
}

// chrome 返回的子项不带 children（getChildren），单个节点也不带（get），只有 getSubTree/getTree 带；这里照此裁剪。
function bare(node: FakeNode): FakeNode {
  return { ...node, children: undefined };
}

describe("bookmark handlers", () => {
  let root: FakeNode;
  let nodes: Map<string, FakeNode>;
  let registry: HandlerRegistry;
  let nextId: number;
  const bookmarks = {
    getTree: vi.fn(),
    get: vi.fn(),
    getChildren: vi.fn(),
    getSubTree: vi.fn(),
    search: vi.fn(),
    create: vi.fn(),
    move: vi.fn(),
    update: vi.fn(),
    remove: vi.fn(),
    removeTree: vi.fn(),
  };

  beforeEach(() => {
    root = buildTree();
    nodes = flatten(root);
    nextId = 100;
    bookmarks.getTree.mockImplementation(() => Promise.resolve([root]));
    bookmarks.get.mockImplementation((id: string) => {
      const node = nodes.get(id);
      return node === undefined
        ? Promise.reject(new Error("Can't find bookmark for id."))
        : Promise.resolve([bare(node)]);
    });
    bookmarks.getChildren.mockImplementation((id: string) => {
      const node = nodes.get(id);
      return node === undefined
        ? Promise.reject(new Error("Can't find parent bookmark for id."))
        : Promise.resolve((node.children ?? []).map(bare));
    });
    bookmarks.getSubTree.mockImplementation((id: string) => {
      const node = nodes.get(id);
      return node === undefined ? Promise.reject(new Error("Can't find bookmark for id.")) : Promise.resolve([node]);
    });
    bookmarks.search.mockImplementation((query: string) =>
      Promise.resolve(
        [...nodes.values()]
          .filter((n) => n.id !== "0" && `${n.title} ${n.url ?? ""}`.toLowerCase().includes(query.toLowerCase()))
          .map(bare),
      ),
    );
    bookmarks.create.mockImplementation((info: { parentId?: string; title?: string; url?: string; index?: number }) => {
      const parentId = info.parentId ?? "2";
      const node: FakeNode = { id: String(nextId++), title: info.title ?? "", url: info.url, parentId };
      return Promise.resolve(node);
    });
    bookmarks.move.mockImplementation((id: string) => Promise.resolve(bare(nodes.get(id)!)));
    bookmarks.update.mockImplementation((id: string) => Promise.resolve(bare(nodes.get(id)!)));
    bookmarks.remove.mockResolvedValue(undefined);
    bookmarks.removeTree.mockResolvedValue(undefined);
    Object.values(bookmarks).forEach((fn) => fn.mockClear());
    vi.stubGlobal("chrome", { bookmarks });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("bookmarks.list", () => {
    it("lists the top-level folders under the root when no folder is given, with child counts", async () => {
      const outcome = await registry.dispatch("bookmarks.list", {});

      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: false,
          nodes: [
            { id: "1", type: "folder", title: "Bookmarks bar", parentId: "0", index: 0, childCount: 2 },
            { id: "2", type: "folder", title: "Other bookmarks", parentId: "0", index: 1, childCount: 1 },
            { id: "3", type: "folder", title: "Mobile bookmarks", parentId: "0", index: 2, childCount: 0 },
          ],
        },
      });
    });

    it("lists only the direct children of a folder, keeping page-controlled titles untouched", async () => {
      const outcome = await registry.dispatch("bookmarks.list", { folder: "1" });

      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          hasMore: false,
          nodes: [
            { id: "10", type: "folder", title: "Dev", parentId: "1", index: 0, childCount: 2 },
            {
              id: "14",
              type: "bookmark",
              title: "<b>News</b>",
              url: "https://news.example/",
              parentId: "1",
              index: 1,
              addedAt: 2000,
            },
          ],
        },
      });
      if (outcome.ok) expect(validateBookmarksListResult(outcome.result)).toBe(true);
    });

    it("caps a non-recursive listing at limit and reports hasMore", async () => {
      const outcome = await registry.dispatch("bookmarks.list", { folder: "1", limit: 1 });

      expect(outcome).toMatchObject({ ok: true, result: { hasMore: true, nodes: [{ id: "10" }] } });
    });

    it("rejects a limit above 1000 before reading any bookmark", async () => {
      const outcome = await registry.dispatch("bookmarks.list", { limit: 1001 });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(bookmarks.getTree).not.toHaveBeenCalled();
      expect(bookmarks.getChildren).not.toHaveBeenCalled();
    });

    it("returns the whole subtree depth-first when recursive, ignoring the default limit", async () => {
      const outcome = await registry.dispatch("bookmarks.list", { folder: "1", recursive: true });

      expect(nodesOf(outcome).map((n) => n.id)).toEqual(["10", "11", "12", "13", "14"]);
      expect(outcome).toMatchObject({ ok: true, result: { hasMore: false } });
      expect(nodesOf(outcome).find((n) => n.id === "12")).toMatchObject({
        type: "folder",
        parentId: "10",
        index: 1,
        childCount: 1,
      });
    });

    it("returns every node below the root when recursive without a folder", async () => {
      const outcome = await registry.dispatch("bookmarks.list", { recursive: true });

      expect(nodesOf(outcome).map((n) => n.id)).toEqual(["1", "10", "11", "12", "13", "14", "2", "20", "3"]);
    });

    it("returns NOT_FOUND for an unknown folder and INVALID_REQUEST when the id is a bookmark", async () => {
      expect(await registry.dispatch("bookmarks.list", { folder: "999" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(await registry.dispatch("bookmarks.list", { folder: "14" })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
    });
  });

  describe("bookmarks.search", () => {
    it("matches titles and URLs and gives each result the titles of its enclosing folders", async () => {
      const outcome = await registry.dispatch("bookmarks.search", { query: "go" });

      expect(outcome).toMatchObject({ ok: true, result: { contentTrust: "untrusted-page-content", hasMore: false } });
      const found = nodesOf(outcome);
      expect(found.find((n) => n.id === "13")).toEqual({
        id: "13",
        type: "bookmark",
        title: "Go Blog",
        url: "https://go.dev/blog/",
        parentId: "12",
        index: 0,
        path: ["Bookmarks bar", "Dev", "Go"],
      });
      expect(found.find((n) => n.id === "12")).toMatchObject({
        type: "folder",
        childCount: 1,
        path: ["Bookmarks bar", "Dev"],
      });
      if (outcome.ok) expect(validateBookmarksSearchResult(outcome.result)).toBe(true);
    });

    it("gives an empty path to a built-in top-level folder found by search", async () => {
      const outcome = await registry.dispatch("bookmarks.search", { query: "other bookmarks" });

      expect(outcome).toMatchObject({ ok: true, result: { nodes: [{ id: "2", path: [] }] } });
    });

    it("caps results at limit and reports hasMore", async () => {
      const outcome = await registry.dispatch("bookmarks.search", { query: "e", limit: 2 });

      expect(outcome).toMatchObject({ ok: true, result: { hasMore: true } });
      expect(nodesOf(outcome)).toHaveLength(2);
    });

    it("rejects a limit above 1000 before searching", async () => {
      const outcome = await registry.dispatch("bookmarks.search", { query: "x", limit: 5000 });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(bookmarks.search).not.toHaveBeenCalled();
    });
  });

  describe("bookmarks.add and bookmarks.mkdir", () => {
    it("adds a bookmark to Other bookmarks by default, titled with its URL, and returns the new id", async () => {
      const outcome = await registry.dispatch("bookmarks.add", { url: "https://new.example/" });

      expect(outcome).toEqual({ ok: true, result: { id: "100" } });
      expect(bookmarks.create).toHaveBeenCalledWith({ title: "https://new.example/", url: "https://new.example/" });
    });

    it("adds into the given folder at the given index with the given title", async () => {
      const outcome = await registry.dispatch("bookmarks.add", {
        url: "https://new.example/",
        title: "New",
        folder: "10",
        index: 1,
      });

      expect(outcome).toEqual({ ok: true, result: { id: "100" } });
      expect(bookmarks.create).toHaveBeenCalledWith({
        parentId: "10",
        title: "New",
        url: "https://new.example/",
        index: 1,
      });
    });

    it("rejects an invalid URL, a bookmark or the root as parent, and an index past the end, creating nothing", async () => {
      const bad = [
        { url: "not a url" },
        { url: "https://a.example/", folder: "14" },
        { url: "https://a.example/", folder: "0" },
        { url: "https://a.example/", folder: "10", index: 3 },
      ];
      for (const params of bad) {
        expect(await registry.dispatch("bookmarks.add", params)).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      }
      expect(bookmarks.create).not.toHaveBeenCalled();
    });

    it("returns NOT_FOUND when the target folder does not exist", async () => {
      expect(await registry.dispatch("bookmarks.add", { url: "https://a.example/", folder: "999" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(bookmarks.create).not.toHaveBeenCalled();
    });

    it("creates a folder without a url and returns its id", async () => {
      const outcome = await registry.dispatch("bookmarks.mkdir", { title: "Reading", folder: "1", index: 0 });

      expect(outcome).toEqual({ ok: true, result: { id: "100" } });
      expect(bookmarks.create).toHaveBeenCalledWith({ parentId: "1", title: "Reading", index: 0 });
    });

    it("creates a folder in Other bookmarks by default and rejects the root or a bookmark as parent", async () => {
      expect(await registry.dispatch("bookmarks.mkdir", { title: "Reading" })).toMatchObject({ ok: true });
      expect(bookmarks.create).toHaveBeenLastCalledWith({ title: "Reading" });
      bookmarks.create.mockClear();
      for (const folder of ["0", "14"]) {
        expect(await registry.dispatch("bookmarks.mkdir", { title: "x", folder })).toMatchObject({
          ok: false,
          code: "INVALID_REQUEST",
        });
      }
      expect(bookmarks.create).not.toHaveBeenCalled();
    });
  });

  describe("bookmarks.move", () => {
    it("moves several nodes into a folder in order, placing them consecutively from the index", async () => {
      const outcome = await registry.dispatch("bookmarks.move", { ids: ["14", "20"], folder: "10", index: 0 });

      expect(outcome).toEqual({ ok: true, result: { ids: ["14", "20"] } });
      expect(bookmarks.move.mock.calls).toEqual([
        ["14", { parentId: "10", index: 0 }],
        ["20", { parentId: "10", index: 1 }],
      ]);
    });

    it("appends to the folder when no index is given", async () => {
      await registry.dispatch("bookmarks.move", { ids: ["14"], folder: "2" });

      expect(bookmarks.move).toHaveBeenCalledWith("14", { parentId: "2" });
    });

    it("moves nothing and returns NOT_FOUND when any id or the destination is unknown", async () => {
      expect(await registry.dispatch("bookmarks.move", { ids: ["14", "999"], folder: "10" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(await registry.dispatch("bookmarks.move", { ids: ["14"], folder: "999" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(bookmarks.move).not.toHaveBeenCalled();
    });

    it("moves nothing when the batch contains the root or a built-in top-level folder", async () => {
      for (const protectedId of ["0", "1", "2", "3"]) {
        const outcome = await registry.dispatch("bookmarks.move", { ids: ["14", protectedId], folder: "10" });
        expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      }
      expect(bookmarks.move).not.toHaveBeenCalled();
    });

    it("moves nothing when the destination is a bookmark, the root, or inside a moved folder", async () => {
      const bad = [
        { ids: ["11"], folder: "14" },
        { ids: ["11"], folder: "0" },
        { ids: ["10"], folder: "10" },
        { ids: ["10"], folder: "13" },
        { ids: ["11"], folder: "10", index: 5 },
      ];
      for (const params of bad) {
        expect(await registry.dispatch("bookmarks.move", params)).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      }
      expect(bookmarks.move).not.toHaveBeenCalled();
    });

    it("moves a repeated id only once", async () => {
      const outcome = await registry.dispatch("bookmarks.move", { ids: ["14", "14"], folder: "2" });

      expect(outcome).toEqual({ ok: true, result: { ids: ["14"] } });
      expect(bookmarks.move).toHaveBeenCalledTimes(1);
    });
  });

  describe("bookmarks.edit", () => {
    it("updates only the fields that are given", async () => {
      expect(await registry.dispatch("bookmarks.edit", { id: "14", title: "Renamed" })).toEqual({
        ok: true,
        result: { id: "14" },
      });
      expect(bookmarks.update).toHaveBeenLastCalledWith("14", { title: "Renamed" });

      await registry.dispatch("bookmarks.edit", { id: "14", url: "https://other.example/x", title: "T" });
      expect(bookmarks.update).toHaveBeenLastCalledWith("14", { title: "T", url: "https://other.example/x" });
    });

    it("renames a folder", async () => {
      expect(await registry.dispatch("bookmarks.edit", { id: "10", title: "Renamed" })).toMatchObject({ ok: true });
      expect(bookmarks.update).toHaveBeenCalledWith("10", { title: "Renamed" });
    });

    it("rejects a URL on a folder with INVALID_REQUEST and changes nothing", async () => {
      const outcome = await registry.dispatch("bookmarks.edit", { id: "10", url: "https://x.example/" });

      expect(outcome).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
        message: expect.stringContaining("folder") as unknown,
      });
      expect(bookmarks.update).not.toHaveBeenCalled();
    });

    it("rejects the root and built-in top-level folders with INVALID_REQUEST", async () => {
      for (const id of ["0", "1", "2", "3"]) {
        expect(await registry.dispatch("bookmarks.edit", { id, title: "x" })).toMatchObject({
          ok: false,
          code: "INVALID_REQUEST",
        });
      }
      expect(bookmarks.update).not.toHaveBeenCalled();
    });

    it("rejects an edit that changes nothing, an invalid URL, and an unknown id", async () => {
      expect(await registry.dispatch("bookmarks.edit", { id: "14" })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
      expect(await registry.dispatch("bookmarks.edit", { id: "14", url: "nope" })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
      });
      expect(await registry.dispatch("bookmarks.edit", { id: "999", title: "x" })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
      expect(bookmarks.update).not.toHaveBeenCalled();
    });
  });

  describe("bookmarks.remove", () => {
    // 从树上摘下节点，模拟用户在审批期间删除或移走它。
    function detach(id: string): FakeNode {
      const node = nodes.get(id)!;
      const parent = nodes.get(node.parentId!)!;
      parent.children = parent.children!.filter((child) => child.id !== id);
      return node;
    }

    function attach(node: FakeNode, parentId: string): void {
      const parent = nodes.get(parentId)!;
      node.parentId = parentId;
      parent.children = [...(parent.children ?? []), node];
      flatten(node, nodes);
    }

    async function prepared(ids: string[]) {
      const outcome = await registry.prepare("bookmarks.remove", { ids });
      if (!outcome.ok) {
        throw new Error(`prepare failed: ${outcome.code} ${outcome.message}`);
      }
      return outcome.request;
    }

    it("summarises what will be deleted before approval, counting an id inside another listed folder once", async () => {
      const outcome = await registry.prepare("bookmarks.remove", { ids: ["14", "10", "13", "11", "14"] });

      expect(outcome).toEqual({
        ok: true,
        request: {
          kind: "bookmarks.remove",
          detail: {
            summary: { items: 2, bookmarks: 1, folders: 1, containedBookmarks: 2, containedFolders: 1 },
            items: [
              {
                id: "14",
                type: "bookmark",
                title: "<b>News</b>",
                url: "https://news.example/",
                parentId: "1",
                path: ["Bookmarks bar"],
              },
              {
                id: "10",
                type: "folder",
                title: "Dev",
                parentId: "1",
                path: ["Bookmarks bar"],
                bookmarks: 2,
                folders: 1,
                contents: [
                  { id: "11", type: "bookmark", title: "MDN", url: "https://developer.mozilla.org/", parentId: "10" },
                  { id: "12", type: "folder", title: "Go", parentId: "10" },
                  { id: "13", type: "bookmark", title: "Go Blog", url: "https://go.dev/blog/", parentId: "12" },
                ],
              },
            ],
          },
        },
      });
      expect(bookmarks.remove).not.toHaveBeenCalled();
      expect(bookmarks.removeTree).not.toHaveBeenCalled();
    });

    it("rejects an unknown id with NOT_FOUND before asking for approval", async () => {
      expect(await registry.prepare("bookmarks.remove", { ids: ["14", "999"] })).toMatchObject({
        ok: false,
        code: "NOT_FOUND",
      });
    });

    it("rejects the root and built-in top-level folders with INVALID_REQUEST before asking for approval", async () => {
      for (const id of ["0", "1", "2", "3"]) {
        expect(await registry.prepare("bookmarks.remove", { ids: ["14", id] })).toMatchObject({
          ok: false,
          code: "INVALID_REQUEST",
        });
      }
    });

    it("accepts 500 ids in one request and rejects 501 with INVALID_REQUEST without reading bookmarks", async () => {
      expect(await registry.prepare("bookmarks.remove", { ids: Array<string>(500).fill("14") })).toMatchObject({
        ok: true,
      });
      bookmarks.get.mockClear();

      expect(await registry.prepare("bookmarks.remove", { ids: Array<string>(501).fill("14") })).toMatchObject({
        ok: false,
        code: "INVALID_REQUEST",
        message: expect.stringContaining("500") as unknown,
      });
      expect(bookmarks.get).not.toHaveBeenCalled();
    });

    it("deletes exactly the shown bookmarks and folders once approved and reports how many were deleted", async () => {
      const request = await prepared(["14", "10"]);

      const outcome = await registry.execute(request);

      expect(outcome).toEqual({ ok: true, result: { ids: ["14", "10"], bookmarks: 3, folders: 2 } });
      if (outcome.ok) expect(validateBookmarksRemoveResult(outcome.result)).toBe(true);
      expect(bookmarks.remove.mock.calls).toEqual([["14"]]);
      expect(bookmarks.removeTree.mock.calls).toEqual([["10"]]);
    });

    const changes: Array<[string, () => void]> = [
      ["a listed bookmark was deleted", () => detach("14")],
      ["a listed bookmark was moved to another folder", () => attach(detach("14"), "2")],
      ["a bookmark inside a listed folder was deleted", () => detach("11")],
      ["a bookmark was moved out of a listed folder", () => attach(detach("13"), "2")],
      [
        "a bookmark was added inside a listed folder",
        () => attach({ id: "50", title: "New", url: "https://n.example/" }, "12"),
      ],
      ["a bookmark inside a listed folder was retitled", () => (nodes.get("11")!.title = "Changed")],
    ];
    for (const [change, apply] of changes) {
      it(`answers CONFLICT and deletes nothing when ${change} before approval`, async () => {
        const request = await prepared(["14", "10"]);
        apply();

        expect(await registry.execute(request)).toMatchObject({ ok: false, code: "CONFLICT" });
        expect(bookmarks.remove).not.toHaveBeenCalled();
        expect(bookmarks.removeTree).not.toHaveBeenCalled();
      });
    }

    it("is never run directly: dispatching it without approval is refused", async () => {
      await expect(registry.dispatch("bookmarks.remove", { ids: ["14"] })).rejects.toThrow(/no handler registered/);
      expect(bookmarks.remove).not.toHaveBeenCalled();
    });
  });
});
