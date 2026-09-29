import { describe, expect, it } from "vitest";
import { ManualTimers } from "@/offscreen/connection.fixture";
import type { BookmarkRemovalDetail } from "@/shared/approvals";
import type { OffscreenCommand, RpcContext } from "@/shared/messages";
import { Approvals } from "./approvals";
import { Background, type StorageLike } from "./controller";
import { HandlerError, HandlerRegistry } from "./registry";

class MemoryStorage implements StorageLike {
  readonly items = new Map<string, unknown>();

  get(keys: string[]): Promise<Record<string, unknown>> {
    return Promise.resolve(
      Object.fromEntries(keys.filter((k) => this.items.has(k)).map((k) => [k, this.items.get(k)])),
    );
  }

  set(items: Record<string, unknown>): Promise<void> {
    Object.entries(items).forEach(([k, v]) => this.items.set(k, v));
    return Promise.resolve();
  }

  remove(keys: string | string[]): Promise<void> {
    [keys].flat().forEach((k) => this.items.delete(k));
    return Promise.resolve();
  }
}

const detail: BookmarkRemovalDetail = {
  summary: { items: 1, bookmarks: 1, folders: 0, containedBookmarks: 0, containedFolders: 0 },
  items: [{ id: "14", type: "bookmark", title: "News", url: "https://news.example/", parentId: "1", path: ["Bar"] }],
};

function setup(storage = new MemoryStorage()) {
  const commands: OffscreenCommand[] = [];
  let reply: unknown = undefined;
  const registry = new HandlerRegistry();
  registry.register("windows.list", () => Promise.resolve({ windows: [] }));
  registry.registerApproval("bookmarks.remove", {
    prepare: (params) =>
      params.ids.includes("999")
        ? Promise.reject(new HandlerError("NOT_FOUND", "no bookmark 999"))
        : Promise.resolve(detail),
    execute: () => Promise.resolve({ ids: ["14"], bookmarks: 1, folders: 0 }),
  });
  registry.registerApproval("extensions.uninstall", {
    prepare: (params) =>
      Promise.resolve({
        id: params.id,
        name: "Tab Tidy",
        version: "0.9.3",
        description: "",
        installType: "normal",
        enabled: true,
      }),
    execute: () => Promise.reject(new Error("requires a user gesture")),
    inWindow: true,
  });
  const offscreen = (command: OffscreenCommand) => {
    commands.push(command);
    return Promise.resolve(reply);
  };
  const windowsOpened: string[] = [];
  const approvals = new Approvals({
    storage: new MemoryStorage(),
    timers: new ManualTimers(),
    windows: {
      create: (options) => {
        windowsOpened.push(options.url);
        return Promise.resolve({ id: 1 });
      },
      update: () => Promise.resolve({}),
      remove: () => Promise.resolve(),
    },
    badge: {
      setBadgeText: () => Promise.resolve(),
      setBadgeBackgroundColor: () => Promise.resolve(),
      setBadgeTextColor: () => Promise.resolve(),
    },
    pageUrl: "approval/index.html",
    browserName: () => Promise.resolve("edge-1234"),
    settle: async (requestId, outcome) => {
      await offscreen({ target: "offscreen", type: "settle", requestId, outcome });
    },
    execute: (request) => registry.execute(request),
    executesInWindow: (kind) => registry.executesInWindow(kind),
    broadcast: () => undefined,
  });
  const background = new Background({
    storage,
    offscreen,
    registry,
    approvals,
    browser: Promise.resolve({
      product: { brand: "Microsoft Edge", slug: "edge", product: "Edge" },
      version: "129.0.2792.65",
    }),
    extensionVersion: "0.1.0",
  });
  return {
    background,
    storage,
    commands,
    windowsOpened,
    setReply: (next: unknown) => {
      reply = next;
    },
  };
}

describe("instance identity and configuration", () => {
  it("generates an instance ID and default name on first start and reuses them afterwards", async () => {
    const first = setup();
    const config = await first.background.handle({ target: "background", type: "offscreenReady" });

    expect(config).toMatchObject({ instanceId: expect.stringMatching(/^[0-9a-f]{32}$/) as unknown });
    const { instanceId, name } = config as { instanceId: string; name: string };
    expect(name).toBe(`edge-${instanceId.slice(0, 4)}`);
    expect(first.storage.items.get("instanceId")).toBe(instanceId);
    expect(first.storage.items.get("name")).toBe(name);

    const restarted = setup(first.storage);
    await expect(restarted.background.handle({ target: "background", type: "offscreenReady" })).resolves.toMatchObject({
      instanceId,
      name,
    });
  });

  it("creates a single identity when several requests race on first start", async () => {
    const { background, storage } = setup();
    const [a, b] = await Promise.all([
      background.handle({ target: "background", type: "offscreenReady" }),
      background.handle({ target: "background", type: "offscreenReady" }),
    ]);

    expect((a as { instanceId: string }).instanceId).toBe((b as { instanceId: string }).instanceId);
    expect(storage.items.get("instanceId")).toBe((a as { instanceId: string }).instanceId);
  });

  it("hands the offscreen document the stored pairing, the default address and the declared methods", async () => {
    const { background, storage } = setup();
    await storage.set({ key: "ab".repeat(32) });

    await expect(background.handle({ target: "background", type: "offscreenReady" })).resolves.toMatchObject({
      key: "ab".repeat(32),
      address: "127.0.0.1:8643",
      methods: ["windows.list", "bookmarks.remove", "extensions.uninstall"],
      product: "Edge",
      productVersion: "129.0.2792.65",
      extensionVersion: "0.1.0",
    });
  });

  it("hands over the default name for pairing even after the instance was renamed", async () => {
    const { background, storage } = setup();
    const { instanceId } = (await background.handle({ target: "background", type: "offscreenReady" })) as {
      instanceId: string;
    };
    await storage.set({ name: "work" });

    await expect(background.handle({ target: "background", type: "offscreenReady" })).resolves.toMatchObject({
      name: "work",
      defaultName: `edge-${instanceId.slice(0, 4)}`,
    });
  });

  it("reports no key before pairing", async () => {
    const { background } = setup();

    await expect(background.handle({ target: "background", type: "offscreenReady" })).resolves.toMatchObject({
      key: null,
    });
  });
});

describe("persistence reported by the offscreen document", () => {
  it("saves the key and name of a completed pairing", async () => {
    const { background, storage } = setup();
    await background.handle({ target: "background", type: "paired", key: "cd".repeat(32), name: "edge-1234" });

    expect(storage.items.get("key")).toBe("cd".repeat(32));
    expect(storage.items.get("name")).toBe("edge-1234");
  });

  it("saves a name the daemon accepted", async () => {
    const { background, storage } = setup();
    await background.handle({ target: "background", type: "renamed", name: "work" });

    expect(storage.items.get("name")).toBe("work");
  });

  it("runs business requests through the handler registry", async () => {
    const { background } = setup();

    await expect(
      background.handle({
        target: "background",
        type: "rpc",
        method: "windows.list",
        input: {},
        context: context("r0"),
      }),
    ).resolves.toEqual({ ok: true, result: { windows: [] } });
  });
});

function context(requestId: string): RpcContext {
  return { requestId, clientId: "sctl-cli", connection: "conn-1", receivedAt: 1_000_000 };
}

describe("requests that need approval", () => {
  const remove = (requestId: string, ids = ["14"]) =>
    ({
      target: "background",
      type: "rpc",
      method: "bookmarks.remove",
      input: { ids },
      context: context(requestId),
    }) as const;
  const statuses = async (background: Background) =>
    (
      (await background.handle({ target: "background", type: "approvalView" })) as {
        items: Array<{ id: string; status: string }>;
      }
    ).items.map((item) => [item.id, item.status]);

  it("defers the answer, opens the approval window, and sends the result to the offscreen document once approved", async () => {
    const { background, commands, windowsOpened } = setup();

    await expect(background.handle(remove("r1"))).resolves.toEqual({ deferred: true });
    expect(windowsOpened).toEqual(["approval/index.html"]);
    expect(commands).toEqual([]);

    await background.handle({ target: "background", type: "approvalDecide", id: "r1", decision: "approve" });

    expect(commands).toEqual([
      {
        target: "offscreen",
        type: "settle",
        requestId: "r1",
        outcome: { ok: true, result: { ids: ["14"], bookmarks: 1, folders: 0 } },
      },
    ]);
  });

  it("leaves an approved uninstall to the approval window and sends the outcome it reports to the offscreen document", async () => {
    const { background, commands } = setup();
    await background.handle({
      target: "background",
      type: "rpc",
      method: "extensions.uninstall",
      input: { id: "abc" },
      context: context("u1"),
    });

    await expect(
      background.handle({ target: "background", type: "approvalDecide", id: "u1", decision: "approve" }),
    ).resolves.toEqual({ executeInWindow: true });
    expect(commands).toEqual([]);

    const outcome = {
      ok: true,
      result: { contentTrust: "untrusted-page-content", id: "abc", name: "Tab Tidy" },
    } as const;
    await background.handle({ target: "background", type: "approvalFinish", id: "u1", outcome });

    expect(commands).toEqual([{ target: "offscreen", type: "settle", requestId: "u1", outcome }]);
  });

  it("answers a failed pre-check at once without queueing it", async () => {
    const { background, windowsOpened } = setup();

    await expect(background.handle(remove("r1", ["999"]))).resolves.toEqual({
      ok: false,
      code: "NOT_FOUND",
      message: "no bookmark 999",
    });
    expect(windowsOpened).toEqual([]);
    expect(await statuses(background)).toEqual([]);
  });

  it("voids a request the daemon cancelled, and every request of a lost connection or a restarted offscreen document", async () => {
    const { background, commands } = setup();
    await background.handle(remove("r1"));
    await background.handle(remove("r2"));
    await background.handle(remove("r3"));

    await background.handle({ target: "background", type: "rpcCancel", requestId: "r1" });
    await background.handle({ target: "background", type: "disconnected", connection: "conn-2" });
    expect(await statuses(background)).toEqual([
      ["r1", "cancelled"],
      ["r2", "pending"],
      ["r3", "pending"],
    ]);

    await background.handle({ target: "background", type: "disconnected", connection: "conn-1" });
    await background.handle(remove("r4"));
    await background.handle({ target: "background", type: "offscreenReady" });
    expect(await statuses(background)).toEqual([
      ["r1", "cancelled"],
      ["r2", "voided"],
      ["r3", "voided"],
      ["r4", "voided"],
    ]);
    expect(commands).toEqual([]);
  });
});

describe("popup requests", () => {
  it("forwards getState to the offscreen document", async () => {
    const { background, commands, setReply } = setup();
    setReply({ status: "unpaired" });

    await expect(background.handle({ target: "background", type: "getState" })).resolves.toEqual({
      status: "unpaired",
    });
    expect(commands).toEqual([{ target: "offscreen", type: "getState" }]);
  });

  it("pairs with the normalized code and refuses a malformed one", async () => {
    const { background, commands } = setup();

    await expect(background.handle({ target: "background", type: "pair", code: "7k3m-9qpx" })).resolves.toEqual({
      ok: true,
    });
    await expect(background.handle({ target: "background", type: "pair", code: "12" })).resolves.toEqual({
      ok: false,
      error: "invalid-code",
    });
    expect(commands).toEqual([{ target: "offscreen", type: "pair", code: "7K3M9QPX" }]);
  });

  it("refuses an invalid name without reconnecting and passes on the offscreen result otherwise", async () => {
    const { background, commands, setReply } = setup();
    setReply({ ok: false, error: "name-taken" });

    await expect(background.handle({ target: "background", type: "rename", name: "Bad Name" })).resolves.toEqual({
      ok: false,
      error: "invalid-name",
    });
    await expect(background.handle({ target: "background", type: "rename", name: "work" })).resolves.toEqual({
      ok: false,
      error: "name-taken",
    });
    expect(commands).toEqual([{ target: "offscreen", type: "rename", name: "work" }]);
  });

  it("saves a valid daemon address and reconnects to it, and refuses an invalid one", async () => {
    const { background, commands, storage } = setup();

    await expect(
      background.handle({ target: "background", type: "setAddress", address: " 127.0.0.1:9000 " }),
    ).resolves.toEqual({ ok: true });
    await expect(background.handle({ target: "background", type: "setAddress", address: "http://x" })).resolves.toEqual(
      { ok: false, error: "invalid-address" },
    );
    expect(storage.items.get("address")).toBe("127.0.0.1:9000");
    expect(commands).toEqual([{ target: "offscreen", type: "setAddress", address: "127.0.0.1:9000" }]);

    await expect(background.handle({ target: "background", type: "offscreenReady" })).resolves.toMatchObject({
      address: "127.0.0.1:9000",
    });
  });

  it("forget deletes the stored key and disconnects", async () => {
    const { background, commands, storage } = setup();
    await storage.set({ key: "ab".repeat(32), name: "work" });

    await background.handle({ target: "background", type: "forget" });
    expect(storage.items.has("key")).toBe(false);
    expect(storage.items.get("name")).toBe("work");
    expect(commands).toEqual([{ target: "offscreen", type: "forget" }]);
  });

  it("retry now is passed to the offscreen document", async () => {
    const { background, commands } = setup();
    await background.handle({ target: "background", type: "retryNow" });

    expect(commands).toEqual([{ target: "offscreen", type: "retryNow" }]);
  });
});
