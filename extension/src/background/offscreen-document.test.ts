import { describe, expect, it } from "vitest";
import { OFFSCREEN_URL, createOffscreenKeeper, type OffscreenApi } from "./offscreen-document";

function fakeApi(existing: number) {
  const created: string[] = [];
  let contexts = existing;
  const api: OffscreenApi = {
    getContexts: () => Promise.resolve(Array.from({ length: contexts })),
    createDocument: ({ url }) => {
      created.push(url);
      return new Promise((resolve) =>
        setTimeout(() => {
          contexts++;
          resolve();
        }, 1),
      );
    },
  };
  return { api, created };
}

describe("offscreen document", () => {
  it("creates the document holding the connection when none exists", async () => {
    const { api, created } = fakeApi(0);
    await createOffscreenKeeper(api)();

    expect(created).toEqual([OFFSCREEN_URL]);
  });

  it("reuses an existing document", async () => {
    const { api, created } = fakeApi(1);
    await createOffscreenKeeper(api)();

    expect(created).toEqual([]);
  });

  it("creates only one document when several callers race", async () => {
    const { api, created } = fakeApi(0);
    const ensure = createOffscreenKeeper(api);
    await Promise.all([ensure(), ensure(), ensure()]);

    expect(created).toEqual([OFFSCREEN_URL]);
  });
});
