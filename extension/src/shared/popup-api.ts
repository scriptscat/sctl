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
}

function isStateBroadcast(message: unknown): message is StateBroadcast {
  const m = message as Partial<StateBroadcast> | null;
  return typeof m === "object" && m !== null && m.target === "popup" && m.type === "state";
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
  };
}
