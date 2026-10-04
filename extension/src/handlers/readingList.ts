import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { requireApi } from "./api";
import { listLimit, takePage } from "./list";

// 最低版本的 Chrome 已经提供 chrome.readingList，但 Edge 等 Chromium 浏览器是否提供尚未确认，所以仍按需检测。
function readingListApi(): typeof chrome.readingList {
  return requireApi("readingList");
}

// 阅读列表只收 http/https 地址，别的地址 chrome 只会笼统地拒绝。
function isListableUrl(url: string): boolean {
  let protocol: string;
  try {
    protocol = new URL(url).protocol;
  } catch {
    return false;
  }
  return protocol === "http:" || protocol === "https:";
}

// 添加时把不能收的地址翻译成 INVALID_REQUEST。
function requireListableUrl(url: string): void {
  if (!isListableUrl(url)) {
    throw new HandlerError("INVALID_REQUEST", `the reading list only holds http and https URLs, not ${url}`);
  }
}

async function inList(api: typeof chrome.readingList, url: string): Promise<boolean> {
  return (await api.query({ url })).length > 0;
}

// 多个 URL 的操作先确认全部在列表中，任何一个不在都不改动任何条目（全有或全无）；重复的 URL 只处理一次。
async function requireAllInList(api: typeof chrome.readingList, urls: string[]): Promise<string[]> {
  const unique = [...new Set(urls)];
  for (const url of unique) {
    // 列表只收 http/https 地址，别的地址必然不在其中；不拿它去查询，chrome 对它只会笼统地拒绝。
    if (!isListableUrl(url) || !(await inList(api, url))) {
      throw new HandlerError("NOT_FOUND", `${url} is not in the reading list`);
    }
  }
  return unique;
}

const handleList: RpcHandler<"readingList.list"> = async (params) => {
  const api = readingListApi();
  const limit = listLimit(params.limit);
  const entries = await api.query(params.read === undefined ? {} : { hasBeenRead: params.read });
  // chrome 不保证返回顺序；按添加时间从新到旧排，limit 截取的才是确定的一页。
  entries.sort((a, b) => b.creationTime - a.creationTime);
  const page = takePage(entries, limit);
  return {
    // 标题由网页控制，原样返回，不做任何改写。
    contentTrust: "untrusted-page-content",
    hasMore: page.hasMore,
    entries: page.items.map((e) => ({
      url: e.url,
      title: e.title,
      read: e.hasBeenRead,
      createdAt: e.creationTime,
      updatedAt: e.lastUpdateTime,
    })),
  };
};

const handleAdd: RpcHandler<"readingList.add"> = async (params) => {
  const api = readingListApi();
  requireListableUrl(params.url);
  if (await inList(api, params.url)) {
    throw new HandlerError("CONFLICT", `${params.url} is already in the reading list`);
  }
  const title = params.title ?? params.url;
  await api.addEntry({ url: params.url, title, hasBeenRead: false });
  return { url: params.url, title };
};

const handleMarkRead: RpcHandler<"readingList.markRead"> = async (params) => {
  const api = readingListApi();
  const read = params.read ?? true;
  const urls = await requireAllInList(api, params.urls);
  for (const url of urls) {
    await api.updateEntry({ url, hasBeenRead: read });
  }
  return { urls, read };
};

const handleRemove: RpcHandler<"readingList.remove"> = async (params) => {
  const api = readingListApi();
  const urls = await requireAllInList(api, params.urls);
  for (const url of urls) {
    await api.removeEntry({ url });
  }
  return { urls };
};

export function registerReadingListHandlers(registry: HandlerRegistry): void {
  registry.register("readingList.list", handleList);
  registry.register("readingList.add", handleAdd);
  registry.register("readingList.markRead", handleMarkRead);
  registry.register("readingList.remove", handleRemove);
}
