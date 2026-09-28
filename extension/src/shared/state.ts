// 连接状态由 offscreen 文档里的连接持有者产生，经 service worker 转交弹窗渲染。

// 每个状态都带上实例身份与 daemon 地址，弹窗在任何状态下都能显示它们。
export interface InstanceSummary {
  instanceId: string;
  name: string;
  address: string;
}

// code-rejected：配对码错误或已过期（daemon 不回显握手失败原因，两者无法区分）；
// name-taken：首次配对时默认名称已被另一个已配对实例占用，这次配对不会被 daemon 保存。
export type PairFailure = "code-rejected" | "name-taken";

export type StatusDetail =
  | { status: "unpaired" }
  | { status: "pairing" }
  | { status: "pair-failed"; reason: PairFailure }
  | { status: "pair-unreachable" }
  // product/productVersion 是本浏览器自报给 daemon 的品牌与版本；connectedAt 是握手完成的时间戳（毫秒），
  // 弹窗据此计算已连接时长，重新打开弹窗也不会从零算起。
  | { status: "connected"; daemonVersion: string; product: string; productVersion: string; connectedAt: number }
  // attempt 是连续失败的次数（0 表示首次连接）；retryAt 是下一次重试的时间戳（毫秒），正在尝试时为 null。
  | { status: "reconnecting"; attempt: number; retryAt: number | null }
  | { status: "rejected" };

export type ConnectionState = InstanceSummary & StatusDetail;

export type ConnectionStatus = ConnectionState["status"];
