import type { Strings } from "@/i18n";

// ConnectionState 不带「已连接起始时间」，只能用本地观察到 status 变为 connected 的那一刻做近似
// （弹窗每次打开都会重新计算，短于实际连接时长；已知限制，见任务报告）。
export function formatConnectedSince(s: Strings, sinceMs: number, nowMs: number): string {
  const elapsedMinutes = Math.floor(Math.max(0, nowMs - sinceMs) / 60000);
  if (elapsedMinutes < 1) {
    return s.sinceJustNow;
  }
  if (elapsedMinutes < 60) {
    return s.sinceMinutes(elapsedMinutes);
  }
  return s.sinceHours(Math.floor(elapsedMinutes / 60));
}
