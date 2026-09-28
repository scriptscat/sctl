import { describe, expect, it } from "vitest";
import { DEFAULT_PREFS, isDarkMode, loadPrefs, savePrefs } from "./preferences";
import type { KeyValueStorage } from "./storage";

function fakeStorage(initial: Record<string, unknown> = {}): KeyValueStorage & { data: Record<string, unknown> } {
  const data = { ...initial };
  return {
    data,
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]))),
    set: (items) => {
      Object.assign(data, items);
      return Promise.resolve();
    },
  };
}

describe("loadPrefs", () => {
  it("defaults to following the system and the browser when nothing is stored", async () => {
    await expect(loadPrefs(fakeStorage())).resolves.toEqual(DEFAULT_PREFS);
  });

  it("returns the stored appearance and language", async () => {
    const storage = fakeStorage({ appearance: "dark", language: "zh" });
    await expect(loadPrefs(storage)).resolves.toEqual({ appearance: "dark", language: "zh" });
  });

  it("ignores corrupted values and falls back to defaults", async () => {
    const storage = fakeStorage({ appearance: "purple", language: 42 });
    await expect(loadPrefs(storage)).resolves.toEqual(DEFAULT_PREFS);
  });
});

describe("savePrefs", () => {
  it("persists both fields so a later loadPrefs sees them", async () => {
    const storage = fakeStorage();
    await savePrefs(storage, { appearance: "light", language: "en" });
    await expect(loadPrefs(storage)).resolves.toEqual({ appearance: "light", language: "en" });
  });
});

describe("isDarkMode", () => {
  it("is dark when appearance is explicitly dark, regardless of the system", () => {
    expect(isDarkMode("dark", false)).toBe(true);
  });

  it("is light when appearance is explicitly light, regardless of the system", () => {
    expect(isDarkMode("light", true)).toBe(false);
  });

  it("follows the system preference when appearance is 'system'", () => {
    expect(isDarkMode("system", true)).toBe(true);
    expect(isDarkMode("system", false)).toBe(false);
  });
});
