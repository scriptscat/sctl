import type { StorageLike } from "@/background/controller";
import { HandlerError, type RpcHandler } from "@/background/registry";
import type { NotificationMethod, NotificationParams, RpcResult } from "@/protocol/generated/protocol.generated";
import { requireTab } from "./targets";

// 守护进程持有 5 分钟的权威超时；这里只是扩展侧的兜底，防止守护进程失联后调试器提示条一直挂着。
export const IDLE_DETACH_MS = 10 * 60_000;
const IDLE_REASON = "idle_timeout";

// 请求体与响应体回传前截断到的字节数（文本按 UTF-8 计）。截断必须在这里做：Chrome 只能一次取回整个体，
// 超过一个 4 MiB 协议帧的体经 debugger.send 原样回传时一个字节也拿不到。
const BODY_LIMIT_BYTES = 1024 * 1024;

type BodyResult = RpcResult<"debugger.body">;
type Unavailable = NonNullable<BodyResult["unavailable"]>;

// Chrome 不再保留一个体时 getResponseBody / getRequestPostData 的报错原文（真机探针，Chrome 125 与 153）。
const UNAVAILABLE: [RegExp, Unavailable][] = [
  // 页面导航之后，之前所有请求的资源记录都被丢弃。
  [/No resource with given id/, "navigated"],
  // 请求进行中、失败、没有响应体，或页面没有读取 fetch 的响应体。
  [/No data found for resource with given identifier/, "noData"],
  // 超出 Chrome 的缓冲（单个资源约 20 MB），或被后来的响应挤出。
  [/evicted from inspector cache/, "evicted"],
  [/No post data available/, "noPostData"],
];

export type NotifyFn = <N extends NotificationMethod>(method: N, params: NotificationParams<N>) => void;

// 已附加标签页的持久记录所在的存储，生产环境是 chrome.storage.session：它活过 service worker 重启、浏览器重启时
// 清空，与调试器附加的生命周期一致。service worker 会被回收重启，而 Chrome 里的调试器附加不随之消失；只记在
// 内存里的话，重启后再附加会被 Chrome 以「已有调试器附加」拒绝，事件被丢弃，也没人再断开它。
export type AttachedTabsStorage = Pick<StorageLike, "get" | "set">;

const ATTACHED_TABS_KEY = "debuggerTabs";
// 守护进程正在录制的标签页，同样要活过 service worker 重启，否则重启后兜底计时会把录制中的标签页断开。
const RECORDING_TABS_KEY = "debuggerRecording";
// 有未处理 JS 弹框的标签页，同样要活过 service worker 重启：释放前要先关闭这些弹框。
const DIALOG_TABS_KEY = "debuggerDialogs";
// 原始 CDP 端点占用的标签页：与录制一样免除兜底空闲断开，并活过 service worker 重启。
const OWNED_TABS_KEY = "debuggerOwned";

// 不能附加调试器的页面：浏览器内部页、扩展页与应用商店（Chrome 拒绝附加，列出来只会让端点的客户端附加失败）。
const UNATTACHABLE_SCHEMES = ["chrome:", "chrome-extension:", "chrome-untrusted:", "chrome-search:", "devtools:"];
const WEB_STORE_PREFIXES = ["https://chromewebstore.google.com/", "https://chrome.google.com/webstore"];

function isAttachableUrl(url: string): boolean {
  return (
    !UNATTACHABLE_SCHEMES.some((scheme) => url.startsWith(scheme)) && !WEB_STORE_PREFIXES.some((p) => url.startsWith(p))
  );
}

type TabInfo = RpcResult<"debugger.targets">["targets"][number];

// 按标签页维护 chrome.debugger 附加状态：发送命令时按需附加，把事件和被动分离转成通知，并在空闲、连接断开时释放。
export class DebuggerRelay {
  // 已附加（或正在附加）的标签页 → 空闲计时器；录制中的标签页没有计时器（undefined），不会被兜底断开。
  private readonly idle = new Map<number, ReturnType<typeof setTimeout> | undefined>();
  private readonly recording = new Set<number>();
  // 端点占用的标签页，可以还没附加（端点打开的新标签页）。
  private readonly owned = new Set<number>();
  // 这次附加期间打开、还没关闭的 JS 弹框所在的标签页。
  private readonly dialogs = new Set<number>();
  private readonly attaching = new Map<number, Promise<void>>();
  // 恢复重启前的附加记录；恢复完成前到达的事件排在它之后处理，保持原有顺序。
  private readonly restored: Promise<void>;
  private ready = false;

  constructor(
    private readonly notify: NotifyFn,
    private readonly storage: AttachedTabsStorage,
  ) {
    // 恢复失败只损失重启前的记录（退回没有持久化时的行为），不能让之后的每条命令都跟着失败。
    this.restored = this.restore()
      .catch((error: unknown) => {
        console.error("failed to restore the attached tabs; starting without them", error);
      })
      .finally(() => {
        this.ready = true;
      });
  }

  readonly send: RpcHandler<"debugger.send"> = async (params) => {
    await this.restored;
    await this.ensureAttached(params.tabId);
    this.armIdle(params.tabId);
    const target =
      params.sessionId === undefined ? { tabId: params.tabId } : { tabId: params.tabId, sessionId: params.sessionId };
    try {
      const result = await chrome.debugger.sendCommand(target, params.method, params.params);
      return { result: (result ?? {}) as Record<string, unknown> };
    } catch (error) {
      if (!this.idle.has(params.tabId)) {
        // 命令执行期间 onDetach 已经把标签页移出，说明是分离打断了命令，而不是命令本身出错。
        throw new HandlerError("DEBUGGER_DETACHED", "the debugger detached while the command was running");
      }
      // CDP 命令错误（未知方法、参数不合法、上下文已失效）是调用方给出的请求有问题，带着 CDP 的原话返回。
      throw new HandlerError("INVALID_REQUEST", errorMessage(error));
    }
  };

  readonly detach: RpcHandler<"debugger.detach"> = async (params) => {
    await this.restored;
    // 附加进行中的标签页等附加结束再决定：daemon 放弃一次附加（命令已超时）后发来的断开若落空，
    // 随后完成的附加就只剩扩展侧的兜底计时。附加失败由发起它的 send 报告，这里只等它结束。
    const pending = params.tabId === undefined ? [...this.attaching.values()] : [this.attaching.get(params.tabId)];
    await Promise.allSettled(pending.filter((p) => p !== undefined));
    const tabIds =
      params.tabId === undefined ? [...this.idle.keys()] : this.idle.has(params.tabId) ? [params.tabId] : [];
    await Promise.all(tabIds.map((tabId) => this.release(tabId)));
    return { tabIds };
  };

  // 守护进程开始或停止录制一个标签页：录制期间免除兜底空闲断开，停止后重新计时。
  // 停止一个没附加的标签页是空操作；开始录制要求标签页已附加（守护进程总是先附加），否则按分离处理。
  readonly record: RpcHandler<"debugger.record"> = async (params) => {
    await this.restored;
    // 与 detach 一样等附加结束再判断，避免把刚附加完的标签页当成没附加。
    await Promise.allSettled([this.attaching.get(params.tabId)].filter((p) => p !== undefined));
    if (!this.idle.has(params.tabId)) {
      if (params.recording) {
        throw new HandlerError("DEBUGGER_DETACHED", `the debugger is not attached to tab ${params.tabId}`);
      }
      return { recording: false };
    }
    if (params.recording) {
      this.recording.add(params.tabId);
    } else {
      this.recording.delete(params.tabId);
    }
    this.armIdle(params.tabId);
    this.persist();
    return { recording: params.recording };
  };

  // 列出能附加调试器的标签页及其 DevTools 目标 ID：端点用它回答浏览器级的目标发现。
  readonly targets: RpcHandler<"debugger.targets"> = async () => {
    const targets = (await chrome.debugger.getTargets()).flatMap(toTabInfo);
    return { targets };
  };

  // 真实的 User-Agent：端点的 Browser.getVersion 要回答它，客户端据此判断浏览器版本。
  readonly userAgent: RpcHandler<"debugger.userAgent"> = () => Promise.resolve({ userAgent: navigator.userAgent });

  // 为端点打开标签页并立即标为端点占用：端点随后才附加，中间不会被任何兜底计时误伤。
  readonly open: RpcHandler<"debugger.open"> = async (params) => {
    const lastFocused = await chrome.windows.getLastFocused();
    const tab = await chrome.tabs.create({
      url: params.url,
      windowId: lastFocused.id!,
      active: params.background !== true,
    });
    const tabId = tab.id!;
    this.owned.add(tabId);
    this.persist();
    const target = (await chrome.debugger.getTargets()).find((t) => t.tabId === tabId);
    if (target === undefined) {
      throw new HandlerError("NOT_FOUND", `no debuggable target for tab ${tabId}`);
    }
    return { tabId, targetId: target.id };
  };

  // 为端点关闭标签页；关闭时 Chrome 会分离调试器并走 onDetach，占用记录在那里与标签页一起清掉。
  readonly close: RpcHandler<"debugger.close"> = async (params) => {
    await requireTab(params.tabId);
    await chrome.tabs.remove(params.tabId);
    this.owned.delete(params.tabId);
    this.persist();
    return { tabId: params.tabId };
  };

  // 标记或取消标记端点占用的标签页：占用期间免除兜底空闲断开，取消后重新计时。可作用于未附加的标签页。
  readonly own: RpcHandler<"debugger.own"> = async (params) => {
    await this.restored;
    if (params.owned) {
      this.owned.add(params.tabId);
    } else {
      this.owned.delete(params.tabId);
    }
    if (this.idle.has(params.tabId)) {
      this.armIdle(params.tabId);
    }
    this.persist();
    return { owned: params.owned };
  };

  // 取回一个请求的请求体或响应体，截断到 BODY_LIMIT_BYTES 后回传，并给出原始大小。Chrome 不再保留它时
  // 不算错误，答 unavailable 与原因。和 record 一样不附加：daemon 只为已附加时记下的请求取体。
  readonly body: RpcHandler<"debugger.body"> = async (params) => {
    await this.restored;
    await Promise.allSettled([this.attaching.get(params.tabId)].filter((p) => p !== undefined));
    if (!this.idle.has(params.tabId)) {
      throw new HandlerError("DEBUGGER_DETACHED", `the debugger is not attached to tab ${params.tabId}`);
    }
    this.armIdle(params.tabId);
    const target =
      params.sessionId === undefined ? { tabId: params.tabId } : { tabId: params.tabId, sessionId: params.sessionId };
    const method = params.part === "request" ? "Network.getRequestPostData" : "Network.getResponseBody";
    let raw: { body?: string; postData?: string; base64Encoded?: boolean };
    try {
      raw = (await chrome.debugger.sendCommand(target, method, { requestId: params.requestId })) as typeof raw;
    } catch (error) {
      if (!this.idle.has(params.tabId)) {
        throw new HandlerError("DEBUGGER_DETACHED", "the debugger detached while the body was read");
      }
      const message = errorMessage(error);
      const unavailable = UNAVAILABLE.find(([pattern]) => pattern.test(message));
      if (unavailable) {
        return { unavailable: unavailable[1] };
      }
      throw new HandlerError("INVALID_REQUEST", message);
    }
    return truncateBody((params.part === "request" ? raw.postData : raw.body) ?? "", raw.base64Encoded === true);
  };

  // 连接断开后没有人能再驱动这些标签页，全部释放；此时也无法通知守护进程。
  async detachAll(): Promise<void> {
    await this.restored;
    await Promise.allSettled([...this.attaching.values()]);
    await Promise.all([...this.idle.keys()].map((tabId) => this.release(tabId)));
  }

  onEvent(source: chrome.debugger.DebuggerSession, method: string, params?: object): void {
    if (!this.ready) {
      void this.restored.then(() => this.onEvent(source, method, params));
      return;
    }
    if (source.tabId === undefined || !this.idle.has(source.tabId)) {
      return;
    }
    this.trackDialog(source.tabId, method);
    this.notify("debugger.event", {
      tabId: source.tabId,
      method,
      ...(source.sessionId === undefined ? {} : { sessionId: source.sessionId }),
      ...(params === undefined ? {} : { params: params as Record<string, unknown> }),
    });
  }

  // 用户关掉提示条、标签页关闭或导航到不可调试页面时 Chrome 主动分离。
  onDetach(source: chrome.debugger.Debuggee, reason: string): void {
    if (!this.ready) {
      void this.restored.then(() => this.onDetach(source, reason));
      return;
    }
    if (source.tabId === undefined || !this.idle.has(source.tabId)) {
      return;
    }
    this.forget(source.tabId);
    this.notify("debugger.detached", { tabId: source.tabId, reason });
  }

  // 标签页的创建、变化与关闭报告给守护进程，由它转成端点客户端的目标事件。新建时目标可能还没出现在
  // getTargets 里，此时不报告，等随后的变化通知补上。
  onTabCreated(tab: chrome.tabs.Tab): void {
    void this.reportTab("debugger.tabCreated", tab.id!, tab.url || tab.pendingUrl || "");
  }

  onTabUpdated(tabId: number, changeInfo: chrome.tabs.OnUpdatedInfo): void {
    if (changeInfo.url === undefined && changeInfo.title === undefined && changeInfo.status === undefined) {
      return;
    }
    void this.reportTab("debugger.tabUpdated", tabId, changeInfo.url);
  }

  onTabRemoved(tabId: number): void {
    if (this.owned.delete(tabId)) {
      this.persist();
    }
    this.notify("debugger.tabRemoved", { tabId });
  }

  private async reportTab(method: "debugger.tabCreated" | "debugger.tabUpdated", tabId: number, hint?: string) {
    if (hint !== undefined && !isAttachableUrl(hint)) {
      return;
    }
    try {
      const info = (await chrome.debugger.getTargets()).flatMap(toTabInfo).find((t) => t.tabId === tabId);
      if (info !== undefined) {
        this.notify(method, info);
      }
    } catch (error) {
      console.error(`failed to report ${method} for tab ${tabId}`, error);
    }
  }

  private ensureAttached(tabId: number): Promise<void> {
    if (this.idle.has(tabId)) {
      return Promise.resolve();
    }
    let pending = this.attaching.get(tabId);
    if (!pending) {
      pending = this.attach(tabId).finally(() => this.attaching.delete(tabId));
      this.attaching.set(tabId, pending);
    }
    return pending;
  }

  private async attach(tabId: number): Promise<void> {
    await requireTab(tabId);
    try {
      await chrome.debugger.attach({ tabId }, "1.3");
    } catch (error) {
      throw new HandlerError("PAGE_NOT_AUTOMATABLE", errorMessage(error));
    }
    this.armIdle(tabId);
    this.persist();
  }

  // 重启前附加的标签页仍附加着就接着管理（重新开始兜底计时）；已经不在的告诉 daemon，它可能还当它附加着。
  private async restore(): Promise<void> {
    const items = await this.storage.get([ATTACHED_TABS_KEY, RECORDING_TABS_KEY, DIALOG_TABS_KEY, OWNED_TABS_KEY]);
    const stored = items[ATTACHED_TABS_KEY];
    const saved = Array.isArray(stored) ? (stored as number[]) : [];
    const storedRecording = items[RECORDING_TABS_KEY];
    const wasRecording = new Set(Array.isArray(storedRecording) ? (storedRecording as number[]) : []);
    const storedDialogs = items[DIALOG_TABS_KEY];
    const hadDialog = new Set(Array.isArray(storedDialogs) ? (storedDialogs as number[]) : []);
    const storedOwned = items[OWNED_TABS_KEY];
    const wasOwned = Array.isArray(storedOwned) ? (storedOwned as number[]) : [];
    if (saved.length === 0 && wasOwned.length === 0) {
      return;
    }
    const targets = await chrome.debugger.getTargets();
    // 重启期间被关掉的标签页不再占用，否则记录只增不减。
    const existing = new Set(targets.map((t) => t.tabId));
    for (const tabId of wasOwned) {
      if (existing.has(tabId)) {
        this.owned.add(tabId);
      }
    }
    if (this.owned.size !== wasOwned.length) {
      this.persist();
    }
    const attached = new Set(targets.filter((t) => t.attached).map((t) => t.tabId));
    const gone: number[] = [];
    for (const tabId of saved) {
      if (attached.has(tabId)) {
        if (wasRecording.has(tabId)) {
          this.recording.add(tabId);
        }
        if (hadDialog.has(tabId)) {
          this.dialogs.add(tabId);
        }
        this.armIdle(tabId);
      } else {
        gone.push(tabId);
      }
    }
    if (gone.length > 0) {
      this.persist();
      for (const tabId of gone) {
        this.notify("debugger.detached", { tabId, reason: "target_closed" });
      }
    }
  }

  private persist(): void {
    this.storage
      .set({
        [ATTACHED_TABS_KEY]: [...this.idle.keys()],
        [RECORDING_TABS_KEY]: [...this.recording],
        [DIALOG_TABS_KEY]: [...this.dialogs],
        [OWNED_TABS_KEY]: [...this.owned],
      })
      .catch((error: unknown) => {
        console.error("failed to save the attached tabs", error);
      });
  }

  private armIdle(tabId: number): void {
    clearTimeout(this.idle.get(tabId));
    if (this.recording.has(tabId) || this.owned.has(tabId)) {
      this.idle.set(tabId, undefined);
      return;
    }
    this.idle.set(
      tabId,
      setTimeout(() => {
        void this.release(tabId).then(() => this.notify("debugger.detached", { tabId, reason: IDLE_REASON }));
      }, IDLE_DETACH_MS),
    );
  }

  // 弹框事件只在附加期间送达，弹框所在的子会话里打开时事件也在顶层会话上（真机探针）。
  private trackDialog(tabId: number, method: string): void {
    if (method === "Page.javascriptDialogOpening") {
      this.dialogs.add(tabId);
    } else if (method === "Page.javascriptDialogClosed") {
      this.dialogs.delete(tabId);
    } else {
      return;
    }
    this.persist();
  }

  private forget(tabId: number): void {
    clearTimeout(this.idle.get(tabId));
    this.idle.delete(tabId);
    this.recording.delete(tabId);
    this.owned.delete(tabId);
    this.dialogs.delete(tabId);
    this.persist();
  }

  // 释放前先关闭（dismiss）已知的弹框：断开之后它成了孤儿，之后任何调试会话都处理不了它、附加用的命令全部阻塞，
  // 只有导航能关掉（真机探针）。只有处理弹框的命令在弹框打开期间仍有回应，并且只能发往顶层会话。
  private async release(tabId: number): Promise<void> {
    const hadDialog = this.dialogs.has(tabId);
    this.forget(tabId);
    if (hadDialog) {
      try {
        await chrome.debugger.sendCommand({ tabId }, "Page.handleJavaScriptDialog", { accept: false });
      } catch (error) {
        console.warn(`dismissing the JS dialog on tab ${tabId} before detaching failed`, error);
      }
    }
    try {
      await chrome.debugger.detach({ tabId });
    } catch (error) {
      // 标签页可能恰好已被关闭或被用户分离；目标状态（未附加）已经达成，不该让释放流程失败。
      console.warn(`detaching the debugger from tab ${tabId} failed`, error);
    }
  }
}

// truncateBody 把体截到 BODY_LIMIT_BYTES：base64 按解码后的字节截，再重新编码；文本按 UTF-8 字节截，
// 不切开一个字符。size 是截断前的字节数。
function truncateBody(body: string, base64Encoded: boolean): BodyResult {
  if (base64Encoded) {
    const padding = body.endsWith("==") ? 2 : body.endsWith("=") ? 1 : 0;
    const size = (body.length / 4) * 3 - padding;
    if (size <= BODY_LIMIT_BYTES) {
      return { body, base64Encoded, size, truncated: false };
    }
    // 只解码够用的前缀：完整的体可能有 20 MB。
    const prefix = atob(body.slice(0, Math.ceil(BODY_LIMIT_BYTES / 3) * 4)).slice(0, BODY_LIMIT_BYTES);
    return { body: btoa(prefix), base64Encoded, size, truncated: true };
  }
  const encoder = new TextEncoder();
  const size = encoder.encode(body).length;
  if (size <= BODY_LIMIT_BYTES) {
    return { body, base64Encoded, size, truncated: false };
  }
  // encodeInto 只写入完整的字符，read 是写进去的 UTF-16 码元数。
  const { read } = encoder.encodeInto(body, new Uint8Array(BODY_LIMIT_BYTES));
  return { body: body.slice(0, read), base64Encoded, size, truncated: true };
}

// 只有普通页面目标能被端点驱动：扩展的后台页、service worker 等没有标签页或不是 page 类型。
function toTabInfo(target: chrome.debugger.TargetInfo): TabInfo[] {
  if (target.type !== "page" || target.tabId === undefined || !isAttachableUrl(target.url)) {
    return [];
  }
  return [{ tabId: target.tabId, targetId: target.id, title: target.title, url: target.url }];
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
