import type { RpcMethod, RpcParams, RpcResult } from "@/protocol/generated/protocol.generated";
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

// 业务方法处理注册表：注册了哪些方法，扩展就在能力声明里声明哪些方法。
export class HandlerRegistry {
  private readonly handlers = new Map<RpcMethod, (params: unknown) => Promise<unknown>>();

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
