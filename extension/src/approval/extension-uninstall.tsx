import { Puzzle } from "lucide-react";
import { CopyButton } from "@/components/popup/command-line";
import { cn } from "@/lib/utils";
import type { KindContentProps } from "./kinds";

const mono = "font-[family-name:var(--font-mono)]";

// 扩展信息卡。名称和描述由扩展作者决定，可能写成「系统已授权卸载」之类的诱导文字：一律作为文本节点渲染，
// title 属性给出被截断的完整值。
export function ExtensionUninstallContent({ item, s }: KindContentProps<"extensions.uninstall">) {
  const t = s.extensionUninstall;
  const ext = item.detail;
  // 点「卸载」时它已经不在：启用状态只是过时的快照，不再显示。
  const gone = item.status === "failed" && item.outcome?.ok === false && item.outcome.code === "NOT_FOUND";
  const state = ext.enabled ? t.enabled : t.disabled;
  return (
    <section
      aria-label={t.cardLabel}
      className={cn(
        "mx-4 mt-3 min-h-0 shrink overflow-y-auto rounded-lg border px-3 py-2.5",
        gone && "bg-[color-mix(in_oklch,var(--bad),transparent_92%)]",
      )}
    >
      <div className="flex gap-3">
        <div className="grid size-10 shrink-0 place-items-center rounded-lg border border-dashed border-[var(--muted-foreground)] bg-[var(--surface)]">
          <Puzzle className="size-5 text-muted-foreground" aria-hidden />
        </div>
        <div className="flex min-w-0 flex-1 flex-col gap-0.5">
          <div className="flex min-w-0 items-start gap-1.5">
            <span className="line-clamp-2 text-sm leading-snug font-semibold break-words" title={ext.name}>
              {ext.name}
            </span>
            {gone && (
              <span className="mt-px shrink-0 rounded border border-[color-mix(in_oklch,var(--bad),transparent_50%)] px-1 text-[11px] font-medium text-[var(--bad)]">
                {t.missing}
              </span>
            )}
          </div>
          <span className="flex items-center gap-1.5 text-xs text-muted-foreground">
            <span className="tabular-nums">{t.version(ext.version)}</span>
            {!gone && (
              <>
                <span aria-hidden>·</span>
                <span className="inline-flex items-center gap-1 text-foreground" title={`${t.stateLabel}: ${state}`}>
                  <span
                    className={cn(
                      "size-1.5 rounded-full",
                      ext.enabled ? "bg-[var(--ok)]" : "bg-[var(--muted-foreground)]",
                    )}
                    aria-hidden
                  />
                  {state}
                </span>
              </>
            )}
          </span>
        </div>
      </div>
      {ext.description && (
        <p
          className="mt-2 line-clamp-2 text-xs leading-relaxed break-words text-muted-foreground"
          title={ext.description}
        >
          {ext.description}
        </p>
      )}
      <dl className="mt-2 grid grid-cols-[auto_minmax(0,1fr)] items-center gap-x-3 gap-y-1 text-xs">
        <dt className="text-muted-foreground">{t.idLabel}</dt>
        <dd className="flex min-w-0 items-center gap-0.5">
          <span className={cn("truncate select-all", mono)} title={ext.id}>
            {ext.id}
          </span>
          <CopyButton text={ext.id} label={t.copyId} />
        </dd>
        <dt className="text-muted-foreground">{t.installLabel}</dt>
        <dd>{t.installType[ext.installType] ?? ext.installType}</dd>
      </dl>
    </section>
  );
}
