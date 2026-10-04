import { RPC_METHODS, type RpcMethod, type RpcParams, type RpcResult } from "@/protocol/generated/protocol.generated";
import type { ApprovalDetails, ApprovalKind, ApprovalRequest } from "@/shared/approvals";
import type { ErrorCode, RpcOutcome } from "@/shared/messages";

// 入参已由 offscreen 在 WebSocket 边界用生成的校验器验证过，处理函数可直接按类型使用。
export type RpcHandler<M extends RpcMethod> = (params: RpcParams<M>) => Promise<RpcResult<M>>;

// 处理函数用它报告协议登记的领域错误；其他异常一律作为 INTERNAL_ERROR，不把内部细节交给 daemon。
export class HandlerError extends Error {
  constructor(
    readonly code: ErrorCode,
    message: string,
  ) {
    super(message);
    this.name = "HandlerError";
  }
}

// 方法的破坏级别（docs/protocol.md §3）；L1 要求入参带 confirm: true。
export type MethodLevel = "L0" | "L1" | "L2";

// L2 方法分两步：prepare 在打开审批窗口之前做完全部校验并给出窗口展示的内容（校验不过就直接报错，不进窗口）；
// execute 在用户批准后按展示的内容执行。两步之间 service worker 可能被回收，所以 execute 只依赖持久化的 detail。
export interface ApprovalHandler<K extends ApprovalKind> {
  prepare(params: RpcParams<K>): Promise<ApprovalDetails[K]>;
  execute(detail: ApprovalDetails[K]): Promise<RpcResult<K>>;
  // execute 需要用户手势（chrome.management.uninstall，见 T1 真机探针）：service worker 里没有手势，只能由审批窗口
  // 在执行按钮的点击处理里调用（runApproved）。service worker 只把批准记为执行中，结论由窗口回报。
  inWindow?: true;
}

export type PrepareOutcome = { ok: true; request: ApprovalRequest } | { ok: false; code: ErrorCode; message: string };

// 业务方法处理注册表：注册了哪些方法，扩展就在能力声明里声明哪些方法。
export class HandlerRegistry {
  private readonly handlers = new Map<RpcMethod, (params: unknown) => Promise<unknown>>();
  private readonly approvals = new Map<ApprovalKind, ApprovalHandler<ApprovalKind>>();

  register<M extends RpcMethod>(method: M, handler: RpcHandler<M>): void {
    this.requireUnregistered(method);
    if (levelOf(method) === "L2") {
      throw new Error(`${method} is L2 and must be registered with registerApproval`);
    }
    this.handlers.set(method, handler as (params: unknown) => Promise<unknown>);
  }

  // L2 方法只能这样注册：它永远不会被 dispatch 直接执行。
  registerApproval<K extends ApprovalKind>(method: K, handler: ApprovalHandler<K>): void {
    this.requireUnregistered(method);
    if (levelOf(method) !== "L2") {
      throw new Error(`${method} is not L2 and needs no approval`);
    }
    this.approvals.set(method, handler);
  }

  private requireUnregistered(method: RpcMethod): void {
    if (this.handlers.has(method) || this.approvals.has(method as ApprovalKind)) {
      throw new Error(`handler for ${method} is already registered`);
    }
  }

  methods(): RpcMethod[] {
    return [...this.handlers.keys(), ...this.approvals.keys()];
  }

  requiresApproval(method: RpcMethod): method is ApprovalKind {
    return this.approvals.has(method as ApprovalKind);
  }

  async prepare(method: ApprovalKind, params: unknown): Promise<PrepareOutcome> {
    const handler = this.approval(method);
    const outcome = await settle(method, () => handler.prepare(params as RpcParams<ApprovalKind>));
    // detail 就是 method 的处理函数给出的，二者对应；method 是联合类型时 tsc 看不出这一点。
    return outcome.ok ? { ok: true, request: { kind: method, detail: outcome.result } as ApprovalRequest } : outcome;
  }

  executesInWindow(kind: ApprovalKind): boolean {
    return this.approval(kind).inWindow === true;
  }

  execute(request: ApprovalRequest): Promise<RpcOutcome> {
    const handler = this.approval(request.kind);
    if (handler.inWindow) {
      throw new Error(`${request.kind} needs a user gesture and must be carried out by the approval window`);
    }
    return runApproved(request.kind, handler, request.detail);
  }

  private approval(method: ApprovalKind): ApprovalHandler<ApprovalKind> {
    const handler = this.approvals.get(method);
    if (!handler) {
      throw new Error(`no approval handler registered for ${method}`);
    }
    return handler;
  }

  async dispatch(method: RpcMethod, params: unknown): Promise<RpcOutcome> {
    const handler = this.handlers.get(method);
    if (!handler) {
      // offscreen 只转发已声明的方法，走到这里说明声明与注册不一致。
      throw new Error(`no handler registered for ${method}`);
    }
    // daemon 转发前已检查过确认；这里再查一次，daemon 的检查出错或被绕过时也不会执行 L1 操作。
    if (levelOf(method) === "L1" && !isConfirmed(params)) {
      return {
        ok: false,
        code: "CONFIRMATION_REQUIRED",
        message: `${method} requires explicit confirmation: pass confirm: true`,
      };
    }
    return settle(method, () => handler(params));
  }
}

// 执行一个已批准的 L2 请求并把结果换成 RpcOutcome；service worker 与审批窗口（inWindow 的请求）共用它。
export function runApproved<K extends ApprovalKind>(
  kind: K,
  handler: ApprovalHandler<K>,
  detail: ApprovalDetails[K],
): Promise<RpcOutcome> {
  return settle(kind, () => handler.execute(detail));
}

// 处理函数的结果换成 RpcOutcome：HandlerError 原样交出领域错误码，其他异常不外泄内部细节。
async function settle<T>(method: RpcMethod, run: () => Promise<T>): Promise<{ ok: true; result: T } | RpcFailure> {
  try {
    return { ok: true, result: await run() };
  } catch (error) {
    if (error instanceof HandlerError) {
      return { ok: false, code: error.code, message: error.message };
    }
    console.error(`handler for ${method} failed`, error);
    return { ok: false, code: "INTERNAL_ERROR", message: "internal error" };
  }
}

type RpcFailure = Extract<RpcOutcome, { ok: false }>;

function isConfirmed(params: unknown): boolean {
  return typeof params === "object" && params !== null && (params as { confirm?: unknown }).confirm === true;
}

function levelOf(method: RpcMethod): MethodLevel {
  return RPC_METHODS[method].level;
}
