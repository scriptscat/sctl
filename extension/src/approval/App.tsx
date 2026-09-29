import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState, type RefObject } from "react";
import {
  AlertCircle,
  Ban,
  CheckCircle2,
  ChevronLeft,
  ChevronRight,
  Globe,
  Inbox,
  Loader2,
  Timer,
  TimerOff,
  TriangleAlert,
  Unplug,
} from "lucide-react";
import { Note } from "@/components/popup/note";
import { Button } from "@/components/ui/button";
import { resolveLanguage, stringsFor, type Lang, type Strings } from "@/i18n";
import { cn } from "@/lib/utils";
import { isDarkMode, loadPrefs, type Prefs } from "@/popup/preferences";
import { chromeLocalStorage, type KeyValueStorage } from "@/popup/storage";
import type { ApprovalItem, ApprovalKind, ApprovalView } from "@/shared/approvals";
import type { ErrorCode } from "@/shared/messages";
import { createApprovalApi, type ApprovalApi } from "./api";
import { kindView, type ApprovalItemOf, type ApprovalKindView, type Notice } from "./kinds";

const mono = "font-[family-name:var(--font-mono)]";

// 与弹窗「断开并忘记」确认按钮同一组令牌：浅色白字、深色深字，都满足 4.5:1。焦点框沿用全局的主色环并留出间隙：
// 红色环在深色背景上几乎看不见，而这个按钮最需要一眼看出焦点在不在它上面。
const danger =
  "bg-[var(--bad)] text-[var(--bad-foreground)] hover:bg-[var(--bad-hover)] focus-visible:ring-offset-2 focus-visible:ring-offset-background";

// 倒计时少于这么多秒时改用警示色。
const URGENT_SECONDS = 60;

export interface ApprovalAppProps {
  api?: ApprovalApi;
  localStorage?: KeyValueStorage;
  // 用于测试注入固定值；默认取真实环境。
  browserLanguage?: string;
  systemPrefersDark?: () => boolean;
  now?: () => number;
}

function pad(n: number): string {
  return String(n).padStart(2, "0");
}

function clockTime(ms: number): string {
  const d = new Date(ms);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

function mmss(seconds: number): string {
  return `${Math.floor(seconds / 60)}:${pad(seconds % 60)}`;
}

function sentences(lang: Lang, parts: string[]): string {
  return parts.join(lang === "zh" ? "" : " ");
}

export function ApprovalApp({
  api: injectedApi,
  localStorage = chromeLocalStorage,
  browserLanguage = typeof navigator === "undefined" ? "en" : navigator.language,
  systemPrefersDark = () => typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches,
  now = () => Date.now(),
}: ApprovalAppProps) {
  const api = useMemo(() => injectedApi ?? createApprovalApi(), [injectedApi]);
  const [view, setView] = useState<ApprovalView | null>(null);
  const [prefs, setPrefs] = useState<Prefs | null>(null);

  useEffect(() => {
    let cancelled = false;
    // 视图的回复要经过 service worker 的队列，广播可能先到；先到的广播更新，不能被回复覆盖。
    let broadcastSeen = false;
    api.view().then(
      (v) => {
        if (!cancelled && !broadcastSeen) setView(v);
      },
      (error: unknown) => console.error("failed to read the approval queue", error),
    );
    const unsubscribe = api.subscribe((v) => {
      broadcastSeen = true;
      if (!cancelled) setView(v);
    });
    void loadPrefs(localStorage).then((p) => {
      if (!cancelled) setPrefs(p);
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
    // 只在挂载时执行一次：api/localStorage 在窗口生命周期内不会更换。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 外观和语言跟随弹窗里的设置（docs/specs「审批窗口」视觉）。
  const lang = resolveLanguage(prefs?.language ?? "browser", browserLanguage);
  useEffect(() => {
    if (!prefs) return;
    document.documentElement.classList.toggle("dark", isDarkMode(prefs.appearance, systemPrefersDark()));
    document.documentElement.lang = lang;
  }, [prefs, lang, systemPrefersDark]);
  const s = stringsFor(lang);

  // 当前请求按 id 跟踪：新请求到达时停在原处；当前请求离开队列后，顶上来的是同一位置的下一个。
  const [selected, setSelected] = useState({ id: "", index: 0 });
  const items = view?.items ?? [];
  const found = items.findIndex((item) => item.id === selected.id);
  const index = found >= 0 ? found : Math.max(0, Math.min(selected.index, items.length - 1));
  const current: ApprovalItem | undefined = items[index];
  if (current && (current.id !== selected.id || index !== selected.index)) {
    setSelected({ id: current.id, index });
  }

  const title = current ? kindView(current).windowTitle(s) : s.product;
  useLayoutEffect(() => {
    document.title = title;
  }, [title]);

  // 窗口打开、切换请求或当前请求结束时，焦点落在安全的按钮上：等待中是「拒绝」，结束后是「关闭」。
  const safeRef = useRef<HTMLButtonElement>(null);
  const focusKey = view && prefs ? `${current?.id ?? ""}:${current?.status ?? "empty"}` : null;
  // 在绘制前移动焦点：键盘用户按下的第一个键不能落在别处。
  useLayoutEffect(() => {
    if (focusKey !== null) safeRef.current?.focus();
  }, [focusKey]);

  if (!view || !prefs) {
    return <main className="p-4 text-sm text-muted-foreground">{s.product}</main>;
  }

  const pending = items.filter((item) => item.status === "pending").length;
  const go = (i: number) => setSelected({ id: items[i].id, index: i });

  return (
    <div className="flex h-dvh flex-col bg-background text-foreground">
      <header className="flex shrink-0 items-center gap-2 px-4 pt-3 pb-2">
        <img src="/icons/icon-48.png" alt="" className="size-6 shrink-0" draggable={false} />
        <span className="text-sm font-semibold">{s.product}</span>
        <span className="flex-1" />
        {view.browserName && (
          <span
            role="group"
            aria-label={s.approval.instance(view.browserName)}
            className={cn(
              "inline-flex h-6 max-w-[55%] items-center gap-1 rounded-full bg-[var(--surface)] px-2 text-xs",
              mono,
            )}
          >
            <Globe className="size-3.5 shrink-0 text-[var(--brand)]" aria-hidden />
            <span className="truncate">{view.browserName}</span>
          </span>
        )}
      </header>

      {items.length > 1 && (
        <nav
          aria-label={s.approval.queueNav}
          className="mx-4 mb-1 flex shrink-0 items-center justify-between rounded-lg bg-[var(--surface)] py-0.5 pr-0.5 pl-3 text-xs"
        >
          <span className="font-medium">{pending > 0 ? s.approval.queuePending(pending) : ""}</span>
          <span className="flex items-center gap-0.5">
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={s.approval.prev}
              disabled={index === 0}
              onClick={() => go(index - 1)}
            >
              <ChevronLeft aria-hidden />
            </Button>
            <span className="min-w-10 text-center tabular-nums">{s.approval.pager(index + 1, items.length)}</span>
            <Button
              variant="ghost"
              size="icon-xs"
              aria-label={s.approval.next}
              disabled={index >= items.length - 1}
              onClick={() => go(index + 1)}
            >
              <ChevronRight aria-hidden />
            </Button>
          </span>
        </nav>
      )}

      {current ? (
        <Request
          key={current.id}
          item={current}
          s={s}
          lang={lang}
          now={now}
          pending={pending}
          safeRef={safeRef}
          onDecide={(decision) => {
            api.decide(current.id, decision).catch((error: unknown) => console.error("failed to decide", error));
          }}
          onDismiss={() => {
            api.dismiss(current.id).catch((error: unknown) => console.error("failed to dismiss", error));
          }}
        />
      ) : (
        <Empty
          s={s}
          safeRef={safeRef}
          onClose={() => {
            api.closeWindow().catch((error: unknown) => console.error("failed to close the window", error));
          }}
        />
      )}
    </div>
  );
}

function Empty({
  s,
  safeRef,
  onClose,
}: {
  s: Strings;
  safeRef: RefObject<HTMLButtonElement | null>;
  onClose: () => void;
}) {
  return (
    <main className="flex flex-1 flex-col items-center justify-center gap-2 px-6 text-center">
      <Inbox className="size-8 text-muted-foreground" aria-hidden />
      <h1 className="text-base font-semibold">{s.approval.emptyTitle}</h1>
      <p className="text-xs text-muted-foreground">{s.approval.emptyBody}</p>
      <Button ref={safeRef} variant="outline" className="mt-2" onClick={onClose}>
        {s.approval.close}
      </Button>
    </main>
  );
}

// 等待中的请求每秒刷新一次倒计时；tick 只用来触发重渲染。
function useSecondTicks(active: boolean): void {
  const [, tick] = useState(0);
  useEffect(() => {
    if (!active) return;
    const id = setInterval(() => tick((t) => t + 1), 1000);
    return () => clearInterval(id);
  }, [active]);
}

function Request<K extends ApprovalKind>({
  item,
  s,
  lang,
  now,
  pending,
  safeRef,
  onDecide,
  onDismiss,
}: {
  item: ApprovalItemOf<K>;
  s: Strings;
  lang: Lang;
  now: () => number;
  pending: number;
  safeRef: RefObject<HTMLButtonElement | null>;
  onDecide: (decision: "approve" | "reject") => void;
  onDismiss: () => void;
}) {
  const kind = kindView(item);
  const titleId = useId();
  const waiting = item.status === "pending";
  const executing = item.status === "executing";
  useSecondTicks(waiting);
  const remaining = Math.max(0, Math.ceil((item.expiresAt - now()) / 1000));
  const Content = kind.Content;

  return (
    <main aria-labelledby={titleId} className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-col gap-1 px-4 pt-2">
        <h1 id={titleId} className="text-lg leading-snug font-semibold">
          {kind.title(s, item.status)}
        </h1>
        <p className="flex flex-wrap items-center gap-x-1 text-xs text-muted-foreground">
          <span>{s.approval.from}</span>
          <span
            className={cn(
              "max-w-[60%] truncate rounded border border-dashed border-[var(--muted-foreground)] px-1",
              item.requester === null ? "italic" : cn("text-foreground", mono),
            )}
            title={s.approval.selfReportedHint}
          >
            {item.requester ?? s.approval.noRequester}
          </span>
          <span title={s.approval.selfReportedHint}>{s.approval.selfReported}</span>
        </p>
        <p className="flex flex-wrap items-center gap-x-2 text-xs text-muted-foreground">
          <span>{s.approval.received(clockTime(item.receivedAt))}</span>
          {waiting && (
            <>
              <span aria-hidden>·</span>
              <span
                className={cn(
                  "inline-flex items-center gap-1 tabular-nums",
                  remaining < URGENT_SECONDS ? "font-medium text-[var(--warn)]" : "text-foreground",
                )}
              >
                <Timer className="size-3.5" aria-hidden />
                {s.approval.countdown(mmss(remaining))}
              </span>
            </>
          )}
        </p>
      </div>

      <Content item={item} s={s} />

      <div className="shrink-0 px-4 py-3">
        <Outcome item={item} s={s} lang={lang} kind={kind} />
      </div>

      <footer className="mt-auto flex shrink-0 flex-col gap-2 border-t px-4 pt-3 pb-3">
        {waiting || executing ? (
          <>
            <div className="grid grid-cols-2 gap-2">
              <Button ref={safeRef} variant="outline" size="lg" disabled={executing} onClick={() => onDecide("reject")}>
                {s.approval.reject}
              </Button>
              <Button size="lg" className={danger} disabled={executing} onClick={() => onDecide("approve")}>
                {executing ? (
                  <>
                    <Loader2 className="animate-spin" aria-hidden />
                    {kind.executing(s).label}
                  </>
                ) : (
                  kind.approve(s, item)
                )}
              </Button>
            </div>
            <p className="text-center text-[11px] text-muted-foreground" role={executing ? "status" : undefined}>
              {executing ? kind.executing(s).hint : s.approval.closeHint(pending)}
            </p>
          </>
        ) : (
          <Button ref={safeRef} size="lg" className="w-full" onClick={onDismiss}>
            {s.approval.close}
          </Button>
        )}
      </footer>
    </main>
  );
}

function Outcome<K extends ApprovalKind>({
  item,
  s,
  lang,
  kind,
}: {
  item: ApprovalItemOf<K>;
  s: Strings;
  lang: Lang;
  kind: ApprovalKindView<K>;
}) {
  switch (item.status) {
    case "pending":
    case "executing":
      return (
        <Note tone="warn" icon={TriangleAlert} title={kind.irreversible(s)}>
          {s.approval.antiLure}
        </Note>
      );
    case "expired":
      return (
        <Note tone="warn" icon={TimerOff} title={s.approval.expiredTitle}>
          {sentences(lang, [kind.nothingDone(s), s.approval.rerun])}
        </Note>
      );
    case "cancelled":
      return (
        <Note tone="warn" icon={Ban} title={s.approval.cancelledTitle}>
          {kind.nothingDone(s)}
        </Note>
      );
    case "voided":
      return (
        <Note tone="warn" icon={Unplug} title={s.approval.voidedTitle}>
          {sentences(lang, [kind.nothingDone(s), s.approval.rerun])}
        </Note>
      );
    case "done": {
      const notice: Notice = kind.done(s, item, resultOf(item));
      return (
        <div role="status" className="flex gap-2.5 rounded-lg border px-3 py-2.5 text-xs">
          <CheckCircle2 className="mt-0.5 size-3.5 shrink-0 text-[var(--ok)]" aria-hidden />
          <div className="flex flex-col gap-0.5">
            <span className="font-medium">{notice.title}</span>
            <span className="text-muted-foreground">{sentences(lang, notice.body)}</span>
          </div>
        </div>
      );
    }
    case "failed": {
      const notice = kind.failed(s, item, errorOf(item));
      return (
        <Note tone="bad" icon={AlertCircle} title={notice.title}>
          {sentences(lang, notice.body)}
        </Note>
      );
    }
  }
}

// 执行的结论只在 done/failed 上出现，由 service worker 与状态一同写入；缺了说明状态机出错，不能假装成功或失败。
function resultOf(item: ApprovalItem): unknown {
  if (item.outcome?.ok !== true) {
    throw new Error(`approval ${item.id} is done without a successful outcome`);
  }
  return item.outcome.result;
}

function errorOf(item: ApprovalItem): { code: ErrorCode; message: string } {
  if (item.outcome?.ok !== false) {
    throw new Error(`approval ${item.id} failed without an error outcome`);
  }
  return item.outcome;
}
