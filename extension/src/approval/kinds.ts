import type { ComponentType } from "react";
import type { Strings } from "@/i18n";
import type { ApprovalDetails, ApprovalItem, ApprovalKind, ApprovalStatus } from "@/shared/approvals";
import type { ErrorCode } from "@/shared/messages";
import { BookmarkRemovalContent } from "./bookmark-removal";

export type ApprovalItemOf<K extends ApprovalKind> = ApprovalItem & { kind: K; detail: ApprovalDetails[K] };

// 结束后的说明；body 是若干句话，由窗口按语言拼接。
export interface Notice {
  title: string;
  body: string[];
}

export interface KindContentProps<K extends ApprovalKind> {
  item: ApprovalItemOf<K>;
  s: Strings;
}

// 一种 L2 请求在审批窗口里的全部差异：文案与中间的内容区（影响摘要 + 逐项列表）。外框、请求方、倒计时、
// 队列、防诱导提示、拒绝/关闭按钮与各终态的处理都由窗口统一负责。
export interface ApprovalKindView<K extends ApprovalKind> {
  windowTitle(s: Strings): string;
  title(s: Strings, status: ApprovalStatus): string;
  // 等待决定与执行中时显示的不可撤销提示标题，防诱导提示作为它的正文。
  irreversible(s: Strings): string;
  approve(s: Strings, item: ApprovalItemOf<K>): string;
  executing(s: Strings): { label: string; hint: string };
  // 超时、取消、作废时说明什么都没有发生。
  nothingDone(s: Strings): string;
  done(s: Strings, item: ApprovalItemOf<K>, result: unknown): Notice;
  failed(s: Strings, item: ApprovalItemOf<K>, error: { code: ErrorCode; message: string }): Notice;
  Content: ComponentType<KindContentProps<K>>;
}

const bookmarkRemoval: ApprovalKindView<"bookmarks.remove"> = {
  windowTitle: (s) => s.bookmarkRemoval.windowTitle,
  title: (s, status) => s.bookmarkRemoval.title[status],
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

// 新的请求类型在 shared/approvals.ts 的 ApprovalDetails 里登记后，tsc 要求在这里给出它的视图。
export const APPROVAL_KINDS: { [K in ApprovalKind]: ApprovalKindView<K> } = {
  "bookmarks.remove": bookmarkRemoval,
};

export function kindView<K extends ApprovalKind>(item: ApprovalItemOf<K>): ApprovalKindView<K> {
  return APPROVAL_KINDS[item.kind];
}
