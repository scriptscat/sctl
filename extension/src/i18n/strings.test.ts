import { describe, expect, it } from "vitest";
import { resolveLanguage } from "./strings";

describe("resolveLanguage", () => {
  it("keeps an explicit language preference regardless of the browser language", () => {
    expect(resolveLanguage("zh", "en-US")).toBe("zh");
    expect(resolveLanguage("en", "zh-CN")).toBe("en");
  });

  it("falls back to the browser language when the preference is 'browser'", () => {
    expect(resolveLanguage("browser", "zh-CN")).toBe("zh");
    expect(resolveLanguage("browser", "en-US")).toBe("en");
  });

  it("treats any non-Chinese browser language as English", () => {
    expect(resolveLanguage("browser", "fr-FR")).toBe("en");
  });
});
