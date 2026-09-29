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

// 被 daemon forget 之后用户点了"重新配对"：弹窗关闭重开时靠它回到配对表单，而不是又落回被拒页。
// 与草稿分开存，因为草稿也可能是普通未配对路径留下的。
const RE_PAIR_KEY = "rePairFlow";

export async function loadRePairFlow(storage: SessionStorage): Promise<boolean> {
  const items = await storage.get([RE_PAIR_KEY]);
  return items[RE_PAIR_KEY] === true;
}

export async function saveRePairFlow(storage: SessionStorage, active: boolean): Promise<void> {
  if (active) {
    await storage.set({ [RE_PAIR_KEY]: true });
    return;
  }
  // 标记大多数时候本就不存在，先读再删，避免每次打开弹窗都无谓写会话存储。
  if (await loadRePairFlow(storage)) await storage.remove(RE_PAIR_KEY);
}
