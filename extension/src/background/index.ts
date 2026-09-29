import { DebuggerRelay } from "@/handlers/debugger";
import { registerHandlers } from "@/handlers";
import type { BackgroundMessage } from "@/shared/messages";
import { listen, request } from "@/shared/messaging";
import { readBrowserInfo } from "./browser-info";
import { Background } from "./controller";
import { createOffscreenKeeper } from "./offscreen-document";
import { HandlerRegistry } from "./registry";

// 通知在连接不可用时被 offscreen 丢弃，失败只记录；relay 先于 background 创建，通知总在之后异步发出。
const relay = new DebuggerRelay((method, params) => {
  background.notify(method, params).catch((error: unknown) => console.error(`failed to send ${method}`, error));
}, chrome.storage.session);
const registry = new HandlerRegistry();
registerHandlers(registry, relay);

const ensureOffscreen = createOffscreenKeeper({
  getContexts: (filter) => chrome.runtime.getContexts(filter),
  createDocument: (parameters) => chrome.offscreen.createDocument(parameters),
});

const background = new Background({
  storage: chrome.storage.local,
  offscreen: async (command) => {
    await ensureOffscreen();
    return request(chrome.runtime, command);
  },
  registry,
  onConnectionClosed: () => relay.detachAll(),
  browser: readBrowserInfo(),
  extensionVersion: chrome.runtime.getManifest().version,
});

// 监听器必须在顶层同步注册，service worker 被消息唤醒时才能收到这条消息。
listen<BackgroundMessage>(chrome.runtime, "background", (message) => background.handle(message));

// chrome.debugger 的监听器同样必须在顶层同步注册，事件才能唤醒 service worker。
chrome.debugger.onEvent.addListener((source, method, params) => relay.onEvent(source, method, params));
chrome.debugger.onDetach.addListener((source, reason) => relay.onDetach(source, reason));

// 连接住在 offscreen 文档里；浏览器启动、扩展安装或 service worker 被唤醒时都确保它存在。
function keepConnection(): void {
  ensureOffscreen().catch((error: unknown) => console.error("failed to create the offscreen document", error));
}

chrome.runtime.onStartup.addListener(keepConnection);
chrome.runtime.onInstalled.addListener(keepConnection);
keepConnection();
