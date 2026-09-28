import { describe, expect, it } from "vitest";
import {
  INSTANCE_ID,
  PAIRING_CODE,
  SESSION_KEY,
  acceptSession,
  authenticate,
  conflict,
  harness,
  hello,
  until,
} from "./connection.fixture";

const DELIVERED_KEY = "ff".repeat(32);

describe("session handshake", () => {
  it("binds the instance identity into authentication, declares its name and capabilities, then connects", async () => {
    const h = harness();
    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 0, retryAt: null });
    const socket = h.socket();
    expect(socket.url).toBe("ws://127.0.0.1:8643/");
    socket.open();

    const reply = await authenticate(socket, { auth: { mode: "session", key: SESSION_KEY } });
    expect(reply.id).toBe("auth-1");
    expect(reply.result).toMatchObject({ mode: "session", peer: { kind: "browser", instanceId: INSTANCE_ID } });
    expect(reply.result?.nonceE).toMatch(/^[0-9a-f]{64}$/);

    const capabilities = await hello(socket);
    expect(capabilities.params).toEqual({
      schemaVersion: "1.0.0",
      methods: ["tabs.list"],
      peer: { name: "chrome-3f2a", product: "Chrome", productVersion: "129.0.6668.58", extensionVersion: "0.1.0" },
    });
    expect(h.state().status).toBe("reconnecting");

    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.state().status === "connected");
    expect(h.state()).toEqual({
      status: "connected",
      daemonVersion: "0.1.0",
      product: "Chrome",
      productVersion: "129.0.6668.58",
      connectedAt: h.timers.now(),
      instanceId: INSTANCE_ID,
      name: "chrome-3f2a",
      address: "127.0.0.1:8643",
    });
    expect(h.states.at(-1)).toEqual(h.state());
  });

  it("stops retrying and reports rejected when the daemon cannot prove it holds the session key", async () => {
    const h = harness();
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "session", key: SESSION_KEY }, corruptDaemonMac: true });
    await until(() => socket.closedWith !== null);
    socket.drop(socket.closedWith ?? 1005);

    expect(h.state().status).toBe("rejected");
    expect(h.timers.pending()).toBe(0);
  });

  it("stops retrying and reports rejected when the daemon refuses the handshake", async () => {
    const h = harness();
    const socket = h.socket();
    socket.open();
    const reply = socket.nextSent();
    socket.receive({
      jsonrpc: "2.0",
      id: "auth-1",
      method: "$session.authenticate",
      params: { nonceD: "d".repeat(64) },
    });
    await reply;
    socket.drop(1008);

    await until(() => h.state().status !== "reconnecting");
    expect(h.state().status).toBe("rejected");
    expect(h.timers.pending()).toBe(0);
    expect(h.sockets).toHaveLength(1);
  });

  it("abandons a handshake that does not complete within the auth timeout and retries", () => {
    const h = harness();
    const socket = h.socket();
    socket.open();
    h.timers.advance(4999);
    expect(socket.closedWith).toBeNull();
    h.timers.advance(1);
    expect(socket.closedWith).not.toBeNull();
    socket.drop(socket.closedWith ?? 1005);

    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 1 });
  });

  it("closes the connection on a malformed challenge and retries", async () => {
    const h = harness();
    const socket = h.socket();
    socket.open();
    socket.receive({ jsonrpc: "2.0", id: "auth-1", method: "$session.authenticate", params: { nonceD: "zz" } });
    await until(() => socket.closedWith !== null);
    socket.drop(socket.closedWith ?? 1005);

    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 1 });
  });
});

describe("reconnect backoff", () => {
  it("retries an unreachable daemon after 1 s, doubling each time up to 30 s", () => {
    const h = harness();
    const delays: number[] = [];
    for (let attempt = 1; attempt <= 7; attempt++) {
      const before = h.sockets.length;
      h.socket().drop(1006);
      const state = h.state();
      expect(state).toMatchObject({ status: "reconnecting", attempt });
      const retryAt = state.status === "reconnecting" ? state.retryAt : null;
      if (retryAt === null) {
        throw new Error("no retry scheduled");
      }
      const delay = retryAt - h.timers.now();
      delays.push(delay);
      h.timers.advance(delay - 1);
      expect(h.sockets).toHaveLength(before);
      h.timers.advance(1);
      expect(h.sockets).toHaveLength(before + 1);
    }
    expect(delays).toEqual([1000, 2000, 4000, 8000, 16000, 30000, 30000]);
  });

  it("retry now connects immediately and resets the backoff", () => {
    const h = harness();
    h.socket().drop(1006);
    h.timers.advance(1000);
    h.socket().drop(1006);
    h.timers.advance(2000);
    h.socket().drop(1006);
    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 3, retryAt: h.timers.now() + 4000 });

    h.connection.retryNow();
    expect(h.sockets).toHaveLength(4);
    expect(h.state()).toMatchObject({ status: "reconnecting", retryAt: null });
    h.socket().drop(1006);
    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 1, retryAt: h.timers.now() + 1000 });
    expect(h.timers.pending()).toBe(1);
  });

  it("reconnects 1 s after an established connection drops", async () => {
    const h = harness();
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");
    h.socket().drop(1001);

    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 1, retryAt: h.timers.now() + 1000 });
  });
});

describe("pairing", () => {
  it("starts unpaired and persists the delivered key only after the daemon accepts the capabilities", async () => {
    const h = harness({ key: null });
    expect(h.state().status).toBe("unpaired");
    expect(h.sockets).toHaveLength(0);

    h.connection.pair(PAIRING_CODE);
    expect(h.state().status).toBe("pairing");
    const socket = h.socket();
    socket.open();
    const reply = await authenticate(socket, {
      auth: { mode: "pairing", code: PAIRING_CODE, deliverKey: DELIVERED_KEY },
    });
    expect(reply.result).toMatchObject({ mode: "pairing", peer: { kind: "browser", instanceId: INSTANCE_ID } });
    const capabilities = await hello(socket);
    expect(capabilities.params?.peer).toMatchObject({ name: "chrome-3f2a" });
    expect(h.persisted.keys).toEqual([]);

    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.state().status === "connected");
    expect(h.persisted.keys).toEqual([{ key: DELIVERED_KEY, name: "chrome-3f2a" }]);

    // 之后的重连改用下发的会话密钥。
    socket.drop(1001);
    h.timers.advance(1000);
    await acceptSession(h.socket(), DELIVERED_KEY);
    await until(() => h.state().status === "connected");
  });

  it("reports a wrong or expired code as pair-failed without retrying", async () => {
    const h = harness({ key: null });
    h.connection.pair(PAIRING_CODE);
    const socket = h.socket();
    socket.open();
    const reply = socket.nextSent();
    socket.receive({
      jsonrpc: "2.0",
      id: "auth-1",
      method: "$session.authenticate",
      params: { nonceD: "d".repeat(64) },
    });
    await reply;
    socket.drop(1008);

    await until(() => h.state().status !== "pairing");
    expect(h.state()).toMatchObject({ status: "pair-failed", reason: "code-rejected" });
    expect(h.timers.pending()).toBe(0);
    expect(h.persisted.keys).toEqual([]);
  });

  it("reports an unreachable daemon as pair-unreachable with the address it tried", () => {
    const h = harness({ key: null, address: "127.0.0.1:9000" });
    h.connection.pair(PAIRING_CODE);
    h.socket().drop(1006);

    expect(h.state()).toMatchObject({ status: "pair-unreachable", address: "127.0.0.1:9000" });
    expect(h.timers.pending()).toBe(0);
  });

  it("reports a default name held by another instance as name-taken and keeps the pairing unsaved", async () => {
    const h = harness({ key: null });
    h.connection.pair(PAIRING_CODE);
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "pairing", code: PAIRING_CODE, deliverKey: DELIVERED_KEY } });
    conflict(socket, await hello(socket));

    await until(() => h.state().status === "pair-failed");
    expect(h.state()).toMatchObject({ status: "pair-failed", reason: "name-taken" });
    expect(h.persisted.keys).toEqual([]);
    expect(h.timers.pending()).toBe(0);
  });

  it("registers the default name when pairing again after the instance was renamed", async () => {
    const h = harness({ key: null, name: "work" });
    h.connection.pair(PAIRING_CODE);
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "pairing", code: PAIRING_CODE, deliverKey: DELIVERED_KEY } });
    const capabilities = await hello(socket);
    expect(capabilities.params?.peer).toMatchObject({ name: "chrome-3f2a" });

    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.state().status === "connected");
    expect(h.state().name).toBe("chrome-3f2a");
    expect(h.persisted.keys).toEqual([{ key: DELIVERED_KEY, name: "chrome-3f2a" }]);
  });

  it("stays unpaired when forgotten while the new pairing is still being saved", async () => {
    const h = harness({ key: null });
    h.connection.pair(PAIRING_CODE);
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "pairing", code: PAIRING_CODE, deliverKey: DELIVERED_KEY } });
    const capabilities = await hello(socket);
    const release = h.holdPersistence();
    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.persisted.keys.length === 1);

    h.connection.forget();
    release();
    await new Promise((resolve) => setTimeout(resolve, 5));
    h.connection.setAddress("127.0.0.1:9000");

    expect(h.state().status).toBe("unpaired");
    expect(h.sockets).toHaveLength(1);
  });

  it("re-pairing after a rejection replaces the old key", async () => {
    const h = harness();
    const first = h.socket();
    first.open();
    first.drop(1008);
    expect(h.state().status).toBe("rejected");

    h.connection.pair(PAIRING_CODE);
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "pairing", code: PAIRING_CODE, deliverKey: DELIVERED_KEY } });
    const capabilities = await hello(socket);
    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.state().status === "connected");
    expect(h.persisted.keys).toEqual([{ key: DELIVERED_KEY, name: "chrome-3f2a" }]);
  });
});

describe("rename", () => {
  async function connected() {
    const h = harness();
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");
    return h;
  }

  it("reconnects with the new name and persists it once the daemon accepts", async () => {
    const h = await connected();
    const old = h.socket();
    const result = h.connection.rename("work-laptop");
    expect(old.closedWith).not.toBeNull();

    const capabilities = await acceptSession(h.socket());
    expect(capabilities.params?.peer).toMatchObject({ name: "work-laptop" });
    await expect(result).resolves.toEqual({ ok: true });
    expect(h.persisted.names).toEqual(["work-laptop"]);
    expect(h.state()).toMatchObject({ status: "connected", name: "work-laptop" });
  });

  it("keeps the previous name and reconnects with it when another browser holds the new name", async () => {
    const h = await connected();
    const result = h.connection.rename("taken");
    const socket = h.socket();
    socket.open();
    await authenticate(socket, { auth: { mode: "session", key: SESSION_KEY } });
    conflict(socket, await hello(socket));

    await expect(result).resolves.toEqual({ ok: false, error: "name-taken" });
    const retry = await acceptSession(h.socket());
    expect(retry.params?.peer).toMatchObject({ name: "chrome-3f2a" });
    await until(() => h.state().status === "connected");
    expect(h.state()).toMatchObject({ name: "chrome-3f2a" });
    expect(h.persisted.names).toEqual([]);
  });

  it("keeps the name the daemon accepted when the address changes while the rename is being saved", async () => {
    const h = await connected();
    const result = h.connection.rename("work-laptop");
    const socket = h.socket();
    const release = h.holdPersistence();
    socket.open();
    await authenticate(socket, { auth: { mode: "session", key: SESSION_KEY } });
    const capabilities = await hello(socket);
    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
    await until(() => h.persisted.names.length === 1);

    h.connection.setAddress("127.0.0.1:9000");
    release();
    await expect(result).resolves.toEqual({ ok: true });
    const next = await acceptSession(h.socket());
    expect(next.params?.peer).toMatchObject({ name: "work-laptop" });
  });

  it("reports the rename as failed and reconnects with the saved name when the accepted name cannot be saved", async () => {
    const h = await connected();
    const result = h.connection.rename("work-laptop");
    const socket = h.socket();
    h.failPersistence(new Error("storage quota exceeded"));
    socket.open();
    await authenticate(socket, { auth: { mode: "session", key: SESSION_KEY } });
    const capabilities = await hello(socket);
    socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });

    await expect(result).resolves.toEqual({ ok: false, error: "not-connected" });
    await until(() => socket.closedWith !== null);
    socket.drop(1000);
    h.timers.advance(1000);
    const retry = await acceptSession(h.socket());
    expect(retry.params?.peer).toMatchObject({ name: "chrome-3f2a" });
  });

  it("keeps the previous name when the daemon cannot be reached during the rename", async () => {
    const h = await connected();
    const result = h.connection.rename("work-laptop");
    h.socket().drop(1006);

    await expect(result).resolves.toEqual({ ok: false, error: "not-connected" });
    expect(h.state()).toMatchObject({ status: "reconnecting", attempt: 1, name: "chrome-3f2a" });
  });

  it("refuses to rename while not connected", async () => {
    const h = harness();
    h.socket().drop(1006);

    await expect(h.connection.rename("work-laptop")).resolves.toEqual({ ok: false, error: "not-connected" });
    expect(h.sockets).toHaveLength(1);
  });
});

describe("address and forget", () => {
  it("reconnects to a changed address and keeps the pairing", async () => {
    const h = harness();
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");
    const old = h.socket();

    h.connection.setAddress("127.0.0.1:9000");
    expect(old.closedWith).not.toBeNull();
    expect(h.socket().url).toBe("ws://127.0.0.1:9000/");
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");
    expect(h.state()).toMatchObject({ address: "127.0.0.1:9000" });
  });

  it("updates the address shown before pairing without connecting", () => {
    const h = harness({ key: null });
    h.connection.setAddress("127.0.0.1:9000");

    expect(h.state()).toMatchObject({ status: "unpaired", address: "127.0.0.1:9000" });
    expect(h.sockets).toHaveLength(0);
  });

  it("forget closes the connection and returns to unpaired without retrying", async () => {
    const h = harness();
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");

    h.connection.forget();
    expect(h.socket().closedWith).not.toBeNull();
    expect(h.state().status).toBe("unpaired");
    expect(h.timers.pending()).toBe(0);
  });
});

describe("business requests", () => {
  async function connected() {
    const h = harness();
    await acceptSession(h.socket());
    await until(() => h.state().status === "connected");
    return h;
  }

  it("dispatches a declared method's input and answers with its result", async () => {
    const h = await connected();
    const socket = h.socket();
    const response = socket.nextSent();
    socket.receive({ jsonrpc: "2.0", id: "r1", method: "tabs.list", params: { clientId: "sctl-cli", input: {} } });

    expect(await response).toEqual({
      jsonrpc: "2.0",
      id: "r1",
      result: { contentTrust: "untrusted-page-content", tabs: [] },
    });
    expect(h.dispatched).toEqual([{ method: "tabs.list", input: {} }]);
  });

  it("answers a handler failure with the application error code", async () => {
    const h = await connected();
    h.setOutcome({ ok: false, code: "NOT_FOUND", message: "no such window" });
    const socket = h.socket();
    const response = socket.nextSent();
    socket.receive({ jsonrpc: "2.0", id: "r2", method: "tabs.list", params: { input: { windowId: 9 } } });

    expect(await response).toEqual({
      jsonrpc: "2.0",
      id: "r2",
      error: { code: -32000, message: "no such window", data: { code: "NOT_FOUND" } },
    });
  });

  it("answers invalid input with INVALID_REQUEST without dispatching", async () => {
    const h = await connected();
    const socket = h.socket();
    const response = socket.nextSent();
    socket.receive({ jsonrpc: "2.0", id: "r3", method: "tabs.list", params: { input: { windowId: "x" } } });

    expect((await response).error).toMatchObject({ code: -32000, data: { code: "INVALID_REQUEST" } });
    expect(h.dispatched).toEqual([]);
  });

  it("answers a method it did not declare with METHOD_NOT_FOUND", async () => {
    const h = await connected();
    const socket = h.socket();
    const response = socket.nextSent();
    socket.receive({ jsonrpc: "2.0", id: "r4", method: "tabs.open", params: { input: { url: "https://a.test" } } });

    expect((await response).error).toMatchObject({ code: -32000, data: { code: "METHOD_NOT_FOUND" } });
    expect(h.dispatched).toEqual([]);
  });

  it("answers daemon pings", async () => {
    const h = await connected();
    const socket = h.socket();
    const response = socket.nextSent();
    socket.receive({ jsonrpc: "2.0", id: "p1", method: "$session.ping", params: {} });

    expect(await response).toEqual({ jsonrpc: "2.0", id: "p1", result: {} });
  });
});
