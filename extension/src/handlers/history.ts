import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { needs } from "./api";
import { listLimit, takePage } from "./list";

// chrome 的历史时间戳是带小数的毫秒；协议用整数，四舍五入不影响排序意义。
function toMillis(time: number | undefined): number {
  return Math.round(time ?? 0);
}

function requireOrderedRange(startTime: number | undefined, endTime: number | undefined): void {
  if (startTime !== undefined && endTime !== undefined && startTime > endTime) {
    throw new HandlerError("INVALID_REQUEST", "startTime must not be after endTime");
  }
}

// chrome 对格式不对的 URL 只笼统地拒绝；多个 URL 的删除先全部校验，任何一个不合法都不删除。
function requireUrl(url: string): void {
  try {
    new URL(url);
  } catch {
    throw new HandlerError("INVALID_REQUEST", `${url} is not a valid URL`);
  }
}

const handleSearch: RpcHandler<"history.search"> = async (params) => {
  const limit = listLimit(params.limit);
  requireOrderedRange(params.startTime, params.endTime);
  // 不给时间范围时搜索全部历史：chrome 默认只看最近 24 小时，所以 startTime 必须显式为 0。
  const query: chrome.history.HistoryQuery = {
    text: params.text ?? "",
    startTime: params.startTime ?? 0,
    // 多取一条，用来判断是否还有未返回的条目。
    maxResults: limit + 1,
  };
  if (params.endTime !== undefined) {
    query.endTime = params.endTime;
  }
  const found = await chrome.history.search(query);
  // chrome 不保证返回顺序；按最后访问时间从新到旧排，limit 截取的才是确定的一页。
  found.sort((a, b) => toMillis(b.lastVisitTime) - toMillis(a.lastVisitTime));
  const page = takePage(found, limit);
  return {
    // 标题和 URL 由网页控制，原样返回，不做任何改写。
    contentTrust: "untrusted-page-content",
    hasMore: page.hasMore,
    items: page.items.map((entry) => ({
      url: entry.url!,
      title: entry.title ?? "",
      lastVisitTime: toMillis(entry.lastVisitTime),
      visitCount: entry.visitCount ?? 0,
    })),
  };
};

const handleVisits: RpcHandler<"history.visits"> = async (params) => {
  const limit = listLimit(params.limit);
  requireUrl(params.url);
  const visits = await chrome.history.getVisits({ url: params.url });
  visits.sort((a, b) => toMillis(b.visitTime) - toMillis(a.visitTime));
  const page = takePage(visits, limit);
  return {
    hasMore: page.hasMore,
    visits: page.items.map((visit) => ({ visitTime: toMillis(visit.visitTime), transition: visit.transition })),
  };
};

const handleRemove: RpcHandler<"history.remove"> = async (params) => {
  const urls = [...new Set(params.urls)];
  urls.forEach(requireUrl);
  for (const url of urls) {
    await chrome.history.deleteUrl({ url });
  }
  return { urls };
};

const handleClear: RpcHandler<"history.clear"> = async (params) => {
  requireOrderedRange(params.startTime, params.endTime);
  if (params.startTime === undefined && params.endTime === undefined) {
    await chrome.history.deleteAll();
    return { all: true };
  }
  await chrome.history.deleteRange({ startTime: params.startTime ?? 0, endTime: params.endTime ?? Date.now() });
  return { all: false };
};

export function registerHistoryHandlers(registry: HandlerRegistry): void {
  registry.register("history.search", needs("history", handleSearch));
  registry.register("history.visits", needs("history", handleVisits));
  registry.register("history.remove", needs("history", handleRemove));
  registry.register("history.clear", needs("history", handleClear));
}
