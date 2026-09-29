import type { ApprovalBroadcast, ApprovalMessage, ApprovalView } from "./approvals";
import type { PairResult, PopupRequest, RenameResult, SetAddressResult, StateBroadcast } from "./messages";
import { type RuntimeLike, request } from "./messaging";
import type { ConnectionState } from "./state";

// 弹窗与连接之间的全部接口：命令发给 service worker，状态变化由 offscreen 广播推送。
export interface PopupApi {
  getState(): Promise<ConnectionState>;
  subscribe(listener: (state: ConnectionState) => void): () => void;
  pair(code: string): Promise<PairResult>;
  rename(name: string): Promise<RenameResult>;
  retryNow(): Promise<void>;
  forget(): Promise<void>;
  setAddress(address: string): Promise<SetAddressResult>;
  // 待审批队列：弹窗据此显示「N 个待批准请求 · 查看」，点击后把审批窗口带到前台。
  getApprovals(): Promise<ApprovalView>;
  subscribeApprovals(listener: (view: ApprovalView) => void): () => void;
  focusApprovals(): Promise<void>;
}

function isStateBroadcast(message: unknown): message is StateBroadcast {
  const m = message as Partial<StateBroadcast> | null;
  return typeof m === "object" && m !== null && m.target === "popup" && m.type === "state";
}

function isApprovalBroadcast(message: unknown): message is ApprovalBroadcast {
  const m = message as Partial<ApprovalBroadcast> | null;
  return typeof m === "object" && m !== null && m.target === "approval" && m.type === "approvals";
}

// service worker 每次队列变化后广播的视图；弹窗和审批窗口都靠它跟上队列。
export function watchApprovals(runtime: RuntimeLike, listener: (view: ApprovalView) => void): () => void {
  const onMessage = (message: unknown) => {
    if (isApprovalBroadcast(message)) {
      listener(message.view);
    }
    return false;
  };
  runtime.onMessage.addListener(onMessage);
  return () => runtime.onMessage.removeListener(onMessage);
}

export function createPopupApi(runtime: RuntimeLike = chrome.runtime): PopupApi {
  const send = <R>(message: PopupRequest) => request<R>(runtime, message);
  return {
    getState: () => send({ target: "background", type: "getState" }),
    subscribe(listener) {
      const onMessage = (message: unknown) => {
        if (isStateBroadcast(message)) {
          listener(message.state);
        }
        return false;
      };
      runtime.onMessage.addListener(onMessage);
      return () => runtime.onMessage.removeListener(onMessage);
    },
    pair: (code) => send({ target: "background", type: "pair", code }),
    rename: (name) => send({ target: "background", type: "rename", name }),
    retryNow: () => send({ target: "background", type: "retryNow" }),
    forget: () => send({ target: "background", type: "forget" }),
    setAddress: (address) => send({ target: "background", type: "setAddress", address }),
    getApprovals: () =>
      request<ApprovalView>(runtime, { target: "background", type: "approvalView" } satisfies ApprovalMessage),
    subscribeApprovals: (listener) => watchApprovals(runtime, listener),
    focusApprovals: () =>
      request<void>(runtime, { target: "background", type: "approvalFocus" } satisfies ApprovalMessage),
  };
}
