import type { Appearance, LangPref } from "@/i18n";
import type { KeyValueStorage } from "./storage";

export interface Prefs {
  appearance: Appearance;
  language: LangPref;
}

// 外观和语言默认都跟随系统和浏览器（docs/specs 设计决策 13）。
export const DEFAULT_PREFS: Prefs = { appearance: "system", language: "browser" };

const APPEARANCES: readonly Appearance[] = ["system", "light", "dark"];
const LANG_PREFS: readonly LangPref[] = ["browser", "zh", "en"];

function isAppearance(value: unknown): value is Appearance {
  return typeof value === "string" && (APPEARANCES as readonly string[]).includes(value);
}

function isLangPref(value: unknown): value is LangPref {
  return typeof value === "string" && (LANG_PREFS as readonly string[]).includes(value);
}

export async function loadPrefs(storage: KeyValueStorage): Promise<Prefs> {
  const items = await storage.get(["appearance", "language"]);
  return {
    appearance: isAppearance(items.appearance) ? items.appearance : DEFAULT_PREFS.appearance,
    language: isLangPref(items.language) ? items.language : DEFAULT_PREFS.language,
  };
}

export async function savePrefs(storage: KeyValueStorage, prefs: Prefs): Promise<void> {
  await storage.set({ appearance: prefs.appearance, language: prefs.language });
}

export function isDarkMode(appearance: Appearance, systemPrefersDark: boolean): boolean {
  return appearance === "dark" || (appearance === "system" && systemPrefersDark);
}
