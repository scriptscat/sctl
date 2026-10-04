import type {
  ERROR_CODES,
  NotificationMethod,
  NotificationParams,
  RpcMethod,
} from "@/protocol/generated/protocol.generated";
import type { ApprovalMessage } from "./approvals";
import type { ConnectionState } from "./state";

export type ErrorCode = (typeof ERROR_CODES)[number];

// 业务方法处理结果；失败码必须是协议登记的领域错误码，由 offscreen 转成 JSON-RPC -32000 错误。
export type RpcOutcome = { ok: true; result: unknown } | { ok: false; code: ErrorCode; message: string };

// background 对一条业务请求的应答：L2 请求先答 deferred，审批得出结论后再以 settle 命令单独送回 offscreen。
// 不能把这条 runtime 消息的应答一直挂到用户决定：MV3 的 service worker 随时可能被回收，挂着的应答随之丢失。
export type RpcReply = RpcOutcome | { deferred: true };

// 一条业务请求的来历。requestId 是 daemon 的 JSON-RPC 请求 id；clientId 是请求方自报的标签，未经验证；
// connection 标识收到它的那条 WebSocket 连接，连接断开时据此作废它的请求；receivedAt 是 offscreen 收到的时间。
export interface RpcContext {
  requestId: string;
  clientId: string | null;
  connection: string;
  receivedAt: number;
}

export type PairResult = { ok: true } | { ok: false; error: "invalid-code" };
export type RenameResult = { ok: true } | { ok: false; error: "invalid-name" | "not-connected" | "name-taken" };
export type SetAddressResult = { ok: true } | { ok: false; error: "invalid-address" };

// offscreen 启动连接所需的全部配置；只有 service worker 能读写扩展存储，由它组装后交给 offscreen。
export interface ConnectionConfig {
  instanceId: string;
  name: string;
  // 配对总以默认名称登记（docs/specs 浏览器实例的身份与配对），即使此前改过名。
  defaultName: string;
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
  | { target: "background"; type: "rpc"; method: RpcMethod; input: unknown; context: RpcContext }
  | { target: "background"; type: "rpcCancel"; requestId: string }
  | { target: "background"; type: "disconnected"; connection: string }
  // 已连接的会话结束（断开、忘记、换地址），依赖这条连接的调试器附加要随之释放。
  | { target: "background"; type: "connectionClosed" };

export type BackgroundMessage = PopupRequest | OffscreenEvent | ApprovalMessage;

export type OffscreenCommand =
  | { target: "offscreen"; type: "getState" }
  | { target: "offscreen"; type: "pair"; code: string }
  | { target: "offscreen"; type: "rename"; name: string }
  | { target: "offscreen"; type: "retryNow" }
  | { target: "offscreen"; type: "forget" }
  | { target: "offscreen"; type: "setAddress"; address: string }
  | { target: "offscreen"; type: "settle"; requestId: string; outcome: RpcOutcome }
  | {
      [N in NotificationMethod]: { target: "offscreen"; type: "notify"; method: N; params: NotificationParams<N> };
    }[NotificationMethod];

export interface StateBroadcast {
  target: "popup";
  type: "state";
  state: ConnectionState;
}
