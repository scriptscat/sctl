import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { needs } from "./api";
import { takePage } from "./list";

// Chrome 最多保留 25 条最近关闭的项（chrome.sessions.MAX_SESSION_RESULTS）。
const MAX_RECENT = 25;

// chrome.sessions 的 lastModified 以秒计，协议里的时间一律是毫秒。
function closedTime(session: chrome.sessions.Session): number {
  return session.lastModified * 1000;
}

const handleList: RpcHandler<"recent.list"> = async (params) => {
  const limit = params.limit ?? MAX_RECENT;
  // 生成的校验器不检查 maximum（见 listLimit），上限只能在这里兑现。
  if (limit > MAX_RECENT) {
    throw new HandlerError("INVALID_REQUEST", `limit must be at most ${MAX_RECENT}`);
  }

  // 取 Chrome 保留的全部（最多 25 条），才知道 limit 之外是否还有。
  const page = takePage(await chrome.sessions.getRecentlyClosed({ maxResults: MAX_RECENT }), limit);

  const items = page.items.map((session) => {
    if (session.tab) {
      return {
        sessionId: session.tab.sessionId!,
        type: "tab" as const,
        closedTime: closedTime(session),
        title: session.tab.title ?? "",
        url: session.tab.url ?? "",
      };
    } else if (session.window) {
      return {
        sessionId: session.window.sessionId!,
        type: "window" as const,
        closedTime: closedTime(session),
        title: session.window.tabs?.[0]?.title ?? "",
        url: session.window.tabs?.[0]?.url ?? "",
        tabCount: session.window.tabs?.length ?? 0,
      };
    }
    throw new Error("Invalid session: no tab or window");
  });

  return {
    items,
    hasMore: page.hasMore,
    contentTrust: "untrusted-page-content" as const,
  };
};

const handleRestore: RpcHandler<"recent.restore"> = async (params) => {
  let restored: chrome.sessions.Session | undefined;
  // 空串是调用方给出的会话 ID，不是「不给」：按不存在的 ID 交给 chrome，不能退回成恢复最近一项。
  // chrome 对不存在的会话 ID（以及没有可恢复的项）只会 reject；只翻译这一步，后面的不变量失败仍是内部错误。
  try {
    restored =
      params.sessionId === undefined
        ? await chrome.sessions.restore()
        : await chrome.sessions.restore(params.sessionId);
  } catch (error) {
    throw new HandlerError("NOT_FOUND", error instanceof Error ? error.message : String(error));
  }
  if (!restored) {
    throw new HandlerError("NOT_FOUND", "Session not found");
  }
  if (restored.tab) {
    return { tabId: restored.tab.id! };
  }
  if (restored.window) {
    return { windowId: restored.window.id! };
  }
  throw new Error("Invalid restored session: no tab or window");
};

export function registerRecentlyClosedHandlers(registry: HandlerRegistry): void {
  registry.register("recent.list", needs("sessions", handleList));
  registry.register("recent.restore", needs("sessions", handleRestore));
}
