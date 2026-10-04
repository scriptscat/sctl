import type { ComponentType } from "react";
import type { Strings } from "@/i18n";
import type { ApprovalDetails, ApprovalItem, ApprovalKind } from "@/shared/approvals";
import { runApproved } from "@/background/registry";
import { uninstallExtension } from "@/handlers/extensions";
import type { ErrorCode, RpcOutcome } from "@/shared/messages";
import { BookmarkRemovalContent } from "./bookmark-removal";
import { ExtensionUninstallContent } from "./extension-uninstall";

export type ApprovalItemOf<K extends ApprovalKind> = ApprovalItem & { kind: K; detail: ApprovalDetails[K] };

// 说明；body 是若干句话，由窗口按语言拼接。失败默认用错误色，tone 为 warn 时表示用户自己取消这类非错误的结局。
export interface Notice {
  title: string;
  body: string[];
  tone?: "warn";
}

export interface KindContentProps<K extends ApprovalKind> {
  item: ApprovalItemOf<K>;
  s: Strings;
}

// 一种 L2 请求在审批窗口里的全部差异：文案与中间的内容区（影响摘要 + 逐项列表）。外框、请求方、倒计时、
// 队列、防诱导提示、拒绝/关闭按钮与各终态的处理都由窗口统一负责。
export interface ApprovalKindView<K extends ApprovalKind> {
  windowTitle(s: Strings): string;
  title(s: Strings, item: ApprovalItemOf<K>): string;
  // 等待决定与执行中时显示的不可撤销提示标题，防诱导提示作为它的正文。
  irreversible(s: Strings): string;
  approve(s: Strings, item: ApprovalItemOf<K>): string;
  // 等待决定时执行按钮下方的补充说明。
  approveHint?(s: Strings): string;
  // notice 给出时，执行中显示它（例如请用户去 Chrome 的确认框里操作），代替不可撤销提示。
  executing(s: Strings): { label: string; hint: string; notice?: Notice };
  // 超时、取消、作废时说明什么都没有发生。
  nothingDone(s: Strings): string;
  done(s: Strings, item: ApprovalItemOf<K>, result: unknown): Notice;
  failed(s: Strings, item: ApprovalItemOf<K>, error: { code: ErrorCode; message: string }): Notice;
  Content: ComponentType<KindContentProps<K>>;
  // 需要用户手势的请求（HandlerRegistry.executesInWindow）由窗口在执行按钮的点击里执行。执行期间倒计时照常显示：
  // 等的是用户在别处的操作，daemon 的期限并不暂停。
  inWindow?: { execute(detail: ApprovalDetails[K]): Promise<RpcOutcome> };
}

const bookmarkRemoval: ApprovalKindView<"bookmarks.remove"> = {
  windowTitle: (s) => s.bookmarkRemoval.windowTitle,
  title: (s, item) => s.bookmarkRemoval.title[item.status],
  irreversible: (s) => s.bookmarkRemoval.irreversible,
  approve: (s, item) => s.bookmarkRemoval.approve(item.detail.summary.items),
  executing: (s) => ({ label: s.bookmarkRemoval.deleting, hint: s.bookmarkRemoval.deletingHint }),
  nothingDone: (s) => s.bookmarkRemoval.nothingDeleted,
  done: (s, item, result) => {
    // 形状来自 handlers/bookmarks.ts 的 bookmarks.remove 执行结果。
    const { bookmarks, folders } = result as { bookmarks: number; folders: number };
    return {
      title: s.bookmarkRemoval.done(item.detail.summary.items),
      body: [s.bookmarkRemoval.doneDetail(bookmarks, folders)],
    };
  },
  failed: (s, _item, error) =>
    error.code === "CONFLICT"
      ? { title: s.bookmarkRemoval.conflictTitle, body: [s.bookmarkRemoval.conflictBody, s.approval.rerun] }
      : { title: s.bookmarkRemoval.failedTitle, body: [error.message] },
  Content: BookmarkRemovalContent,
};

const extensionUninstall: ApprovalKindView<"extensions.uninstall"> = {
  windowTitle: (s) => s.extensionUninstall.windowTitle,
  title: (s, item) => {
    const t = s.extensionUninstall.title;
    if (item.status !== "failed") return t[item.status];
    const code = item.outcome?.ok === false ? item.outcome.code : null;
    return code === "USER_REJECTED" ? t.declined : code === "NOT_FOUND" ? t.gone : t.failed;
  },
  irreversible: (s) => s.extensionUninstall.irreversible,
  approve: (s) => s.extensionUninstall.approve,
  approveHint: (s) => s.extensionUninstall.chromeHint,
  executing: (s) => ({
    label: s.extensionUninstall.waiting,
    hint: s.extensionUninstall.waitingFooter,
    notice: { title: s.extensionUninstall.waitingTitle, body: [s.extensionUninstall.waitingBody] },
  }),
  nothingDone: (s) => s.extensionUninstall.nothingUninstalled,
  done: (s, item) => ({
    title: s.extensionUninstall.done(item.detail.name),
    body: [s.extensionUninstall.doneDetail],
  }),
  failed: (s, _item, error) => {
    const t = s.extensionUninstall;
    switch (error.code) {
      case "USER_REJECTED":
        return { title: t.declinedTitle, body: [t.declinedBody, s.approval.rerun], tone: "warn" };
      case "NOT_FOUND":
        return { title: t.goneTitle, body: [t.goneBody] };
      default:
        return { title: t.failedTitle, body: [error.message] };
    }
  },
  Content: ExtensionUninstallContent,
  inWindow: { execute: (detail) => runApproved("extensions.uninstall", uninstallExtension, detail) },
};

// 新的请求类型在 shared/approvals.ts 的 ApprovalDetails 里登记后，tsc 要求在这里给出它的视图。
export const APPROVAL_KINDS: { [K in ApprovalKind]: ApprovalKindView<K> } = {
  "bookmarks.remove": bookmarkRemoval,
  "extensions.uninstall": extensionUninstall,
};

export function kindView<K extends ApprovalKind>(item: ApprovalItemOf<K>): ApprovalKindView<K> {
  return APPROVAL_KINDS[item.kind];
}
