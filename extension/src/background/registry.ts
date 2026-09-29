import { RPC_METHODS, type RpcMethod, type RpcParams, type RpcResult } from "@/protocol/generated/protocol.generated";
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

// 业务方法处理注册表：注册了哪些方法，扩展就在能力声明里声明哪些方法。
export class HandlerRegistry {
  private readonly handlers = new Map<RpcMethod, (params: unknown) => Promise<unknown>>();

  // 级别默认取自生成的协议；测试注入它，才能在还没有 L1 方法的协议上驱动确认检查。
  constructor(private readonly levelOf: (method: RpcMethod) => MethodLevel = (method) => RPC_METHODS[method].level) {}

  register<M extends RpcMethod>(method: M, handler: RpcHandler<M>): void {
    if (this.handlers.has(method)) {
      throw new Error(`handler for ${method} is already registered`);
    }
    this.handlers.set(method, handler as (params: unknown) => Promise<unknown>);
  }

  methods(): RpcMethod[] {
    return [...this.handlers.keys()];
  }

  async dispatch(method: RpcMethod, params: unknown): Promise<RpcOutcome> {
    const handler = this.handlers.get(method);
    if (!handler) {
      // offscreen 只转发已声明的方法，走到这里说明声明与注册不一致。
      throw new Error(`no handler registered for ${method}`);
    }
    // daemon 转发前已检查过确认；这里再查一次，daemon 的检查出错或被绕过时也不会执行 L1 操作。
    if (this.levelOf(method) === "L1" && !isConfirmed(params)) {
      return {
        ok: false,
        code: "CONFIRMATION_REQUIRED",
        message: `${method} requires explicit confirmation: pass confirm: true`,
      };
    }
    try {
      return { ok: true, result: await handler(params) };
    } catch (error) {
      if (error instanceof HandlerError) {
        return { ok: false, code: error.code, message: error.message };
      }
      console.error(`handler for ${method} failed`, error);
      return { ok: false, code: "INTERNAL_ERROR", message: "internal error" };
    }
  }
}

function isConfirmed(params: unknown): boolean {
  return typeof params === "object" && params !== null && (params as { confirm?: unknown }).confirm === true;
}
