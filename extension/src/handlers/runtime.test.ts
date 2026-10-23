import { describe, expect, it, vi } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateExtensionReloadResult } from "@/protocol/generated/validators.generated";
import { registerRuntimeHandlers } from "./runtime";

describe("extension.reload", () => {
  it("answers first and reloads the extension only after the answer has had time to leave", async () => {
    vi.useFakeTimers();
    try {
      const reload = vi.fn();
      const registry = new HandlerRegistry();
      registerRuntimeHandlers(registry, reload);

      const result = await registry.dispatch("extension.reload", {});
      expect(result).toEqual({ ok: true, result: {} });
      expect(validateExtensionReloadResult({})).toBe(true);
      expect(reload).not.toHaveBeenCalled();

      await vi.runAllTimersAsync();
      expect(reload).toHaveBeenCalledOnce();
    } finally {
      vi.useRealTimers();
    }
  });
});
