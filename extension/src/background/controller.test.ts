import { describe, expect, it } from "vitest";
import type { OffscreenCommand } from "@/shared/messages";
import { Background, type StorageLike } from "./controller";
import { HandlerRegistry } from "./registry";

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

function setup(storage = new MemoryStorage()) {
  const commands: OffscreenCommand[] = [];
  const disconnects = { count: 0 };
  let reply: unknown = undefined;
  const registry = new HandlerRegistry();
  registry.register("windows.list", () => Promise.resolve({ windows: [] }));
  const background = new Background({
    storage,
    offscreen: (command) => {
      commands.push(command);
      return Promise.resolve(reply);
    },
    registry,
    onConnectionClosed: () => {
      disconnects.count++;
      return Promise.resolve();
    },
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
    disconnects,
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
      methods: ["windows.list"],
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
      background.handle({ target: "background", type: "rpc", method: "windows.list", input: {} }),
    ).resolves.toEqual({ ok: true, result: { windows: [] } });
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

describe("connection lifecycle and notifications", () => {
  it("runs the connection-closed hook when the offscreen document reports a lost connection", async () => {
    const { background, disconnects } = setup();

    await background.handle({ target: "background", type: "connectionClosed" });

    expect(disconnects.count).toBe(1);
  });

  it("forwards a notification to the offscreen document", async () => {
    const { background, commands } = setup();

    await background.notify("debugger.detached", { tabId: 5, reason: "canceled_by_user" });

    expect(commands).toEqual([
      {
        target: "offscreen",
        type: "notify",
        method: "debugger.detached",
        params: { tabId: 5, reason: "canceled_by_user" },
      },
    ]);
  });
});
