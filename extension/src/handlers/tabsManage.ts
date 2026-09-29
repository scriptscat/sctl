import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { requireAllTabs, requireTab, requireWindow } from "./targets";

async function requireAllWindows(windowIds: number[]): Promise<number[]> {
  const unique = [...new Set(windowIds)];
  for (const windowId of unique) {
    await requireWindow(windowId);
  }
  return unique;
}

const WINDOW_STATES = ["normal", "minimized", "maximized", "fullscreen"];

// 扩展侧不再校验协议 schema，取值范围在这里兑现；先于任何 chrome.* 调用，失败时什么都不改。
function requireState(state: string | undefined): void {
  if (state !== undefined && !WINDOW_STATES.includes(state)) {
    throw new HandlerError("INVALID_REQUEST", `invalid window state ${JSON.stringify(state)}`);
  }
}

const handleMove: RpcHandler<"tabs.move"> = async (params) => {
  if (params.index !== undefined && (!Number.isInteger(params.index) || params.index < -1)) {
    throw new HandlerError("INVALID_REQUEST", "index must be an integer of -1 or more");
  }
  const tabIds = await requireAllTabs(params.tabIds);
  if (params.windowId !== undefined) {
    await requireWindow(params.windowId);
  }
  await chrome.tabs.move(tabIds, {
    ...(params.windowId === undefined ? {} : { windowId: params.windowId }),
    // -1 表示末尾；未给位置时也放到末尾，与命令行的说明一致。
    index: params.index ?? -1,
  });
  return { tabIds };
};

// pin/unpin/mute/unmute 只是 chrome.tabs.update 的不同属性，共用一个实现；参数类型相同，任选其一作类型。
function updateTabs<M extends "tabs.pin" | "tabs.unpin" | "tabs.mute" | "tabs.unmute">(
  properties: chrome.tabs.UpdateProperties,
): RpcHandler<M> {
  return async (params) => {
    const tabIds = await requireAllTabs(params.tabIds);
    for (const tabId of tabIds) {
      await chrome.tabs.update(tabId, properties);
    }
    return { tabIds };
  };
}

const handleReload: RpcHandler<"tabs.reload"> = async (params) => {
  const tabIds = await requireAllTabs(params.tabIds);
  for (const tabId of tabIds) {
    await chrome.tabs.reload(tabId, { bypassCache: params.bypassCache === true });
  }
  return { tabIds };
};

const handleDuplicate: RpcHandler<"tabs.duplicate"> = async (params) => {
  await requireTab(params.tabId);
  const tab = await chrome.tabs.duplicate(params.tabId);
  // 持有 tabs 权限且标签页存在时，chrome 一定返回新标签页。
  return { tabId: tab!.id! };
};

const handleWindowsOpen: RpcHandler<"windows.open"> = async (params) => {
  requireState(params.state);
  const win = await chrome.windows.create({
    ...(params.urls === undefined || params.urls.length === 0 ? {} : { url: params.urls }),
    ...(params.state === undefined ? {} : { state: params.state }),
  });
  return { windowId: win!.id! };
};

const handleWindowsClose: RpcHandler<"windows.close"> = async (params) => {
  const windowIds = await requireAllWindows(params.windowIds);
  for (const windowId of windowIds) {
    await chrome.windows.remove(windowId);
  }
  return { windowIds };
};

const handleWindowsFocus: RpcHandler<"windows.focus"> = async (params) => {
  await requireWindow(params.windowId);
  await chrome.windows.update(params.windowId, { focused: true });
  return { windowId: params.windowId };
};

const handleWindowsState: RpcHandler<"windows.state"> = async (params) => {
  requireState(params.state);
  await requireWindow(params.windowId);
  await chrome.windows.update(params.windowId, { state: params.state });
  return { windowId: params.windowId, state: params.state };
};

export function registerTabsManageHandlers(registry: HandlerRegistry): void {
  registry.register("tabs.move", handleMove);
  registry.register("tabs.pin", updateTabs({ pinned: true }));
  registry.register("tabs.unpin", updateTabs({ pinned: false }));
  registry.register("tabs.mute", updateTabs({ muted: true }));
  registry.register("tabs.unmute", updateTabs({ muted: false }));
  registry.register("tabs.reload", handleReload);
  registry.register("tabs.duplicate", handleDuplicate);
  registry.register("windows.open", handleWindowsOpen);
  registry.register("windows.close", handleWindowsClose);
  registry.register("windows.focus", handleWindowsFocus);
  registry.register("windows.state", handleWindowsState);
}
