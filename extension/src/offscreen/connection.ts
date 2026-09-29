import { CRYPTO, LIMITS, SCHEMA_VERSION, type RpcMethod } from "@/protocol/generated/protocol.generated";
import { RPC_PARAM_VALIDATORS } from "@/protocol/generated/validators.generated";
import { websocketUrl } from "@/shared/address";
import type { ConnectionConfig, RenameResult, RpcOutcome } from "@/shared/messages";
import type { ConnectionState, StatusDetail } from "@/shared/state";
import {
  type Bytes,
  type MacMaterial,
  constantTimeEqualHex,
  daemonMac,
  decryptSessionKey,
  derivePairingKeys,
  extensionMac,
  hexToBytes,
  randomNonceHex,
} from "./crypto";
import { type JsonRpcMessage, decodeFrame, isRecord } from "./wire";

// 退避从 1 秒开始逐次翻倍，最长 30 秒。
const BASE_RETRY_MS = 1000;
const MAX_RETRY_MS = 30_000;
// daemon 以 1008 关闭即握手被拒，原因从不回显（docs/protocol.md §2.1）。
const CLOSE_POLICY_VIOLATION = 1008;
const CLOSE_NORMAL = 1000;
// 浏览器在连接没能建立时报告的关闭码。
const CLOSE_ABNORMAL = 1006;
// 应用层失败统一用 -32000，领域错误码放在 error.data.code（docs/protocol.md §4）。
const RPC_APPLICATION_ERROR = -32000;
const NONCE_PATTERN = new RegExp(`^[0-9a-f]{${CRYPTO.nonceBytes * 2}}$`);
const utf8 = new TextEncoder();

export interface SocketEvents {
  open(): void;
  message(data: string): void;
  close(code: number): void;
}

export interface SocketLike {
  send(data: string): void;
  close(code?: number): void;
}

export interface Timers {
  setTimeout(fn: () => void, ms: number): number;
  clearTimeout(id: number): void;
  now(): number;
}

export interface ConnectionDeps {
  createSocket(url: string, events: SocketEvents): SocketLike;
  timers: Timers;
  onState(state: ConnectionState): void;
  // 持久化只能由 service worker 完成；配对与改名都在 daemon 接受能力声明、登记生效之后才落盘。
  persistKey(key: string, name: string): Promise<void>;
  persistName(name: string): Promise<void>;
  dispatch(method: RpcMethod, input: unknown): Promise<RpcOutcome>;
}

type Auth = { mode: "session"; key: string } | { mode: "pairing"; code: string };

// challenge → proof → hello → capabilities → connected，与 docs/protocol.md §2 的会话顺序一一对应。
type Phase = "challenge" | "proof" | "hello" | "capabilities" | "connected";

// 一次连接尝试。它被替换或关闭后，其套接字事件与未完成的异步步骤全部作废。
interface Attempt {
  socket: SocketLike;
  auth: Auth;
  name: string;
  phase: Phase;
  opened: boolean;
  verify: { material: MacMaterial; nonceD: string; nonceE: string; enc: Bytes | null } | null;
  capabilitiesId: string;
  daemonVersion: string;
  pairedKey: string | null;
  // 扩展主动关闭时记下的结论，close 事件据此决定下一状态；为 null 时按关闭码判断。
  verdict: "rejected" | "conflict" | null;
  authTimer: number | null;
  // WebCrypto 是异步的：帧按到达顺序串行处理，close 排在已到达的帧之后。
  queue: Promise<void>;
  inFlight: number;
}

interface PendingRename {
  from: string;
  resolve(result: RenameResult): void;
}

// Connection 持有到 daemon 的唯一 WebSocket，驱动带实例身份的配对/会话握手、重连退避与业务请求应答。
export class Connection {
  private config: ConnectionConfig | null = null;
  private state: ConnectionState | null = null;
  private attempt: Attempt | null = null;
  private failures = 0;
  private retryTimer: number | null = null;
  private renaming: PendingRename | null = null;

  constructor(private readonly deps: ConnectionDeps) {}

  start(config: ConnectionConfig): void {
    this.config = { ...config };
    if (config.key) {
      this.connectSession();
    } else {
      this.setState({ status: "unpaired" });
    }
  }

  getState(): ConnectionState {
    if (!this.state) {
      throw new Error("connection has not been started");
    }
    return this.state;
  }

  pair(code: string): void {
    this.reset();
    const config = this.cfg();
    config.name = config.defaultName;
    this.open({ mode: "pairing", code });
    this.setState({ status: "pairing" });
  }

  rename(name: string): Promise<RenameResult> {
    const config = this.cfg();
    if (this.state?.status !== "connected") {
      return Promise.resolve({ ok: false, error: "not-connected" });
    }
    if (name === config.name) {
      return Promise.resolve({ ok: true });
    }
    return new Promise((resolve) => {
      const from = config.name;
      this.reset();
      this.renaming = { from, resolve };
      config.name = name;
      this.connectSession();
    });
  }

  retryNow(): void {
    if (this.retryTimer === null) {
      return;
    }
    this.clearRetry();
    this.failures = 0;
    this.connectSession();
  }

  forget(): void {
    this.reset();
    this.cfg().key = null;
    this.setState({ status: "unpaired" });
  }

  setAddress(address: string): void {
    const config = this.cfg();
    this.reset();
    config.address = address;
    if (config.key) {
      this.connectSession();
    } else {
      this.setState({ status: "unpaired" });
    }
  }

  private cfg(): ConnectionConfig {
    if (!this.config) {
      throw new Error("connection has not been started");
    }
    return this.config;
  }

  private setState(detail: StatusDetail): void {
    const config = this.cfg();
    this.state = { ...detail, instanceId: config.instanceId, name: config.name, address: config.address };
    this.deps.onState(this.state);
  }

  // 放弃当前尝试与计划中的重试，退避从头开始；进行中的改名视为失败并恢复原名称。
  private reset(): void {
    this.clearRetry();
    this.failures = 0;
    this.detach();
    this.abandonRename("not-connected");
  }

  private detach(): void {
    const attempt = this.attempt;
    if (!attempt) {
      return;
    }
    this.attempt = null;
    this.clearAuthTimer(attempt);
    attempt.socket.close(CLOSE_NORMAL);
  }

  private abandonRename(error: "not-connected" | "name-taken"): void {
    const renaming = this.renaming;
    if (!renaming) {
      return;
    }
    this.renaming = null;
    this.cfg().name = renaming.from;
    renaming.resolve({ ok: false, error });
  }

  private clearRetry(): void {
    if (this.retryTimer !== null) {
      this.deps.timers.clearTimeout(this.retryTimer);
      this.retryTimer = null;
    }
  }

  private clearAuthTimer(attempt: Attempt): void {
    if (attempt.authTimer !== null) {
      this.deps.timers.clearTimeout(attempt.authTimer);
      attempt.authTimer = null;
    }
  }

  private connectSession(): void {
    const key = this.cfg().key;
    if (!key) {
      throw new Error("session connect requires a paired key");
    }
    this.open({ mode: "session", key });
    this.setState({ status: "reconnecting", attempt: this.failures, retryAt: null });
  }

  private open(auth: Auth): void {
    const config = this.cfg();
    // 套接字事件总在创建之后异步到达，此时 attempt 已经赋值。
    const events: SocketEvents = {
      open: () => this.onOpen(attempt),
      message: (data) => this.onMessage(attempt, data),
      close: (code) => this.onClose(attempt, code),
    };
    const attempt: Attempt = {
      socket: this.createSocket(websocketUrl(config.address), events),
      auth,
      name: config.name,
      phase: "challenge",
      opened: false,
      verify: null,
      capabilitiesId: "",
      daemonVersion: "",
      pairedKey: null,
      verdict: null,
      authTimer: null,
      queue: Promise.resolve(),
      inFlight: 0,
    };
    this.attempt = attempt;
    // 连不上也不报错的地址（丢包、只接 TCP 不应答升级）要等浏览器自己的连接超时，远长于配对码的有效期，
    // 期间配对表单一直停在"配对中"无法操作；建立连接因此同样限定在 authTimeoutMs 内。
    this.armTimeout(attempt);
  }

  // 浏览器会同步拒绝某些地址（例如被屏蔽的端口会抛 SecurityError）。把它当作一次没能打开的连接：
  // 关闭事件异步送达，与真实套接字一致，调用方在 open() 之后设置的状态不会盖过它得出的结论。
  private createSocket(url: string, events: SocketEvents): SocketLike {
    try {
      return this.deps.createSocket(url, events);
    } catch (error) {
      console.warn("the browser refused to open a WebSocket to the daemon", error);
      this.deps.timers.setTimeout(() => events.close(CLOSE_ABNORMAL), 0);
      return { send: () => undefined, close: () => undefined };
    }
  }

  // 超时后放弃这次尝试；连接打开时重新计时，认证本身仍有完整的 authTimeoutMs。
  private armTimeout(attempt: Attempt): void {
    this.clearAuthTimer(attempt);
    attempt.authTimer = this.deps.timers.setTimeout(() => {
      attempt.authTimer = null;
      if (attempt === this.attempt) {
        attempt.socket.close(CLOSE_NORMAL);
      }
    }, LIMITS.authTimeoutMs);
  }

  private onOpen(attempt: Attempt): void {
    if (attempt !== this.attempt) {
      return;
    }
    attempt.opened = true;
    this.armTimeout(attempt);
  }

  private onMessage(attempt: Attempt, data: string): void {
    if (attempt !== this.attempt) {
      return;
    }
    let message: JsonRpcMessage;
    try {
      message = decodeFrame(data);
    } catch (error) {
      console.warn("closing connection after a malformed frame", error);
      attempt.socket.close(CLOSE_NORMAL);
      return;
    }
    attempt.inFlight++;
    attempt.queue = attempt.queue
      .then(() => (attempt === this.attempt ? this.handle(attempt, message) : undefined))
      .catch((error: unknown) => {
        console.error("closing connection after a failed handshake step", error);
        if (attempt === this.attempt) {
          attempt.socket.close(CLOSE_NORMAL);
        }
      })
      .finally(() => {
        attempt.inFlight--;
      });
  }

  private onClose(attempt: Attempt, code: number): void {
    if (attempt !== this.attempt) {
      return;
    }
    if (attempt.inFlight > 0) {
      attempt.queue = attempt.queue.then(() => this.onClose(attempt, code));
      return;
    }
    this.attempt = null;
    this.clearAuthTimer(attempt);
    if (attempt.auth.mode === "pairing" && attempt.phase !== "connected") {
      this.pairingClosed(attempt);
      return;
    }
    if (attempt.verdict === "conflict" && this.renaming) {
      // 新名称被另一个实例占用：恢复原名称并立即用它重连。
      this.abandonRename("name-taken");
      this.connectSession();
      return;
    }
    const refused = attempt.opened && attempt.phase !== "connected" && code === CLOSE_POLICY_VIOLATION;
    if (attempt.verdict !== null || refused) {
      this.abandonRename("not-connected");
      this.setState({ status: "rejected" });
      return;
    }
    this.abandonRename("not-connected");
    this.scheduleRetry();
  }

  private pairingClosed(attempt: Attempt): void {
    if (attempt.verdict === "conflict") {
      this.setState({ status: "pair-failed", reason: "name-taken" });
    } else if (!attempt.opened) {
      this.setState({ status: "pair-unreachable" });
    } else {
      this.setState({ status: "pair-failed", reason: "code-rejected" });
    }
  }

  private scheduleRetry(): void {
    this.failures++;
    const delay = Math.min(BASE_RETRY_MS * 2 ** (this.failures - 1), MAX_RETRY_MS);
    this.retryTimer = this.deps.timers.setTimeout(() => {
      this.retryTimer = null;
      this.connectSession();
    }, delay);
    this.setState({ status: "reconnecting", attempt: this.failures, retryAt: this.deps.timers.now() + delay });
  }

  private send(attempt: Attempt, message: Omit<JsonRpcMessage, "jsonrpc">): void {
    this.sendFrame(attempt, JSON.stringify({ jsonrpc: "2.0", ...message }));
  }

  private sendFrame(attempt: Attempt, frame: string): void {
    if (attempt === this.attempt) {
      attempt.socket.send(frame);
    }
  }

  // 握手期间出现不符合会话顺序的消息即视为协议错误，关闭连接。
  private violation(attempt: Attempt, message: JsonRpcMessage): void {
    console.warn("closing connection after an unexpected message", attempt.phase, message.method ?? message.id);
    attempt.socket.close(CLOSE_NORMAL);
  }

  private async handle(attempt: Attempt, message: JsonRpcMessage): Promise<void> {
    switch (attempt.phase) {
      case "challenge":
        return this.answerChallenge(attempt, message);
      case "proof":
        return this.verifyProof(attempt, message);
      case "hello":
        return this.declareCapabilities(attempt, message);
      case "capabilities":
        return this.finishHandshake(attempt, message);
      case "connected":
        return this.serve(attempt, message);
    }
  }

  private async answerChallenge(attempt: Attempt, message: JsonRpcMessage): Promise<void> {
    const nonceD = message.params?.nonceD;
    if (message.method !== "$session.authenticate" || !message.id || typeof nonceD !== "string") {
      return this.violation(attempt, message);
    }
    if (!NONCE_PATTERN.test(nonceD)) {
      return this.violation(attempt, message);
    }
    const instanceId = this.cfg().instanceId;
    let material: MacMaterial;
    let enc: Bytes | null = null;
    if (attempt.auth.mode === "session") {
      material = { mode: "session", instanceId, key: hexToBytes(attempt.auth.key) };
    } else {
      const keys = await derivePairingKeys(attempt.auth.code);
      material = { mode: "pairing", instanceId, key: keys.mac };
      enc = keys.enc;
    }
    const nonceE = randomNonceHex();
    const hmac = await extensionMac(material, nonceD, nonceE);
    attempt.verify = { material, nonceD, nonceE, enc };
    attempt.phase = "proof";
    this.send(attempt, {
      id: message.id,
      result: { mode: attempt.auth.mode, nonceE, hmac, peer: { kind: "browser", instanceId } },
    });
  }

  private async verifyProof(attempt: Attempt, message: JsonRpcMessage): Promise<void> {
    const verify = attempt.verify;
    const hmac = message.params?.hmac;
    if (message.method !== "$session.authenticated" || message.id || !verify || typeof hmac !== "string") {
      return this.violation(attempt, message);
    }
    const expected = await daemonMac(verify.material, verify.nonceD, verify.nonceE);
    if (!constantTimeEqualHex(expected, hmac)) {
      // 对端不持有本实例的密钥，重试也不会成功。
      attempt.verdict = "rejected";
      attempt.socket.close(CLOSE_NORMAL);
      return;
    }
    if (verify.enc) {
      const delivery = message.params?.key;
      if (!isRecord(delivery) || typeof delivery.ciphertext !== "string" || typeof delivery.iv !== "string") {
        return this.violation(attempt, message);
      }
      attempt.pairedKey = await decryptSessionKey(verify.enc, { ciphertext: delivery.ciphertext, iv: delivery.iv });
    }
    this.clearAuthTimer(attempt);
    attempt.phase = "hello";
  }

  private declareCapabilities(attempt: Attempt, message: JsonRpcMessage): void {
    const daemonVersion = message.params?.daemonVersion;
    if (message.method !== "$session.hello" || typeof daemonVersion !== "string") {
      return this.violation(attempt, message);
    }
    const config = this.cfg();
    attempt.daemonVersion = daemonVersion;
    attempt.capabilitiesId = crypto.randomUUID();
    attempt.phase = "capabilities";
    this.send(attempt, {
      id: attempt.capabilitiesId,
      method: "$session.capabilities",
      params: {
        schemaVersion: SCHEMA_VERSION,
        methods: config.methods,
        peer: {
          name: attempt.name,
          product: config.product,
          productVersion: config.productVersion,
          extensionVersion: config.extensionVersion,
        },
      },
    });
  }

  private async finishHandshake(attempt: Attempt, message: JsonRpcMessage): Promise<void> {
    if (message.method || message.id !== attempt.capabilitiesId) {
      return this.violation(attempt, message);
    }
    if (message.error) {
      // 名称冲突之后 daemon 会关闭连接；记下结论，由 close 事件决定回退还是报告。
      if (message.error.data?.code === "CONFLICT") {
        attempt.verdict = "conflict";
      }
      attempt.socket.close(CLOSE_NORMAL);
      return;
    }
    // daemon 已经登记了这个名称：改名在此刻生效，之后的命令不能再把它当作未完成的改名回退。
    const renaming = this.renaming;
    this.renaming = null;
    if (attempt.pairedKey) {
      await this.deps.persistKey(attempt.pairedKey, attempt.name);
      // 保存期间用户可能已经忘记或重新配对，此时不能把这把密钥装回来。
      if (attempt !== this.attempt) {
        return;
      }
      this.cfg().key = attempt.pairedKey;
    }
    if (renaming) {
      try {
        await this.deps.persistName(attempt.name);
      } catch (error) {
        // 存储里仍是原名称：按改名失败处理，并让随后的重连用原名称把 daemon 的登记改回去，两边保持一致。
        if (attempt === this.attempt) {
          this.cfg().name = renaming.from;
        }
        renaming.resolve({ ok: false, error: "not-connected" });
        throw error;
      }
      renaming.resolve({ ok: true });
    }
    if (attempt !== this.attempt) {
      return;
    }
    attempt.phase = "connected";
    this.failures = 0;
    const config = this.cfg();
    this.setState({
      status: "connected",
      daemonVersion: attempt.daemonVersion,
      product: config.product,
      productVersion: config.productVersion,
      connectedAt: this.deps.timers.now(),
    });
  }

  private serve(attempt: Attempt, message: JsonRpcMessage): void {
    const { id, method } = message;
    if (!method || method === "$session.shutdown" || method === "$/cancelRequest") {
      // 响应不会出现（连接建立后扩展不发请求）；shutdown 之后 daemon 会关闭连接；
      // 浏览器方法都是即时完成的，没有可作废的挂起操作。
      return;
    }
    if (!id) {
      return;
    }
    if (method === "$session.ping") {
      this.send(attempt, { id, result: {} });
      return;
    }
    const declared = this.cfg().methods.find((m) => m === method);
    if (!declared) {
      this.fail(attempt, id, "METHOD_NOT_FOUND", `method ${method} is not supported`);
      return;
    }
    const input = message.params?.input;
    if (!RPC_PARAM_VALIDATORS[declared](input)) {
      this.fail(attempt, id, "INVALID_REQUEST", `invalid input for ${method}`);
      return;
    }
    // 业务请求不进入帧队列：一个慢调用不能挡住心跳应答。
    void this.deps.dispatch(declared, input).then(
      (outcome) => {
        if (outcome.ok) {
          this.reply(attempt, id, method, outcome.result as Record<string, unknown>);
        } else {
          this.fail(attempt, id, outcome.code, outcome.message);
        }
      },
      (error: unknown) => {
        console.error(`handler for ${method} failed`, error);
        this.fail(attempt, id, "INTERNAL_ERROR", "internal error");
      },
    );
  }

  // daemon 按 LIMITS.maxFrameBytes 限制读取，超限的帧会让它断开整条连接、作废全部在途请求；
  // 所以超大结果只能在发送前换成 PAYLOAD_TOO_LARGE。上限按 UTF-8 字节计，不是字符数。
  private reply(attempt: Attempt, id: string, method: string, result: Record<string, unknown>): void {
    const frame = JSON.stringify({ jsonrpc: "2.0", id, result });
    if (utf8.encode(frame).length > LIMITS.maxFrameBytes) {
      this.fail(
        attempt,
        id,
        "PAYLOAD_TOO_LARGE",
        `result of ${method} exceeds the ${LIMITS.maxFrameBytes}-byte frame limit`,
      );
      return;
    }
    this.sendFrame(attempt, frame);
  }

  private fail(attempt: Attempt, id: string, code: string, message: string): void {
    this.send(attempt, { id, error: { code: RPC_APPLICATION_ERROR, message, data: { code } } });
  }
}
