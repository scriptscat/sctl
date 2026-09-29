import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import type { BookmarksListResult } from "@/protocol/generated/protocol.generated";
import { listLimit, takePage } from "./list";

type Node = chrome.bookmarks.BookmarkTreeNode;
type WireNode = BookmarksListResult["nodes"][number];

// chrome.bookmarks 对不存在的 ID 只会 reject（非数字 ID 同理），这里翻译成协议的 NOT_FOUND 领域错误。
export async function requireBookmark(id: string): Promise<Node> {
  try {
    const [node] = await chrome.bookmarks.get(id);
    return node;
  } catch {
    throw new HandlerError("NOT_FOUND", `no bookmark ${id}`);
  }
}

// 多个 ID 的操作先确认全部存在，任何一个不存在都不改动任何节点（全有或全无）；重复的 ID 只处理一次。
export async function requireAllBookmarks(ids: string[]): Promise<Node[]> {
  const found: Node[] = [];
  for (const id of new Set(ids)) {
    found.push(await requireBookmark(id));
  }
  return found;
}

// 根节点（没有父节点）和浏览器内置的顶层文件夹（书签栏、其他书签、移动设备书签，即根的直接子项）
// 不能被移动、编辑或删除。不按 ID 或标题判断：内置文件夹的 ID 与标题随浏览器和语言而异，父节点是根才可靠。
export async function isProtectedBookmark(node: Node): Promise<boolean> {
  if (node.parentId === undefined) {
    return true;
  }
  const parent = await requireBookmark(node.parentId);
  return parent.parentId === undefined;
}

// 受保护的节点只能作为读取对象；写操作在这里统一翻译成 INVALID_REQUEST，删除也复用它。
export async function requireUnprotected(nodes: Node[]): Promise<void> {
  for (const node of nodes) {
    if (await isProtectedBookmark(node)) {
      throw new HandlerError(
        "INVALID_REQUEST",
        `bookmark ${node.id} is the root or a built-in top-level folder and cannot be changed`,
      );
    }
  }
}

function isFolder(node: Node): boolean {
  return node.url === undefined;
}

async function requireFolder(id: string): Promise<Node> {
  const folder = await requireBookmark(id);
  if (!isFolder(folder)) {
    throw new HandlerError("INVALID_REQUEST", `bookmark ${id} is not a folder`);
  }
  return folder;
}

// 放新节点的目标必须是文件夹；根节点虽是文件夹，chrome 不允许在它下面建东西。
async function requireDestination(id: string): Promise<Node> {
  const folder = await requireFolder(id);
  if (folder.parentId === undefined) {
    throw new HandlerError("INVALID_REQUEST", `bookmark ${id} is the root and cannot hold bookmarks`);
  }
  return folder;
}

// chrome 对越界的 index 只给笼统的错误；先比对文件夹当前的子项数，翻译成 INVALID_REQUEST。
async function requireIndex(folderId: string, index: number | undefined): Promise<void> {
  if (index === undefined) {
    return;
  }
  const count = (await chrome.bookmarks.getChildren(folderId)).length;
  if (index > count) {
    throw new HandlerError("INVALID_REQUEST", `index ${index} is past the end of folder ${folderId} (${count} items)`);
  }
}

function requireValidUrl(url: string): void {
  try {
    new URL(url);
  } catch {
    throw new HandlerError("INVALID_REQUEST", `${url} is not a valid URL`);
  }
}

// childCount 只在节点带着 children 时才知道；getChildren/get 返回的节点不带，由调用方传入。
function toWire(node: Node, childCount?: number): WireNode {
  const wire: WireNode = {
    id: node.id,
    type: isFolder(node) ? "folder" : "bookmark",
    // 标题和 URL 由网页控制，原样返回，不做任何改写。
    title: node.title,
    index: node.index ?? 0,
  };
  if (node.url !== undefined) wire.url = node.url;
  if (node.parentId !== undefined) wire.parentId = node.parentId;
  if (node.dateAdded !== undefined) wire.addedAt = node.dateAdded;
  if (isFolder(node)) wire.childCount = childCount ?? node.children?.length;
  return wire;
}

// 子树按先序展开成扁平列表，不含起点本身；层级靠每项的 parentId 表达。
function descendants(node: Node): Node[] {
  return (node.children ?? []).flatMap((child) => [child, ...descendants(child)]);
}

const handleList: RpcHandler<"bookmarks.list"> = async (params) => {
  const limit = listLimit(params.limit);
  const trust = "untrusted-page-content" as const;
  if (params.recursive === true) {
    // 整棵子树不受条数上限约束，只受单帧大小限制（spec 书签一节）。
    const start =
      params.folder === undefined ? (await chrome.bookmarks.getTree())[0] : await requireFolder(params.folder);
    const [subtree] = await chrome.bookmarks.getSubTree(start.id);
    return { contentTrust: trust, hasMore: false, nodes: descendants(subtree).map((node) => toWire(node)) };
  }
  const folderId = params.folder ?? (await chrome.bookmarks.getTree())[0].id;
  if (params.folder !== undefined) {
    await requireFolder(params.folder);
  }
  const page = takePage(await chrome.bookmarks.getChildren(folderId), limit);
  const nodes = await Promise.all(
    page.items.map(async (node) =>
      toWire(node, isFolder(node) ? (await chrome.bookmarks.getChildren(node.id)).length : undefined),
    ),
  );
  return { contentTrust: trust, hasMore: page.hasMore, nodes };
};

const handleSearch: RpcHandler<"bookmarks.search"> = async (params) => {
  const limit = listLimit(params.limit);
  const found = await chrome.bookmarks.search(params.query);
  const page = takePage(found, limit);
  // 结果里的节点不带祖先信息；取一次整棵树，沿 parentId 逐级取出文件夹标题。
  const [tree] = await chrome.bookmarks.getTree();
  const byId = new Map<string, Node>();
  const index = (node: Node) => {
    byId.set(node.id, node);
    node.children?.forEach(index);
  };
  index(tree);
  const pathOf = (node: Node): string[] => {
    const path: string[] = [];
    for (
      let parent = byId.get(node.parentId ?? "");
      parent?.parentId !== undefined;
      parent = byId.get(parent.parentId)
    ) {
      path.unshift(parent.title);
    }
    return path;
  };
  return {
    contentTrust: "untrusted-page-content",
    hasMore: page.hasMore,
    nodes: page.items.map((node) => {
      const full = byId.get(node.id) ?? node;
      return { ...toWire(full), path: pathOf(full) };
    }),
  };
};

const handleAdd: RpcHandler<"bookmarks.add"> = async (params) => {
  requireValidUrl(params.url);
  if (params.folder !== undefined) {
    await requireDestination(params.folder);
    await requireIndex(params.folder, params.index);
  }
  const created = await chrome.bookmarks.create({
    // 不给 parentId 时 chrome 放进「其他书签」，与 spec 的默认一致，不依赖各浏览器不同的内置 ID。
    ...(params.folder === undefined ? {} : { parentId: params.folder }),
    title: params.title ?? params.url,
    url: params.url,
    ...(params.index === undefined ? {} : { index: params.index }),
  });
  return { id: created.id };
};

const handleMkdir: RpcHandler<"bookmarks.mkdir"> = async (params) => {
  if (params.folder !== undefined) {
    await requireDestination(params.folder);
    await requireIndex(params.folder, params.index);
  }
  const created = await chrome.bookmarks.create({
    ...(params.folder === undefined ? {} : { parentId: params.folder }),
    title: params.title,
    ...(params.index === undefined ? {} : { index: params.index }),
  });
  return { id: created.id };
};

// 目标文件夹是被移动文件夹自己或其后代时，chrome 只会笼统地拒绝；沿祖先链检查并翻译成 INVALID_REQUEST。
async function requireNotInside(destination: Node, moving: Node[]): Promise<void> {
  const movingFolders = new Set(moving.filter(isFolder).map((node) => node.id));
  for (let cursor: Node | undefined = destination; cursor !== undefined;) {
    if (movingFolders.has(cursor.id)) {
      throw new HandlerError("INVALID_REQUEST", `cannot move folder ${cursor.id} into itself or its own descendant`);
    }
    cursor = cursor.parentId === undefined ? undefined : await requireBookmark(cursor.parentId);
  }
}

const handleMove: RpcHandler<"bookmarks.move"> = async (params) => {
  // 先做完全部检查再动手：任何一项不通过都不移动任何节点（全有或全无）。
  const nodes = await requireAllBookmarks(params.ids);
  await requireUnprotected(nodes);
  const destination = await requireDestination(params.folder);
  await requireNotInside(destination, nodes);
  await requireIndex(params.folder, params.index);
  for (const [offset, node] of nodes.entries()) {
    await chrome.bookmarks.move(node.id, {
      parentId: params.folder,
      // 多个节点从给定位置起依次排开，保持传入的先后顺序。
      ...(params.index === undefined ? {} : { index: params.index + offset }),
    });
  }
  return { ids: nodes.map((node) => node.id) };
};

const handleEdit: RpcHandler<"bookmarks.edit"> = async (params) => {
  const node = await requireBookmark(params.id);
  await requireUnprotected([node]);
  if (params.title === undefined && params.url === undefined) {
    throw new HandlerError("INVALID_REQUEST", "nothing to change: give a title or a url");
  }
  if (params.url !== undefined) {
    if (isFolder(node)) {
      throw new HandlerError("INVALID_REQUEST", `bookmark ${node.id} is a folder and cannot have a URL`);
    }
    requireValidUrl(params.url);
  }
  await chrome.bookmarks.update(node.id, {
    ...(params.title === undefined ? {} : { title: params.title }),
    ...(params.url === undefined ? {} : { url: params.url }),
  });
  return { id: node.id };
};

export function registerBookmarkHandlers(registry: HandlerRegistry): void {
  registry.register("bookmarks.list", handleList);
  registry.register("bookmarks.search", handleSearch);
  registry.register("bookmarks.add", handleAdd);
  registry.register("bookmarks.mkdir", handleMkdir);
  registry.register("bookmarks.move", handleMove);
  registry.register("bookmarks.edit", handleEdit);
}
