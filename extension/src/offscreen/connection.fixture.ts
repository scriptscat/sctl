// 连接测试的替身：可手动推进的计时器、可由测试驱动的假 WebSocket，以及按 docs/protocol.md §2
// 独立实现 daemon 一侧握手的假 daemon（MAC 直接用 WebCrypto 计算，不复用被测实现）。
import { CRYPTO, SCHEMA_VERSION } from "@/protocol/generated/protocol.generated";
import type { ConnectionConfig, RpcContext, RpcReply } from "@/shared/messages";
import type { ConnectionState } from "@/shared/state";
import { Connection, type SocketEvents, type SocketLike, type Timers } from "./connection";
import { derivePairingKeys } from "./crypto";

export const INSTANCE_ID = "3f2a9c0e5b7d41e8a6f0c2d4e6f80a1b";
export const SESSION_KEY = "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f";
export const PAIRING_CODE = "7K3M9QPX";

export function config(overrides: Partial<ConnectionConfig> = {}): ConnectionConfig {
  return {
    instanceId: INSTANCE_ID,
    name: "chrome-3f2a",
    defaultName: "chrome-3f2a",
    address: "127.0.0.1:8643",
    key: SESSION_KEY,
    methods: ["tabs.list"],
    product: "Chrome",
    productVersion: "129.0.6668.58",
    extensionVersion: "0.1.0",
    ...overrides,
  };
}

export class ManualTimers implements Timers {
  private current = 1_000_000;
  private seq = 0;
  private readonly tasks = new Map<number, { at: number; fn: () => void }>();

  now(): number {
    return this.current;
  }

  setTimeout(fn: () => void, ms: number): number {
    const id = ++this.seq;
    this.tasks.set(id, { at: this.current + ms, fn });
    return id;
  }

  clearTimeout(id: number): void {
    this.tasks.delete(id);
  }

  pending(): number {
    return this.tasks.size;
  }

  advance(ms: number): void {
    const target = this.current + ms;
    for (;;) {
      const due = [...this.tasks.entries()].filter(([, t]) => t.at <= target).sort((a, b) => a[1].at - b[1].at)[0];
      if (!due) {
        break;
      }
      this.tasks.delete(due[0]);
      this.current = due[1].at;
      due[1].fn();
    }
    this.current = target;
  }
}

export interface WireMessage {
  jsonrpc: "2.0";
  id?: string;
  method?: string;
  params?: Record<string, unknown>;
  result?: Record<string, unknown>;
  error?: { code: number; message: string; data?: { code: string } };
}

export class FakeSocket implements SocketLike {
  readonly sent: WireMessage[] = [];
  closedWith: number | null = null;
  private waiters: Array<() => void> = [];

  constructor(
    readonly url: string,
    private readonly events: SocketEvents,
  ) {}

  send(data: string): void {
    this.sent.push(JSON.parse(data) as WireMessage);
    const waiters = this.waiters;
    this.waiters = [];
    waiters.forEach((wake) => wake());
  }

  close(code = 1005): void {
    this.closedWith ??= code;
  }

  open(): void {
    this.events.open();
  }

  receive(message: WireMessage): void {
    this.events.message(JSON.stringify(message));
  }

  // 模拟对端或网络关闭：浏览器在连接失败时以 1006 关闭，daemon 拒绝握手时以 1008 关闭。
  drop(code: number): void {
    this.closedWith ??= code;
    this.events.close(code);
  }

  async nextSent(): Promise<WireMessage> {
    const count = this.sent.length;
    while (this.sent.length === count) {
      await new Promise<void>((resolve, reject) => {
        const timer = setTimeout(() => reject(new Error(`timed out waiting for a frame on ${this.url}`)), 1000);
        this.waiters.push(() => {
          clearTimeout(timer);
          resolve();
        });
      });
    }
    return this.sent[count];
  }
}

const encoder = new TextEncoder();

function hex(bytes: ArrayBuffer | Uint8Array): string {
  return Array.from(new Uint8Array(bytes), (b) => b.toString(16).padStart(2, "0")).join("");
}

function fromHex(text: string): Uint8Array<ArrayBuffer> {
  return Uint8Array.from(text.match(/../g) ?? [], (pair) => parseInt(pair, 16));
}

async function hmac(key: Uint8Array<ArrayBuffer>, text: string): Promise<string> {
  const k = await crypto.subtle.importKey("raw", key, { name: "HMAC", hash: "SHA-256" }, false, ["sign"]);
  return hex(await crypto.subtle.sign("HMAC", k, encoder.encode(text)));
}

export const NONCE_D = "d".repeat(64);

export interface AuthOptions {
  // 会话模式用 hex 长期密钥，配对模式用 daemon 显示的配对码。
  auth: { mode: "session"; key: string } | { mode: "pairing"; code: string; deliverKey: string };
  corruptDaemonMac?: boolean;
}

// 扮演 daemon 完成认证：发出挑战、按 §2.1 校验扩展应答，然后发送 $session.authenticated。返回扩展的应答。
export async function authenticate(socket: FakeSocket, options: AuthOptions): Promise<WireMessage> {
  const reply = socket.nextSent();
  socket.receive({ jsonrpc: "2.0", id: "auth-1", method: "$session.authenticate", params: { nonceD: NONCE_D } });
  const response = await reply;
  const result = response.result as { mode: string; nonceE: string; hmac: string; peer: { instanceId: string } };
  const auth = options.auth;
  const pairing = auth.mode === "pairing";
  const keys = auth.mode === "pairing" ? await derivePairingKeys(auth.code) : null;
  const macKey = keys ? keys.mac : fromHex(auth.mode === "session" ? auth.key : "");
  const extContext = pairing ? CRYPTO.context.browserPairExt : CRYPTO.context.browserSessionExt;
  const expected = await hmac(macKey, extContext + result.peer.instanceId + NONCE_D + result.nonceE);
  if (expected !== result.hmac) {
    throw new Error("extension MAC does not verify");
  }
  const daemonContext = pairing ? CRYPTO.context.browserPairDaemon : CRYPTO.context.browserSessionDaemon;
  let mac = await hmac(macKey, daemonContext + result.peer.instanceId + result.nonceE + NONCE_D);
  if (options.corruptDaemonMac) {
    mac = mac.replace(/^./, (c) => (c === "0" ? "1" : "0"));
  }
  const params: Record<string, unknown> = { hmac: mac };
  if (keys && auth.mode === "pairing") {
    const iv = crypto.getRandomValues(new Uint8Array(12));
    const aes = await crypto.subtle.importKey("raw", keys.enc, { name: "AES-GCM" }, false, ["encrypt"]);
    const sealed = await crypto.subtle.encrypt({ name: "AES-GCM", iv }, aes, fromHex(auth.deliverKey));
    params.key = {
      ciphertext: btoa(String.fromCharCode(...new Uint8Array(sealed))),
      iv: btoa(String.fromCharCode(...iv)),
    };
  }
  socket.receive({ jsonrpc: "2.0", method: "$session.authenticated", params });
  return response;
}

// 发出 hello 并返回扩展随后的能力声明请求。
export async function hello(socket: FakeSocket): Promise<WireMessage> {
  const request = socket.nextSent();
  socket.receive({ jsonrpc: "2.0", method: "$session.hello", params: { daemonVersion: "0.1.0" } });
  const capabilities = await request;
  if (capabilities.method !== "$session.capabilities" || capabilities.params?.schemaVersion !== SCHEMA_VERSION) {
    throw new Error("expected a capabilities request");
  }
  return capabilities;
}

export async function acceptSession(socket: FakeSocket, key = SESSION_KEY): Promise<WireMessage> {
  socket.open();
  await authenticate(socket, { auth: { mode: "session", key } });
  const capabilities = await hello(socket);
  socket.receive({ jsonrpc: "2.0", id: capabilities.id, result: {} });
  return capabilities;
}

export function conflict(socket: FakeSocket, capabilities: WireMessage): void {
  socket.receive({
    jsonrpc: "2.0",
    id: capabilities.id,
    error: { code: -32000, message: "name taken", data: { code: "CONFLICT" } },
  });
  socket.drop(1000);
}

export function harness(start: Partial<ConnectionConfig> = {}) {
  const timers = new ManualTimers();
  const sockets: FakeSocket[] = [];
  const states: ConnectionState[] = [];
  const persisted: { keys: Array<{ key: string; name: string }>; names: string[] } = { keys: [], names: [] };
  const dispatched: Array<{ method: string; input: unknown }> = [];
  const contexts: RpcContext[] = [];
  const cancelled: string[] = [];
  const disconnects: string[] = [];
  let outcome: RpcReply | Promise<RpcReply> = {
    ok: true,
    result: { contentTrust: "untrusted-page-content", tabs: [] },
  };
  // 已连接的会话结束的次数（onDisconnected），调试器附加随之释放。
  const sessionEnds = { count: 0 };
  // 测试可以让持久化挂起，模拟 service worker 写存储期间用户又发出了别的命令。
  let persistence: Promise<void> = Promise.resolve();
  // 测试可以让持久化失败，模拟 service worker 写存储出错。
  let persistFailure: Error | null = null;
  // 测试可以让 WebSocket 构造抛错，模拟浏览器拒绝连接某个地址（例如被屏蔽的端口）。
  let socketRefusal: Error | null = null;
  const connection = new Connection({
    createSocket: (url, events) => {
      if (socketRefusal) {
        throw socketRefusal;
      }
      const socket = new FakeSocket(url, events);
      sockets.push(socket);
      return socket;
    },
    timers,
    onState: (state) => states.push(state),
    persistKey: (key, name) => {
      persisted.keys.push({ key, name });
      return persistence;
    },
    persistName: (name) => {
      persisted.names.push(name);
      return persistFailure ? Promise.reject(persistFailure) : persistence;
    },
    onDisconnected: () => {
      sessionEnds.count++;
    },
    dispatch: (method, input, context) => {
      dispatched.push({ method, input });
      contexts.push(context);
      return Promise.resolve(outcome);
    },
    cancel: (requestId) => {
      cancelled.push(requestId);
      return Promise.resolve();
    },
    disconnected: (connection) => {
      disconnects.push(connection);
      return Promise.resolve();
    },
  });
  connection.start(config(start));
  return {
    connection,
    timers,
    sockets,
    states,
    persisted,
    dispatched,
    contexts,
    cancelled,
    disconnects,
    sessionEnds,
    socket: (): FakeSocket => {
      const last = sockets.at(-1);
      if (!last) {
        throw new Error("no socket was opened");
      }
      return last;
    },
    state: (): ConnectionState => connection.getState(),
    // 传入挂起的 Promise 可以让 background 的应答迟到，模拟审批请求还在预校验时 daemon 就取消了它。
    setOutcome: (next: RpcReply | Promise<RpcReply>) => {
      outcome = next;
    },
    refuseSockets: (error: Error | null) => {
      socketRefusal = error;
    },
    failPersistence: (error: Error) => {
      persistFailure = error;
    },
    holdPersistence: (): (() => void) => {
      let release = () => {};
      persistence = new Promise((resolve) => {
        release = resolve;
      });
      return release;
    },
  };
}

// 放行挂起的 Promise 之后，后续步骤只由已决议的 Promise 推进（没有 I/O 和计时器），在下一个宏任务之前必然全部跑完；
// 等一个宏任务边界即可，不依赖任何时长。
export function drainMicrotasks(): Promise<void> {
  return new Promise((resolve) => setTimeout(resolve, 0));
}

// 等待异步握手（WebCrypto 走线程池）推进到条件成立。
export async function until(condition: () => boolean): Promise<void> {
  for (let i = 0; i < 200; i++) {
    if (condition()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 1));
  }
  throw new Error("condition not reached");
}
