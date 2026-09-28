import { useCallback, useEffect, useMemo, useState } from "react";
import {
  AlertCircle,
  ArrowLeft,
  Check,
  Loader2,
  Pencil,
  RefreshCw,
  Settings as SettingsIcon,
  ShieldAlert,
  Unplug,
  X,
} from "lucide-react";
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogFooter,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { CommandLine, CopyButton } from "@/components/popup/command-line";
import { Note } from "@/components/popup/note";
import { Tether } from "@/components/popup/tether";
import { linkFor } from "@/components/popup/link";
import { Segmented } from "@/components/popup/segmented";
import { resolveLanguage, stringsFor, type Strings } from "@/i18n";
import { normalizePairingCode } from "@/shared/pairing-code";
import { parseAddress } from "@/shared/address";
import { isValidName } from "@/shared/identity";
import type { PopupApi } from "@/shared/popup-api";
import { createPopupApi } from "@/shared/popup-api";
import type { ConnectionState } from "@/shared/state";
import { formatConnectedSince } from "./format-duration";
import { clearPairingDraft, loadPairingDraft, savePairingDraft } from "./pairing-draft";
import { DEFAULT_PREFS, isDarkMode, loadPrefs, savePrefs, type Prefs } from "./preferences";
import { chromeLocalStorage, chromeSessionStorage, type KeyValueStorage, type SessionStorage } from "./storage";

const mono = "font-[family-name:var(--font-mono)]";

type ViewMode = "main" | "renaming" | "rename-conflict" | "settings";

export interface AppProps {
  api?: PopupApi;
  localStorage?: KeyValueStorage;
  sessionStorage?: SessionStorage;
  // 用于测试注入固定值；默认取真实环境。
  browserLanguage?: string;
  systemPrefersDark?: () => boolean;
  now?: () => number;
}

function applyDarkClass(dark: boolean): void {
  document.documentElement.classList.toggle("dark", dark);
}

function applyLangAttr(lang: string): void {
  document.documentElement.lang = lang;
}

export function App({
  api: injectedApi,
  localStorage = chromeLocalStorage,
  sessionStorage = chromeSessionStorage,
  browserLanguage = typeof navigator === "undefined" ? "en" : navigator.language,
  systemPrefersDark = () => typeof matchMedia === "function" && matchMedia("(prefers-color-scheme: dark)").matches,
  now = () => Date.now(),
}: AppProps) {
  const api = useMemo(() => injectedApi ?? createPopupApi(), [injectedApi]);

  const [state, setState] = useState<ConnectionState | null>(null);
  const [prefs, setPrefs] = useState<Prefs>(DEFAULT_PREFS);
  const [prefsLoaded, setPrefsLoaded] = useState(false);
  const [viewMode, setViewMode] = useState<ViewMode>("main");
  const [prevViewMode, setPrevViewMode] = useState<ViewMode>("main");
  const [code, setCode] = useState("");
  const [codeDraftLoaded, setCodeDraftLoaded] = useState(false);
  const [codeInvalid, setCodeInvalid] = useState(false);
  const [pairAgain, setPairAgain] = useState(false);
  const [renameDraft, setRenameDraft] = useState("");
  const [renameBusy, setRenameBusy] = useState(false);
  const [addressDraft, setAddressDraft] = useState("");
  const [addressInvalid, setAddressInvalid] = useState(false);
  const [forgetOpen, setForgetOpen] = useState(false);
  const [connectedSince, setConnectedSince] = useState<number | null>(null);

  // 收到新连接状态时一并推导「已连接起始时间」的近似值：ConnectionState 不带真实的连接起始时间戳，
  // 记住本次弹窗会话里第一次观察到 connected 的时刻是可用信息里最接近的替代（已知局限，见任务报告）。
  const applyState = useCallback(
    (s: ConnectionState) => {
      setState(s);
      setConnectedSince((prev) => (s.status === "connected" ? (prev ?? now()) : null));
    },
    [now],
  );

  // 初次加载：连接状态、外观/语言设置、配对码草稿（会话存储，重开弹窗时恢复）。
  useEffect(() => {
    let cancelled = false;
    void api.getState().then((s) => {
      if (!cancelled) applyState(s);
    });
    const unsubscribe = api.subscribe((s) => {
      if (!cancelled) applyState(s);
    });
    void loadPrefs(localStorage).then((p) => {
      if (!cancelled) {
        setPrefs(p);
        setPrefsLoaded(true);
      }
    });
    void loadPairingDraft(sessionStorage).then((draft) => {
      if (!cancelled) {
        setCode(draft);
        setCodeDraftLoaded(true);
      }
    });
    return () => {
      cancelled = true;
      unsubscribe();
    };
    // 只在挂载时执行一次：api/localStorage/sessionStorage 在一次弹窗生命周期内不会更换。
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // 外观和语言立即生效（docs/specs 设置页）。
  useEffect(() => {
    if (!prefsLoaded) return;
    applyDarkClass(isDarkMode(prefs.appearance, systemPrefersDark()));
  }, [prefs.appearance, prefsLoaded, systemPrefersDark]);

  const lang = useMemo(() => resolveLanguage(prefs.language, browserLanguage), [prefs.language, browserLanguage]);
  useEffect(() => {
    if (prefsLoaded) applyLangAttr(lang);
  }, [lang, prefsLoaded]);
  const s: Strings = stringsFor(lang);

  // 配对码草稿持久化（弹窗失焦即关闭，重开时恢复）。等首次加载完成后再开始持久化，
  // 否则挂载时 code 的初始空字符串会在草稿还没读出来之前就把它清空。
  useEffect(() => {
    if (!codeDraftLoaded) return;
    void savePairingDraft(sessionStorage, code);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [code, codeDraftLoaded]);

  const status = state?.status;

  // 重连倒计时与已连接时长都要随时间推进，重连中每秒、已连接每分钟够用；tick 本身只用来触发重渲染。
  const [, forceTick] = useState(0);
  useEffect(() => {
    if (status !== "reconnecting" && status !== "connected") return;
    const id = setInterval(() => forceTick((t) => t + 1), status === "reconnecting" ? 1000 : 30_000);
    return () => clearInterval(id);
  }, [status]);

  // 离开已连接/认证被拒之后，"重命名中"/"名称冲突"/"重新配对表单" 都不再有意义；在渲染时直接派生，
  // 不需要额外的 effect 去重置它们。
  const view: ViewMode =
    (viewMode === "renaming" || viewMode === "rename-conflict") && status !== "connected" ? "main" : viewMode;
  const pairAgainActive = pairAgain && status === "rejected";

  const onPrefsChange = useCallback(
    (next: Prefs) => {
      setPrefs(next);
      void savePrefs(localStorage, next);
    },
    [localStorage],
  );

  const openSettings = () => {
    setPrevViewMode(view);
    setAddressDraft(state?.address ?? "");
    setAddressInvalid(false);
    setViewMode("settings");
  };

  const onCodeChange = (value: string) => {
    setCode(value.toUpperCase());
    setCodeInvalid(false);
  };

  const submitPair = async () => {
    const normalized = normalizePairingCode(code);
    if (!normalized) {
      setCodeInvalid(true);
      return;
    }
    const result = await api.pair(code);
    if (!result.ok) {
      setCodeInvalid(true);
      return;
    }
    setCodeInvalid(false);
    void clearPairingDraft(sessionStorage);
  };

  const startRename = () => {
    setRenameDraft(state?.name ?? "");
    setViewMode("renaming");
  };

  const saveRename = async () => {
    if (!isValidName(renameDraft)) return;
    setRenameBusy(true);
    try {
      const result = await api.rename(renameDraft);
      if (result.ok) {
        setViewMode("main");
      } else if (result.error === "name-taken") {
        setViewMode("rename-conflict");
      } else {
        setViewMode("main");
      }
    } finally {
      setRenameBusy(false);
    }
  };

  const cancelRename = () => {
    setViewMode("main");
  };

  const confirmForget = async () => {
    await api.forget();
    setForgetOpen(false);
  };

  const saveAddress = async () => {
    const parsed = parseAddress(addressDraft);
    if (!parsed) {
      setAddressInvalid(true);
      return;
    }
    const result = await api.setAddress(addressDraft);
    if (!result.ok) {
      setAddressInvalid(true);
      return;
    }
    setAddressInvalid(false);
    setViewMode(prevViewMode === "settings" ? "main" : prevViewMode);
  };

  if (!state) {
    return <main className="w-[360px] p-4 text-sm text-muted-foreground">{s.product}</main>;
  }

  const link = linkFor(state.status);
  const paired = link !== "idle";
  const busy = state.status === "pairing";
  const showPairingForm = !paired || pairAgainActive;

  return (
    <div className="relative flex w-[360px] flex-col bg-background text-foreground">
      {view === "settings" ? (
        <header className="flex items-center gap-1 px-2 pt-2.5 pb-1.5">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={s.back}
            onClick={() => setViewMode(prevViewMode === "settings" ? "main" : prevViewMode)}
          >
            <ArrowLeft aria-hidden />
          </Button>
          <span className="text-sm font-semibold">{s.settings}</span>
        </header>
      ) : (
        <header className="flex items-center gap-2.5 pt-3.5 pr-2 pb-3 pl-4">
          <img src="/icons/icon-48.png" alt="" className="size-7 shrink-0" draggable={false} />
          <div className="min-w-0 flex-1">
            <div className="text-sm font-semibold">{s.product}</div>
            <div className="truncate text-xs text-muted-foreground">{s.tagline}</div>
          </div>
          <Button variant="ghost" size="icon-sm" aria-label={s.settings} disabled={busy} onClick={openSettings}>
            <SettingsIcon aria-hidden />
          </Button>
        </header>
      )}

      {view === "settings" ? (
        <main className="flex flex-col gap-4 px-4 pt-2 pb-4">
          <section className="flex flex-col gap-1.5">
            <span className="text-xs text-muted-foreground">{s.appearance}</span>
            <Segmented
              label={s.appearance}
              value={prefs.appearance}
              onChange={(v) => onPrefsChange({ ...prefs, appearance: v })}
              options={[
                ["system", s.system],
                ["light", s.light],
                ["dark", s.dark],
              ]}
            />
          </section>
          <section className="flex flex-col gap-1.5">
            <span className="text-xs text-muted-foreground">{s.language}</span>
            <Segmented
              label={s.language}
              value={prefs.language}
              onChange={(v) => onPrefsChange({ ...prefs, language: v })}
              options={[
                ["browser", s.followBrowser],
                ["zh", "中文"],
                ["en", "English"],
              ]}
            />
            <span className="text-xs text-muted-foreground">{s.applyNow}</span>
          </section>
          <div className="h-px bg-border" />
          <section className="flex flex-col gap-1.5">
            <Label htmlFor="daemon-address">{s.daemonAddr}</Label>
            <Input
              id="daemon-address"
              value={addressDraft}
              onChange={(e) => {
                setAddressDraft(e.target.value);
                setAddressInvalid(false);
              }}
              spellCheck={false}
              aria-invalid={addressInvalid || undefined}
              className={mono}
            />
            {addressInvalid && (
              <span role="alert" className="text-xs text-[var(--bad)]">
                {s.addrInvalid}
              </span>
            )}
            <span className="text-xs leading-relaxed text-muted-foreground">{s.addrHint}</span>
            <Button
              className="mt-1 self-end"
              disabled={!addressDraft || addressDraft === state.address}
              onClick={() => void saveAddress()}
            >
              {s.saveReconnect}
            </Button>
          </section>
        </main>
      ) : (
        <main className="flex flex-col gap-3 px-4 pb-4">
          <Tether status={state.status} s={s} name={state.name} address={state.address} />

          {showPairingForm && (
            <>
              <div className="flex flex-col gap-1.5">
                <span className="text-xs text-muted-foreground">{s.step1}</span>
                <CommandLine cmd="sctl connect" copyLabel={s.copy} />
              </div>
              <div className="flex flex-col gap-1.5">
                <Label htmlFor="pairing-code">{s.step2}</Label>
                <Input
                  id="pairing-code"
                  value={code}
                  onChange={(e) => onCodeChange(e.target.value)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") void submitPair();
                  }}
                  placeholder="XXXX-XXXX"
                  spellCheck={false}
                  autoComplete="off"
                  disabled={busy}
                  aria-invalid={codeInvalid || state.status === "pair-failed" || undefined}
                  className={`h-11 text-center text-lg tracking-[0.3em] ${mono}`}
                />
              </div>
              {codeInvalid && (
                <span role="alert" className="text-xs text-[var(--bad)]">
                  {s.invalidCodeHint}
                </span>
              )}
              {state.status === "pair-failed" && state.reason === "code-rejected" && (
                <Note tone="bad" icon={AlertCircle} title={s.pairFailedTitle}>
                  {s.pairFailedBody}
                </Note>
              )}
              {state.status === "pair-failed" && state.reason === "name-taken" && (
                <Note tone="bad" icon={AlertCircle} title={s.nameTakenTitle}>
                  {s.nameTakenBody(state.name)}
                </Note>
              )}
              {state.status === "pair-unreachable" && (
                <Note tone="bad" icon={Unplug} title={s.unreachableTitle}>
                  {s.unreachableBody(state.address)}
                </Note>
              )}
              <Button
                size="lg"
                className="w-full"
                disabled={busy || !normalizePairingCode(code)}
                onClick={() => void submitPair()}
              >
                {busy ? (
                  <>
                    <Loader2 className="animate-spin" aria-hidden />
                    {s.pairingBtn}
                  </>
                ) : (
                  s.pair
                )}
              </Button>
              <p className="text-center text-xs text-muted-foreground">{s.codeHint}</p>
            </>
          )}

          {state.status === "reconnecting" && (
            <Note tone="warn" icon={Unplug} title={s.reconnectTitle}>
              {state.retryAt === null
                ? s.reconnectingNow
                : s.reconnectBody(
                    state.attempt + 1,
                    Math.max(0, Math.ceil((state.retryAt - now()) / 1000)),
                    state.address,
                  )}
            </Note>
          )}
          {state.status === "rejected" && !pairAgainActive && (
            <Note tone="bad" icon={ShieldAlert} title={s.rejectedTitle}>
              {s.rejectedBody}
            </Note>
          )}

          {paired && (
            <>
              {view === "renaming" || view === "rename-conflict" ? (
                <div className="flex flex-col gap-1.5">
                  <Label htmlFor="instance-name">{s.instanceName}</Label>
                  <div className="flex gap-1.5">
                    <Input
                      id="instance-name"
                      value={renameDraft}
                      onChange={(e) => {
                        setRenameDraft(
                          e.target.value
                            .toLowerCase()
                            .replace(/[^a-z0-9-]/g, "")
                            .slice(0, 32),
                        );
                        if (view === "rename-conflict") setViewMode("renaming");
                      }}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" && renameDraft) void saveRename();
                        if (e.key === "Escape") cancelRename();
                      }}
                      disabled={renameBusy}
                      aria-invalid={view === "rename-conflict" || undefined}
                      aria-describedby="rename-hint"
                      className={mono}
                    />
                    <Button
                      size="icon"
                      aria-label={s.save}
                      disabled={!renameDraft || renameBusy}
                      onClick={() => void saveRename()}
                    >
                      <Check aria-hidden />
                    </Button>
                    <Button
                      size="icon"
                      variant="outline"
                      aria-label={s.cancel}
                      disabled={renameBusy}
                      onClick={cancelRename}
                    >
                      <X aria-hidden />
                    </Button>
                  </div>
                  <p
                    id="rename-hint"
                    role={view === "rename-conflict" ? "alert" : undefined}
                    className={
                      view === "rename-conflict"
                        ? "text-xs leading-relaxed text-[var(--bad)]"
                        : "text-xs leading-relaxed text-muted-foreground"
                    }
                  >
                    {view === "rename-conflict" ? s.nameTaken(renameDraft) : s.nameHint}
                  </p>
                </div>
              ) : (
                <div className="flex items-center justify-between">
                  <span className="text-xs text-muted-foreground">{s.instanceName}</span>
                  <Button
                    variant="ghost"
                    size="xs"
                    className="text-primary"
                    disabled={state.status !== "connected"}
                    onClick={startRename}
                  >
                    <Pencil aria-hidden />
                    {s.rename}
                  </Button>
                </div>
              )}

              <dl
                className={`grid grid-cols-[auto_1fr] items-center gap-x-4 gap-y-1 rounded-lg border px-3 py-2 text-xs ${link !== "ok" ? "opacity-55" : ""}`}
              >
                <dt className="text-muted-foreground">{s.id}</dt>
                <dd className="flex min-h-6 items-center justify-end gap-0.5">
                  <span className={`truncate ${mono}`}>{state.instanceId.slice(0, 13)}…</span>
                  <CopyButton text={state.instanceId} label={s.copy} />
                </dd>
                <dt className="text-muted-foreground">{s.since}</dt>
                <dd className="min-h-6 content-center text-right">
                  {state.status === "connected" && connectedSince !== null
                    ? formatConnectedSince(s, connectedSince, now())
                    : "—"}
                </dd>
              </dl>

              {state.status === "connected" && (
                <div className="flex flex-col gap-1.5">
                  <span className="text-xs text-muted-foreground">{s.useHint}</span>
                  <CommandLine cmd={`sctl tabs list --browser ${state.name}`} copyLabel={s.copy} />
                </div>
              )}
              {state.status === "reconnecting" && (
                <Button onClick={() => void api.retryNow()}>
                  <RefreshCw aria-hidden />
                  {s.retry}
                </Button>
              )}
              {state.status === "rejected" && !pairAgainActive && (
                <Button onClick={() => setPairAgain(true)}>{s.pairAgain}</Button>
              )}
            </>
          )}
        </main>
      )}

      {view !== "settings" && (
        <footer className="flex items-center border-t px-2 py-1.5">
          <Button
            variant="ghost"
            size="sm"
            className={`font-normal text-muted-foreground ${mono}`}
            aria-label={s.daemonAddr}
            disabled={busy}
            onClick={openSettings}
          >
            ws://{state.address}
          </Button>
          <span className="flex-1" />
          {paired ? (
            <Button
              variant="ghost"
              size="sm"
              className="text-[var(--bad)] hover:bg-[color-mix(in_oklch,var(--bad),transparent_90%)] hover:text-[var(--bad)]"
              onClick={() => setForgetOpen(true)}
            >
              {s.forget}
            </Button>
          ) : (
            <span className="pr-2 text-xs text-muted-foreground">{chrome.runtime.getManifest().version}</span>
          )}
        </footer>
      )}

      <AlertDialog open={forgetOpen} onOpenChange={setForgetOpen}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{s.forgetTitle}</AlertDialogTitle>
            <AlertDialogDescription>
              {s.forgetBefore}
              <span className={`mx-1 rounded bg-[var(--surface)] px-1 text-foreground ${mono}`}>
                {s.forgetCommand(state.name)}
              </span>
              {s.forgetAfter}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <AlertDialogFooter>
            <AlertDialogCancel autoFocus>{s.cancel}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-[var(--bad)] text-white hover:bg-[color-mix(in_oklch,var(--bad),black_12%)]"
              onClick={() => void confirmForget()}
            >
              {s.forget}
            </AlertDialogAction>
          </AlertDialogFooter>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  );
}
