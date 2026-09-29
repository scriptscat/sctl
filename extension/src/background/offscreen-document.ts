// 相对扩展根目录的路径，与构建产物 dist/offscreen/index.html 对应。
export const OFFSCREEN_URL = "offscreen/index.html";

export interface OffscreenApi {
  getContexts(filter: { contextTypes: ["OFFSCREEN_DOCUMENT"] }): Promise<unknown[]>;
  createDocument(parameters: {
    url: string;
    reasons: `${chrome.offscreen.Reason}`[];
    justification: string;
  }): Promise<void>;
}

// 返回一个幂等的 ensure：offscreen 文档不存在时创建它，并发调用只创建一次（Chrome 同时只允许一个）。
export function createOffscreenKeeper(api: OffscreenApi): () => Promise<void> {
  let creating: Promise<void> | null = null;
  return async () => {
    if (creating) {
      return creating;
    }
    creating = (async () => {
      const existing = await api.getContexts({ contextTypes: ["OFFSCREEN_DOCUMENT"] });
      if (existing.length === 0) {
        await api.createDocument({
          url: OFFSCREEN_URL,
          // 没有专门针对 WebSocket 的 reason；选一个不会被 Chrome 按时长回收的，justification 如实说明用途。
          reasons: ["WORKERS"],
          justification: "Keep the WebSocket connection to the local sctl daemon open while the service worker sleeps",
        });
      }
    })();
    try {
      await creating;
    } finally {
      creating = null;
    }
  };
}
