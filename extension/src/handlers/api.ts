import { type ApprovalHandler, HandlerError, type RpcHandler } from "@/background/registry";
import type { RpcMethod } from "@/protocol/generated/protocol.generated";
import type { ApprovalKind } from "@/shared/approvals";

// 各数据领域依赖的 chrome.* 命名空间。类型声明总带着它们，运行时却可能没有（例如别的 Chromium 浏览器）：每次调用时检测，
// 缺少时整个领域回 UNSUPPORTED 并点名缺少的 API，而不是在 undefined 上崩成 INTERNAL_ERROR。
export type ChromeNamespace =
  | "bookmarks"
  | "readingList"
  | "tabGroups"
  | "history"
  | "sessions"
  | "downloads"
  | "cookies"
  | "browsingData"
  | "management";

export function requireApi<N extends ChromeNamespace>(namespace: N): (typeof chrome)[N] {
  const api = (chrome as Partial<typeof chrome>)[namespace];
  if (api === undefined) {
    throw new HandlerError("UNSUPPORTED", `this browser does not provide chrome.${namespace}`);
  }
  return api;
}

// needs 让处理函数先确认它的领域 API 存在。
export function needs<M extends RpcMethod>(namespace: ChromeNamespace, handler: RpcHandler<M>): RpcHandler<M> {
  return async (params) => {
    requireApi(namespace);
    return handler(params);
  };
}

// needsForApproval 同 needs，用于 L2 方法：检测放在 prepare 里，缺少 API 时不打开审批窗口。
export function needsForApproval<K extends ApprovalKind>(
  namespace: ChromeNamespace,
  handler: ApprovalHandler<K>,
): ApprovalHandler<K> {
  return {
    ...handler,
    prepare: async (params) => {
      requireApi(namespace);
      return handler.prepare(params);
    },
  };
}
