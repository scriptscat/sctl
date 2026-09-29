import { LIMITS } from "@/protocol/generated/protocol.generated";
import type { ApprovalStatus, BookmarkRemovalDetail } from "@/shared/approvals";

export type Lang = "zh" | "en";
// 弹窗自带词典，语言可运行时切换，不能用 chrome.i18n（docs/specs 设计决策 13）。
export type LangPref = "browser" | Lang;
export type Appearance = "system" | "light" | "dark";

const codeTtlMinutes = Math.round(LIMITS.extPairingCodeTtlMs / 60000);

type RemovalSummary = BookmarkRemovalDetail["summary"];

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

export interface Strings {
  product: string;
  tagline: string;
  settings: string;
  back: string;
  status: {
    unpaired: string;
    pairing: string;
    connected: string;
    reconnecting: string;
    rejected: string;
  };
  thisBrowser: string;
  step1: string;
  step2: string;
  pair: string;
  pairingBtn: string;
  codeHint: string;
  invalidCodeHint: string;
  pairFailedTitle: string;
  pairFailedBody: string;
  nameTakenTitle: string;
  nameTakenBody: (name: string) => string;
  unreachableTitle: string;
  unreachableBody: (addr: string) => string;
  instanceName: string;
  rename: string;
  save: string;
  cancel: string;
  nameHint: string;
  nameTaken: (name: string) => string;
  id: string;
  browser: string;
  daemon: string;
  since: string;
  sinceJustNow: string;
  sinceMinutes: (n: number) => string;
  sinceHours: (n: number) => string;
  useHint: string;
  copy: string;
  forget: string;
  forgetTitle: string;
  forgetBefore: string;
  forgetCommand: (name: string) => string;
  forgetAfter: string;
  reconnectTitle: string;
  reconnectBody: (retry: number, seconds: number, addr: string) => string;
  reconnectingNow: string;
  retry: string;
  rejectedTitle: string;
  rejectedBody: string;
  pairAgain: string;
  appearance: string;
  system: string;
  light: string;
  dark: string;
  language: string;
  followBrowser: string;
  daemonAddr: string;
  addrHint: string;
  addrInvalid: string;
  saveReconnect: string;
  applyNow: string;
  pendingApprovals: (n: number) => string;
  viewApprovals: string;
  approval: ApprovalStrings;
  bookmarkRemoval: BookmarkRemovalStrings;
  extensionUninstall: ExtensionUninstallStrings;
}

// 审批窗口里与请求类型无关的部分：外框、请求方、倒计时、队列、拒绝与关闭。
export interface ApprovalStrings {
  instance: (name: string) => string;
  queueNav: string;
  queuePending: (n: number) => string;
  pager: (index: number, total: number) => string;
  prev: string;
  next: string;
  from: string;
  selfReported: string;
  selfReportedHint: string;
  noRequester: string;
  received: (time: string) => string;
  countdown: (mmss: string) => string;
  antiLure: string;
  reject: string;
  close: string;
  // pending 是队列里等待决定的请求数：关窗会把它们全部拒绝。
  closeHint: (pending: number) => string;
  expiredTitle: string;
  cancelledTitle: string;
  voidedTitle: string;
  rerun: string;
  emptyTitle: string;
  emptyBody: string;
}

// 删除书签请求专用的文案。
export interface BookmarkRemovalStrings {
  windowTitle: string;
  title: Record<ApprovalStatus, string>;
  // requested=true 用于没有执行的终态，措辞从「将删除」改为「请求删除」。
  summary: (c: RemovalSummary, requested: boolean) => string;
  folderCounts: (bookmarks: number, folders: number) => string;
  preview: (name: string) => string;
  previewMore: (n: number) => string;
  listLabel: string;
  bookmark: string;
  folder: string;
  location: string;
  untitled: string;
  irreversible: string;
  approve: (n: number) => string;
  deleting: string;
  deletingHint: string;
  nothingDeleted: string;
  done: (n: number) => string;
  doneDetail: (bookmarks: number, folders: number) => string;
  conflictTitle: string;
  conflictBody: string;
  failedTitle: string;
}

// 卸载扩展请求专用的文案。declined 是在 Chrome 确认框里取消，gone 是点「卸载」时扩展已经不在。
export interface ExtensionUninstallStrings {
  windowTitle: string;
  title: Record<ApprovalStatus | "declined" | "gone", string>;
  cardLabel: string;
  version: (version: string) => string;
  stateLabel: string;
  enabled: string;
  disabled: string;
  missing: string;
  idLabel: string;
  copyId: string;
  installLabel: string;
  // 键是 chrome.management 的 installType；没有列出的取值原样显示。
  installType: Record<string, string>;
  irreversible: string;
  chromeHint: string;
  approve: string;
  waiting: string;
  waitingTitle: string;
  waitingBody: string;
  waitingFooter: string;
  nothingUninstalled: string;
  done: (name: string) => string;
  doneDetail: string;
  declinedTitle: string;
  declinedBody: string;
  goneTitle: string;
  goneBody: string;
  failedTitle: string;
}

export const dictionary: Record<Lang, Strings> = {
  zh: {
    product: "sctl Browser",
    tagline: "让 sctl 控制这个浏览器",
    settings: "设置",
    back: "返回",
    status: {
      unpaired: "未配对",
      pairing: "配对中",
      connected: "已连接",
      reconnecting: "重连中",
      rejected: "需要重新配对",
    },
    thisBrowser: "这个浏览器",
    step1: "在终端运行",
    step2: "输入终端显示的配对码",
    pair: "配对",
    pairingBtn: "正在配对…",
    codeHint: `配对码 ${codeTtlMinutes} 分钟内有效`,
    invalidCodeHint: "配对码格式不正确",
    pairFailedTitle: "配对失败",
    pairFailedBody: "配对码不正确或已过期。请重新运行 sctl connect 获取新的配对码。",
    nameTakenTitle: "默认名称已被占用",
    nameTakenBody: (name: string) =>
      `默认名称 ${name} 已被另一个已配对浏览器占用。请在该浏览器上重命名，或运行 sctl browsers forget ${name}，然后重新配对。`,
    unreachableTitle: "连不上 sctl serve",
    unreachableBody: (addr: string) => `${addr} 没有响应。请先在终端运行 sctl serve，或在设置里修改地址。`,
    instanceName: "实例名称",
    rename: "重命名",
    save: "保存",
    cancel: "取消",
    nameHint: "小写字母、数字和 -，最多 32 个字符。保存后会重新连接一次。",
    nameTaken: (name: string) => `名称 ${name} 已被另一个浏览器使用，换一个吧。`,
    id: "实例 ID",
    browser: "浏览器",
    daemon: "daemon",
    since: "已连接",
    sinceJustNow: "刚刚",
    sinceMinutes: (n: number) => `${n} 分钟`,
    sinceHours: (n: number) => `${n} 小时`,
    useHint: "在命令里指定这个浏览器",
    copy: "复制",
    forget: "断开并忘记",
    forgetTitle: "断开并忘记这个 daemon？",
    forgetBefore: "扩展会删除本地保存的密钥，之后需要重新运行 sctl connect 配对。daemon 那边的密钥要另外运行",
    forgetCommand: (name: string) => `sctl browsers forget ${name}`,
    forgetAfter: "删除。",
    reconnectTitle: "连不上 sctl serve",
    reconnectBody: (retry: number, seconds: number, addr: string) =>
      `${seconds} 秒后进行第 ${retry} 次重试。请确认 sctl serve 正在 ${addr} 运行。`,
    reconnectingNow: "正在重试…",
    retry: "立即重试",
    rejectedTitle: "daemon 不再认可这个浏览器",
    rejectedBody: "可能执行过 sctl browsers forget，或 sctl serve 换了数据目录。需要重新配对。",
    pairAgain: "重新配对",
    appearance: "外观",
    system: "跟随系统",
    light: "浅色",
    dark: "深色",
    language: "语言",
    followBrowser: "跟随浏览器",
    daemonAddr: "daemon 地址",
    addrHint: "要和 sctl serve 的 --listen-address 一致。保存后按新地址重连，配对保持不变。",
    addrInvalid: "地址格式不正确，应为 host:port。",
    saveReconnect: "保存并重连",
    applyNow: "外观和语言立即生效。",
    pendingApprovals: (n: number) => `${n} 个待批准请求`,
    viewApprovals: "查看",
    approval: {
      instance: (name: string) => `浏览器实例 ${name}`,
      queueNav: "请求队列",
      queuePending: (n: number) => `${n} 个待批准请求`,
      pager: (index: number, total: number) => `${index} / ${total}`,
      prev: "上一个请求",
      next: "下一个请求",
      from: "来自",
      selfReported: "（自报，未验证）",
      selfReportedHint: "这个标签由请求方自己填写，扩展无法核实它的真实身份。",
      noRequester: "未提供标签",
      received: (time: string) => `${time} 收到`,
      countdown: (mmss: string) => `${mmss} 后自动拒绝`,
      antiLure: "只在你确认这是自己发起的请求时批准。",
      reject: "拒绝",
      close: "关闭",
      closeHint: (pending: number) =>
        pending > 1 ? `关闭此窗口等同于拒绝全部 ${pending} 个请求` : "关闭此窗口等同于拒绝",
      expiredTitle: "已超时，已自动拒绝",
      cancelledTitle: "请求方已取消，此请求已失效",
      voidedTitle: "与 daemon 的连接已断开，此请求已失效",
      rerun: "仍需要时请重新运行命令。",
      emptyTitle: "没有待批准的请求",
      emptyBody: "所有请求都已处理，可以关闭这个窗口。",
    },
    bookmarkRemoval: {
      windowTitle: "批准删除书签 · sctl Browser",
      title: {
        pending: "批准删除书签？",
        executing: "正在删除书签",
        done: "已删除书签",
        failed: "未删除书签",
        expired: "请求已超时",
        cancelled: "请求已取消",
        voided: "请求已失效",
      },
      summary: (c: RemovalSummary, requested: boolean) => {
        const parts = [
          c.bookmarks > 0 ? `${c.bookmarks} 个书签` : "",
          c.folders > 0
            ? `${c.folders} 个文件夹（含 ${c.containedBookmarks} 个书签、${c.containedFolders} 个子文件夹）`
            : "",
        ];
        return `${requested ? "请求删除" : "将删除"} ${c.items} 项：${parts.filter(Boolean).join("、")}`;
      },
      folderCounts: (bookmarks: number, folders: number) => `含 ${bookmarks} 个书签、${folders} 个子文件夹`,
      preview: (name: string) => `预览「${name}」的内容`,
      previewMore: (n: number) => `还有 ${n} 项未列出`,
      listLabel: "将删除的项目",
      bookmark: "书签",
      folder: "文件夹",
      location: "所在文件夹",
      untitled: "（无标题）",
      irreversible: "删除后无法通过 sctl 撤销",
      approve: (n: number) => `删除 ${n} 项`,
      deleting: "正在删除…",
      deletingHint: "正在删除，请勿关闭窗口…",
      nothingDeleted: "未删除任何内容。",
      done: (n: number) => `已删除 ${n} 项`,
      doneDetail: (bookmarks: number, folders: number) => `共 ${bookmarks} 个书签、${folders} 个文件夹`,
      conflictTitle: "书签在此期间有变动，未删除任何内容",
      conflictBody: "删除是全有或全无的，整个请求已作废。",
      failedTitle: "删除失败",
    },
    extensionUninstall: {
      windowTitle: "批准卸载扩展 · sctl Browser",
      title: {
        pending: "批准卸载扩展？",
        executing: "等待 Chrome 确认",
        done: "已卸载扩展",
        failed: "未卸载扩展",
        declined: "未卸载",
        gone: "无法卸载",
        expired: "请求已超时",
        cancelled: "请求已取消",
        voided: "请求已失效",
      },
      cardLabel: "要卸载的扩展",
      version: (version: string) => `版本 ${version}`,
      stateLabel: "状态",
      enabled: "已启用",
      disabled: "已停用",
      missing: "已不存在",
      idLabel: "扩展 ID",
      copyId: "复制扩展 ID",
      installLabel: "安装方式",
      installType: {
        normal: "Chrome 应用商店",
        development: "开发者模式加载",
        sideload: "由其他程序安装",
        admin: "企业策略",
        other: "其他",
      },
      irreversible: "卸载后无法通过 sctl 恢复",
      chromeHint: "点「卸载」后 Chrome 还会弹出自己的确认框",
      approve: "卸载",
      waiting: "等待 Chrome…",
      waitingTitle: "请在 Chrome 的确认框中完成卸载",
      waitingBody: "在那里点「移除」才会真正卸载；点「取消」则保持原样。",
      waitingFooter: "结果以你在 Chrome 确认框中的选择为准",
      nothingUninstalled: "未卸载任何扩展。",
      done: (name: string) => `已卸载「${name}」`,
      doneDetail: "Chrome 已移除这个扩展。",
      declinedTitle: "已在 Chrome 中取消，未卸载",
      declinedBody: "扩展保持原样。",
      goneTitle: "该扩展已不存在",
      goneBody: "请求发出后它已被移除（例如在扩展管理页手动卸载），没有执行任何操作。",
      failedTitle: "卸载失败",
    },
  },
  en: {
    product: "sctl Browser",
    tagline: "Let sctl control this browser",
    settings: "Settings",
    back: "Back",
    status: {
      unpaired: "Not paired",
      pairing: "Pairing",
      connected: "Connected",
      reconnecting: "Reconnecting",
      rejected: "Pair again",
    },
    thisBrowser: "This browser",
    step1: "Run in your terminal",
    step2: "Enter the pairing code it shows",
    pair: "Pair",
    pairingBtn: "Pairing…",
    codeHint: `The code is valid for ${codeTtlMinutes} minutes`,
    invalidCodeHint: "That doesn't look like a pairing code",
    pairFailedTitle: "Pairing failed",
    pairFailedBody: "The code is wrong or has expired. Run sctl connect again to get a new one.",
    nameTakenTitle: "The default name is taken",
    nameTakenBody: (name: string) =>
      `The default name ${name} is already used by another paired browser. Rename it there, or run sctl browsers forget ${name}, then pair again.`,
    unreachableTitle: "Can't reach sctl serve",
    unreachableBody: (addr: string) =>
      `Nothing is answering at ${addr}. Start sctl serve in your terminal, or change the address in Settings.`,
    instanceName: "Instance name",
    rename: "Rename",
    save: "Save",
    cancel: "Cancel",
    nameHint: "Lowercase letters, digits and -, up to 32 characters. Saving reconnects once.",
    nameTaken: (name: string) => `Another browser is already named ${name}. Pick a different name.`,
    id: "Instance ID",
    browser: "Browser",
    daemon: "Daemon",
    since: "Connected for",
    sinceJustNow: "just now",
    sinceMinutes: (n: number) => `${n} min`,
    sinceHours: (n: number) => `${n} h`,
    useHint: "Target this browser in commands",
    copy: "Copy",
    forget: "Disconnect and forget",
    forgetTitle: "Disconnect and forget this daemon?",
    forgetBefore:
      "The extension deletes its saved key, so you'll need to run sctl connect to pair again. The daemon keeps its key until you run",
    forgetCommand: (name: string) => `sctl browsers forget ${name}`,
    forgetAfter: ".",
    reconnectTitle: "Can't reach sctl serve",
    reconnectBody: (retry: number, seconds: number, addr: string) =>
      `Retry ${retry} in ${seconds}s. Check that sctl serve is running at ${addr}.`,
    reconnectingNow: "Retrying…",
    retry: "Retry now",
    rejectedTitle: "The daemon no longer recognises this browser",
    rejectedBody:
      "It may have run sctl browsers forget, or sctl serve is using a different data directory. Pair again to reconnect.",
    pairAgain: "Pair again",
    appearance: "Appearance",
    system: "System",
    light: "Light",
    dark: "Dark",
    language: "Language",
    followBrowser: "Browser default",
    daemonAddr: "Daemon address",
    addrHint: "Must match sctl serve --listen-address. Saving reconnects to the new address; pairing is kept.",
    addrInvalid: "That doesn't look like host:port.",
    saveReconnect: "Save and reconnect",
    applyNow: "Appearance and language apply right away.",
    pendingApprovals: (n: number) => plural(n, "request awaiting approval", "requests awaiting approval"),
    viewApprovals: "View",
    approval: {
      instance: (name: string) => `Browser instance ${name}`,
      queueNav: "Request queue",
      queuePending: (n: number) => plural(n, "pending request", "pending requests"),
      pager: (index: number, total: number) => `${index} / ${total}`,
      prev: "Previous request",
      next: "Next request",
      from: "From",
      selfReported: "(self-reported, unverified)",
      selfReportedHint: "The requester chose this label itself; the extension can't verify who it really is.",
      noRequester: "no label given",
      received: (time: string) => `Received ${time}`,
      countdown: (mmss: string) => `Auto-rejects in ${mmss}`,
      antiLure: "Approve only if you started this request yourself.",
      reject: "Reject",
      close: "Close",
      closeHint: (pending: number) =>
        pending > 1 ? `Closing this window rejects all ${pending} requests` : "Closing this window rejects the request",
      expiredTitle: "Timed out and rejected automatically",
      cancelledTitle: "The requester cancelled; this request is void",
      voidedTitle: "Lost the connection to the daemon; this request is void",
      rerun: "Run the command again if you still need it.",
      emptyTitle: "No pending requests",
      emptyBody: "Every request has been handled. You can close this window.",
    },
    bookmarkRemoval: {
      windowTitle: "Approve deleting bookmarks · sctl Browser",
      title: {
        pending: "Approve deleting bookmarks?",
        executing: "Deleting bookmarks",
        done: "Bookmarks deleted",
        failed: "Bookmarks not deleted",
        expired: "Request timed out",
        cancelled: "Request cancelled",
        voided: "Request void",
      },
      summary: (c: RemovalSummary, requested: boolean) => {
        const parts = [
          c.bookmarks > 0 ? plural(c.bookmarks, "bookmark", "bookmarks") : "",
          c.folders > 0
            ? `${plural(c.folders, "folder", "folders")} (containing ${plural(c.containedBookmarks, "bookmark", "bookmarks")}, ${plural(c.containedFolders, "subfolder", "subfolders")})`
            : "",
        ];
        return `${requested ? "Asked to delete" : "Deletes"} ${plural(c.items, "item", "items")}: ${parts.filter(Boolean).join(", ")}`;
      },
      folderCounts: (bookmarks: number, folders: number) =>
        `Contains ${plural(bookmarks, "bookmark", "bookmarks")}, ${plural(folders, "subfolder", "subfolders")}`,
      preview: (name: string) => `Preview the contents of “${name}”`,
      previewMore: (n: number) => `${n} more not shown`,
      listLabel: "Items to delete",
      bookmark: "Bookmark",
      folder: "Folder",
      location: "In folder",
      untitled: "(untitled)",
      irreversible: "sctl can't undo this",
      approve: (n: number) => `Delete ${plural(n, "item", "items")}`,
      deleting: "Deleting…",
      deletingHint: "Deleting, keep this window open…",
      nothingDeleted: "Nothing was deleted.",
      done: (n: number) => `Deleted ${plural(n, "item", "items")}`,
      doneDetail: (bookmarks: number, folders: number) =>
        `${plural(bookmarks, "bookmark", "bookmarks")} and ${plural(folders, "folder", "folders")} in total`,
      conflictTitle: "Bookmarks changed in the meantime; nothing was deleted",
      conflictBody: "Deletion is all-or-nothing, so the whole request was dropped.",
      failedTitle: "Deletion failed",
    },
    extensionUninstall: {
      windowTitle: "Approve uninstalling an extension · sctl Browser",
      title: {
        pending: "Approve uninstalling this extension?",
        executing: "Waiting for Chrome",
        done: "Extension uninstalled",
        failed: "Extension not uninstalled",
        declined: "Not uninstalled",
        gone: "Can't uninstall",
        expired: "Request timed out",
        cancelled: "Request cancelled",
        voided: "Request void",
      },
      cardLabel: "Extension to uninstall",
      version: (version: string) => `Version ${version}`,
      stateLabel: "State",
      enabled: "Enabled",
      disabled: "Disabled",
      missing: "No longer installed",
      idLabel: "Extension ID",
      copyId: "Copy extension ID",
      installLabel: "Installed via",
      installType: {
        normal: "Chrome Web Store",
        development: "Loaded unpacked (developer mode)",
        sideload: "Installed by another program",
        admin: "Enterprise policy",
        other: "Other",
      },
      irreversible: "sctl can't bring it back once uninstalled",
      chromeHint: "After you click Uninstall, Chrome asks you to confirm in its own dialog",
      approve: "Uninstall",
      waiting: "Waiting for Chrome…",
      waitingTitle: "Finish uninstalling in Chrome's dialog",
      waitingBody: "Chrome uninstalls it only if you click Remove there; Cancel leaves it as it is.",
      waitingFooter: "The outcome follows your choice in Chrome's dialog",
      nothingUninstalled: "Nothing was uninstalled.",
      done: (name: string) => `Uninstalled “${name}”`,
      doneDetail: "Chrome removed the extension.",
      declinedTitle: "Cancelled in Chrome; nothing was uninstalled",
      declinedBody: "The extension is unchanged.",
      goneTitle: "The extension is no longer installed",
      goneBody: "It was removed after the request was made (for example on the extensions page); nothing was done.",
      failedTitle: "Uninstall failed",
    },
  },
} as const;

export function resolveLanguage(pref: LangPref, browserLanguage: string): Lang {
  if (pref !== "browser") {
    return pref;
  }
  return browserLanguage.toLowerCase().startsWith("zh") ? "zh" : "en";
}

export function stringsFor(lang: Lang): Strings {
  return dictionary[lang];
}
