import { useId, useState } from "react";
import { Bookmark, ChevronRight, Folder, FolderOpen, Trash2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { BookmarkRemovalStrings } from "@/i18n";
import { cn } from "@/lib/utils";
import type { ApprovalStatus, BookmarkRemovalEntry, BookmarkRemovalItem } from "@/shared/approvals";
import type { KindContentProps } from "./kinds";

const mono = "font-[family-name:var(--font-mono)]";

// 超过这么多项改用紧凑行（docs/specs「审批窗口」视觉）：只显示标题和所在文件夹，URL 放进悬停提示。
const COMPACT_ABOVE = 10;
// 展开预览最多列出的后代数；文件夹可能含成百上千项，全部渲染只会拖慢窗口。
const PREVIEW_LIMIT = 50;

// 书签标题、URL、文件夹名都由网页或请求方决定，可能写成「管理员已批准」之类的诱导文字：一律作为文本节点渲染。
function Title({
  text,
  t,
  className,
  hint,
}: {
  text: string;
  t: BookmarkRemovalStrings;
  className?: string;
  hint?: string;
}) {
  return text ? (
    <span className={cn("truncate", className)} title={hint ?? text}>
      {text}
    </span>
  ) : (
    <span className={cn("truncate text-muted-foreground italic", className)} title={hint}>
      {t.untitled}
    </span>
  );
}

function PathLine({ path, t }: { path: string[]; t: BookmarkRemovalStrings }) {
  if (path.length === 0) return null;
  const joined = path.join(" / ");
  return (
    <span className="flex min-w-0 items-center gap-1 text-[11px] text-muted-foreground">
      <FolderOpen className="size-3 shrink-0" role="img" aria-label={t.location} />
      <span className="truncate" title={joined}>
        {joined}
      </span>
    </span>
  );
}

function KindIcon({
  type,
  t,
  className,
}: {
  type: "bookmark" | "folder";
  t: BookmarkRemovalStrings;
  className?: string;
}) {
  const Icon = type === "bookmark" ? Bookmark : Folder;
  return (
    <Icon
      role="img"
      aria-label={type === "bookmark" ? t.bookmark : t.folder}
      className={cn("shrink-0", type === "bookmark" ? "text-[var(--brand)]" : "text-muted-foreground", className)}
    />
  );
}

// contents 按先序排列，每项带着 parentId；深度由此推出，用来缩进。
function Preview({
  item,
  t,
  id,
}: {
  item: Extract<BookmarkRemovalItem, { type: "folder" }>;
  t: BookmarkRemovalStrings;
  id: string;
}) {
  const depth = new Map<string, number>([[item.id, 0]]);
  const rows = item.contents.slice(0, PREVIEW_LIMIT).map((entry: BookmarkRemovalEntry) => {
    const d = (depth.get(entry.parentId) ?? 0) + 1;
    depth.set(entry.id, d);
    return { entry, d };
  });
  return (
    <ul id={id} className="mt-1.5 flex flex-col gap-1 border-l-2 pl-2.5 text-xs">
      {rows.map(({ entry, d }) => (
        <li key={entry.id} className="flex min-w-0 items-center gap-1.5" style={{ paddingLeft: `${(d - 1) * 12}px` }}>
          <KindIcon type={entry.type} t={t} className="size-3" />
          <Title text={entry.title} t={t} hint={entry.url} />
        </li>
      ))}
      {item.contents.length > rows.length && (
        <li className="text-muted-foreground">{t.previewMore(item.contents.length - rows.length)}</li>
      )}
    </ul>
  );
}

function FullRow({ item, t }: { item: BookmarkRemovalItem; t: BookmarkRemovalStrings }) {
  const [open, setOpen] = useState(false);
  const previewId = useId();
  return (
    <li className="flex gap-2.5 px-3 py-2">
      <KindIcon type={item.type} t={t} className="mt-0.5 size-4" />
      <div className="flex min-w-0 flex-1 flex-col gap-0.5">
        <Title text={item.title} t={t} className="text-sm font-medium" />
        {item.type === "bookmark" ? (
          <span className={cn("truncate text-xs text-muted-foreground", mono)} title={item.url}>
            {item.url}
          </span>
        ) : (
          <span className="text-xs text-muted-foreground">{t.folderCounts(item.bookmarks, item.folders)}</span>
        )}
        <PathLine path={item.path} t={t} />
        {item.type === "folder" && open && <Preview item={item} t={t} id={previewId} />}
      </div>
      {item.type === "folder" && item.contents.length > 0 && (
        <Button
          variant="ghost"
          size="icon-xs"
          className="mt-0.5"
          aria-label={t.preview(item.title || t.untitled)}
          aria-expanded={open}
          aria-controls={open ? previewId : undefined}
          onClick={() => setOpen((o) => !o)}
        >
          <ChevronRight
            className={cn("transition-transform motion-reduce:transition-none", open && "rotate-90")}
            aria-hidden
          />
        </Button>
      )}
    </li>
  );
}

function CompactRow({ item, t }: { item: BookmarkRemovalItem; t: BookmarkRemovalStrings }) {
  const hint = item.type === "bookmark" ? item.url : t.folderCounts(item.bookmarks, item.folders);
  const joined = item.path.join(" / ");
  return (
    <li className="flex min-w-0 items-center gap-2 px-3 py-1.5 text-xs">
      <KindIcon type={item.type} t={t} className="size-3.5" />
      <Title text={item.title} t={t} hint={hint} className="min-w-0 flex-1 font-medium" />
      {joined && (
        <span className="max-w-[45%] shrink-0 truncate text-[11px] text-muted-foreground" title={joined}>
          {joined}
        </span>
      )}
    </li>
  );
}

// 没有执行的终态里，摘要从「将删除」改为「请求删除」。
function isRequestedOnly(status: ApprovalStatus): boolean {
  return status !== "pending" && status !== "executing" && status !== "done";
}

export function BookmarkRemovalContent({ item, s }: KindContentProps<"bookmarks.remove">) {
  const t = s.bookmarkRemoval;
  const requested = isRequestedOnly(item.status);
  const compact = item.detail.items.length > COMPACT_ABOVE;
  return (
    <>
      <div className="mx-4 mt-3 flex shrink-0 items-start gap-2 rounded-lg bg-[var(--surface)] px-3 py-2 text-sm">
        <Trash2
          className={cn("mt-0.5 size-4 shrink-0", requested ? "text-muted-foreground" : "text-[var(--bad)]")}
          aria-hidden
        />
        <p className="font-medium">{t.summary(item.detail.summary, requested)}</p>
      </div>
      <ul aria-label={t.listLabel} className="mx-4 mt-2 min-h-0 shrink divide-y overflow-y-auto rounded-lg border">
        {item.detail.items.map((entry) =>
          compact ? <CompactRow key={entry.id} item={entry} t={t} /> : <FullRow key={entry.id} item={entry} t={t} />,
        )}
      </ul>
    </>
  );
}
