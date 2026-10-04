import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { registerBookmarkHandlers } from "./bookmarks";
import { registerBrowsingDataHandlers } from "./browsingData";
import { registerCookiesHandlers } from "./cookies";
import type { DebuggerRelay } from "./debugger";
import { registerDownloadsHandlers } from "./downloads";
import { registerExtensionsHandlers } from "./extensions";
import { registerHistoryHandlers } from "./history";
import { registerReadingListHandlers } from "./readingList";
import { registerRecentlyClosedHandlers } from "./recentlyClosed";
import { requireTab, requireWindow } from "./targets";
import { registerTabGroupHandlers } from "./tabGroups";
import { registerTabsManageHandlers } from "./tabsManage";

const handleTabsList: RpcHandler<"tabs.list"> = async (params) => {
  if (params.windowId !== undefined) {
    await requireWindow(params.windowId);
  }
  const tabs = await chrome.tabs.query(params.windowId === undefined ? {} : { windowId: params.windowId });
  return {
    // 标题和 URL 由网页控制，原样返回，不做任何改写。
    contentTrust: "untrusted-page-content",
    tabs: tabs.map((tab) => ({
      // 持有 tabs 权限时，chrome 保证真实标签页的 id/title/url 都已填充。
      tabId: tab.id!,
      windowId: tab.windowId,
      active: tab.active,
      pinned: tab.pinned,
      groupId: tab.groupId,
      title: tab.title!,
      url: tab.url!,
    })),
  };
};

const handleTabsOpen: RpcHandler<"tabs.open"> = async (params) => {
  let windowId: number;
  if (params.windowId !== undefined) {
    await requireWindow(params.windowId);
    windowId = params.windowId;
  } else {
    // 未指定窗口时，默认打开在最近使用（最后获得焦点）的窗口里。
    const lastFocused = await chrome.windows.getLastFocused();
    windowId = lastFocused.id!;
  }
  const tab = await chrome.tabs.create({ url: params.url, windowId, active: params.background !== true });
  return { tabId: tab.id! };
};

const handleTabsClose: RpcHandler<"tabs.close"> = async (params) => {
  // 逐个确认存在，任何一个不存在都不关闭任何标签页，避免部分成功难以解释。
  for (const tabId of params.tabIds) {
    await requireTab(tabId);
  }
  await chrome.tabs.remove(params.tabIds);
  return { tabIds: params.tabIds };
};

const handleTabsActivate: RpcHandler<"tabs.activate"> = async (params) => {
  const tab = await requireTab(params.tabId);
  await chrome.tabs.update(params.tabId, { active: true });
  await chrome.windows.update(tab.windowId, { focused: true });
  return { tabId: params.tabId, windowId: tab.windowId };
};

// 页面命令未指定标签页时的默认目标。用户多半是在终端里驱动 sctl，此时没有任何浏览器窗口处于聚焦状态，
// 所以取最后获得焦点的普通窗口，而不是当前聚焦的窗口。
const handleTabsCurrent: RpcHandler<"tabs.current"> = async () => {
  let win: chrome.windows.Window;
  try {
    win = await chrome.windows.getLastFocused({ populate: true, windowTypes: ["normal"] });
  } catch {
    throw new HandlerError("NOT_FOUND", "no normal browser window is open");
  }
  const active = win.tabs?.find((tab) => tab.active);
  if (active === undefined) {
    throw new HandlerError("NOT_FOUND", `window ${win.id} has no active tab`);
  }
  return { tabId: active.id!, windowId: win.id! };
};

// 只切换窗口内的激活标签页，不调用 windows.update：页面命令从不抢窗口焦点（spec 设计决策 4）。
const handleTabsSelect: RpcHandler<"tabs.select"> = async (params) => {
  const tab = await requireTab(params.tabId);
  await chrome.tabs.update(params.tabId, { active: true });
  return { tabId: params.tabId, windowId: tab.windowId };
};

const handleWindowsList: RpcHandler<"windows.list"> = async () => {
  const windows = await chrome.windows.getAll({ populate: true });
  return {
    windows: windows.map((win) => ({
      windowId: win.id!,
      focused: win.focused,
      state: win.state!,
      tabCount: win.tabs!.length,
    })),
  };
};

// 全部浏览器方法的处理函数在这里注册；注册了哪些方法，扩展就向 daemon 声明哪些能力。
export function registerHandlers(registry: HandlerRegistry, relay: DebuggerRelay): void {
  registry.register("tabs.list", handleTabsList);
  registry.register("tabs.open", handleTabsOpen);
  registry.register("tabs.close", handleTabsClose);
  registry.register("tabs.activate", handleTabsActivate);
  registry.register("tabs.current", handleTabsCurrent);
  registry.register("tabs.select", handleTabsSelect);
  registry.register("windows.list", handleWindowsList);
  registerReadingListHandlers(registry);
  registerBookmarkHandlers(registry);
  registerTabsManageHandlers(registry);
  registerTabGroupHandlers(registry);
  registerHistoryHandlers(registry);
  registerBrowsingDataHandlers(registry);
  registerRecentlyClosedHandlers(registry);
  registerDownloadsHandlers(registry);
  registerCookiesHandlers(registry);
  registerExtensionsHandlers(registry);
  registry.register("debugger.send", relay.send);
  registry.register("debugger.detach", relay.detach);
  registry.register("debugger.record", relay.record);
}
