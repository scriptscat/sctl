import type { ERROR_CODES, RpcMethod } from "@/protocol/generated/protocol.generated";
import type { ConnectionState } from "./state";

export type ErrorCode = (typeof ERROR_CODES)[number];

// 业务方法处理结果；失败码必须是协议登记的领域错误码，由 offscreen 转成 JSON-RPC -32000 错误。
export type RpcOutcome = { ok: true; result: unknown } | { ok: false; code: ErrorCode; message: string };

export type PairResult = { ok: true } | { ok: false; error: "invalid-code" };
export type RenameResult = { ok: true } | { ok: false; error: "invalid-name" | "not-connected" | "name-taken" };
export type SetAddressResult = { ok: true } | { ok: false; error: "invalid-address" };

// offscreen 启动连接所需的全部配置；只有 service worker 能读写扩展存储，由它组装后交给 offscreen。
export interface ConnectionConfig {
  instanceId: string;
  name: string;
  address: string;
  // 长期会话密钥（小写 hex），未配对时为 null。
  key: string | null;
  methods: RpcMethod[];
  product: string;
  productVersion: string;
  extensionVersion: string;
}

// 同一条 chrome.runtime 消息会送达除发送者外的所有扩展页面，target 决定由谁处理。
export type PopupRequest =
  | { target: "background"; type: "getState" }
  | { target: "background"; type: "pair"; code: string }
  | { target: "background"; type: "rename"; name: string }
  | { target: "background"; type: "retryNow" }
  | { target: "background"; type: "forget" }
  | { target: "background"; type: "setAddress"; address: string };

export type OffscreenEvent =
  | { target: "background"; type: "offscreenReady" }
  | { target: "background"; type: "paired"; key: string; name: string }
  | { target: "background"; type: "renamed"; name: string }
  | { target: "background"; type: "rpc"; method: RpcMethod; input: unknown };

export type BackgroundMessage = PopupRequest | OffscreenEvent;

export type OffscreenCommand =
  | { target: "offscreen"; type: "getState" }
  | { target: "offscreen"; type: "pair"; code: string }
  | { target: "offscreen"; type: "rename"; name: string }
  | { target: "offscreen"; type: "retryNow" }
  | { target: "offscreen"; type: "forget" }
  | { target: "offscreen"; type: "setAddress"; address: string };

export interface StateBroadcast {
  target: "popup";
  type: "state";
  state: ConnectionState;
}
