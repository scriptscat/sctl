import type { ComponentType, ReactNode, SVGProps } from "react";
import { cn } from "@/lib/utils";

// 错误提示用 alert 角色；非致命提示（如重连中）用 status，两者都是 docs/specs 的无障碍要求。
export function Note({
  tone,
  icon: Icon,
  title,
  children,
  className,
}: {
  tone: "warn" | "bad";
  icon: ComponentType<SVGProps<SVGSVGElement>>;
  title: string;
  children: ReactNode;
  className?: string;
}) {
  const color = tone === "bad" ? "var(--bad)" : "var(--warn)";
  return (
    // role=alert 用于错误提示（docs/specs 无障碍要求）；非致命提示（如重连倒计时）不设 live-region
    // 角色，避免倒计时每秒变化都触发朗读——连接状态本身已经由 Tether 的 polite 区域播报一次。
    <div
      role={tone === "bad" ? "alert" : undefined}
      className={cn("flex gap-2.5 rounded-lg border px-3 py-2.5 text-xs", className)}
      style={{
        borderColor: `color-mix(in oklch, ${color}, transparent 60%)`,
        background: `color-mix(in oklch, ${color}, transparent 91%)`,
      }}
    >
      <Icon className="mt-0.5 size-3.5 shrink-0" style={{ color }} aria-hidden />
      <div className="flex flex-col gap-0.5">
        <span className="font-medium">{title}</span>
        <span className="leading-relaxed text-muted-foreground">{children}</span>
      </div>
    </div>
  );
}
