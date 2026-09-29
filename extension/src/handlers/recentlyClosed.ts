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
  if (limit < 1 || limit > MAX_RECENT) {
    throw new HandlerError("INVALID_REQUEST", `limit must be between 1 and ${MAX_RECENT}`);
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
  try {
    let restored: chrome.sessions.Session | undefined;
    if (params.sessionId) {
      restored = await chrome.sessions.restore(params.sessionId);
    } else {
      restored = await chrome.sessions.restore();
    }

    if (!restored) {
      throw new HandlerError("NOT_FOUND", "Session not found");
    }

    if (restored.tab) {
      return { tabId: restored.tab.id! };
    } else if (restored.window) {
      return { windowId: restored.window.id! };
    }

    throw new Error("Invalid restored session: no tab or window");
  } catch (err) {
    if (err instanceof HandlerError) {
      throw err;
    }
    throw new HandlerError("NOT_FOUND", "Session not found");
  }
};

export function registerRecentlyClosedHandlers(registry: HandlerRegistry): void {
  registry.register("recent.list", needs("sessions", handleList));
  registry.register("recent.restore", needs("sessions", handleRestore));
}
