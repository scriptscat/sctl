import type { StorageLike } from "@/background/controller";
import { HandlerError, type RpcHandler } from "@/background/registry";
import type { NotificationMethod, NotificationParams } from "@/protocol/generated/protocol.generated";
import { requireTab } from "./lookup";

// 守护进程持有 5 分钟的权威超时；这里只是扩展侧的兜底，防止守护进程失联后调试器提示条一直挂着。
export const IDLE_DETACH_MS = 10 * 60_000;
const IDLE_REASON = "idle_timeout";

export type NotifyFn = <N extends NotificationMethod>(method: N, params: NotificationParams<N>) => void;

// 已附加标签页的持久记录所在的存储，生产环境是 chrome.storage.session：它活过 service worker 重启、浏览器重启时
// 清空，与调试器附加的生命周期一致。service worker 会被回收重启，而 Chrome 里的调试器附加不随之消失；只记在
// 内存里的话，重启后再附加会被 Chrome 以「已有调试器附加」拒绝，事件被丢弃，也没人再断开它。
export type AttachedTabsStorage = Pick<StorageLike, "get" | "set">;

const ATTACHED_TABS_KEY = "debuggerTabs";

// 按标签页维护 chrome.debugger 附加状态：发送命令时按需附加，把事件和被动分离转成通知，并在空闲、连接断开时释放。
export class DebuggerRelay {
  // 已附加（或正在附加）的标签页 → 空闲计时器。
  private readonly idle = new Map<number, ReturnType<typeof setTimeout>>();
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
    const stored = (await this.storage.get([ATTACHED_TABS_KEY]))[ATTACHED_TABS_KEY];
    const saved = Array.isArray(stored) ? (stored as number[]) : [];
    if (saved.length === 0) {
      return;
    }
    const attached = new Set((await chrome.debugger.getTargets()).filter((t) => t.attached).map((t) => t.tabId));
    const gone: number[] = [];
    for (const tabId of saved) {
      if (attached.has(tabId)) {
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
    this.storage.set({ [ATTACHED_TABS_KEY]: [...this.idle.keys()] }).catch((error: unknown) => {
      console.error("failed to save the attached tabs", error);
    });
  }

  private armIdle(tabId: number): void {
    clearTimeout(this.idle.get(tabId));
    this.idle.set(
      tabId,
      setTimeout(() => {
        void this.release(tabId).then(() => this.notify("debugger.detached", { tabId, reason: IDLE_REASON }));
      }, IDLE_DETACH_MS),
    );
  }

  private forget(tabId: number): void {
    clearTimeout(this.idle.get(tabId));
    this.idle.delete(tabId);
    this.persist();
  }

  private async release(tabId: number): Promise<void> {
    this.forget(tabId);
    try {
      await chrome.debugger.detach({ tabId });
    } catch (error) {
      // 标签页可能恰好已被关闭或被用户分离；目标状态（未附加）已经达成，不该让释放流程失败。
      console.warn(`detaching the debugger from tab ${tabId} failed`, error);
    }
  }
}

function errorMessage(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
