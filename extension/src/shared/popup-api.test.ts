import { describe, expect, it } from "vitest";
import type { ApprovalView } from "./approvals";
import { listen, type RuntimeLike } from "./messaging";
import { createPopupApi } from "./popup-api";
import type { ConnectionState } from "./state";

type Listener = Parameters<RuntimeLike["onMessage"]["addListener"]>[0];

// 模拟 chrome.runtime：消息送达除发送者之外的所有监听者，第一个 sendResponse 的回复即结果。
class FakeRuntime {
  private readonly listeners = new Set<{ owner: string; fn: Listener }>();

  as(owner: string): RuntimeLike {
    return {
      sendMessage: (message: unknown) =>
        new Promise((resolve) => {
          let answered = false;
          for (const { owner: other, fn } of this.listeners) {
            if (other === owner) {
              continue;
            }
            fn(message, {}, (reply: unknown) => {
              if (!answered) {
                answered = true;
                resolve(reply);
              }
            });
          }
          setTimeout(() => {
            if (!answered) {
              resolve(undefined);
            }
          }, 5);
        }),
      onMessage: {
        addListener: (fn: Listener) => this.listeners.add({ owner, fn }),
        removeListener: (fn: Listener) => {
          for (const entry of this.listeners) {
            if (entry.fn === fn) {
              this.listeners.delete(entry);
            }
          }
        },
      },
    };
  }

  count(): number {
    return this.listeners.size;
  }
}

const STATE: ConnectionState = {
  status: "unpaired",
  instanceId: "3f2a9c0e5b7d41e8a6f0c2d4e6f80a1b",
  name: "chrome-3f2a",
  address: "127.0.0.1:8643",
};

describe("popup message API", () => {
  it("asks the background for the state and for commands, ignoring other contexts' traffic", async () => {
    const runtime = new FakeRuntime();
    const received: unknown[] = [];
    listen(runtime.as("background"), "background", (message) => {
      received.push(message);
      return Promise.resolve(message.type === "getState" ? STATE : { ok: true });
    });
    listen(runtime.as("offscreen"), "offscreen", () => Promise.reject(new Error("must not be asked")));
    const api = createPopupApi(runtime.as("popup"));

    await expect(api.getState()).resolves.toEqual(STATE);
    await expect(api.pair("7K3M-9QPX")).resolves.toEqual({ ok: true });
    await expect(api.rename("work")).resolves.toEqual({ ok: true });
    await expect(api.setAddress("127.0.0.1:9000")).resolves.toEqual({ ok: true });
    await api.retryNow();
    await api.forget();
    expect(received).toEqual([
      { target: "background", type: "getState" },
      { target: "background", type: "pair", code: "7K3M-9QPX" },
      { target: "background", type: "rename", name: "work" },
      { target: "background", type: "setAddress", address: "127.0.0.1:9000" },
      { target: "background", type: "retryNow" },
      { target: "background", type: "forget" },
    ]);
  });

  it("rejects when the background fails the request", async () => {
    const runtime = new FakeRuntime();
    listen(runtime.as("background"), "background", () => Promise.reject(new Error("storage unavailable")));

    await expect(createPopupApi(runtime.as("popup")).getState()).rejects.toThrow("storage unavailable");
  });

  it("delivers state broadcasts to subscribers until they unsubscribe", async () => {
    const runtime = new FakeRuntime();
    const api = createPopupApi(runtime.as("popup"));
    const seen: ConnectionState[] = [];
    const unsubscribe = api.subscribe((state) => seen.push(state));
    const offscreen = runtime.as("offscreen");

    await offscreen.sendMessage({ target: "popup", type: "state", state: STATE });
    await offscreen.sendMessage({ target: "background", type: "renamed", name: "x" });
    unsubscribe();
    await offscreen.sendMessage({ target: "popup", type: "state", state: STATE });

    expect(seen).toEqual([STATE]);
    expect(runtime.count()).toBe(0);
  });

  it("asks the background for the approval queue and to bring the approval window forward", async () => {
    const runtime = new FakeRuntime();
    const received: unknown[] = [];
    const view: ApprovalView = { browserName: "chrome-3f2a", items: [] };
    listen(runtime.as("background"), "background", (message) => {
      received.push(message);
      return Promise.resolve(message.type === "approvalView" ? view : undefined);
    });
    const api = createPopupApi(runtime.as("popup"));

    await expect(api.getApprovals()).resolves.toEqual(view);
    await api.focusApprovals();
    expect(received).toEqual([
      { target: "background", type: "approvalView" },
      { target: "background", type: "approvalFocus" },
    ]);
  });

  it("delivers approval queue broadcasts, and nothing else, to subscribers until they unsubscribe", async () => {
    const runtime = new FakeRuntime();
    const api = createPopupApi(runtime.as("popup"));
    const seen: ApprovalView[] = [];
    const unsubscribe = api.subscribeApprovals((view) => seen.push(view));
    const background = runtime.as("background");
    const view: ApprovalView = { browserName: "chrome-3f2a", items: [] };

    await background.sendMessage({ target: "approval", type: "approvals", view });
    await background.sendMessage({ target: "popup", type: "state", state: STATE });
    unsubscribe();
    await background.sendMessage({ target: "approval", type: "approvals", view });

    expect(seen).toEqual([view]);
    expect(runtime.count()).toBe(0);
  });
});
