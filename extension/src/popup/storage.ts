// chrome.storage 的最小子集，弹窗只需要 get/set（会话存储还需要 remove）。两者的形状与
// src/background/controller.ts 的 StorageLike 一致，chrome.storage.local/session 原生满足它们。
export interface KeyValueStorage {
  get(keys: string[]): Promise<Record<string, unknown>>;
  set(items: Record<string, unknown>): Promise<void>;
}

export interface SessionStorage extends KeyValueStorage {
  remove(keys: string | string[]): Promise<void>;
}

// 用方法转发而不是直接导出 chrome.storage.local/session：后者在模块求值时就会读取全局 chrome，
// 会在任何测试有机会用 vi.stubGlobal 注入假对象之前抛错；转发到调用时才访问，模块本身可以安全导入。
export const chromeLocalStorage: KeyValueStorage = {
  get: (keys) => chrome.storage.local.get(keys),
  set: (items) => chrome.storage.local.set(items),
};

export const chromeSessionStorage: SessionStorage = {
  get: (keys) => chrome.storage.session.get(keys),
  set: (items) => chrome.storage.session.set(items),
  remove: (keys) => chrome.storage.session.remove(keys),
};
