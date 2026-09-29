import { type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { registerBookmarkHandlers } from "./bookmarks";
import { registerBrowsingDataHandlers } from "./browsingData";
import { registerDownloadsHandlers } from "./downloads";
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
export function registerHandlers(registry: HandlerRegistry): void {
  registry.register("tabs.list", handleTabsList);
  registry.register("tabs.open", handleTabsOpen);
  registry.register("tabs.close", handleTabsClose);
  registry.register("tabs.activate", handleTabsActivate);
  registry.register("windows.list", handleWindowsList);
  registerReadingListHandlers(registry);
  registerBookmarkHandlers(registry);
  registerTabsManageHandlers(registry);
  registerTabGroupHandlers(registry);
  registerHistoryHandlers(registry);
  registerBrowsingDataHandlers(registry);
  registerRecentlyClosedHandlers(registry);
  registerDownloadsHandlers(registry);
}
