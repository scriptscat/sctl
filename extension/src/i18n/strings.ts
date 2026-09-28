import { LIMITS } from "@/protocol/generated/protocol.generated";

export type Lang = "zh" | "en";
// 弹窗自带词典，语言可运行时切换，不能用 chrome.i18n（docs/specs 设计决策 13）。
export type LangPref = "browser" | Lang;
export type Appearance = "system" | "light" | "dark";

const codeTtlMinutes = Math.round(LIMITS.extPairingCodeTtlMs / 60000);

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
  nameInvalid: string;
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
  copied: string;
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
    nameInvalid: "名称只能包含小写字母、数字和 -，长度 1-32 个字符。",
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
    copied: "已复制",
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
    nameInvalid: "Names may only use lowercase letters, digits and -, 1-32 characters.",
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
    copied: "Copied",
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
