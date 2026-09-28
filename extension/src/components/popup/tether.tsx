import { Globe, Loader2, SquareTerminal } from "lucide-react";
import type { ConnectionStatus } from "@/shared/state";
import type { Strings } from "@/i18n";
import { cn } from "@/lib/utils";
import { linkFor, type Link } from "./link";

function statusKey(status: ConnectionStatus): keyof Strings["status"] {
  const link = linkFor(status);
  if (status === "pairing") {
    return "pairing";
  }
  if (link === "idle") {
    return "unpaired";
  }
  if (link === "warn") {
    return "reconnecting";
  }
  if (link === "bad") {
    return "rejected";
  }
  return "connected";
}

const LINK_COLOR: Record<Link, string> = {
  idle: "var(--muted-foreground)",
  ok: "var(--ok)",
  warn: "var(--warn)",
  bad: "var(--bad)",
};

const LINK_DASH: Record<Link, string | undefined> = {
  idle: "2 6",
  ok: undefined,
  warn: "5 5",
  bad: "10 14",
};

export function Tether({
  status,
  s,
  name,
  address,
}: {
  status: ConnectionStatus;
  s: Strings;
  name: string;
  address: string;
}) {
  const link = linkFor(status);
  const paired = link !== "idle";
  const color = LINK_COLOR[link];
  const mono = "font-[family-name:var(--font-mono)]";
  return (
    <div className="flex items-center gap-2 rounded-xl bg-[var(--surface)] px-3 py-3">
      <div className="flex w-[92px] flex-col items-center gap-1 text-center">
        <Globe className="size-5 text-primary" aria-hidden />
        {paired ? (
          <span className={cn("max-w-full truncate text-sm font-semibold", mono)}>{name}</span>
        ) : (
          <span className="text-xs text-muted-foreground">{s.thisBrowser}</span>
        )}
      </div>
      <div className="relative h-5 flex-1">
        <svg className="absolute inset-0 h-full w-full overflow-visible" aria-hidden="true">
          <line
            x1="0"
            y1="50%"
            x2="100%"
            y2="50%"
            stroke={color}
            strokeWidth="2"
            strokeDasharray={LINK_DASH[link]}
            strokeLinecap="round"
            className={link === "warn" ? "tether-flow" : undefined}
          />
        </svg>
        {/* polite 实时区域：连接状态变化在这里播报（docs/specs 无障碍要求）。 */}
        <span
          role="status"
          aria-live="polite"
          className="absolute top-1/2 left-1/2 flex -translate-x-1/2 -translate-y-1/2 items-center gap-1 rounded-full bg-[var(--surface)] px-2 text-[11px] whitespace-nowrap"
          style={{ color }}
        >
          {status === "pairing" && <Loader2 className="size-3 animate-spin" aria-hidden />}
          {s.status[statusKey(status)]}
        </span>
      </div>
      <div className="flex w-[92px] flex-col items-center gap-1 text-center">
        <SquareTerminal className="size-5 text-primary" aria-hidden />
        <span className={cn("text-[11px] text-muted-foreground", mono)}>{address}</span>
      </div>
    </div>
  );
}
