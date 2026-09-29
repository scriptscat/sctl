import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { needs } from "./api";
import type { CookiesListResult } from "@/protocol/generated/protocol.generated";
import { listLimit, takePage } from "./list";

type CookieItem = CookiesListResult["items"][number];

// getAll 不带 partitionKey 时不返回分区（CHIPS）Cookie（T1 真机探针）；给空对象才返回全部。
const ALL_PARTITIONS = { partitionKey: {} } as const;

function toItem(cookie: chrome.cookies.Cookie): CookieItem {
  const item: CookieItem = {
    name: cookie.name,
    value: cookie.value,
    domain: cookie.domain,
    path: cookie.path,
    secure: cookie.secure,
    httpOnly: cookie.httpOnly,
    sameSite: cookie.sameSite,
    session: cookie.session,
  };
  if (cookie.expirationDate !== undefined) {
    item.expires = Math.round(cookie.expirationDate * 1000);
  }
  if (cookie.partitionKey?.topLevelSite !== undefined) {
    item.partitionTopLevelSite = cookie.partitionKey.topLevelSite;
  }
  return item;
}

// chrome.cookies.remove 用 url 定位 Cookie：按域名与路径重建，HTTPS-only 的 Cookie 必须用 https。
function cookieUrl(cookie: chrome.cookies.Cookie): string {
  const host = cookie.domain.startsWith(".") ? cookie.domain.slice(1) : cookie.domain;
  return `${cookie.secure ? "https" : "http"}://${host}${cookie.path}`;
}

// remove 删除一个 Cookie，分区 Cookie 必须带上它自己的分区键；Chrome 没删到东西时返回 null。
async function removeCookie(url: string, cookie: chrome.cookies.Cookie): Promise<boolean> {
  const details: chrome.cookies.CookieDetails = { url, name: cookie.name };
  if (cookie.partitionKey !== undefined) {
    details.partitionKey = cookie.partitionKey;
  }
  return (await chrome.cookies.remove(details)) !== null;
}

const handleList: RpcHandler<"cookies.list"> = async (params) => {
  if (params.url !== undefined && params.domain !== undefined) {
    throw new HandlerError("INVALID_REQUEST", "url and domain are mutually exclusive");
  }
  const limit = listLimit(params.limit);
  const details: chrome.cookies.GetAllDetails = { ...ALL_PARTITIONS };
  if (params.url !== undefined) {
    details.url = params.url;
  }
  if (params.domain !== undefined) {
    details.domain = params.domain;
  }
  if (params.name !== undefined) {
    details.name = params.name;
  }
  const page = takePage(await chrome.cookies.getAll(details), limit);
  return {
    // Cookie 名称和值由网页控制，原样返回。
    contentTrust: "untrusted-page-content",
    hasMore: page.hasMore,
    items: page.items.map(toItem),
  };
};

// url 与 name 指定的那一个 Cookie：同名时优先非分区的那个，与浏览器发请求时的常见选择一致。get 与 rm 都针对它。
async function requireCookie(url: string, name: string): Promise<chrome.cookies.Cookie> {
  const found = await chrome.cookies.getAll({ ...ALL_PARTITIONS, url, name });
  const match = found.find((c) => c.partitionKey === undefined) ?? found[0];
  if (match === undefined) {
    throw new HandlerError("NOT_FOUND", `no cookie ${name} for ${url}`);
  }
  return match;
}

const handleGet: RpcHandler<"cookies.get"> = async (params) => {
  return { contentTrust: "untrusted-page-content", cookie: toItem(await requireCookie(params.url, params.name)) };
};

const handleSet: RpcHandler<"cookies.set"> = async (params) => {
  const details: chrome.cookies.SetDetails = { url: params.url, name: params.name, value: params.value };
  if (params.domain !== undefined) {
    details.domain = params.domain;
  }
  if (params.path !== undefined) {
    details.path = params.path;
  }
  if (params.secure !== undefined) {
    details.secure = params.secure;
  }
  if (params.httpOnly !== undefined) {
    details.httpOnly = params.httpOnly;
  }
  if (params.sameSite !== undefined) {
    details.sameSite = params.sameSite;
  }
  if (params.expires !== undefined) {
    details.expirationDate = params.expires / 1000;
  }
  let stored: chrome.cookies.Cookie | null;
  try {
    stored = await chrome.cookies.set(details);
  } catch (error) {
    throw new HandlerError("INVALID_REQUEST", error instanceof Error ? error.message : String(error));
  }
  if (stored === null) {
    throw new HandlerError("INVALID_REQUEST", `chrome rejected cookie ${params.name} for ${params.url}`);
  }
  return { contentTrust: "untrusted-page-content", cookie: toItem(stored) };
};

// rm 删除单个 Cookie（spec 破坏级别表），同名的分区 Cookie 是别的 Cookie，不在其内。
const handleRemove: RpcHandler<"cookies.remove"> = async (params) => {
  const cookie = await requireCookie(params.url, params.name);
  return { deleted: (await removeCookie(params.url, cookie)) ? 1 : 0 };
};

const handleClear: RpcHandler<"cookies.clear"> = async (params) => {
  if ((params.domain === undefined) === (params.all === undefined)) {
    throw new HandlerError("INVALID_REQUEST", "exactly one of domain and all is required");
  }
  const details: chrome.cookies.GetAllDetails = { ...ALL_PARTITIONS };
  if (params.domain !== undefined) {
    // Chrome 的 domain 过滤包含该域名的子域名。
    details.domain = params.domain;
  }
  let deleted = 0;
  for (const cookie of await chrome.cookies.getAll(details)) {
    if (await removeCookie(cookieUrl(cookie), cookie)) {
      deleted += 1;
    }
  }
  return { deleted };
};

export function registerCookiesHandlers(registry: HandlerRegistry): void {
  registry.register("cookies.list", needs("cookies", handleList));
  registry.register("cookies.get", needs("cookies", handleGet));
  registry.register("cookies.set", needs("cookies", handleSet));
  registry.register("cookies.remove", needs("cookies", handleRemove));
  registry.register("cookies.clear", needs("cookies", handleClear));
}
