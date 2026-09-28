import { describe, expect, it } from "vitest";
import { clearPairingDraft, loadPairingDraft, savePairingDraft } from "./pairing-draft";
import type { SessionStorage } from "./storage";

function fakeSession(initial: Record<string, unknown> = {}): SessionStorage & { data: Record<string, unknown> } {
  const data = { ...initial };
  return {
    data,
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]))),
    set: (items) => {
      Object.assign(data, items);
      return Promise.resolve();
    },
    remove: (keys) => {
      for (const key of Array.isArray(keys) ? keys : [keys]) {
        delete data[key];
      }
      return Promise.resolve();
    },
  };
}

describe("pairing code draft", () => {
  it("restores an empty draft when nothing was saved", async () => {
    await expect(loadPairingDraft(fakeSession())).resolves.toBe("");
  });

  it("restores a previously saved draft", async () => {
    const storage = fakeSession({ pairingCodeDraft: "K7QM-3XRD" });
    await expect(loadPairingDraft(storage)).resolves.toBe("K7QM-3XRD");
  });

  it("round-trips through save and load", async () => {
    const storage = fakeSession();
    await savePairingDraft(storage, "AB12-CD34");
    await expect(loadPairingDraft(storage)).resolves.toBe("AB12-CD34");
  });

  it("clears the draft once the code is emptied", async () => {
    const storage = fakeSession({ pairingCodeDraft: "AB12-CD34" });
    await savePairingDraft(storage, "");
    await expect(loadPairingDraft(storage)).resolves.toBe("");
  });

  it("clears the draft explicitly after a successful pairing", async () => {
    const storage = fakeSession({ pairingCodeDraft: "AB12-CD34" });
    await clearPairingDraft(storage);
    await expect(loadPairingDraft(storage)).resolves.toBe("");
  });
});
