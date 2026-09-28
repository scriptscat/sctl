import type { SessionStorage } from "./storage";

// 弹窗一失去焦点就会关闭，用户输入的配对码存进会话存储（chrome.storage.session），
// 重新打开弹窗时恢复（docs/specs 扩展弹窗）。
const KEY = "pairingCodeDraft";

export async function loadPairingDraft(storage: SessionStorage): Promise<string> {
  const items = await storage.get([KEY]);
  const value = items[KEY];
  return typeof value === "string" ? value : "";
}

export async function savePairingDraft(storage: SessionStorage, code: string): Promise<void> {
  if (!code) {
    await storage.remove(KEY);
    return;
  }
  await storage.set({ [KEY]: code });
}

export async function clearPairingDraft(storage: SessionStorage): Promise<void> {
  await storage.remove(KEY);
}
