// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { createElement } from "react";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type {
  ApprovalItem,
  ApprovalStatus,
  ApprovalView,
  BookmarkRemovalDetail,
  BookmarkRemovalItem,
} from "@/shared/approvals";
import type { RpcOutcome } from "@/shared/messages";
import type { KeyValueStorage } from "@/popup/storage";
import type { ApprovalApi } from "./api";
import { ApprovalApp } from "./App";

type Lang = "zh" | "en";

// 固定在本地时间 14:32:00，收到时间与倒计时都据此可预期。
const NOW = new Date(2026, 8, 29, 14, 32, 0).getTime();

function fakeApi(initial: ApprovalView) {
  let listeners: Array<(view: ApprovalView) => void> = [];
  const api = {
    view: vi.fn(() => Promise.resolve(initial)),
    subscribe: vi.fn((listener: (view: ApprovalView) => void) => {
      listeners.push(listener);
      return () => {
        listeners = listeners.filter((l) => l !== listener);
      };
    }),
    decide: vi.fn<(id: string, decision: "approve" | "reject") => Promise<void>>(() => Promise.resolve()),
    dismiss: vi.fn<(id: string) => Promise<void>>(() => Promise.resolve()),
    closeWindow: vi.fn(() => Promise.resolve()),
  } satisfies ApprovalApi;
  return {
    api,
    emit: (next: ApprovalView) => {
      for (const listener of listeners) listener(next);
    },
  };
}

function fakeLocalStorage(initial: Record<string, unknown>): KeyValueStorage {
  const data = { ...initial };
  return {
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]))),
    set: (items) => {
      Object.assign(data, items);
      return Promise.resolve();
    },
  };
}

function bookmark(id: string, title: string, url: string, path = ["书签栏", "工作"]): BookmarkRemovalItem {
  return { id, type: "bookmark", title, url, parentId: "p", path };
}

const ARCHIVE: BookmarkRemovalItem = {
  id: "f1",
  type: "folder",
  title: "2025 归档",
  parentId: "p",
  path: ["书签栏", "工作"],
  bookmarks: 37,
  folders: 4,
  contents: [
    { id: "q1", type: "folder", title: "Q1", parentId: "f1" },
    { id: "b9", type: "bookmark", title: "年终总结草稿", url: "https://docs.example.com/y", parentId: "q1" },
  ],
};

function detail(items: BookmarkRemovalItem[]): BookmarkRemovalDetail {
  const folders = items.filter((i) => i.type === "folder");
  return {
    summary: {
      items: items.length,
      bookmarks: items.length - folders.length,
      folders: folders.length,
      containedBookmarks: folders.reduce((n, f) => n + (f.type === "folder" ? f.bookmarks : 0), 0),
      containedFolders: folders.reduce((n, f) => n + (f.type === "folder" ? f.folders : 0), 0),
    },
    items,
  };
}

const BATCH = detail([
  bookmark("b1", "周报", "https://wiki.example.com/team/space/weekly/2026-38"),
  bookmark("b2", "设计评审记录", "https://docs.example.com/review"),
  ARCHIVE,
]);

function request(
  id: string,
  status: ApprovalStatus = "pending",
  d: BookmarkRemovalDetail = BATCH,
  extra: Partial<ApprovalItem> = {},
): ApprovalItem {
  return {
    id,
    kind: "bookmarks.remove",
    detail: d,
    requester: "sctl-cli",
    receivedAt: NOW - 60_000,
    expiresAt: NOW + 296_000,
    status,
    outcome: null,
    ...extra,
  };
}

function viewOf(...items: ApprovalItem[]): ApprovalView {
  return { browserName: "chrome-3f2a", items };
}

function renderWindow(
  view: ApprovalView,
  opts: { lang?: Lang; prefs?: Record<string, unknown>; now?: () => number } = {},
) {
  const wired = fakeApi(view);
  const result = render(
    createElement(ApprovalApp, {
      api: wired.api,
      localStorage: fakeLocalStorage(opts.prefs ?? { language: opts.lang ?? "en" }),
      browserLanguage: "en-US",
      systemPrefersDark: () => false,
      now: opts.now ?? (() => NOW),
    }),
  );
  return { ...result, ...wired };
}

// 每种状态在两种语言下的标题与说明。
const COPY = {
  zh: {
    title: {
      pending: "批准删除书签？",
      executing: "正在删除书签",
      done: "已删除书签",
      failed: "未删除书签",
      expired: "请求已超时",
      cancelled: "请求已取消",
      voided: "请求已失效",
    },
    summary: "将删除 3 项：2 个书签、1 个文件夹（含 37 个书签、4 个子文件夹）",
    requestedSummary: "请求删除 3 项：2 个书签、1 个文件夹（含 37 个书签、4 个子文件夹）",
    from: "来自",
    unverified: "（自报，未验证）",
    received: "14:31 收到",
    countdown: "4:56 后自动拒绝",
    irreversible: "删除后无法通过 sctl 撤销",
    antiLure: "只在你确认这是自己发起的请求时批准。",
    reject: "拒绝",
    approve: "删除 3 项",
    deleting: "正在删除…",
    closeHint: "关闭此窗口等同于拒绝",
    close: "关闭",
    done: "已删除 3 项",
    conflict: "书签在此期间有变动，未删除任何内容",
    failed: "删除失败",
    expired: "已超时，已自动拒绝",
    cancelled: "请求方已取消，此请求已失效",
    voided: "与 daemon 的连接已断开，此请求已失效",
    instance: "浏览器实例 chrome-3f2a",
    folderCounts: "含 37 个书签、4 个子文件夹",
    preview: "预览「2025 归档」的内容",
    list: "将删除的项目",
    pager: "1 / 3",
    next: "下一个请求",
    prev: "上一个请求",
    empty: "没有待批准的请求",
  },
  en: {
    title: {
      pending: "Approve deleting bookmarks?",
      executing: "Deleting bookmarks",
      done: "Bookmarks deleted",
      failed: "Bookmarks not deleted",
      expired: "Request timed out",
      cancelled: "Request cancelled",
      voided: "Request void",
    },
    summary: "Deletes 3 items: 2 bookmarks, 1 folder (containing 37 bookmarks, 4 subfolders)",
    requestedSummary: "Asked to delete 3 items: 2 bookmarks, 1 folder (containing 37 bookmarks, 4 subfolders)",
    from: "From",
    unverified: "(self-reported, unverified)",
    received: "Received 14:31",
    countdown: "Auto-rejects in 4:56",
    irreversible: "sctl can't undo this",
    antiLure: "Approve only if you started this request yourself.",
    reject: "Reject",
    approve: "Delete 3 items",
    deleting: "Deleting…",
    closeHint: "Closing this window rejects the request",
    close: "Close",
    done: "Deleted 3 items",
    conflict: "Bookmarks changed in the meantime; nothing was deleted",
    failed: "Deletion failed",
    expired: "Timed out and rejected automatically",
    cancelled: "The requester cancelled; this request is void",
    voided: "Lost the connection to the daemon; this request is void",
    instance: "Browser instance chrome-3f2a",
    folderCounts: "Contains 37 bookmarks, 4 subfolders",
    preview: "Preview the contents of “2025 归档”",
    list: "Items to delete",
    pager: "1 / 3",
    next: "Next request",
    prev: "Previous request",
    empty: "No pending requests",
  },
} as const;

const LANGS = ["zh", "en"] as const;

beforeEach(() => {
  vi.stubGlobal("navigator", { ...navigator, language: "en-US" });
});

afterEach(() => {
  cleanup();
  vi.useRealTimers();
  vi.unstubAllGlobals();
  document.documentElement.classList.remove("dark");
});

describe("approval window states (zh and en)", () => {
  it.each(LANGS)("renders a pending bookmark removal with every required element in %s", async (lang) => {
    const c = COPY[lang];
    renderWindow(viewOf(request("r1")), { lang });

    expect(await screen.findByRole("heading", { level: 1, name: c.title.pending })).toBeInTheDocument();
    expect(screen.getByLabelText(c.instance)).toHaveTextContent("chrome-3f2a");
    expect(screen.getByText("sctl-cli")).toBeInTheDocument();
    expect(screen.getByText(c.unverified)).toBeInTheDocument();
    expect(screen.getByText(c.received)).toBeInTheDocument();
    expect(screen.getByText(c.countdown)).toBeInTheDocument();
    expect(screen.getByText(c.summary)).toBeInTheDocument();
    expect(screen.getByText(c.irreversible)).toBeInTheDocument();
    expect(screen.getByText(c.antiLure)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: c.reject })).toBeEnabled();
    expect(screen.getByRole("button", { name: c.approve })).toBeEnabled();
    expect(screen.getByText(c.closeHint)).toBeInTheDocument();
    expect(document.title).toBe(
      lang === "zh" ? "批准删除书签 · sctl Browser" : "Approve deleting bookmarks · sctl Browser",
    );
  });

  it.each(LANGS)(
    "lists each bookmark with title, full URL and folder path, and each folder with its counts in %s",
    async (lang) => {
      const c = COPY[lang];
      const user = userEvent.setup();
      renderWindow(viewOf(request("r1")), { lang });

      const list = await screen.findByRole("list", { name: c.list });
      const url = within(list).getByText("https://wiki.example.com/team/space/weekly/2026-38");
      expect(url).toHaveAttribute("title", "https://wiki.example.com/team/space/weekly/2026-38");
      expect(within(list).getByText("周报")).toBeInTheDocument();
      expect(within(list).getAllByText("书签栏 / 工作")).toHaveLength(3);
      expect(within(list).getByText(c.folderCounts)).toBeInTheDocument();

      const expand = within(list).getByRole("button", { name: c.preview });
      expect(expand).toHaveAttribute("aria-expanded", "false");
      expect(within(list).queryByText("年终总结草稿")).not.toBeInTheDocument();
      await user.click(expand);
      expect(expand).toHaveAttribute("aria-expanded", "true");
      expect(within(list).getByText("Q1")).toBeInTheDocument();
      expect(within(list).getByText("年终总结草稿")).toBeInTheDocument();
    },
  );

  it.each(LANGS)("renders the executing state with both decisions disabled in %s", async (lang) => {
    const c = COPY[lang];
    renderWindow(viewOf(request("r1", "executing")), { lang });

    await screen.findByRole("heading", { level: 1, name: c.title.executing });
    expect(screen.getByRole("button", { name: c.reject })).toBeDisabled();
    expect(screen.getByRole("button", { name: c.deleting })).toBeDisabled();
    expect(screen.queryByText(c.countdown)).not.toBeInTheDocument();
  });

  it.each(LANGS)("renders the done state with a close action that dismisses it in %s", async (lang) => {
    const c = COPY[lang];
    const user = userEvent.setup();
    const outcome: RpcOutcome = { ok: true, result: { ids: ["b1", "b2", "f1"], bookmarks: 39, folders: 5 } };
    const { api } = renderWindow(viewOf(request("r1", "done", BATCH, { outcome })), { lang });

    await screen.findByRole("heading", { level: 1, name: c.title.done });
    expect(screen.getByRole("status")).toHaveTextContent(c.done);
    expect(screen.queryByRole("button", { name: c.reject })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: c.close }));
    expect(api.dismiss).toHaveBeenCalledWith("r1");
    expect(api.decide).not.toHaveBeenCalled();
  });

  it.each(LANGS)("explains a CONFLICT failure as nothing deleted in %s", async (lang) => {
    const c = COPY[lang];
    const outcome: RpcOutcome = { ok: false, code: "CONFLICT", message: "bookmark b1 changed" };
    renderWindow(viewOf(request("r1", "failed", BATCH, { outcome })), { lang });

    await screen.findByRole("heading", { level: 1, name: c.title.failed });
    expect(screen.getByRole("alert")).toHaveTextContent(c.conflict);
    expect(screen.getByText(c.requestedSummary)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: c.close })).toBeInTheDocument();
  });

  it.each(LANGS)("shows the error message of any other failure in %s", async (lang) => {
    const c = COPY[lang];
    const outcome: RpcOutcome = { ok: false, code: "INTERNAL_ERROR", message: "the extension restarted" };
    renderWindow(viewOf(request("r1", "failed", BATCH, { outcome })), { lang });

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(c.failed);
    expect(alert).toHaveTextContent("the extension restarted");
  });

  it.each(
    LANGS.flatMap((lang) => (["expired", "cancelled", "voided"] as const).map((status) => [lang, status] as const)),
  )("renders the %s %s state with only a close action", async (lang, status) => {
    const c = COPY[lang];
    const user = userEvent.setup();
    const { api } = renderWindow(viewOf(request("r1", status)), { lang });

    await screen.findByRole("heading", { level: 1, name: c.title[status] });
    expect(screen.getByText(c[status])).toBeInTheDocument();
    expect(screen.getByText(c.requestedSummary)).toBeInTheDocument();
    expect(screen.queryByText(c.countdown)).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: c.reject })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: c.approve })).not.toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: c.close }));
    expect(api.dismiss).toHaveBeenCalledWith("r1");
  });

  it.each(LANGS)("renders an empty queue with a close action in %s", async (lang) => {
    const c = COPY[lang];
    const user = userEvent.setup();
    const { api } = renderWindow(viewOf(), { lang });

    await screen.findByRole("heading", { level: 1, name: c.empty });
    await user.click(screen.getByRole("button", { name: c.close }));
    expect(api.closeWindow).toHaveBeenCalled();
  });

  it("marks a missing requester label instead of leaving it blank", async () => {
    renderWindow(viewOf(request("r1", "pending", BATCH, { requester: null })), { lang: "en" });
    expect(await screen.findByText("no label given")).toBeInTheDocument();
  });

  it("warns that closing rejects every pending request when several are queued", async () => {
    const outcome: RpcOutcome = { ok: true, result: { ids: ["b1"], bookmarks: 1, folders: 0 } };
    renderWindow(viewOf(request("r1"), request("r2"), request("r3", "done", BATCH, { outcome })), { lang: "zh" });
    expect(await screen.findByText("关闭此窗口等同于拒绝全部 2 个请求")).toBeInTheDocument();
  });
});

describe("decisions", () => {
  it("sends reject and approve for the request on screen", async () => {
    const user = userEvent.setup();
    const { api } = renderWindow(viewOf(request("r1")), { lang: "en" });

    await user.click(await screen.findByRole("button", { name: "Reject" }));
    expect(api.decide).toHaveBeenLastCalledWith("r1", "reject");
    await user.click(screen.getByRole("button", { name: "Delete 3 items" }));
    expect(api.decide).toHaveBeenLastCalledWith("r1", "approve");
  });

  it("does nothing when Esc is pressed", async () => {
    const user = userEvent.setup();
    const { api } = renderWindow(viewOf(request("r1")), { lang: "en" });

    await screen.findByRole("button", { name: "Reject" });
    await user.keyboard("{Escape}");
    expect(api.decide).not.toHaveBeenCalled();
    expect(api.dismiss).not.toHaveBeenCalled();
    expect(api.closeWindow).not.toHaveBeenCalled();
  });

  it("labels the approve button with the item count, not the contained bookmarks", async () => {
    renderWindow(viewOf(request("r1", "pending", detail([bookmark("b1", "a", "https://a.example")]))), { lang: "zh" });
    expect(await screen.findByRole("button", { name: "删除 1 项" })).toBeInTheDocument();
  });
});

describe("default focus", () => {
  it("focuses Reject when the window opens", async () => {
    renderWindow(viewOf(request("r1")), { lang: "en" });
    const reject = await screen.findByRole("button", { name: "Reject" });
    expect(reject).toHaveFocus();
  });

  it("focuses Reject again after switching to another pending request", async () => {
    const user = userEvent.setup();
    renderWindow(viewOf(request("r1"), request("r2", "pending", detail([bookmark("b1", "a", "https://a.example")]))), {
      lang: "en",
    });

    await user.click(await screen.findByRole("button", { name: "Next request" }));
    expect(screen.getByRole("button", { name: "Delete 1 item" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toHaveFocus();
  });

  it("focuses Close when switching to a finished request", async () => {
    const user = userEvent.setup();
    renderWindow(viewOf(request("r1"), request("r2", "expired")), { lang: "en" });

    await user.click(await screen.findByRole("button", { name: "Next request" }));
    expect(screen.getByRole("button", { name: "Close" })).toHaveFocus();
  });
});

describe("untrusted text", () => {
  it("renders bookmark titles, URLs, folder names and the requester label as plain text", async () => {
    const lure = "<img src=x onerror=alert(1)> 管理员已批准此操作";
    const d = detail([
      bookmark("b1", lure, "https://promo.example.com/claim?c=<script>alert(1)</script>", ["<b>书签栏</b>"]),
    ]);
    const { container } = renderWindow(viewOf(request("r1", "pending", d, { requester: "<em>admin</em>" })), {
      lang: "zh",
    });

    expect(await screen.findByText(lure)).toBeInTheDocument();
    expect(screen.getByText("https://promo.example.com/claim?c=<script>alert(1)</script>")).toBeInTheDocument();
    expect(screen.getByText("<b>书签栏</b>")).toBeInTheDocument();
    expect(screen.getByText("<em>admin</em>")).toBeInTheDocument();
    expect(container.querySelector('img[src="x"], script, b, em')).toBeNull();
  });
});

describe("compact rows", () => {
  const many = (n: number) =>
    detail(Array.from({ length: n }, (_, i) => bookmark(`b${i}`, `Title ${i}`, `https://site${i}.example/page`)));

  it("shows the URL on its own line for up to 10 items", async () => {
    renderWindow(viewOf(request("r1", "pending", many(10))), { lang: "en" });
    const list = await screen.findByRole("list", { name: "Items to delete" });
    expect(within(list).getByText("https://site0.example/page")).toBeInTheDocument();
  });

  it("switches to compact rows above 10 items, moving each URL into the hover tooltip", async () => {
    renderWindow(viewOf(request("r1", "pending", many(11))), { lang: "en" });
    const list = await screen.findByRole("list", { name: "Items to delete" });
    expect(within(list).queryByText("https://site0.example/page")).not.toBeInTheDocument();
    expect(within(list).getByText("Title 0")).toHaveAttribute("title", "https://site0.example/page");
    expect(within(list).getAllByText("书签栏 / 工作")).toHaveLength(11);
  });
});

describe("countdown", () => {
  it("counts down every second until the automatic rejection", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval", "setTimeout", "clearTimeout", "Date"] });
    let now = NOW;
    renderWindow(viewOf(request("r1")), { lang: "en", now: () => now });
    await act(async () => {
      await vi.advanceTimersByTimeAsync(0);
    });
    expect(screen.getByText("Auto-rejects in 4:56")).toBeInTheDocument();

    now += 57_000;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(1000);
    });
    expect(screen.getByText("Auto-rejects in 3:59")).toBeInTheDocument();
  });

  it("never shows a negative countdown", async () => {
    renderWindow(viewOf(request("r1", "pending", BATCH, { expiresAt: NOW - 5000 })), { lang: "en" });
    expect(await screen.findByText("Auto-rejects in 0:00")).toBeInTheDocument();
  });
});

describe("queue pager", () => {
  it("pages through queued requests in arrival order", async () => {
    const user = userEvent.setup();
    renderWindow(
      viewOf(
        request("r1"),
        request("r2", "pending", detail([bookmark("b1", "a", "https://a.example")])),
        request("r3", "cancelled"),
      ),
      { lang: "zh" },
    );

    const pager = await screen.findByRole("navigation");
    expect(pager).toHaveTextContent("1 / 3");
    expect(within(pager).getByText("2 个待批准请求")).toBeInTheDocument();
    expect(within(pager).getByRole("button", { name: "上一个请求" })).toBeDisabled();
    await user.click(within(pager).getByRole("button", { name: "下一个请求" }));
    expect(pager).toHaveTextContent("2 / 3");
    expect(screen.getByRole("button", { name: "删除 1 项" })).toBeInTheDocument();
    await user.click(within(pager).getByRole("button", { name: "下一个请求" }));
    expect(pager).toHaveTextContent("3 / 3");
    expect(within(pager).getByRole("button", { name: "下一个请求" })).toBeDisabled();
  });

  it("has no pager for a single request", async () => {
    renderWindow(viewOf(request("r1")), { lang: "en" });
    await screen.findByRole("button", { name: "Reject" });
    expect(screen.queryByRole("navigation")).not.toBeInTheDocument();
  });

  it("stays on the request being read when a new one arrives", async () => {
    const user = userEvent.setup();
    const { emit } = renderWindow(viewOf(request("r1"), request("r2", "expired")), { lang: "en" });
    await user.click(await screen.findByRole("button", { name: "Next request" }));

    act(() => emit(viewOf(request("r1"), request("r2", "expired"), request("r3"))));
    expect(screen.getByRole("navigation")).toHaveTextContent("2 / 3");
    expect(screen.getByRole("heading", { level: 1, name: "Request timed out" })).toBeInTheDocument();
  });

  it("moves to the next request once the one on screen leaves the queue", async () => {
    const one = detail([bookmark("b1", "a", "https://a.example")]);
    const { emit } = renderWindow(viewOf(request("r1"), request("r2", "pending", one)), { lang: "en" });
    await screen.findByRole("button", { name: "Delete 3 items" });

    act(() => emit(viewOf(request("r2", "pending", one))));
    expect(screen.getByRole("button", { name: "Delete 1 item" })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reject" })).toHaveFocus();
  });

  it("keeps a broadcast that arrives before the initial view reply", async () => {
    const wired = fakeApi(viewOf(request("r1")));
    let answer: (view: ApprovalView) => void = () => undefined;
    wired.api.view.mockReturnValue(new Promise((resolve) => (answer = resolve)));
    render(
      createElement(ApprovalApp, {
        api: wired.api,
        localStorage: fakeLocalStorage({ language: "en" }),
        browserLanguage: "en-US",
        systemPrefersDark: () => false,
        now: () => NOW,
      }),
    );

    const outcome: RpcOutcome = { ok: true, result: { ids: ["b1", "b2", "f1"], bookmarks: 39, folders: 5 } };
    act(() => wired.emit(viewOf(request("r1", "done", BATCH, { outcome }))));
    await act(() => {
      answer(viewOf(request("r1")));
      return Promise.resolve();
    });
    expect(screen.getByRole("heading", { level: 1, name: "Bookmarks deleted" })).toBeInTheDocument();
  });
});

describe("preferences shared with the popup", () => {
  it("uses the dark appearance and language saved by the popup", async () => {
    renderWindow(viewOf(request("r1")), { prefs: { appearance: "dark", language: "zh" } });
    await screen.findByRole("heading", { level: 1, name: "批准删除书签？" });
    expect(document.documentElement).toHaveClass("dark");
    expect(document.documentElement.lang).toBe("zh");
  });

  it("follows the browser language by default", async () => {
    render(
      createElement(ApprovalApp, {
        api: fakeApi(viewOf(request("r1"))).api,
        localStorage: fakeLocalStorage({}),
        browserLanguage: "zh-CN",
        systemPrefersDark: () => false,
        now: () => NOW,
      }),
    );
    expect(await screen.findByRole("heading", { level: 1, name: "批准删除书签？" })).toBeInTheDocument();
    expect(document.documentElement).not.toHaveClass("dark");
  });
});
