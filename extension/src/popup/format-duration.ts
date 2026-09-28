import type { Strings } from "@/i18n";

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
