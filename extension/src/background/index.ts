import { registerHandlers } from "@/handlers";
import { uninstallObserved } from "@/handlers/extensions";
import { APPROVAL_PAGE, type ApprovalBroadcast } from "@/shared/approvals";
import type { BackgroundMessage, OffscreenCommand } from "@/shared/messages";
import { broadcast, listen, request } from "@/shared/messaging";
import { Approvals } from "./approvals";
import { readBrowserInfo } from "./browser-info";
import { Background } from "./controller";
import { createOffscreenKeeper } from "./offscreen-document";
import { HandlerRegistry } from "./registry";

const registry = new HandlerRegistry();
registerHandlers(registry);

const ensureOffscreen = createOffscreenKeeper({
  getContexts: (filter) => chrome.runtime.getContexts(filter),
  createDocument: (parameters) => chrome.offscreen.createDocument(parameters),
});

const toOffscreen = async (command: OffscreenCommand) => {
  await ensureOffscreen();
  return request(chrome.runtime, command);
};

const approvals = new Approvals({
  storage: chrome.storage.session,
  timers: {
    setTimeout: (fn, ms) => self.setTimeout(fn, ms),
    clearTimeout: (id) => self.clearTimeout(id),
    now: () => Date.now(),
  },
  windows: {
    create: (options) => chrome.windows.create(options),
    update: (windowId, info) => chrome.windows.update(windowId, info),
    remove: (windowId) => chrome.windows.remove(windowId),
  },
  badge: chrome.action,
  pageUrl: chrome.runtime.getURL(APPROVAL_PAGE),
  browserName: async () => {
    const { name } = await chrome.storage.local.get(["name"]);
    return typeof name === "string" ? name : "";
  },
  settle: async (requestId, outcome) => {
    await toOffscreen({ target: "offscreen", type: "settle", requestId, outcome });
  },
  execute: (approval) => registry.execute(approval),
  executesInWindow: (kind) => registry.executesInWindow(kind),
  broadcast: (view) =>
    broadcast(chrome.runtime, { target: "approval", type: "approvals", view } satisfies ApprovalBroadcast),
});

const background = new Background({
  storage: chrome.storage.local,
  offscreen: toOffscreen,
  registry,
  approvals,
  browser: readBrowserInfo(),
  extensionVersion: chrome.runtime.getManifest().version,
});

// 监听器必须在顶层同步注册，service worker 被消息唤醒时才能收到这条消息。
listen<BackgroundMessage>(chrome.runtime, "background", (message) => background.handle(message));
// 用户直接关掉审批窗口时，排队中的请求全部视为拒绝。
chrome.windows.onRemoved.addListener((windowId) => {
  approvals
    .windowRemoved(windowId)
    .catch((error: unknown) => console.error("failed to handle the closed approval window", error));
});

// 已批准的卸载在 Chrome 确认框里被确认：审批窗口在确认框打开期间被关掉时，结论只能从这里得知。
chrome.management.onUninstalled.addListener((id) => {
  approvals
    .observed(uninstallObserved(id))
    .catch((error: unknown) => console.error("failed to conclude an observed uninstall", error));
});

// 连接住在 offscreen 文档里；浏览器启动、扩展安装或 service worker 被唤醒时都确保它存在。
function keepConnection(): void {
  ensureOffscreen().catch((error: unknown) => console.error("failed to create the offscreen document", error));
}

chrome.runtime.onStartup.addListener(keepConnection);
chrome.runtime.onInstalled.addListener(keepConnection);
keepConnection();
// service worker 被唤醒时恢复待审批状态：重新计时，并把休眠期间已过期限的请求判为超时。
approvals.restore().catch((error: unknown) => console.error("failed to restore pending approvals", error));
