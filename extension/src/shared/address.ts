import { TRANSPORT } from "@/protocol/generated/protocol.generated";

// daemon 默认监听地址取自协议常量，与 sctl serve 的默认 --listen-address 一致。
export const DEFAULT_ADDRESS = new URL(TRANSPORT.defaultUrl).host;

// 地址必须是 host:port（IPv6 带方括号），与 sctl serve --listen-address 的写法相同；不接受协议头、路径或缺省端口。
const ADDRESS_PATTERN = /^(\[[0-9a-fA-F:.]+\]|[A-Za-z0-9.-]+):(\d{1,5})$/;
const MAX_PORT = 65535;

export function parseAddress(input: string): string | null {
  const trimmed = input.trim();
  const match = ADDRESS_PATTERN.exec(trimmed);
  if (!match) {
    return null;
  }
  const port = Number(match[2]);
  if (port < 1 || port > MAX_PORT) {
    return null;
  }
  // 正则放过的主机仍可能不是合法主机（如 999.1.1.1、[:::]），WebSocket 构造时才抛错；在保存前按同一 URL 规则挡掉。
  try {
    new URL(websocketUrl(trimmed));
  } catch {
    return null;
  }
  return trimmed;
}

export function websocketUrl(address: string): string {
  return `ws://${address}/`;
}
