import type { RpcOutcome } from "./messages";

// L2 审批（docs/specs 第 2 期「审批窗口」）在 service worker 与审批窗口之间共用的数据形状。
// 这里的一切都要能放进 chrome.storage.session（结构化克隆），service worker 休眠后据此恢复。

// 相对扩展根目录的审批窗口页面路径，与构建产物 dist/approval/index.html 对应。
export const APPROVAL_PAGE = "approval/index.html";
export const APPROVAL_WINDOW = { width: 440, height: 680 } as const;

// 删除书签时，被删文件夹里的一个后代节点；也是批准时复核的依据。
export interface BookmarkRemovalEntry {
  id: string;
  type: "bookmark" | "folder";
  title: string;
  url?: string;
  parentId: string;
}

// 要删除的一项。path 是外层文件夹标题，由外到内；文件夹带着它包含的书签数、子文件夹数与全部后代（先序展开）。
export type BookmarkRemovalItem =
  | { id: string; type: "bookmark"; title: string; url: string; parentId: string; path: string[] }
  | {
      id: string;
      type: "folder";
      title: string;
      parentId: string;
      path: string[];
      bookmarks: number;
      folders: number;
      contents: BookmarkRemovalEntry[];
    };

export interface BookmarkRemovalDetail {
  // items 是删除的项数（位于另一个被删文件夹之内的 ID 不单独计数）；contained* 是这些文件夹里的内容。
  summary: { items: number; bookmarks: number; folders: number; containedBookmarks: number; containedFolders: number };
  items: BookmarkRemovalItem[];
}

// 卸载扩展时窗口展示的扩展信息，取自预校验时的 chrome.management.get。名称和描述由扩展作者决定，只能当纯文本渲染。
export interface ExtensionUninstallDetail {
  id: string;
  name: string;
  version: string;
  description: string;
  installType: string;
  enabled: boolean;
}

// 每种 L2 请求的展示内容。新的请求类型在这里加一项，kind 取它对应的协议方法名。
export interface ApprovalDetails {
  "bookmarks.remove": BookmarkRemovalDetail;
  "extensions.uninstall": ExtensionUninstallDetail;
}

export type ApprovalKind = keyof ApprovalDetails;

export type ApprovalRequest = { [K in ApprovalKind]: { kind: K; detail: ApprovalDetails[K] } }[ApprovalKind];

// pending：等待决定；executing：已批准、正在执行。其余都是终态，只供窗口展示，关闭窗口后清除：
// done/failed 是执行的结论，expired 是扩展侧期限已到，cancelled 是请求方取消，voided 是与 daemon 的连接断了。
// 拒绝不是展示状态：被拒绝的请求直接移出队列。
export type ApprovalStatus = "pending" | "executing" | "done" | "failed" | "expired" | "cancelled" | "voided";

export type ApprovalItem = ApprovalRequest & {
  // 即 daemon 的 JSON-RPC 请求 id。
  id: string;
  // 请求方自报的标签（params.clientId），未经验证；没有时为 null。
  requester: string | null;
  receivedAt: number;
  // 自动拒绝的时间点（毫秒时间戳），窗口据此显示倒计时。
  expiresAt: number;
  status: ApprovalStatus;
  // 执行结束后的结论（done/failed），其余状态为 null。
  outcome: RpcOutcome | null;
};

export interface ApprovalView {
  // 本浏览器实例的名称，窗口在每个请求上显示。
  browserName: string;
  // 按到达顺序排列。
  items: ApprovalItem[];
}

// approvalDecide 的应答。executeInWindow 为真时，service worker 已把请求标为执行中，由发出批准的窗口在这次点击里执行，
// 再以 approvalFinish 回报结论；为假时没有什么要窗口做的（已在 service worker 里执行完，或请求已不在等待决定）。
export interface ApprovalDecision {
  executeInWindow: boolean;
}

// 审批窗口与弹窗发给 service worker 的请求。
export type ApprovalMessage =
  | { target: "background"; type: "approvalView" }
  | { target: "background"; type: "approvalDecide"; id: string; decision: "approve" | "reject" }
  // 窗口执行完一个交给它的请求（见 ApprovalDecision）后回报的结论。
  | { target: "background"; type: "approvalFinish"; id: string; outcome: RpcOutcome }
  // 从窗口里移走一个已经结束的请求；对等待中的请求无效。
  | { target: "background"; type: "approvalDismiss"; id: string }
  // 用户在窗口里点了关闭：排队中的请求全部视为拒绝。
  | { target: "background"; type: "approvalCloseWindow" }
  // 把审批窗口带到前台（弹窗里的「查看」），没有窗口时重新打开。
  | { target: "background"; type: "approvalFocus" };

// 每次队列变化后 service worker 广播的最新视图。
export interface ApprovalBroadcast {
  target: "approval";
  type: "approvals";
  view: ApprovalView;
}
