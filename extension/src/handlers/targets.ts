import { HandlerError } from "@/background/registry";

// chrome.tabs.get/chrome.windows.get 对不存在的 ID 只会 reject，这里把它翻译成协议的 NOT_FOUND 领域错误。
export async function requireTab(tabId: number): Promise<chrome.tabs.Tab> {
  try {
    return await chrome.tabs.get(tabId);
  } catch {
    throw new HandlerError("NOT_FOUND", `no tab ${tabId}`);
  }
}

export async function requireWindow(windowId: number): Promise<chrome.windows.Window> {
  try {
    return await chrome.windows.get(windowId);
  } catch {
    throw new HandlerError("NOT_FOUND", `no window ${windowId}`);
  }
}

// 多个 ID 的操作先确认全部存在，任何一个不存在都不改动任何对象（全有或全无）；重复的 ID 只处理一次。
export async function requireAllTabs(tabIds: number[]): Promise<number[]> {
  const unique = [...new Set(tabIds)];
  for (const tabId of unique) {
    await requireTab(tabId);
  }
  return unique;
}

export async function requireTabGroup(groupId: number): Promise<chrome.tabGroups.TabGroup> {
  try {
    return await chrome.tabGroups.get(groupId);
  } catch {
    throw new HandlerError("NOT_FOUND", `no tab group ${groupId}`);
  }
}
