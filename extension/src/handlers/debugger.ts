import { HandlerError, type RpcHandler } from "@/background/registry";
import type { NotificationMethod, NotificationParams } from "@/protocol/generated/protocol.generated";
import { requireTab } from "./lookup";

// 守护进程持有 5 分钟的权威超时；这里只是扩展侧的兜底，防止守护进程失联后调试器提示条一直挂着。
export const IDLE_DETACH_MS = 10 * 60_000;
const IDLE_REASON = "idle_timeout";

export type NotifyFn = <N extends NotificationMethod>(method: N, params: NotificationParams<N>) => void;

// 按标签页维护 chrome.debugger 附加状态：发送命令时按需附加，把事件和被动分离转成通知，并在空闲、连接断开时释放。
export class DebuggerRelay {
  // 已附加（或正在附加）的标签页 → 空闲计时器。
  private readonly idle = new Map<number, ReturnType<typeof setTimeout>>();
  private readonly attaching = new Map<number, Promise<void>>();

  constructor(private readonly notify: NotifyFn) {}

  readonly send: RpcHandler<"debugger.send"> = async (params) => {
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
    const tabIds =
      params.tabId === undefined ? [...this.idle.keys()] : this.idle.has(params.tabId) ? [params.tabId] : [];
    await Promise.all(tabIds.map((tabId) => this.release(tabId)));
    return { tabIds };
  };

  // 连接断开后没有人能再驱动这些标签页，全部释放；此时也无法通知守护进程。
  async detachAll(): Promise<void> {
    await Promise.all([...this.idle.keys()].map((tabId) => this.release(tabId)));
  }

  onEvent(source: chrome.debugger.DebuggerSession, method: string, params?: object): void {
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
