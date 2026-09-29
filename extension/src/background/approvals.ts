import { LIMITS } from "@/protocol/generated/protocol.generated";
import { APPROVAL_WINDOW, type ApprovalItem, type ApprovalRequest, type ApprovalView } from "@/shared/approvals";
import type { RpcContext, RpcOutcome } from "@/shared/messages";

// 扩展侧的期限比 daemon 的 writeDecisionTtlMs 早这么多：daemon 的计时从发出请求算起，早于扩展收到它，
// 若两边同时到期，daemon 的取消会先到，窗口就会把超时误显示为「请求方已取消」。提前这一点，超时总由扩展先判定。
export const EXPIRY_MARGIN_MS = 2000;

// 工具栏角标固定用琥珀色底加深色字：后台拿不到当前配色方案，这组颜色在浅色和深色工具栏上都醒目。
const BADGE_BACKGROUND = "#E9A93A";
const BADGE_TEXT = "#0A1622";

const STORAGE_KEY = "approvals";

export interface SessionStorageLike {
  get(keys: string[]): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
}

export interface ApprovalTimers {
  setTimeout(fn: () => void, ms: number): number;
  clearTimeout(id: number): void;
  now(): number;
}

// chrome.windows 的最小子集。
export interface WindowsLike {
  create(options: {
    url: string;
    type: "popup";
    width: number;
    height: number;
    focused: boolean;
  }): Promise<{ id?: number } | undefined>;
  update(windowId: number, info: { focused: true }): Promise<unknown>;
  remove(windowId: number): Promise<void>;
}

// chrome.action 里设置角标的部分。
export interface BadgeLike {
  setBadgeText(details: { text: string }): Promise<void>;
  setBadgeBackgroundColor(details: { color: string }): Promise<void>;
  setBadgeTextColor(details: { color: string }): Promise<void>;
}

export interface ApprovalDeps {
  // chrome.storage.session：service worker 休眠后仍在，浏览器重启或扩展重载时清空，正好与 daemon 连接的寿命一致。
  storage: SessionStorageLike;
  timers: ApprovalTimers;
  windows: WindowsLike;
  badge: BadgeLike;
  // 审批窗口页面的完整 URL（chrome.runtime.getURL(APPROVAL_PAGE)）。
  pageUrl: string;
  browserName(): Promise<string>;
  // 把结论交给 offscreen，由它作为原请求的 JSON-RPC 应答发给 daemon。
  settle(requestId: string, outcome: RpcOutcome): Promise<void>;
  execute(request: ApprovalRequest): Promise<RpcOutcome>;
  broadcast(view: ApprovalView): void;
}

// 持久化的一条请求：窗口展示的内容加上作废它所需的连接标识。
type ApprovalRecord = ApprovalItem & {
  connection: string;
  // 请求方已经不再等待（取消、超时或断开）：执行完成后只更新展示，不再应答。
  detached: boolean;
};

interface ApprovalState {
  windowId: number | null;
  records: ApprovalRecord[];
}

const TERMINAL = new Set<ApprovalItem["status"]>(["done", "failed", "expired", "cancelled", "voided"]);

// Approvals 是 L2 请求的待审批队列与状态机（docs/specs 第 2 期「审批窗口」）。所有状态变更在一个队列里串行执行，
// 决定、取消、超时、断开之间谁先到谁生效。批准后先把 executing 落盘再执行，所以执行至多一次；应答在落盘之前发出，
// service worker 恰在两者之间被回收时恢复后会再应答一次，offscreen 只转发同一请求的第一次应答。
export class Approvals {
  private queue: Promise<unknown> = Promise.resolve();
  private state: ApprovalState | null = null;
  // 本 service worker 里的计时器；休眠后丢失，恢复时按持久化的期限重新设置。
  private readonly timers = new Map<string, number>();

  constructor(private readonly deps: ApprovalDeps) {}

  // service worker 启动时调用：把已过期限的请求判为超时，其余的重新计时，并恢复角标。
  restore(): Promise<void> {
    return this.serialized(() => Promise.resolve());
  }

  enqueue(context: RpcContext, request: ApprovalRequest): Promise<void> {
    return this.serialized(async (state) => {
      state.records.push({
        ...request,
        id: context.requestId,
        requester: context.clientId,
        receivedAt: context.receivedAt,
        expiresAt: context.receivedAt + LIMITS.writeDecisionTtlMs - EXPIRY_MARGIN_MS,
        status: "pending",
        outcome: null,
        connection: context.connection,
        detached: false,
      });
      await this.showWindow(state);
    });
  }

  // daemon 发来 $/cancelRequest：请求方取消，或 daemon 自己的期限到了。期限已过时按超时处理（见 EXPIRY_MARGIN_MS）；
  // 取消不应答（docs/protocol.md §5）。
  cancel(requestId: string): Promise<void> {
    return this.serialized((state) => {
      const record = state.records.find((r) => r.id === requestId);
      if (record) {
        abandon(record, "cancelled");
      }
      return Promise.resolve();
    });
  }

  // 与 daemon 的连接断了：它上面的请求全部作废，调用方由 daemon 得到 OPERATION_EXPIRED，这里无从也无须应答。
  // 不给 connection 时作废全部请求（offscreen 文档重新启动，之前的连接都已不在）。
  disconnected(connection?: string): Promise<void> {
    return this.serialized((state) => {
      for (const record of state.records) {
        if (connection === undefined || record.connection === connection) {
          abandon(record, "voided");
        }
      }
      return Promise.resolve();
    });
  }

  decide(id: string, decision: "approve" | "reject"): Promise<void> {
    let approved: ApprovalRecord | null = null;
    const deciding = this.serialized(async (state) => {
      const record = state.records.find((r) => r.id === id);
      if (record?.status !== "pending") {
        return;
      }
      if (decision === "reject") {
        state.records = state.records.filter((r) => r !== record);
        await this.answer(record, rejection("the request was rejected in the approval window"));
        return;
      }
      record.status = "executing";
      approved = record;
    });
    // 执行不占用队列：执行期间取消、超时、断开和关窗都照常处理（卸载扩展要等 Chrome 自己的确认框）。
    return deciding.then(async () => {
      if (approved) {
        await this.run(approved);
      }
    });
  }

  private async run(approved: ApprovalRecord): Promise<void> {
    const outcome = await this.deps.execute({ kind: approved.kind, detail: approved.detail });
    await this.serialized(async (state) => {
      const record = state.records.find((r) => r.id === approved.id);
      if (!record) {
        return;
      }
      record.status = outcome.ok ? "done" : "failed";
      record.outcome = outcome;
      if (!record.detached) {
        await this.answer(record, outcome);
      }
    });
  }

  dismiss(id: string): Promise<void> {
    return this.serialized((state) => {
      state.records = state.records.filter((r) => r.id !== id || !TERMINAL.has(r.status));
      return Promise.resolve();
    });
  }

  // 窗口里的关闭按钮：排队中的请求全部拒绝，然后关掉窗口。
  closeWindow(): Promise<void> {
    return this.serialized(async (state) => {
      const windowId = state.windowId;
      await this.windowGone(state);
      if (windowId !== null) {
        await this.deps.windows.remove(windowId).catch(() => undefined);
      }
    });
  }

  // chrome.windows.onRemoved：用户直接关掉了审批窗口。
  windowRemoved(windowId: number): Promise<void> {
    return this.serialized((state) => (state.windowId === windowId ? this.windowGone(state) : Promise.resolve()));
  }

  focus(): Promise<void> {
    return this.serialized((state) => (state.records.length > 0 ? this.showWindow(state) : Promise.resolve()));
  }

  view(): Promise<ApprovalView> {
    return this.serialized(async (state) => this.viewOf(state));
  }

  // 关窗即拒绝全部等待中的请求；已结束的请求只是展示，随窗口一起清掉；正在执行的请求不受影响，结论照常应答。
  private async windowGone(state: ApprovalState): Promise<void> {
    state.windowId = null;
    const pending = state.records.filter((r) => r.status === "pending");
    state.records = state.records.filter((r) => r.status === "executing");
    for (const record of pending) {
      await this.answer(record, rejection("the approval window was closed"));
    }
  }

  private async showWindow(state: ApprovalState): Promise<void> {
    if (state.windowId !== null) {
      try {
        await this.deps.windows.update(state.windowId, { focused: true });
        return;
      } catch {
        // 记下的窗口已经不在（例如关窗事件在 service worker 回收前没能处理），重新打开一个。
        state.windowId = null;
      }
    }
    try {
      const created = await this.deps.windows.create({
        url: this.deps.pageUrl,
        type: "popup",
        width: APPROVAL_WINDOW.width,
        height: APPROVAL_WINDOW.height,
        focused: true,
      });
      state.windowId = created?.id ?? null;
    } catch (error) {
      // 打不开窗口时请求照样排队：角标显示着待审批数量，弹窗里的「查看」会再次尝试打开。
      console.error("failed to open the approval window", error);
    }
  }

  private answer(record: ApprovalRecord, outcome: RpcOutcome): Promise<void> {
    record.detached = true;
    return this.deps.settle(record.id, outcome);
  }

  // 串行执行一次状态变更：载入状态、先判定已过期限的请求，再执行变更，然后持久化并刷新计时器、角标与窗口视图。
  private serialized<T>(change: (state: ApprovalState) => Promise<T>): Promise<T> {
    const next = this.queue.then(async () => {
      try {
        const state = await this.load();
        await this.expireDue(state);
        const result = await change(state);
        await this.commit(state);
        return result;
      } catch (error) {
        // 内存里可能留着没有持久化的半截变更；丢掉它，下一次从存储重新载入。
        this.state = null;
        throw error;
      }
    });
    this.queue = next.catch(() => undefined);
    return next;
  }

  private async load(): Promise<ApprovalState> {
    if (this.state) {
      return this.state;
    }
    const items = await this.deps.storage.get([STORAGE_KEY]);
    const state = (items[STORAGE_KEY] as ApprovalState | undefined) ?? { windowId: null, records: [] };
    // 执行跑在 service worker 里，存储里还是 executing 说明上一个 service worker 在执行途中被回收了：结果不得而知。
    for (const record of state.records.filter((r) => r.status === "executing")) {
      record.status = "failed";
      record.outcome = {
        ok: false,
        code: "INTERNAL_ERROR",
        message: "the extension restarted while carrying out the request",
      };
      if (!record.detached) {
        await this.answer(record, record.outcome);
      }
    }
    this.state = state;
    return state;
  }

  private async expireDue(state: ApprovalState): Promise<void> {
    const now = this.deps.timers.now();
    for (const record of state.records) {
      if (now < record.expiresAt || record.detached) continue;
      // 执行中的请求到期也照常执行完：倒计时不因执行而暂停，daemon 那边同样已经到期。
      if (record.status === "pending") {
        record.status = "expired";
      }
      await this.answer(record, { ok: false, code: "OPERATION_EXPIRED", message: "the approval request expired" });
    }
  }

  private async commit(state: ApprovalState): Promise<void> {
    // 窗口已经关了，已结束的请求没有地方展示。
    if (state.windowId === null) {
      state.records = state.records.filter((r) => !TERMINAL.has(r.status));
    }
    const closeWindow = state.windowId !== null && state.records.length === 0 ? state.windowId : null;
    if (closeWindow !== null) {
      state.windowId = null;
    }
    await this.deps.storage.set({ [STORAGE_KEY]: state });
    this.armTimers(state);
    const waiting = state.records.filter((r) => r.status === "pending").length;
    await this.deps.badge.setBadgeBackgroundColor({ color: BADGE_BACKGROUND });
    await this.deps.badge.setBadgeTextColor({ color: BADGE_TEXT });
    await this.deps.badge.setBadgeText({ text: waiting > 0 ? String(waiting) : "" });
    // 队列空了就收起窗口；它的关闭事件到达时已不再是记下的窗口，不会拒绝任何请求。
    if (closeWindow !== null) {
      await this.deps.windows.remove(closeWindow).catch(() => undefined);
    }
    this.deps.broadcast(await this.viewOf(state));
  }

  // 每个还在等待、请求方也还在等的请求各有一个到期计时器；到期时走一次空变更，由 expireDue 判定。
  private armTimers(state: ApprovalState): void {
    const live = new Set<string>();
    for (const record of state.records) {
      if (record.detached || (record.status !== "pending" && record.status !== "executing")) continue;
      live.add(record.id);
      if (!this.timers.has(record.id)) {
        const delay = Math.max(0, record.expiresAt - this.deps.timers.now());
        this.timers.set(
          record.id,
          this.deps.timers.setTimeout(() => {
            this.timers.delete(record.id);
            this.serialized(() => Promise.resolve()).catch((error: unknown) =>
              console.error("failed to expire an approval request", error),
            );
          }, delay),
        );
      }
    }
    for (const [id, timer] of this.timers) {
      if (!live.has(id)) {
        this.deps.timers.clearTimeout(timer);
        this.timers.delete(id);
      }
    }
  }

  private async viewOf(state: ApprovalState): Promise<ApprovalView> {
    return {
      browserName: await this.deps.browserName(),
      items: state.records.map(({ connection: _connection, detached: _detached, ...item }) => item as ApprovalItem),
    };
  }
}

// 请求方不再等待：还没决定的请求就此作废，正在执行的照常执行完，只是不再应答。
function abandon(record: ApprovalRecord, status: "cancelled" | "voided"): void {
  if (record.status === "pending") {
    record.status = status;
  }
  record.detached = true;
}

function rejection(message: string): RpcOutcome {
  return { ok: false, code: "USER_REJECTED", message };
}
