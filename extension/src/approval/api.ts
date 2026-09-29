import type { ApprovalMessage, ApprovalView } from "@/shared/approvals";
import { type RuntimeLike, request } from "@/shared/messaging";
import { watchApprovals } from "@/shared/popup-api";

// 审批窗口与 service worker 之间的全部接口：决定与关闭发给 service worker，队列变化由它广播推送。
export interface ApprovalApi {
  view(): Promise<ApprovalView>;
  subscribe(listener: (view: ApprovalView) => void): () => void;
  // 批准时要等执行结束才兑现；窗口不等它，执行进度随广播到达。
  decide(id: string, decision: "approve" | "reject"): Promise<void>;
  dismiss(id: string): Promise<void>;
  closeWindow(): Promise<void>;
}

export function createApprovalApi(runtime: RuntimeLike = chrome.runtime): ApprovalApi {
  const send = <R>(message: ApprovalMessage) => request<R>(runtime, message);
  return {
    view: () => send({ target: "background", type: "approvalView" }),
    subscribe: (listener) => watchApprovals(runtime, listener),
    decide: (id, decision) => send({ target: "background", type: "approvalDecide", id, decision }),
    dismiss: (id) => send({ target: "background", type: "approvalDismiss", id }),
    closeWindow: () => send({ target: "background", type: "approvalCloseWindow" }),
  };
}
