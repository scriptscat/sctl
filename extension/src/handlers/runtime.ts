import type { HandlerRegistry } from "@/background/registry";

// 回复要经 offscreen 文档的 WebSocket 发出；立即重新加载会连同 offscreen 文档一起销毁，回复就丢了。
const RELOAD_DELAY_MS = 500;

// extension.reload：daemon 更新了 sctl 管理的扩展目录之后调用。已解压的扩展重新加载时从磁盘读取全部文件，
// 存储跟着扩展 ID 走，实例 ID 和会话密钥都保留。
export function registerRuntimeHandlers(
  registry: HandlerRegistry,
  reload: () => void = () => chrome.runtime.reload(),
): void {
  registry.register("extension.reload", () => {
    setTimeout(reload, RELOAD_DELAY_MS);
    return Promise.resolve({});
  });
}
