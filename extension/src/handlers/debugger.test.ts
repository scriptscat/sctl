import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry } from "@/background/registry";
import {
  validateDebuggerBodyResult,
  validateDebuggerDetachResult,
  validateDebuggerSendResult,
  validateDebuggerTargetsResult,
} from "@/protocol/generated/validators.generated";
import type { NotificationMethod, NotificationParams } from "@/protocol/generated/protocol.generated";
import { DebuggerRelay, type AttachedTabsStorage } from "./debugger";
import { registerHandlers } from "./index";

type Notice = { [N in NotificationMethod]: { method: N; params: NotificationParams<N> } }[NotificationMethod];

type AsyncFn = (...args: unknown[]) => Promise<unknown>;

interface DebuggerMock {
  attach: Mock<AsyncFn>;
  detach: Mock<AsyncFn>;
  sendCommand: Mock<AsyncFn>;
  getTargets: Mock<AsyncFn>;
}

// 模拟 chrome.storage.session:service worker 重启后仍在,浏览器重启后清空。
function memoryStore(): AttachedTabsStorage & {
  tabIds: () => unknown;
  recordingTabIds: () => unknown;
  ownedTabIds: () => unknown;
} {
  const items: Record<string, unknown> = {};
  return {
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in items).map((k) => [k, items[k]]))),
    set: (next) => {
      Object.assign(items, structuredClone(next));
      return Promise.resolve();
    },
    tabIds: () => items.debuggerTabs,
    recordingTabIds: () => items.debuggerRecording,
    ownedTabIds: () => items.debuggerOwned,
  };
}

describe("debugger relay", () => {
  let dbg: DebuggerMock;
  let tabsGet: ReturnType<typeof vi.fn>;
  let tabsCreate: ReturnType<typeof vi.fn>;
  let tabsRemove: ReturnType<typeof vi.fn>;
  let notices: Notice[];
  let relay: DebuggerRelay;
  let registry: HandlerRegistry;
  let store: ReturnType<typeof memoryStore>;

  beforeEach(() => {
    vi.useFakeTimers();
    dbg = {
      attach: vi.fn<AsyncFn>().mockResolvedValue(undefined),
      detach: vi.fn<AsyncFn>().mockResolvedValue(undefined),
      sendCommand: vi.fn<AsyncFn>().mockResolvedValue({ ok: 1 }),
      getTargets: vi.fn<AsyncFn>().mockResolvedValue([]),
    };
    tabsGet = vi.fn().mockResolvedValue({ id: 5 });
    tabsCreate = vi.fn().mockResolvedValue({ id: 9 });
    tabsRemove = vi.fn().mockResolvedValue(undefined);
    vi.stubGlobal("chrome", {
      debugger: dbg,
      tabs: { get: tabsGet, create: tabsCreate, remove: tabsRemove },
      windows: { getLastFocused: vi.fn().mockResolvedValue({ id: 3 }) },
    });
    notices = [];
    store = memoryStore();
    relay = new DebuggerRelay((method, params) => {
      notices.push({ method, params } as Notice);
    }, store);
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

  describe("debugger.body", () => {
    const MiB = 1024 * 1024;

    async function attached(): Promise<void> {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Network.enable" });
      dbg.sendCommand.mockReset();
    }

    async function body(params: Record<string, unknown>): Promise<unknown> {
      const outcome = await registry.dispatch("debugger.body", { tabId: 5, requestId: "R1", ...params });
      if (outcome.ok) {
        expect(validateDebuggerBodyResult(outcome.result)).toBe(true);
      }
      return outcome;
    }

    it("reads the request body with Network.getRequestPostData on the named session", async () => {
      await attached();
      dbg.sendCommand.mockResolvedValue({ postData: "a=1&b=é" });

      expect(await body({ sessionId: "S1", part: "request" })).toEqual({
        ok: true,
        result: { body: "a=1&b=é", base64Encoded: false, size: 8, truncated: false },
      });
      expect(dbg.sendCommand).toHaveBeenCalledWith({ tabId: 5, sessionId: "S1" }, "Network.getRequestPostData", {
        requestId: "R1",
      });
    });

    it("reads the response body with Network.getResponseBody on the top-level session", async () => {
      await attached();
      dbg.sendCommand.mockResolvedValue({ body: "hello", base64Encoded: false });

      expect(await body({ part: "response" })).toEqual({
        ok: true,
        result: { body: "hello", base64Encoded: false, size: 5, truncated: false },
      });
      expect(dbg.sendCommand).toHaveBeenCalledWith({ tabId: 5 }, "Network.getResponseBody", { requestId: "R1" });
    });

    it("returns a text body of exactly 1 MiB whole", async () => {
      await attached();
      const text = "x".repeat(MiB);
      dbg.sendCommand.mockResolvedValue({ body: text, base64Encoded: false });

      expect(await body({ part: "response" })).toEqual({
        ok: true,
        result: { body: text, base64Encoded: false, size: MiB, truncated: false },
      });
    });

    it("cuts a longer text body to its first 1 MiB of UTF-8 without splitting a character, and reports the original size", async () => {
      await attached();
      // 1 MiB - 1 个 ASCII 字节之后是一个 3 字节的字符：它放不进 1 MiB，整个留到截断之外。
      const text = "x".repeat(MiB - 1) + "中" + "y".repeat(10);
      dbg.sendCommand.mockResolvedValue({ body: text, base64Encoded: false });

      expect(await body({ part: "response" })).toEqual({
        ok: true,
        result: { body: "x".repeat(MiB - 1), base64Encoded: false, size: MiB - 1 + 3 + 10, truncated: true },
      });
    });

    it("cuts a longer binary body to its first 1 MiB of bytes, still base64, and reports the decoded size", async () => {
      await attached();
      const bytes = new Uint8Array(MiB + 7).map((_, i) => i % 251);
      dbg.sendCommand.mockResolvedValue({ body: Buffer.from(bytes).toString("base64"), base64Encoded: true });

      const outcome = (await body({ part: "response" })) as {
        ok: true;
        result: { body: string; base64Encoded: boolean; size: number; truncated: boolean };
      };
      expect(outcome.result).toMatchObject({ base64Encoded: true, size: MiB + 7, truncated: true });
      // 比较 base64 原文而不是解码后的 Buffer：toEqual 逐元素比较 1 MiB 的 Buffer 要 1.3 s，满载时会超时。
      expect(outcome.result.body).toBe(Buffer.from(bytes.subarray(0, MiB)).toString("base64"));
    });

    it("returns a binary body under 1 MiB whole with its decoded size", async () => {
      await attached();
      dbg.sendCommand.mockResolvedValue({ body: Buffer.from([1, 2, 3, 4]).toString("base64"), base64Encoded: true });

      expect(await body({ part: "response" })).toEqual({
        ok: true,
        result: { body: "AQIDBA==", base64Encoded: true, size: 4, truncated: false },
      });
    });

    it.each([
      ['{"code":-32000,"message":"No resource with given identifier found"}', "navigated"],
      ["No resource with given id was found", "navigated"],
      ['{"code":-32000,"message":"No data found for resource with given identifier"}', "noData"],
      ['{"code":-32000,"message":"Request content was evicted from inspector cache"}', "evicted"],
      ["No post data available for the request", "noPostData"],
    ])("reports Chrome's %s as unavailable: %s", async (message, reason) => {
      await attached();
      dbg.sendCommand.mockRejectedValue(new Error(message));

      expect(await body({ part: "response" })).toEqual({ ok: true, result: { unavailable: reason } });
    });

    it("surfaces any other CDP error as INVALID_REQUEST with the CDP message", async () => {
      await attached();
      dbg.sendCommand.mockRejectedValue(new Error("Session with given id not found."));

      expect(await body({ sessionId: "S9", part: "response" })).toEqual({
        ok: false,
        code: "INVALID_REQUEST",
        message: "Session with given id not found.",
      });
    });

    it("answers DEBUGGER_DETACHED for a tab that is not attached, without attaching", async () => {
      expect(await body({ part: "response" })).toMatchObject({ ok: false, code: "DEBUGGER_DETACHED" });
      expect(dbg.attach).not.toHaveBeenCalled();
      expect(dbg.sendCommand).not.toHaveBeenCalled();
    });

    it("answers DEBUGGER_DETACHED when the tab detaches while the body is read", async () => {
      await attached();
      dbg.sendCommand.mockImplementation(() => {
        relay.onDetach({ tabId: 5 }, "target_closed");
        return Promise.reject(new Error("No resource with given identifier found"));
      });

      expect(await body({ part: "response" })).toMatchObject({ ok: false, code: "DEBUGGER_DETACHED" });
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

    it("releases a tab whose attach was still in flight when the detach arrived, once the attach finishes", async () => {
      let finishAttach!: () => void;
      dbg.attach.mockReturnValueOnce(
        new Promise<void>((resolve) => {
          finishAttach = resolve;
        }),
      );
      const sending = registry.dispatch("debugger.send", { tabId: 7, method: "Page.enable" });
      await vi.waitFor(() => expect(dbg.attach).toHaveBeenCalledWith({ tabId: 7 }, "1.3"));

      const detaching = registry.dispatch("debugger.detach", { tabId: 7 });
      finishAttach();

      expect(await detaching).toEqual({ ok: true, result: { tabIds: [7] } });
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 7 });
      await sending;
    });

    it("detaches all tabs when the connection closes", async () => {
      await relay.detachAll();

      expect(dbg.detach).toHaveBeenCalledTimes(2);
      expect(notices).toEqual([]);
    });
  });

  describe("service worker restart", () => {
    // 新的 relay 与同一个 store 模拟 service worker 被回收后重新启动:内存状态没了,Chrome 里的附加还在。
    function restart(): DebuggerRelay {
      const next = new DebuggerRelay((method, params) => {
        notices.push({ method, params } as Notice);
      }, store);
      registry = new HandlerRegistry();
      registerHandlers(registry, next);
      return next;
    }

    it("keeps driving and reporting a tab it attached before the restart, without attaching again", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      dbg.getTargets.mockResolvedValue([{ type: "page", id: "T5", tabId: 5, attached: true, title: "", url: "" }]);
      dbg.attach.mockRejectedValue(new Error("Another debugger is already attached to the tab with id: 5."));

      const next = restart();
      next.onEvent({ tabId: 5 }, "Page.loadEventFired", { timestamp: 2 });

      expect(await registry.dispatch("debugger.send", { tabId: 5, method: "Runtime.evaluate" })).toMatchObject({
        ok: true,
      });
      expect(dbg.attach).toHaveBeenCalledTimes(1);
      expect(notices).toEqual([
        { method: "debugger.event", params: { tabId: 5, method: "Page.loadEventFired", params: { timestamp: 2 } } },
      ]);
      expect(await registry.dispatch("debugger.detach", { tabId: 5 })).toEqual({ ok: true, result: { tabIds: [5] } });
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
    });

    it("tells the daemon about a tab it had attached that is no longer attached after the restart", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      dbg.getTargets.mockResolvedValue([{ type: "page", id: "T5", tabId: 5, attached: false, title: "", url: "" }]);

      restart();

      expect(await registry.dispatch("debugger.detach", {})).toEqual({ ok: true, result: { tabIds: [] } });
      expect(notices).toEqual([{ method: "debugger.detached", params: { tabId: 5, reason: "target_closed" } }]);
      expect(store.tabIds()).toEqual([]);
    });

    it("starts without the earlier tabs when they cannot be restored, and still attaches on demand", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      dbg.getTargets.mockRejectedValue(new Error("getTargets failed"));
      vi.spyOn(console, "error").mockImplementation(() => undefined);

      restart();

      expect(await registry.dispatch("debugger.send", { tabId: 6, method: "Page.enable" })).toMatchObject({ ok: true });
      expect(dbg.attach).toHaveBeenLastCalledWith({ tabId: 6 }, "1.3");
    });

    it("forgets a tab in the store once it is detached", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      expect(store.tabIds()).toEqual([5]);

      await registry.dispatch("debugger.detach", { tabId: 5 });

      expect(store.tabIds()).toEqual([]);
    });
  });

  describe("recording", () => {
    const IDLE = 10 * 60_000;

    it("does not detach a recording tab when the idle backstop elapses", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      expect(await registry.dispatch("debugger.record", { tabId: 5, recording: true })).toEqual({
        ok: true,
        result: { recording: true },
      });

      await vi.advanceTimersByTimeAsync(3 * IDLE);
      await registry.dispatch("debugger.send", { tabId: 5, method: "B" });
      await vi.advanceTimersByTimeAsync(3 * IDLE);

      expect(dbg.detach).not.toHaveBeenCalled();
      expect(notices).toEqual([]);
    });

    it("re-arms the backstop when recording stops, then detaches after 10 minutes", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.record", { tabId: 5, recording: true });
      await vi.advanceTimersByTimeAsync(2 * IDLE);

      expect(await registry.dispatch("debugger.record", { tabId: 5, recording: false })).toEqual({
        ok: true,
        result: { recording: false },
      });
      await vi.advanceTimersByTimeAsync(IDLE - 1);
      expect(dbg.detach).not.toHaveBeenCalled();
      await vi.advanceTimersByTimeAsync(1);

      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
      expect(notices).toEqual([{ method: "debugger.detached", params: { tabId: 5, reason: "idle_timeout" } }]);
    });

    it("answers DEBUGGER_DETACHED when starting to record a tab that is not attached", async () => {
      expect(await registry.dispatch("debugger.record", { tabId: 5, recording: true })).toMatchObject({
        ok: false,
        code: "DEBUGGER_DETACHED",
      });
      expect(store.recordingTabIds()).toBeUndefined();
    });

    it("succeeds without effect when stopping a tab that is not attached", async () => {
      expect(await registry.dispatch("debugger.record", { tabId: 5, recording: false })).toEqual({
        ok: true,
        result: { recording: false },
      });
      expect(dbg.attach).not.toHaveBeenCalled();
    });

    it("keeps a tab recording across a service worker restart", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.record", { tabId: 5, recording: true });
      expect(store.recordingTabIds()).toEqual([5]);
      dbg.getTargets.mockResolvedValue([{ type: "page", id: "T5", tabId: 5, attached: true, title: "", url: "" }]);

      new DebuggerRelay(() => undefined, store);
      await vi.advanceTimersByTimeAsync(0);
      await vi.advanceTimersByTimeAsync(3 * IDLE);

      expect(dbg.detach).not.toHaveBeenCalled();
    });

    it("drops recording of a tab that is gone after a service worker restart", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.record", { tabId: 5, recording: true });
      dbg.getTargets.mockResolvedValue([]);

      new DebuggerRelay(() => undefined, store);
      await vi.advanceTimersByTimeAsync(0);

      expect(store.recordingTabIds()).toEqual([]);
    });

    it("clears the recording state when the tab is detached by the daemon", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.record", { tabId: 5, recording: true });

      await registry.dispatch("debugger.detach", { tabId: 5 });

      expect(store.recordingTabIds()).toEqual([]);
      await registry.dispatch("debugger.send", { tabId: 5, method: "B" });
      await vi.advanceTimersByTimeAsync(IDLE);
      expect(dbg.detach).toHaveBeenCalledTimes(2);
    });

    it("clears the recording state when Chrome detaches the tab", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "A" });
      await registry.dispatch("debugger.record", { tabId: 5, recording: true });

      relay.onDetach({ tabId: 5 }, "canceled_by_user");

      expect(store.recordingTabIds()).toEqual([]);
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

  describe("dialogs left behind on release", () => {
    // sctl 断开调试器之后，这次附加期间打开的弹框成了孤儿：之后任何调试会话都处理不了它（真机探针）。
    const handled = () => dbg.sendCommand.mock.calls.filter((call) => call[1] === "Page.handleJavaScriptDialog");
    const order = (mock: Mock<AsyncFn>, method?: string) => {
      const index = mock.mock.calls.findIndex((call) => method === undefined || call[1] === method);
      return mock.mock.invocationCallOrder[index];
    };

    beforeEach(async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      await registry.dispatch("debugger.send", { tabId: 6, method: "Page.enable" });
    });

    it("dismisses a dialog it saw open before the idle backstop detaches the tab", async () => {
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });

      await vi.advanceTimersByTimeAsync(10 * 60_000);

      expect(handled()).toEqual([[{ tabId: 5 }, "Page.handleJavaScriptDialog", { accept: false }]]);
      expect(order(dbg.sendCommand, "Page.handleJavaScriptDialog")).toBeLessThan(order(dbg.detach));
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 6 });
    });

    it("dismisses open dialogs before releasing every tab when the connection to the daemon closes", async () => {
      relay.onEvent({ tabId: 6 }, "Page.javascriptDialogOpening", { type: "confirm", message: "sure?" });

      await relay.detachAll();

      expect(handled()).toEqual([[{ tabId: 6 }, "Page.handleJavaScriptDialog", { accept: false }]]);
      expect(order(dbg.sendCommand, "Page.handleJavaScriptDialog")).toBeLessThan(
        dbg.detach.mock.invocationCallOrder[
          dbg.detach.mock.calls.findIndex((call) => (call[0] as { tabId: number }).tabId === 6)
        ],
      );
      expect(dbg.detach).toHaveBeenCalledTimes(2);
    });

    it("dismisses on the top-level session even when the dialog event came from a child session", async () => {
      relay.onEvent({ tabId: 5, sessionId: "S1" }, "Page.javascriptDialogOpening", { type: "alert", message: "x" });

      await relay.detachAll();

      expect(handled()).toEqual([[{ tabId: 5 }, "Page.handleJavaScriptDialog", { accept: false }]]);
    });

    it("also dismisses a dialog it still knows about when the daemon asks for the detach", async () => {
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });

      expect(await registry.dispatch("debugger.detach", { tabId: 5 })).toEqual({ ok: true, result: { tabIds: [5] } });

      expect(handled()).toEqual([[{ tabId: 5 }, "Page.handleJavaScriptDialog", { accept: false }]]);
      expect(order(dbg.sendCommand, "Page.handleJavaScriptDialog")).toBeLessThan(order(dbg.detach));
    });

    it("does not touch a dialog that has already closed, or a tab without one", async () => {
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogClosed", { result: true, userInput: "" });

      await relay.detachAll();
      await vi.advanceTimersByTimeAsync(10 * 60_000);

      expect(handled()).toEqual([]);
    });

    it("still releases the tab when dismissing the dialog fails", async () => {
      vi.spyOn(console, "warn").mockImplementation(() => undefined);
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });
      dbg.sendCommand.mockRejectedValue(new Error("No dialog is showing"));

      await relay.detachAll();

      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
    });

    it("forgets the dialog of a tab Chrome detached on its own", async () => {
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });
      relay.onDetach({ tabId: 5 }, "canceled_by_user");
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });

      await relay.detachAll();

      expect(handled()).toEqual([]);
    });

    it("remembers an open dialog across a service worker restart", async () => {
      relay.onEvent({ tabId: 5 }, "Page.javascriptDialogOpening", { type: "alert", message: "hi" });
      dbg.getTargets.mockResolvedValue([
        { type: "page", id: "T5", tabId: 5, attached: true, title: "", url: "" },
        { type: "page", id: "T6", tabId: 6, attached: true, title: "", url: "" },
      ]);

      const next = new DebuggerRelay(() => undefined, store);
      await next.detachAll();

      expect(handled()).toEqual([[{ tabId: 5 }, "Page.handleJavaScriptDialog", { accept: false }]]);
    });
  });

  describe("endpoint support", () => {
    const IDLE = 10 * 60_000;
    const page = (tabId: number, url: string, title = "") => ({
      type: "page",
      id: `T${tabId}`,
      tabId,
      attached: false,
      title,
      url,
    });

    it("lists attachable tabs with their DevTools target IDs and leaves out chrome://, extension pages and the web store", async () => {
      dbg.getTargets.mockResolvedValue([
        page(1, "https://a.test/", "A"),
        page(2, "chrome://settings/"),
        page(3, "chrome-extension://abc/popup.html"),
        page(4, "https://chromewebstore.google.com/detail/x"),
        page(5, "https://chrome.google.com/webstore/detail/x"),
        page(6, "about:blank"),
        { type: "service_worker", id: "SW", attached: false, title: "", url: "https://a.test/sw.js" },
        { type: "background_page", id: "BG", tabId: 7, attached: false, title: "", url: "https://a.test/bg" },
      ]);

      const outcome = await registry.dispatch("debugger.targets", {});

      expect(outcome).toEqual({
        ok: true,
        result: {
          targets: [
            { tabId: 1, targetId: "T1", title: "A", url: "https://a.test/" },
            { tabId: 6, targetId: "T6", title: "", url: "about:blank" },
          ],
        },
      });
      if (outcome.ok) {
        expect(validateDebuggerTargetsResult(outcome.result)).toBe(true);
      }
    });

    it("returns the browser's real User-Agent", async () => {
      vi.stubGlobal("navigator", { userAgent: "Mozilla/5.0 Chrome/125.0.0.0" });

      expect(await registry.dispatch("debugger.userAgent", {})).toEqual({
        ok: true,
        result: { userAgent: "Mozilla/5.0 Chrome/125.0.0.0" },
      });
    });

    it("opens a tab in the last-focused window, in the background when asked, and returns its target ID", async () => {
      dbg.getTargets.mockResolvedValue([page(9, "about:blank")]);

      expect(await registry.dispatch("debugger.open", { url: "about:blank", background: true })).toEqual({
        ok: true,
        result: { tabId: 9, targetId: "T9" },
      });
      expect(tabsCreate).toHaveBeenCalledWith({ url: "about:blank", windowId: 3, active: false });
    });

    it("answers NOT_FOUND and leaves no endpoint mark when Chrome lists no target for the tab it opened", async () => {
      dbg.getTargets.mockResolvedValue([]);

      expect(await registry.dispatch("debugger.open", { url: "about:blank" })).toEqual({
        ok: false,
        code: "NOT_FOUND",
        message: "no debuggable target for tab 9",
      });
      expect(store.ownedTabIds()).toEqual([]);

      await registry.dispatch("debugger.send", { tabId: 9, method: "Page.enable" });
      await vi.advanceTimersByTimeAsync(IDLE);
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 9 });
    });

    it("drops every endpoint mark when the connection to the daemon closes, so later attaches get the idle backstop", async () => {
      await registry.dispatch("debugger.own", { tabId: 6, owned: true });

      await relay.detachAll();
      expect(store.ownedTabIds()).toEqual([]);

      await registry.dispatch("debugger.send", { tabId: 6, method: "Page.enable" });
      await vi.advanceTimersByTimeAsync(IDLE);
      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 6 });
    });

    it("keeps a tab it opened for the endpoint attached past the idle backstop, and remembers it in the store", async () => {
      dbg.getTargets.mockResolvedValue([page(9, "about:blank")]);
      await registry.dispatch("debugger.open", { url: "about:blank" });
      expect(store.ownedTabIds()).toEqual([9]);

      await registry.dispatch("debugger.send", { tabId: 9, method: "Page.enable" });
      await vi.advanceTimersByTimeAsync(IDLE + 1);

      expect(dbg.detach).not.toHaveBeenCalled();
      expect(notices).toEqual([]);
    });

    it("exempts an existing tab from the idle backstop once the endpoint owns it, and re-arms it when released", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      expect(await registry.dispatch("debugger.own", { tabId: 5, owned: true })).toEqual({
        ok: true,
        result: { owned: true },
      });
      await vi.advanceTimersByTimeAsync(IDLE + 1);
      expect(dbg.detach).not.toHaveBeenCalled();

      expect(await registry.dispatch("debugger.own", { tabId: 5, owned: false })).toEqual({
        ok: true,
        result: { owned: false },
      });
      expect(store.ownedTabIds()).toEqual([]);
      await vi.advanceTimersByTimeAsync(IDLE);

      expect(dbg.detach).toHaveBeenCalledWith({ tabId: 5 });
      expect(notices).toEqual([{ method: "debugger.detached", params: { tabId: 5, reason: "idle_timeout" } }]);
    });

    it("keeps a tab owned across a service worker restart and still exempts it", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      await registry.dispatch("debugger.own", { tabId: 5, owned: true });
      dbg.getTargets.mockResolvedValue([{ ...page(5, "https://a.test/"), attached: true }]);

      new DebuggerRelay((method, params) => {
        notices.push({ method, params } as Notice);
      }, store);
      await vi.advanceTimersByTimeAsync(IDLE + 1);

      expect(dbg.detach).not.toHaveBeenCalled();
      expect(store.ownedTabIds()).toEqual([5]);
    });

    it("drops ownership of a tab that no longer exists after a service worker restart", async () => {
      await registry.dispatch("debugger.own", { tabId: 5, owned: true });
      dbg.getTargets.mockResolvedValue([]);

      const next = new DebuggerRelay(() => undefined, store);
      registry = new HandlerRegistry();
      registerHandlers(registry, next);
      await registry.dispatch("debugger.detach", {});

      expect(store.ownedTabIds()).toEqual([]);
    });

    it("drops ownership when the debugger is detached from the tab or the tab is removed", async () => {
      await registry.dispatch("debugger.send", { tabId: 5, method: "Page.enable" });
      await registry.dispatch("debugger.own", { tabId: 5, owned: true });
      await registry.dispatch("debugger.detach", { tabId: 5 });
      expect(store.ownedTabIds()).toEqual([]);

      await registry.dispatch("debugger.own", { tabId: 6, owned: true });
      relay.onTabRemoved(6);
      await vi.advanceTimersByTimeAsync(0);
      expect(store.ownedTabIds()).toEqual([]);
    });

    it("closes the tabs the endpoint asks for and forgets their ownership", async () => {
      await registry.dispatch("debugger.own", { tabId: 5, owned: true });

      expect(await registry.dispatch("debugger.close", { tabId: 5 })).toEqual({ ok: true, result: { tabId: 5 } });
      expect(tabsRemove).toHaveBeenCalledWith(5);
      expect(store.ownedTabIds()).toEqual([]);
    });

    it("answers NOT_FOUND when closing a tab that does not exist", async () => {
      tabsGet.mockRejectedValue(new Error("No tab with id: 8"));

      expect(await registry.dispatch("debugger.close", { tabId: 8 })).toEqual({
        ok: false,
        code: "NOT_FOUND",
        message: "no tab 8",
      });
      expect(tabsRemove).not.toHaveBeenCalled();
    });

    describe("tab notifications", () => {
      it("reports a created attachable tab with its target ID", async () => {
        dbg.getTargets.mockResolvedValue([page(7, "https://b.test/", "B")]);

        relay.onTabCreated({ id: 7, url: "", pendingUrl: "https://b.test/" } as chrome.tabs.Tab);
        await vi.advanceTimersByTimeAsync(0);

        expect(notices).toEqual([
          {
            method: "debugger.tabCreated",
            params: { tabId: 7, targetId: "T7", title: "B", url: "https://b.test/" },
          },
        ]);
      });

      it("reports a title, URL or load-status change of an attachable tab, and ignores other changes", async () => {
        dbg.getTargets.mockResolvedValue([page(7, "https://b.test/next", "Next")]);

        relay.onTabUpdated(7, { audible: true });
        relay.onTabUpdated(7, { url: "https://b.test/next" });
        await vi.advanceTimersByTimeAsync(0);

        expect(notices).toEqual([
          {
            method: "debugger.tabUpdated",
            params: { tabId: 7, targetId: "T7", title: "Next", url: "https://b.test/next" },
          },
        ]);
      });

      it("does not report tabs that cannot be attached", async () => {
        dbg.getTargets.mockResolvedValue([page(7, "chrome://settings/")]);

        relay.onTabCreated({ id: 7, url: "chrome://settings/" } as chrome.tabs.Tab);
        relay.onTabUpdated(7, { status: "complete" });
        await vi.advanceTimersByTimeAsync(0);

        expect(notices).toEqual([]);
      });

      it("reports a removed tab", () => {
        relay.onTabRemoved(7);

        expect(notices).toEqual([{ method: "debugger.tabRemoved", params: { tabId: 7 } }]);
      });
    });
  });
});
