import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";

const handleList: RpcHandler<"recent.list"> = async (params) => {
  const limit = params.limit ?? 25;
  if (limit < 1 || limit > 25) {
    throw new HandlerError("INVALID_REQUEST", "limit must be between 1 and 25");
  }

  const sessions = await chrome.sessions.getRecentlyClosed({ maxResults: limit });

  const items = sessions.slice(0, limit).map((session) => {
    if (session.tab) {
      return {
        sessionId: session.tab.sessionId!,
        type: "tab" as const,
        closedTime: session.lastModified,
        title: session.tab.title ?? "",
        url: session.tab.url ?? "",
      };
    } else if (session.window) {
      return {
        sessionId: session.window.sessionId!,
        type: "window" as const,
        closedTime: session.lastModified,
        title: session.window.tabs?.[0]?.title ?? "",
        url: session.window.tabs?.[0]?.url ?? "",
        tabCount: session.window.tabs?.length ?? 0,
      };
    }
    throw new Error("Invalid session: no tab or window");
  });

  return {
    items,
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
  registry.register("recent.list", handleList);
  registry.register("recent.restore", handleRestore);
}
