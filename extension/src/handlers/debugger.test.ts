import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import { validateDebuggerDetachResult, validateDebuggerSendResult } from "@/protocol/generated/validators.generated";
import type { NotificationMethod, NotificationParams } from "@/protocol/generated/protocol.generated";
import { DebuggerRelay } from "./debugger";
import { registerHandlers } from "./index";

type Notice = { [N in NotificationMethod]: { method: N; params: NotificationParams<N> } }[NotificationMethod];

type AsyncFn = (...args: unknown[]) => Promise<unknown>;

interface DebuggerMock {
  attach: Mock<AsyncFn>;
  detach: Mock<AsyncFn>;
  sendCommand: Mock<AsyncFn>;
}

describe("debugger relay", () => {
  let dbg: DebuggerMock;
  let tabsGet: ReturnType<typeof vi.fn>;
  let notices: Notice[];
  let relay: DebuggerRelay;
  let registry: HandlerRegistry;

  beforeEach(() => {
    vi.useFakeTimers();
    dbg = {
      attach: vi.fn<AsyncFn>().mockResolvedValue(undefined),
      detach: vi.fn<AsyncFn>().mockResolvedValue(undefined),
      sendCommand: vi.fn<AsyncFn>().mockResolvedValue({ ok: 1 }),
    };
    tabsGet = vi.fn().mockResolvedValue({ id: 5 });
    vi.stubGlobal("chrome", { debugger: dbg, tabs: { get: tabsGet } });
    notices = [];
    relay = new DebuggerRelay((method, params) => {
      notices.push({ method, params } as Notice);
    });
    registry = new HandlerRegistry();
    registerHandlers(registry, relay);
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it("declares capabilities for debugger.send and debugger.detach", () => {
    expect(registry.methods()).toEqual(expect.arrayContaining(["debugger.send", "debugger.detach"]));
  });

  describe("debugger.send", () => {
    it("attaches with protocol 1.3 on first use, then sends the command and returns its result", async () => {
      const outcome = await registry.dispatch("debugger.send", {
        tabId: 5,
        method: "Page.enable",
        params: { a: 1 },
      });

      expect(dbg.attach).toHaveBeenCalledWith({ tabId: 5 }, "1.3");
      expect(dbg.sendCommand).toHaveBeenCalledWith({ tabId: 5 }, "Page.enable", { a: 1 });
      expect(outcome).toEqual({ ok: true, result: { result: { ok: 1 } } });
      if (outcome.ok) {
        expect(validateDebuggerSendResult(outcome.result)).toBe(true);
      }
    });

    it("attaches only once for repeated and concurrent sends to the same tab", async () => {
      await Promise.all([
        registry.dispatch("debugger.send", { tabId: 5, method: "A" }),
        registry.dispatch("debugger.send", { tabId: 5, method: "B" }),
      ]);
      await registry.dispatch("debugger.send", { tabId: 5, method: "C" });

      expect(dbg.attach).toHaveBeenCalledTimes(1);
      expect(dbg.sendCommand).toHaveBeenCalledTimes(3);
    });

    it("targets a child session when sessionId is given", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, sessionId: "S1", method: "DOM.getDocument" });

      expect(dbg.sendCommand).toHaveBeenCalledWith({ tabId: 5, sessionId: "S1" }, "DOM.getDocument", undefined);
    });

    it("returns an empty result when the command has none", async () => {
      dbg.sendCommand.mockResolvedValue(undefined);

      expect(await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" })).toEqual({
        ok: true,
        result: { result: {} },
      });
    });

    it("answers NOT_FOUND for a tab that does not exist without attaching", async () => {
      tabsGet.mockRejectedValue(new Error("No tab with id: 9"));

      expect(await registry.dispatch("debugger.send", { tabId: 9, method: "Page.enable" })).toEqual({
        ok: false,
        code: "NOT_FOUND",
        message: "no tab 9",
      });
      expect(dbg.attach).not.toHaveBeenCalled();
    });

    it("answers PAGE_NOT_AUTOMATABLE with Chrome's reason when attach is refused, and retries attach next time", async () => {
      dbg.attach.mockRejectedValueOnce(new Error("Cannot access a chrome:// URL"));

      expect(await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" })).toEqual({
        ok: false,
        code: "PAGE_NOT_AUTOMATABLE",
        message: "Cannot access a chrome:// URL",
      });
      expect(dbg.sendCommand).not.toHaveBeenCalled();

      expect((await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" })).ok).toBe(true);
      expect(dbg.attach).toHaveBeenCalledTimes(2);
    });

    it("surfaces a CDP command error as INVALID_REQUEST with the CDP message", async () => {
      dbg.sendCommand.mockRejectedValue(new Error("'Foo.bar' wasn't found"));

      expect(await registry.dispatch("debugger.send", { tabId: 5, method: "Foo.bar" })).toEqual({
        ok: false,
        code: "INVALID_REQUEST",
        message: "'Foo.bar' wasn't found",
      });
    });

    it("answers DEBUGGER_DETACHED when the tab detaches while the command runs", async () => {
      dbg.sendCommand.mockImplementation(() => {
        relay.onDetach({ tabId: 5 }, "target_closed");
        return Promise.reject(new Error("Detached while handling command."));
      });

      expect(await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" })).toMatchObject({
        ok: false,
        code: "DEBUGGER_DETACHED",
      });
    });
  });

  describe("chrome events", () => {
    it("forwards chrome.debugger events of an attached tab as debugger.event with the session id", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });

      relay.onEvent({ tabId: 5 }, "Page.loadEventFired", { timestamp: 1 });
      relay.onEvent({ tabId: 5, sessionId: "S1" }, "Target.x", undefined);

      expect(notices).toEqual([
        { method: "debugger.event", params: { tabId: 5, method: "Page.loadEventFired", params: { timestamp: 1 } } },
        { method: "debugger.event", params: { tabId: 5, sessionId: "S1", method: "Target.x" } },
      ]);
    });

    it("ignores events from tabs it did not attach", () => {
      relay.onEvent({ tabId: 8 }, "Page.loadEventFired", {});
      relay.onEvent({ targetId: "x" }, "Page.loadEventFired", {});

      expect(notices).toEqual([]);
    });

    it("reports a user-cancelled infobar as debugger.detached and re-attaches on the next send", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });

      relay.onDetach({ tabId: 5 }, "canceled_by_user");

      expect(notices).toEqual([{ method: "debugger.detached", params: { tabId: 5, reason: "canceled_by_user" } }]);
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      expect(dbg.attach).toHaveBeenCalledTimes(2);
    });

    it("stays silent about detaches of tabs it does not track", () => {
      relay.onDetach({ tabId: 8 }, "target_closed");

      expect(notices).toEqual([]);
    });
  });

  describe("debugger.detach", () => {
    beforeEach(async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      await registry.dispatch("debugger.send", { tabId: 6, method: "Page.enable" });
    });

    it("detaches only the named tab", async () => {
      const outcome = await registry.dispatch("debugger.detach", { tabId: 5 });

      expect(outcome).toEqual({ ok: true, result: { tabIds: [5] } });
      expect(dbg.detach).toHaveBeenCalledTimes(1);
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
      if (outcome.ok) {
        expect(validateDebuggerDetachResult(outcome.result)).toBe(true);
      }
    });

    it("detaches every attached tab when no tab is named", async () => {
      expect(await registry.dispatch("debugger.detach", {})).toEqual({ ok: true, result: { tabIds: [5, 6] } });
      expect(dbg.detach).toHaveBeenCalledTimes(2);
    });

    it("reports no tabs for a tab that is not attached, and does not notify for its own detach", async () => {
      expect(await registry.dispatch("debugger.detach", { tabId: 99 })).toEqual({ ok: true, result: { tabIds: [] } });
      await registry.dispatch("debugger.detach", { tabId: 5 });

      expect(dbg.detach).not.toHaveBeenCalledWith({ tabId: 99 });
      expect(notices).toEqual([]);
    });

    it("attaches again after an explicit detach", async () => {
      await registry.dispatch("debugger.detach", { tabId: 5 });
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });

      expect(dbg.attach).toHaveBeenCalledTimes(3);
    });

    it("detaches all tabs when the connection closes", async () => {
      await relay.detachAll();

      expect(dbg.detach).toHaveBeenCalledTimes(2);
      expect(notices).toEqual([]);
    });
  });

  describe("idle safety net", () => {
    it("detaches a tab 10 minutes after its last send and tells the daemon", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });

      await vi.advanceTimersByTimeAsync(9 * 60_000);
      expect(dbg.detach).not.toHaveBeenCalled();
      await registry.dispatch("debugger.send", { tabId: 5, method: "B" });
      await vi.advanceTimersByTimeAsync(9 * 60_000);
      expect(dbg.detach).not.toHaveBeenCalled();

      await vi.advanceTimersByTimeAsync(60_000);
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
      expect(notices).toEqual([{ method: "debugger.detached", params: { tabId: 5, reason: "idle_timeout" } }]);
    });

    it("does not fire for a tab that was detached explicitly", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.detach", { tabId: 5 });
      dbg.detach.mockClear();

      await vi.advanceTimersByTimeAsync(11 * 60_000);

      expect(dbg.detach).not.toHaveBeenCalled();
      expect(notices).toEqual([]);
    });
  });
});
