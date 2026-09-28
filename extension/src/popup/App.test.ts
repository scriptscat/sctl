// @vitest-environment jsdom
import "@testing-library/jest-dom/vitest";
import { createElement } from "react";
import { act, cleanup, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { PairResult, RenameResult, SetAddressResult } from "@/shared/messages";
import type { PopupApi } from "@/shared/popup-api";
import type { ConnectionState } from "@/shared/state";
import { App } from "./App";
import type { KeyValueStorage, SessionStorage } from "./storage";

function fakeApi(initial: ConnectionState) {
  let listeners: Array<(s: ConnectionState) => void> = [];
  const api = {
    getState: vi.fn(() => Promise.resolve(initial)),
    subscribe: vi.fn((listener: (s: ConnectionState) => void) => {
      listeners.push(listener);
      return () => {
        listeners = listeners.filter((l) => l !== listener);
      };
    }),
    pair: vi.fn<(code: string) => Promise<PairResult>>(() => Promise.resolve({ ok: true })),
    rename: vi.fn<(name: string) => Promise<RenameResult>>(() => Promise.resolve({ ok: true })),
    retryNow: vi.fn(() => Promise.resolve()),
    forget: vi.fn(() => Promise.resolve()),
    setAddress: vi.fn<(address: string) => Promise<SetAddressResult>>(() => Promise.resolve({ ok: true })),
  } satisfies PopupApi;
  return {
    api,
    emit: (next: ConnectionState) => {
      for (const listener of listeners) listener(next);
    },
  };
}

function fakeLocalStorage(initial: Record<string, unknown> = {}): KeyValueStorage & { data: Record<string, unknown> } {
  const data = { ...initial };
  return {
    data,
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]))),
    set: (items) => {
      Object.assign(data, items);
      return Promise.resolve();
    },
  };
}

function fakeSessionStorage(initial: Record<string, unknown> = {}): SessionStorage & { data: Record<string, unknown> } {
  const data = { ...initial };
  return {
    data,
    get: (keys) => Promise.resolve(Object.fromEntries(keys.filter((k) => k in data).map((k) => [k, data[k]]))),
    set: (items) => {
      Object.assign(data, items);
      return Promise.resolve();
    },
    remove: (keys) => {
      for (const key of Array.isArray(keys) ? keys : [keys]) delete data[key];
      return Promise.resolve();
    },
  };
}

const BASE = { instanceId: "3f2a9c0e5b7d41e8a6f0c2d4e6f80a1b", name: "chrome-3f2a", address: "127.0.0.1:8643" };
const CONNECTED = {
  status: "connected" as const,
  daemonVersion: "0.2.0",
  product: "Chrome",
  productVersion: "129.0.6668.58",
  connectedAt: 0,
};

function renderApp(
  state: ConnectionState,
  opts: {
    lang?: "zh" | "en";
    api?: ReturnType<typeof fakeApi>["api"];
    localStorage?: KeyValueStorage;
    sessionStorage?: SessionStorage;
    now?: () => number;
  } = {},
) {
  const wired = opts.api ? { api: opts.api, emit: () => undefined } : fakeApi(state);
  const localStorage = opts.localStorage ?? fakeLocalStorage({ language: opts.lang ?? "en" });
  const sessionStorage = opts.sessionStorage ?? fakeSessionStorage();
  const view = render(
    createElement(App, {
      api: wired.api,
      localStorage,
      sessionStorage,
      now: opts.now ?? (() => 0),
    }),
  );
  return { ...view, api: wired.api, emit: wired.emit, localStorage, sessionStorage };
}

beforeEach(() => {
  vi.stubGlobal("chrome", { runtime: { getManifest: () => ({ version: "0.2.0" }) } });
  vi.stubGlobal("navigator", { ...navigator, clipboard: { writeText: vi.fn() }, language: "en-US" });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
  document.documentElement.classList.remove("dark");
});

describe("popup states (rendered from ConnectionState, zh and en)", () => {
  it.each(["zh", "en"] as const)("renders the unpaired state in %s", async (lang) => {
    renderApp({ ...BASE, status: "unpaired" }, { lang });
    await screen.findByLabelText(lang === "zh" ? "输入终端显示的配对码" : "Enter the pairing code it shows");
    expect(screen.getByRole("button", { name: lang === "zh" ? "配对" : "Pair" })).toBeDisabled();
    expect(screen.getByRole("status")).toHaveTextContent(lang === "zh" ? "未配对" : "Not paired");
  });

  it.each(["zh", "en"] as const)("renders the pairing state with disabled controls in %s", async (lang) => {
    renderApp({ ...BASE, status: "pairing" }, { lang });
    await screen.findByRole("status");
    expect(screen.getByRole("status")).toHaveTextContent(lang === "zh" ? "配对中" : "Pairing");
    expect(
      screen.getByRole("textbox", { name: lang === "zh" ? "输入终端显示的配对码" : "Enter the pairing code it shows" }),
    ).toBeDisabled();
    expect(screen.getByRole("button", { name: lang === "zh" ? "正在配对…" : "Pairing…" })).toBeDisabled();
    expect(screen.getByRole("button", { name: lang === "zh" ? "设置" : "Settings" })).toBeDisabled();
    expect(screen.getByRole("button", { name: lang === "zh" ? "daemon 地址" : "Daemon address" })).toBeDisabled();
  });

  it.each(["zh", "en"] as const)("renders pair-failed (code-rejected) in %s", async (lang) => {
    renderApp({ ...BASE, status: "pair-failed", reason: "code-rejected" }, { lang });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(lang === "zh" ? "配对失败" : "Pairing failed");
  });

  it.each(["zh", "en"] as const)(
    "renders pair-failed (name-taken) mentioning the attempted name in %s",
    async (lang) => {
      renderApp({ ...BASE, status: "pair-failed", reason: "name-taken" }, { lang });
      const alert = await screen.findByRole("alert");
      expect(alert).toHaveTextContent(BASE.name);
      expect(alert).toHaveTextContent(lang === "zh" ? "sctl browsers forget" : "sctl browsers forget");
    },
  );

  it.each(["zh", "en"] as const)("renders pair-unreachable showing the attempted address in %s", async (lang) => {
    renderApp({ ...BASE, status: "pair-unreachable" }, { lang });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(BASE.address);
  });

  it.each(["zh", "en"] as const)("renders the connected state with instance details in %s", async (lang) => {
    renderApp({ ...BASE, ...CONNECTED }, { lang });
    const nameRow = await screen.findByRole("group", { name: lang === "zh" ? "实例名称" : "Instance name" });
    expect(nameRow).toHaveTextContent(BASE.name);
    expect(within(nameRow).getByRole("button", { name: lang === "zh" ? "重命名" : "Rename" })).toBeEnabled();
    expect(screen.getByRole("status")).toHaveTextContent(lang === "zh" ? "已连接" : "Connected");
    expect(screen.getByText(new RegExp(`sctl tabs list --browser ${BASE.name}`))).toBeInTheDocument();
  });

  it.each(["zh", "en"] as const)("copies the full instance ID in the connected state in %s", async (lang) => {
    renderApp({ ...BASE, ...CONNECTED }, { lang });
    const idRow = await screen.findByRole("definition", { name: lang === "zh" ? "实例 ID" : "Instance ID" });
    const user = userEvent.setup();
    const writeText = vi.spyOn(navigator.clipboard, "writeText").mockResolvedValue(undefined);
    await user.click(within(idRow).getByRole("button", { name: lang === "zh" ? "复制" : "Copy" }));
    expect(writeText).toHaveBeenCalledWith(BASE.instanceId);
  });

  it.each(["zh", "en"] as const)(
    "shows the browser, daemon version and time since the connection was established in %s",
    async (lang) => {
      // 弹窗在连接建立 2 小时后才打开：时长必须按连接建立的时刻计算，而不是从弹窗打开时算起。
      renderApp({ ...BASE, ...CONNECTED, connectedAt: 1_000 }, { lang, now: () => 1_000 + 2 * 60 * 60 * 1000 });
      const details = await screen.findByRole("definition", { name: lang === "zh" ? "浏览器" : "Browser" });
      expect(details).toHaveTextContent("Chrome 129.0.6668.58");
      expect(screen.getByRole("definition", { name: lang === "zh" ? "daemon" : "Daemon" })).toHaveTextContent("0.2.0");
      expect(screen.getByRole("definition", { name: lang === "zh" ? "已连接" : "Connected for" })).toHaveTextContent(
        lang === "zh" ? "2 小时" : "2 h",
      );
    },
  );

  it.each(["zh", "en"] as const)("renders the renaming state when Rename is clicked in %s", async (lang) => {
    renderApp({ ...BASE, ...CONNECTED }, { lang });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: lang === "zh" ? "重命名" : "Rename" }));
    expect(screen.getByLabelText(lang === "zh" ? "实例名称" : "Instance name")).toHaveValue(BASE.name);
    expect(screen.getByRole("button", { name: lang === "zh" ? "保存" : "Save" })).toBeInTheDocument();
  });

  it.each(["zh", "en"] as const)("cancels renaming with Esc and keeps the name in %s", async (lang) => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { lang, api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: lang === "zh" ? "重命名" : "Rename" }));
    const input = screen.getByLabelText(lang === "zh" ? "实例名称" : "Instance name");
    await user.clear(input);
    await user.type(input, "home{Escape}");
    expect(screen.queryByRole("textbox", { name: lang === "zh" ? "实例名称" : "Instance name" })).toBeNull();
    expect(screen.getByRole("group", { name: lang === "zh" ? "实例名称" : "Instance name" })).toHaveTextContent(
      BASE.name,
    );
    expect(api.rename).not.toHaveBeenCalled();
  });

  it.each(["zh", "en"] as const)("renders the name-taken (rename conflict) state in %s", async (lang) => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    api.rename.mockResolvedValueOnce({ ok: false, error: "name-taken" });
    renderApp({ ...BASE, ...CONNECTED }, { lang, api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: lang === "zh" ? "重命名" : "Rename" }));
    const input = screen.getByLabelText(lang === "zh" ? "实例名称" : "Instance name");
    await user.clear(input);
    await user.type(input, "home");
    await user.click(screen.getByRole("button", { name: lang === "zh" ? "保存" : "Save" }));
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("home");
  });

  it.each(["zh", "en"] as const)(
    "renders the reconnecting state with a countdown and retry button in %s",
    async (lang) => {
      renderApp({ ...BASE, status: "reconnecting", attempt: 2, retryAt: 5000 }, { lang, now: () => 2000 });
      await screen.findByRole("status");
      expect(screen.getByRole("status")).toHaveTextContent(lang === "zh" ? "重连中" : "Reconnecting");
      expect(screen.getByText(/3s|第 3 次/)).toBeInTheDocument();
      expect(screen.getByRole("button", { name: lang === "zh" ? "立即重试" : "Retry now" })).toBeInTheDocument();
    },
  );

  it.each(["zh", "en"] as const)("renders the rejected state with a pair-again button in %s", async (lang) => {
    renderApp({ ...BASE, status: "rejected" }, { lang });
    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(
      lang === "zh" ? "daemon 不再认可这个浏览器" : "The daemon no longer recognises this browser",
    );
    expect(screen.getByRole("button", { name: lang === "zh" ? "重新配对" : "Pair again" })).toBeInTheDocument();
  });

  it.each(["zh", "en"] as const)("reveals the pairing form again once Pair again is clicked in %s", async (lang) => {
    renderApp({ ...BASE, status: "rejected" }, { lang });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: lang === "zh" ? "重新配对" : "Pair again" }));
    expect(
      screen.getByLabelText(lang === "zh" ? "输入终端显示的配对码" : "Enter the pairing code it shows"),
    ).toBeInTheDocument();
  });

  it.each(["zh", "en"] as const)("renders the forget-confirmation dialog, focused on cancel, in %s", async (lang) => {
    renderApp({ ...BASE, ...CONNECTED }, { lang });
    const user = userEvent.setup();
    await user.click(
      await screen.findByRole("button", { name: lang === "zh" ? "断开并忘记" : "Disconnect and forget" }),
    );
    const dialog = await screen.findByRole("alertdialog");
    expect(within(dialog).getByText(new RegExp(`sctl browsers forget ${BASE.name}`))).toBeInTheDocument();
    expect(within(dialog).getByRole("button", { name: lang === "zh" ? "取消" : "Cancel" })).toHaveFocus();
  });

  it.each(["zh", "en"] as const)("renders the settings screen in %s", async (lang) => {
    renderApp({ ...BASE, ...CONNECTED }, { lang });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: lang === "zh" ? "设置" : "Settings" }));
    expect(screen.getByRole("radiogroup", { name: lang === "zh" ? "外观" : "Appearance" })).toBeInTheDocument();
    expect(screen.getByRole("radiogroup", { name: lang === "zh" ? "语言" : "Language" })).toBeInTheDocument();
    expect(screen.getByLabelText(lang === "zh" ? "daemon 地址" : "Daemon address")).toHaveValue(BASE.address);
  });
});

describe("settings persistence and live effect", () => {
  it("persists the chosen appearance and language to local storage", async () => {
    const localStorage = fakeLocalStorage();
    const { getByRole } = renderApp({ ...BASE, ...CONNECTED }, { localStorage });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    await user.click(getByRole("radio", { name: "Dark" }));
    await user.click(getByRole("radio", { name: "中文" }));
    expect(localStorage.data.appearance).toBe("dark");
    expect(localStorage.data.language).toBe("zh");
  });

  it("applies the dark appearance to the document immediately", async () => {
    renderApp({ ...BASE, ...CONNECTED });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    await user.click(screen.getByRole("radio", { name: "Dark" }));
    expect(document.documentElement.classList.contains("dark")).toBe(true);
  });

  it("applies the chosen language immediately, without reopening the popup", async () => {
    renderApp({ ...BASE, ...CONNECTED });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    await user.click(screen.getByRole("radio", { name: "中文" }));
    expect(screen.getByText("设置")).toBeInTheDocument();
  });

  it("restores previously saved appearance and language on the next open", async () => {
    const localStorage = fakeLocalStorage({ appearance: "dark", language: "zh" });
    renderApp({ ...BASE, ...CONNECTED }, { localStorage });
    // 等语言真正切到中文，确保 loadPrefs 的 Promise 已经落地，而不仅仅是连接状态已加载。
    await screen.findByText("让 sctl 控制这个浏览器");
    // 深色类由 effect 在提交之后设置，文字出现时它可能还没执行。
    await vi.waitFor(() => expect(document.documentElement.classList.contains("dark")).toBe(true));
  });
});

describe("pairing code draft", () => {
  it("restores a pairing code draft saved in session storage when the popup reopens", async () => {
    const sessionStorage = fakeSessionStorage({ pairingCodeDraft: "AB12-CD34" });
    renderApp({ ...BASE, status: "unpaired" }, { sessionStorage });
    expect(await screen.findByDisplayValue("AB12-CD34")).toBeInTheDocument();
  });

  it("never clears an existing draft while mounting, before it has been read", async () => {
    // Regression: the persistence effect used to run once with the initial empty `code` state,
    // wiping a real chrome.storage.session draft before the load had a chance to populate it.
    const sessionStorage = fakeSessionStorage({ pairingCodeDraft: "AB12-CD34" });
    const remove = vi.spyOn(sessionStorage, "remove");
    renderApp({ ...BASE, status: "unpaired" }, { sessionStorage });
    await screen.findByDisplayValue("AB12-CD34");
    expect(remove).not.toHaveBeenCalled();
  });

  it("saves what the user types as the draft, for the next reopen", async () => {
    const sessionStorage = fakeSessionStorage();
    renderApp({ ...BASE, status: "unpaired" }, { sessionStorage });
    const user = userEvent.setup();
    const input = await screen.findByLabelText("Enter the pairing code it shows");
    await user.type(input, "K7QM3XRD");
    expect(sessionStorage.data.pairingCodeDraft).toBe("K7QM3XRD");
  });

  it("keeps the draft when pairing cannot reach the daemon, so the code survives reopening the popup", async () => {
    const sessionStorage = fakeSessionStorage({ pairingCodeDraft: "K7QM3XRD" });
    const { api, emit } = fakeApi({ ...BASE, status: "unpaired" });
    renderApp({ ...BASE, status: "unpaired" }, { sessionStorage, api });
    const user = userEvent.setup();
    await screen.findByDisplayValue("K7QM3XRD");
    await user.click(screen.getByRole("button", { name: "Pair" }));
    expect(api.pair).toHaveBeenCalledWith("K7QM3XRD");
    await act(async () => {
      emit({ ...BASE, status: "pairing" });
      emit({ ...BASE, status: "pair-unreachable" });
      await Promise.resolve();
    });
    await screen.findByRole("alert");
    expect(sessionStorage.data.pairingCodeDraft).toBe("K7QM3XRD");
  });

  it("clears the draft once pairing succeeds", async () => {
    const sessionStorage = fakeSessionStorage({ pairingCodeDraft: "K7QM3XRD" });
    const { api, emit } = fakeApi({ ...BASE, status: "unpaired" });
    renderApp({ ...BASE, status: "unpaired" }, { sessionStorage, api });
    const user = userEvent.setup();
    await screen.findByDisplayValue("K7QM3XRD");
    await user.click(screen.getByRole("button", { name: "Pair" }));
    await act(async () => {
      emit({ ...BASE, ...CONNECTED });
      await Promise.resolve();
    });
    await screen.findByText(/sctl tabs list --browser/);
    await vi.waitFor(() => expect(sessionStorage.data.pairingCodeDraft).toBeUndefined());
  });
});

describe("rename, forget and daemon address flows", () => {
  it("calls the popup API to rename and returns to the normal view on success", async () => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Rename" }));
    const input = screen.getByLabelText("Instance name");
    await user.clear(input);
    await user.type(input, "work{enter}");
    expect(api.rename).toHaveBeenCalledWith("work");
    await screen.findByRole("button", { name: "Rename" });
  });

  it("disables renaming while the instance is not connected", async () => {
    renderApp({ ...BASE, status: "reconnecting", attempt: 0, retryAt: null });
    expect(await screen.findByRole("button", { name: "Rename" })).toBeDisabled();
  });

  it("calls forget on confirmation and leaves the dialog", async () => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Disconnect and forget" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Disconnect and forget" }));
    expect(api.forget).toHaveBeenCalledTimes(1);
  });

  it("does not call forget when cancel is chosen", async () => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Disconnect and forget" }));
    const dialog = await screen.findByRole("alertdialog");
    await user.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(api.forget).not.toHaveBeenCalled();
  });

  it("calls setAddress with the new value and shows the invalid-address error otherwise", async () => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    const addressField = screen.getByLabelText("Daemon address");
    await user.clear(addressField);
    await user.type(addressField, "127.0.0.1:9000");
    await user.click(screen.getByRole("button", { name: "Save and reconnect" }));
    expect(api.setAddress).toHaveBeenCalledWith("127.0.0.1:9000");
  });

  it("rejects an address that isn't host:port without calling the API", async () => {
    const { api } = fakeApi({ ...BASE, ...CONNECTED });
    renderApp({ ...BASE, ...CONNECTED }, { api });
    const user = userEvent.setup();
    await user.click(await screen.findByRole("button", { name: "Settings" }));
    const addressField = screen.getByLabelText("Daemon address");
    await user.clear(addressField);
    await user.type(addressField, "not-an-address");
    await user.click(screen.getByRole("button", { name: "Save and reconnect" }));
    expect(api.setAddress).not.toHaveBeenCalled();
    expect(await screen.findByRole("alert")).toHaveTextContent("host:port");
  });
});

describe("link stroke encodes the connection state", () => {
  it.each([
    ["unpaired", { ...BASE, status: "unpaired" as const }, undefined],
    ["connected", { ...BASE, ...CONNECTED }, undefined],
    ["reconnecting", { ...BASE, status: "reconnecting" as const, attempt: 0, retryAt: null }, "5 5"],
    ["rejected", { ...BASE, status: "rejected" as const }, "10 14"],
  ])("draws a distinct stroke for %s", async (_label, state, expectedDash) => {
    const { container } = renderApp(state);
    await screen.findByRole("status");
    const line = container.querySelector("line");
    expect(line).not.toBeNull();
    expect(line?.getAttribute("stroke-dasharray")).toBe(expectedDash ?? (state.status === "unpaired" ? "2 6" : null));
  });
});

describe("accessibility", () => {
  it("announces the connection status through a polite live region", async () => {
    renderApp({ ...BASE, ...CONNECTED });
    const status = await screen.findByRole("status");
    expect(status).toHaveAttribute("aria-live", "polite");
  });

  it("marks pairing errors with role=alert", async () => {
    renderApp({ ...BASE, status: "pair-failed", reason: "code-rejected" });
    expect(await screen.findByRole("alert")).toBeInTheDocument();
  });
});
