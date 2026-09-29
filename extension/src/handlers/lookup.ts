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
