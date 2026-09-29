import { describe, expect, it } from "vitest";
import { LIMITS } from "@/protocol/generated/protocol.generated";
import { ManualTimers, until } from "@/offscreen/connection.fixture";
import type { ApprovalRequest, ApprovalView } from "@/shared/approvals";
import type { RpcContext, RpcOutcome } from "@/shared/messages";
import { Approvals, EXPIRY_MARGIN_MS, type ApprovalDeps, type SessionStorageLike } from "./approvals";

class MemorySession implements SessionStorageLike {
  readonly items = new Map<string, unknown>();

  get(keys: string[]): Promise<Record<string, unknown>> {
    // chrome.storage 返回的是结构化克隆，调用方改动它不会影响存储里的值。
    return Promise.resolve(
      structuredClone(Object.fromEntries(keys.filter((k) => this.items.has(k)).map((k) => [k, this.items.get(k)]))),
    );
  }

  set(items: Record<string, unknown>): Promise<void> {
    Object.entries(items).forEach(([k, v]) => this.items.set(k, structuredClone(v)));
    return Promise.resolve();
  }
}

class FakeWindows {
  private nextId = 100;
  readonly open = new Set<number>();
  readonly created: Array<{ url: string; type: string; width: number; height: number; focused: boolean }> = [];
  readonly focused: number[] = [];
  readonly removed: number[] = [];

  create(options: { url: string; type: "popup"; width: number; height: number; focused: boolean }) {
    const id = this.nextId++;
    this.created.push(options);
    this.open.add(id);
    return Promise.resolve({ id });
  }

  update(id: number, _info: { focused: true }) {
    if (!this.open.has(id)) {
      return Promise.reject(new Error(`No window with id: ${id}.`));
    }
    this.focused.push(id);
    return Promise.resolve({});
  }

  remove(id: number) {
    this.open.delete(id);
    this.removed.push(id);
    return Promise.resolve();
  }
}

class FakeBadge {
  text = "";
  background = "";
  color = "";

  setBadgeText({ text }: { text: string }) {
    this.text = text;
    return Promise.resolve();
  }

  setBadgeBackgroundColor({ color }: { color: string }) {
    this.background = color;
    return Promise.resolve();
  }

  setBadgeTextColor({ color }: { color: string }) {
    this.color = color;
    return Promise.resolve();
  }
}

const PAGE_URL = "chrome-extension://abc/approval/index.html";

const removal = (id: string): ApprovalRequest => ({
  kind: "bookmarks.remove",
  detail: {
    summary: { items: 1, bookmarks: 1, folders: 0, containedBookmarks: 0, containedFolders: 0 },
    items: [{ id, type: "bookmark", title: `B${id}`, url: `https://${id}.example/`, parentId: "1", path: ["Bar"] }],
  },
});

// 同一套存储、窗口与角标上可以反复“重启” service worker，模拟 MV3 休眠后被唤醒：重启后的实例用新的计时器，
// 旧实例的计时器随被回收的 service worker 一起消失。
function world() {
  const storage = new MemorySession();
  const windows = new FakeWindows();
  const badge = new FakeBadge();
  let timers = new ManualTimers();
  const settled: Array<{ requestId: string; outcome: RpcOutcome }> = [];
  const executed: ApprovalRequest[] = [];
  const views: ApprovalView[] = [];
  let execution: (request: ApprovalRequest) => Promise<RpcOutcome> = () =>
    Promise.resolve({ ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } });

  const start = (clock = timers) => {
    timers = clock;
    const deps: ApprovalDeps = {
      storage,
      timers,
      windows,
      badge,
      pageUrl: PAGE_URL,
      browserName: () => Promise.resolve("chrome-a"),
      settle: (requestId, outcome) => {
        settled.push({ requestId, outcome });
        return Promise.resolve();
      },
      execute: (request) => {
        executed.push(request);
        return execution(request);
      },
      broadcast: (view) => views.push(view),
    };
    return new Approvals(deps);
  };

  const context = (requestId: string, connection = "conn-1"): RpcContext => ({
    requestId,
    clientId: "sctl-cli",
    connection,
    receivedAt: timers.now(),
  });

  return {
    storage,
    windows,
    badge,
    get timers() {
      return timers;
    },
    settled,
    executed,
    views,
    start,
    context,
    executeWith: (next: (request: ApprovalRequest) => Promise<RpcOutcome>) => {
      execution = next;
    },
  };
}

async function withPending(...ids: string[]) {
  const w = world();
  const approvals = w.start();
  for (const id of ids) {
    await approvals.enqueue(w.context(id), removal(id));
  }
  return { w, approvals };
}

const statuses = async (approvals: Approvals) =>
  (await approvals.view()).items.map((item) => [item.id, item.status] as const);

describe("opening the approval window", () => {
  it("opens one 440×680 popup window for the first request and queues later ones in it", async () => {
    const { w, approvals } = await withPending("r1");

    expect(w.windows.created).toEqual([{ url: PAGE_URL, type: "popup", width: 440, height: 680, focused: true }]);

    await approvals.enqueue(w.context("r2"), removal("r2"));

    expect(w.windows.created).toHaveLength(1);
    expect(w.windows.focused).toEqual([100]);
    expect(await statuses(approvals)).toEqual([
      ["r1", "pending"],
      ["r2", "pending"],
    ]);
  });

  it("shows each request with the instance name, the self-reported requester, arrival time and deadline", async () => {
    const { w, approvals } = await withPending("r1");

    const view = await approvals.view();
    expect(view.browserName).toBe("chrome-a");
    expect(view.items[0]).toMatchObject({
      id: "r1",
      kind: "bookmarks.remove",
      requester: "sctl-cli",
      receivedAt: w.timers.now(),
      expiresAt: w.timers.now() + LIMITS.writeDecisionTtlMs - EXPIRY_MARGIN_MS,
      status: "pending",
      detail: removal("r1").detail,
    });
    expect(w.views.at(-1)).toEqual(view);
  });

  it("opens a new window when the recorded one is gone", async () => {
    const { w, approvals } = await withPending("r1");
    w.windows.open.clear();

    await approvals.enqueue(w.context("r2"), removal("r2"));

    expect(w.windows.created).toHaveLength(2);
  });

  it("brings the window forward on request, reopening it when there is none", async () => {
    const { w, approvals } = await withPending("r1");
    await approvals.focus();
    expect(w.windows.focused).toEqual([100]);

    w.windows.open.clear();
    await approvals.focus();
    expect(w.windows.created).toHaveLength(2);
  });
});

describe("toolbar badge", () => {
  it("shows the number of requests awaiting a decision in amber with dark text and clears when none are left", async () => {
    const { w, approvals } = await withPending("r1", "r2");
    expect(w.badge).toMatchObject({ text: "2", background: "#E9A93A", color: "#0A1622" });

    await approvals.decide("r1", "reject");
    expect(w.badge.text).toBe("1");

    await approvals.decide("r2", "approve");
    expect(w.badge.text).toBe("");
  });
});

describe("deciding", () => {
  it("runs an approved request once and answers the requester with its result", async () => {
    const { w, approvals } = await withPending("r1");

    await Promise.all([approvals.decide("r1", "approve"), approvals.decide("r1", "approve")]);

    expect(w.executed).toEqual([removal("r1")]);
    expect(w.settled).toEqual([
      { requestId: "r1", outcome: { ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } } },
    ]);
    expect((await approvals.view()).items[0]).toMatchObject({
      status: "done",
      outcome: { ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } },
    });
  });

  it("answers the failure when the approved request no longer matches what was shown", async () => {
    const { w, approvals } = await withPending("r1");
    w.executeWith(() => Promise.resolve({ ok: false, code: "CONFLICT", message: "bookmarks changed" }));

    await approvals.decide("r1", "approve");

    expect(w.settled).toEqual([
      { requestId: "r1", outcome: { ok: false, code: "CONFLICT", message: "bookmarks changed" } },
    ]);
    expect((await approvals.view()).items[0]).toMatchObject({
      status: "failed",
      outcome: { ok: false, code: "CONFLICT" },
    });
  });

  it("answers USER_REJECTED on rejection, runs nothing, and closes the window once the queue is empty", async () => {
    const { w, approvals } = await withPending("r1", "r2");

    await approvals.decide("r1", "reject");
    await approvals.decide("r1", "approve");

    expect(w.executed).toEqual([]);
    expect(w.settled).toEqual([
      { requestId: "r1", outcome: expect.objectContaining({ ok: false, code: "USER_REJECTED" }) as unknown },
    ]);
    expect(await statuses(approvals)).toEqual([["r2", "pending"]]);
    expect(w.windows.removed).toEqual([]);

    await approvals.decide("r2", "reject");
    expect(w.windows.removed).toEqual([100]);
  });

  it("keeps a finished request on show until it is dismissed", async () => {
    const { w, approvals } = await withPending("r1", "r2");
    await approvals.decide("r1", "approve");

    await approvals.dismiss("r1");
    await approvals.dismiss("r2");

    expect(await statuses(approvals)).toEqual([["r2", "pending"]]);
    expect(w.settled).toHaveLength(1);
  });
});

describe("closing the window", () => {
  it("rejects every queued request when the user closes the window", async () => {
    const { w, approvals } = await withPending("r1", "r2");
    await approvals.decide("r1", "approve");

    await approvals.windowRemoved(100);

    expect(w.settled.map((s) => [s.requestId, s.outcome.ok ? "ok" : s.outcome.code])).toEqual([
      ["r1", "ok"],
      ["r2", "USER_REJECTED"],
    ]);
    expect((await approvals.view()).items).toEqual([]);
    expect(w.badge.text).toBe("");
  });

  it("ignores other windows closing", async () => {
    const { w, approvals } = await withPending("r1");

    await approvals.windowRemoved(7);

    expect(w.settled).toEqual([]);
    expect(await statuses(approvals)).toEqual([["r1", "pending"]]);
  });

  it("rejects every queued request and closes the window when asked to from the window", async () => {
    const { w, approvals } = await withPending("r1", "r2");

    await approvals.closeWindow();

    expect(w.settled.map((s) => s.requestId)).toEqual(["r1", "r2"]);
    expect(w.windows.removed).toEqual([100]);
    await approvals.windowRemoved(100);
    expect(w.settled).toHaveLength(2);
  });

  it("lets a request that is already running finish and answer after the window closes", async () => {
    const { w, approvals } = await withPending("r1");
    let finish: (outcome: RpcOutcome) => void = () => {};
    w.executeWith(() => new Promise((resolve) => (finish = resolve)));

    const deciding = approvals.decide("r1", "approve");
    await Promise.resolve();
    await approvals.windowRemoved(100);
    finish({ ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } });
    await deciding;

    expect(w.settled).toEqual([{ requestId: "r1", outcome: expect.objectContaining({ ok: true }) as unknown }]);
    expect((await approvals.view()).items).toEqual([]);
  });
});

describe("expiry and cancellation", () => {
  it("expires an undecided request at its deadline, answers OPERATION_EXPIRED and no longer runs it", async () => {
    const { w, approvals } = await withPending("r1");

    w.timers.advance(LIMITS.writeDecisionTtlMs - EXPIRY_MARGIN_MS - 1);
    await approvals.view();
    expect(w.settled).toEqual([]);

    w.timers.advance(1);
    await approvals.view();
    expect(w.settled).toEqual([
      { requestId: "r1", outcome: expect.objectContaining({ ok: false, code: "OPERATION_EXPIRED" }) as unknown },
    ]);
    expect(await statuses(approvals)).toEqual([["r1", "expired"]]);

    await approvals.cancel("r1");
    await approvals.decide("r1", "approve");
    expect(w.executed).toEqual([]);
    expect(w.settled).toHaveLength(1);
    expect(w.badge.text).toBe("");
  });

  it("voids a request the requester cancelled without answering it, and never runs it", async () => {
    const { w, approvals } = await withPending("r1");

    await approvals.cancel("r1");
    await approvals.decide("r1", "approve");

    expect(await statuses(approvals)).toEqual([["r1", "cancelled"]]);
    expect(w.executed).toEqual([]);
    expect(w.settled).toEqual([]);
    expect(w.badge.text).toBe("");
  });

  it("does not answer a request cancelled while it was running, but shows how it ended", async () => {
    const { w, approvals } = await withPending("r1");
    let finish: (outcome: RpcOutcome) => void = () => {};
    w.executeWith(() => new Promise((resolve) => (finish = resolve)));

    const deciding = approvals.decide("r1", "approve");
    await Promise.resolve();
    await approvals.cancel("r1");
    finish({ ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } });
    await deciding;

    expect(w.settled).toEqual([]);
    expect(await statuses(approvals)).toEqual([["r1", "done"]]);
  });

  it("voids every request of a connection that dropped, without answering, and keeps other connections' requests", async () => {
    const { w, approvals } = await withPending("r1");
    await approvals.enqueue(w.context("r2", "conn-2"), removal("r2"));

    await approvals.disconnected("conn-1");

    expect(await statuses(approvals)).toEqual([
      ["r1", "voided"],
      ["r2", "pending"],
    ]);
    await approvals.decide("r1", "approve");
    expect(w.executed).toEqual([]);
    expect(w.settled).toEqual([]);

    await approvals.disconnected();
    expect(await statuses(approvals)).toEqual([
      ["r1", "voided"],
      ["r2", "voided"],
    ]);
  });
});

describe("service worker restart", () => {
  it("restores pending requests, their window and deadlines from session storage", async () => {
    const { w } = await withPending("r1", "r2");

    const restarted = w.start(new ManualTimers());
    await restarted.restore();

    expect(await statuses(restarted)).toEqual([
      ["r1", "pending"],
      ["r2", "pending"],
    ]);
    expect(w.badge.text).toBe("2");

    await restarted.decide("r1", "approve");
    expect(w.settled.map((s) => s.requestId)).toEqual(["r1"]);

    await restarted.enqueue(w.context("r3"), removal("r3"));
    expect(w.windows.created).toHaveLength(1);

    w.timers.advance(LIMITS.writeDecisionTtlMs);
    await restarted.view();
    expect(w.settled.map((s) => [s.requestId, s.outcome.ok ? "ok" : s.outcome.code])).toEqual([
      ["r1", "ok"],
      ["r2", "OPERATION_EXPIRED"],
      ["r3", "OPERATION_EXPIRED"],
    ]);
  });

  it("counts a cancellation that wakes it after the deadline as expiry", async () => {
    const { w } = await withPending("r1");
    // service worker 在期限前被回收，期限到时没有计时器触发；daemon 的取消随后把它唤醒。
    const later = new ManualTimers();
    later.advance(LIMITS.writeDecisionTtlMs);
    const restarted = w.start(later);

    await restarted.cancel("r1");

    expect(await statuses(restarted)).toEqual([["r1", "expired"]]);
    expect(w.settled.map((s) => [s.requestId, s.outcome.ok ? "ok" : s.outcome.code])).toEqual([
      ["r1", "OPERATION_EXPIRED"],
    ]);
  });

  it("does not open the window again or answer anything when there is nothing to restore", async () => {
    const w = world();
    const approvals = w.start();

    await approvals.restore();

    expect(w.windows.created).toEqual([]);
    expect(w.settled).toEqual([]);
    expect(w.badge.text).toBe("");
  });
});

describe("recovering from interruptions", () => {
  it("reports a request whose execution was cut short by a service worker restart as failed", async () => {
    const { w, approvals } = await withPending("r1");
    w.executeWith(() => new Promise(() => {}));
    void approvals.decide("r1", "approve");
    await until(() => w.executed.length === 1);

    const restarted = w.start(new ManualTimers());
    await restarted.restore();

    expect((await restarted.view()).items[0]).toMatchObject({
      status: "failed",
      outcome: { ok: false, code: "INTERNAL_ERROR" },
    });
    expect(w.settled.map((s) => [s.requestId, s.outcome.ok ? "ok" : s.outcome.code])).toEqual([
      ["r1", "INTERNAL_ERROR"],
    ]);
  });

  it("keeps a request queued when the window cannot be opened, and opens it on request later", async () => {
    const w = world();
    const approvals = w.start();
    const create = w.windows.create.bind(w.windows);
    w.windows.create = () => Promise.reject(new Error("no browser window"));

    await approvals.enqueue(w.context("r1"), removal("r1"));
    expect(await statuses(approvals)).toEqual([["r1", "pending"]]);
    expect(w.badge.text).toBe("1");

    w.windows.create = create;
    await approvals.focus();
    expect(w.windows.created).toHaveLength(1);
  });
});
