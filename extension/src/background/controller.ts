import { DEFAULT_ADDRESS, parseAddress } from "@/shared/address";
import { type ProductInfo, defaultName, isValidName, newInstanceId } from "@/shared/identity";
import type {
  BackgroundMessage,
  ConnectionConfig,
  OffscreenCommand,
  PairResult,
  RenameResult,
  SetAddressResult,
} from "@/shared/messages";
import { normalizePairingCode } from "@/shared/pairing-code";
import type { NotificationMethod, NotificationParams } from "@/protocol/generated/protocol.generated";
import type { HandlerRegistry } from "./registry";

// chrome.storage.local 的最小子集：实例身份、名称、会话密钥与 daemon 地址都存在扩展本地存储里。
export interface StorageLike {
  get(keys: string[]): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
  remove(keys: string | string[]): Promise<void>;
}

export interface BackgroundDeps {
  storage: StorageLike;
  // 确保 offscreen 文档存在后把命令交给它，返回它的应答。
  offscreen(command: OffscreenCommand): Promise<unknown>;
  registry: HandlerRegistry;
  onConnectionClosed(): Promise<void>;
  // 浏览器的完整版本号只能异步取得（navigator.userAgentData.getHighEntropyValues）。
  browser: Promise<BrowserInfo>;
  extensionVersion: string;
}

export interface BrowserInfo {
  product: ProductInfo;
  version: string;
}

interface Identity {
  instanceId: string;
  name: string;
}

function stored(items: Record<string, unknown>, key: string): string | undefined {
  const value = items[key];
  return typeof value === "string" ? value : undefined;
}

// service worker 一侧：唯一能读写扩展存储的地方，负责弹窗请求的边界校验、持久化，并把业务请求交给处理注册表。
export class Background {
  private identity: Promise<Identity> | null = null;

  constructor(private readonly deps: BackgroundDeps) {}

  handle(message: BackgroundMessage): Promise<unknown> {
    switch (message.type) {
      case "offscreenReady":
        return this.config();
      case "paired":
        return this.deps.storage.set({ key: message.key, name: message.name });
      case "renamed":
        return this.deps.storage.set({ name: message.name });
      case "rpc":
        return this.deps.registry.dispatch(message.method, message.input);
      case "connectionClosed":
        return this.deps.onConnectionClosed();
      case "getState":
        return this.deps.offscreen({ target: "offscreen", type: "getState" });
      case "pair":
        return this.pair(message.code);
      case "rename":
        return this.rename(message.name);
      case "retryNow":
        return this.deps.offscreen({ target: "offscreen", type: "retryNow" });
      case "forget":
        return this.forget();
      case "setAddress":
        return this.setAddress(message.address);
    }
  }

  // 扩展主动发给 daemon 的通知走 offscreen 里的 WebSocket；未连接时由 offscreen 丢弃。
  async notify<N extends NotificationMethod>(method: N, params: NotificationParams<N>): Promise<void> {
    await this.deps.offscreen({ target: "offscreen", type: "notify", method, params } as OffscreenCommand);
  }

  // 实例 ID 首次使用时生成并持久化；同一 service worker 生命周期内的并发调用共享同一次生成。
  private loadIdentity(): Promise<Identity> {
    this.identity ??= (async () => {
      const items = await this.deps.storage.get(["instanceId", "name"]);
      const instanceId = stored(items, "instanceId") ?? newInstanceId();
      const name = stored(items, "name") ?? defaultName((await this.deps.browser).product, instanceId);
      await this.deps.storage.set({ instanceId, name });
      return { instanceId, name };
    })();
    return this.identity;
  }

  private async config(): Promise<ConnectionConfig> {
    const { instanceId } = await this.loadIdentity();
    const items = await this.deps.storage.get(["name", "key", "address"]);
    const browser = await this.deps.browser;
    const name = stored(items, "name");
    if (!name) {
      throw new Error("instance name missing after identity was created");
    }
    return {
      instanceId,
      name,
      defaultName: defaultName(browser.product, instanceId),
      address: stored(items, "address") ?? DEFAULT_ADDRESS,
      key: stored(items, "key") ?? null,
      methods: this.deps.registry.methods(),
      product: browser.product.product,
      productVersion: browser.version,
      extensionVersion: this.deps.extensionVersion,
    };
  }

  private async pair(input: string): Promise<PairResult> {
    const code = normalizePairingCode(input);
    if (!code) {
      return { ok: false, error: "invalid-code" };
    }
    await this.deps.offscreen({ target: "offscreen", type: "pair", code });
    return { ok: true };
  }

  private async rename(name: string): Promise<RenameResult> {
    if (!isValidName(name)) {
      return { ok: false, error: "invalid-name" };
    }
    return (await this.deps.offscreen({ target: "offscreen", type: "rename", name })) as RenameResult;
  }

  // 只删除扩展这边的密钥；daemon 那边的登记要由 sctl browsers forget 删除。
  private async forget(): Promise<void> {
    await this.deps.storage.remove("key");
    await this.deps.offscreen({ target: "offscreen", type: "forget" });
  }

  private async setAddress(input: string): Promise<SetAddressResult> {
    const address = parseAddress(input);
    if (!address) {
      return { ok: false, error: "invalid-address" };
    }
    await this.deps.storage.set({ address });
    await this.deps.offscreen({ target: "offscreen", type: "setAddress", address });
    return { ok: true };
  }
}
