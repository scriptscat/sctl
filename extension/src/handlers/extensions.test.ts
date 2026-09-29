import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from "vitest";
import { HandlerRegistry, runApproved } from "@/background/registry";
import { validateExtensionsListResult } from "@/protocol/generated/validators.generated";
import type { ApprovalRequest, ExtensionUninstallDetail } from "@/shared/approvals";
import { registerHandlers } from "./index";
import { uninstallExtension, uninstallObserved } from "./extensions";

type GetAllFn = () => Promise<chrome.management.ExtensionInfo[]>;
type GetFn = (id: string) => Promise<chrome.management.ExtensionInfo>;
type SetEnabledFn = (id: string, enabled: boolean) => Promise<void>;
type UninstallFn = (id: string, options?: chrome.management.UninstallOptions) => Promise<void>;

interface ManagementMock {
  getAll: Mock<GetAllFn>;
  get: Mock<GetFn>;
  setEnabled: Mock<SetEnabledFn>;
  uninstall: Mock<UninstallFn>;
}

const SELF_ID = "selfselfselfselfselfselfselfself";

function info(id: string, overrides: Partial<chrome.management.ExtensionInfo> = {}): chrome.management.ExtensionInfo {
  return {
    id,
    name: `Name ${id}`,
    shortName: `N ${id}`,
    description: `Description of ${id}`,
    version: "1.2.3",
    mayDisable: true,
    enabled: true,
    isApp: false,
    type: "extension",
    offlineEnabled: false,
    optionsUrl: "",
    permissions: [],
    hostPermissions: [],
    installType: "normal",
    ...overrides,
  };
}

const TIDY = info("tidytidytidytidytidytidytidytidy", {
  name: "Tab Tidy",
  version: "0.9.3",
  description: "按域名整理标签页",
  installType: "development",
});
const POLICY = info("policypolicypolicypolicypolicypo", { installType: "admin", mayDisable: false });

describe("extensions handlers", () => {
  let management: ManagementMock;
  let registry: HandlerRegistry;
  const installed = new Map<string, chrome.management.ExtensionInfo>();

  beforeEach(() => {
    installed.clear();
    for (const ext of [TIDY, POLICY, info(SELF_ID, { name: "sctl Browser" })]) installed.set(ext.id, ext);
    management = {
      getAll: vi.fn<GetAllFn>(() => Promise.resolve([...installed.values()])),
      get: vi.fn<GetFn>((id) => {
        const found = installed.get(id);
        return found ? Promise.resolve(found) : Promise.reject(new Error(`Failed to find extension with id ${id}.`));
      }),
      setEnabled: vi.fn<SetEnabledFn>(() => Promise.resolve()),
      uninstall: vi.fn<UninstallFn>(() => Promise.resolve()),
    };
    vi.stubGlobal("chrome", { management, runtime: { id: SELF_ID } });
    registry = new HandlerRegistry();
    registerHandlers(registry);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  describe("extensions.list", () => {
    it("lists every installed extension and app with the fields the protocol declares, marked untrusted", async () => {
      const outcome = await registry.dispatch("extensions.list", {});

      expect(outcome).toEqual({
        ok: true,
        result: {
          contentTrust: "untrusted-page-content",
          items: [
            {
              id: TIDY.id,
              name: "Tab Tidy",
              version: "0.9.3",
              enabled: true,
              type: "extension",
              installType: "development",
              mayDisable: true,
            },
            {
              id: POLICY.id,
              name: POLICY.name,
              version: "1.2.3",
              enabled: true,
              type: "extension",
              installType: "admin",
              mayDisable: false,
            },
            {
              id: SELF_ID,
              name: "sctl Browser",
              version: "1.2.3",
              enabled: true,
              type: "extension",
              installType: "normal",
              mayDisable: true,
            },
          ],
        },
      });
      expect(validateExtensionsListResult(outcome.ok ? outcome.result : null)).toBe(true);
    });
  });

  describe("extensions.enable", () => {
    it("enables the extension and reports its new state", async () => {
      const outcome = await registry.dispatch("extensions.enable", { id: TIDY.id });

      expect(management.setEnabled).toHaveBeenCalledWith(TIDY.id, true);
      expect(outcome).toEqual({ ok: true, result: { id: TIDY.id, enabled: true } });
    });

    it("returns NOT_FOUND for an unknown id without changing anything", async () => {
      const outcome = await registry.dispatch("extensions.enable", { id: "unknown" });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(management.setEnabled).not.toHaveBeenCalled();
    });

    it("passes Chrome's refusal on as INVALID_REQUEST with Chrome's reason", async () => {
      management.setEnabled.mockRejectedValue(new Error(`Extension ${TIDY.id} cannot be modified by user.`));

      const outcome = await registry.dispatch("extensions.enable", { id: TIDY.id });

      expect(outcome).toEqual({
        ok: false,
        code: "INVALID_REQUEST",
        message: `Extension ${TIDY.id} cannot be modified by user.`,
      });
    });
  });

  describe("extensions.disable", () => {
    it("disables the extension when confirmed and reports its new state", async () => {
      const outcome = await registry.dispatch("extensions.disable", { id: TIDY.id, confirm: true });

      expect(management.setEnabled).toHaveBeenCalledWith(TIDY.id, false);
      expect(outcome).toEqual({ ok: true, result: { id: TIDY.id, enabled: false } });
    });

    it("refuses without confirm: true and changes nothing", async () => {
      const outcome = await registry.dispatch("extensions.disable", { id: TIDY.id });

      expect(outcome).toMatchObject({ ok: false, code: "CONFIRMATION_REQUIRED" });
      expect(management.setEnabled).not.toHaveBeenCalled();
    });

    it("refuses to disable sctl Browser itself with INVALID_REQUEST", async () => {
      const outcome = await registry.dispatch("extensions.disable", { id: SELF_ID, confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(management.setEnabled).not.toHaveBeenCalled();
    });

    it("refuses a policy-installed extension Chrome does not let the user disable, naming Chrome's reason", async () => {
      const outcome = await registry.dispatch("extensions.disable", { id: POLICY.id, confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "INVALID_REQUEST" });
      expect(outcome.ok ? "" : outcome.message).toContain("policy");
      expect(management.setEnabled).not.toHaveBeenCalled();
    });

    it("returns NOT_FOUND for an unknown id", async () => {
      const outcome = await registry.dispatch("extensions.disable", { id: "unknown", confirm: true });

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(management.setEnabled).not.toHaveBeenCalled();
    });

    it("passes Chrome's refusal on as INVALID_REQUEST with Chrome's reason", async () => {
      management.setEnabled.mockRejectedValue(new Error("Extension cannot be modified by user."));

      const outcome = await registry.dispatch("extensions.disable", { id: TIDY.id, confirm: true });

      expect(outcome).toEqual({ ok: false, code: "INVALID_REQUEST", message: "Extension cannot be modified by user." });
    });
  });

  describe("extensions.uninstall", () => {
    it("is an approval method: its checks pass and it yields what the approval window shows, uninstalling nothing", async () => {
      expect(registry.requiresApproval("extensions.uninstall")).toBe(true);
      expect(registry.executesInWindow("extensions.uninstall")).toBe(true);

      const prepared = await registry.prepare("extensions.uninstall", { id: TIDY.id });

      expect(prepared).toEqual({
        ok: true,
        request: {
          kind: "extensions.uninstall",
          detail: {
            id: TIDY.id,
            name: "Tab Tidy",
            version: "0.9.3",
            description: "按域名整理标签页",
            installType: "development",
            enabled: true,
          },
        },
      });
      expect(management.uninstall).not.toHaveBeenCalled();
    });

    it.each([
      ["an unknown id", "unknown", "NOT_FOUND"],
      ["sctl Browser itself", SELF_ID, "INVALID_REQUEST"],
      ["a policy-installed extension", POLICY.id, "INVALID_REQUEST"],
    ])("rejects %s before any approval window opens", async (_label, id, code) => {
      const prepared = await registry.prepare("extensions.uninstall", { id });

      expect(prepared).toMatchObject({ ok: false, code });
      expect(management.uninstall).not.toHaveBeenCalled();
    });

    it("explains a policy-installed extension with Chrome's reason", async () => {
      const prepared = await registry.prepare("extensions.uninstall", { id: POLICY.id });

      expect(prepared.ok ? "" : prepared.message).toContain("policy");
    });

    it("is never carried out by the service worker, which has no user gesture", () => {
      const detail = uninstallDetail(TIDY);
      expect(() => registry.execute({ kind: "extensions.uninstall", detail })).toThrow(/approval window/);
      expect(management.uninstall).not.toHaveBeenCalled();
    });
  });

  describe("uninstalling from the approval window", () => {
    const run = (detail: ExtensionUninstallDetail) => runApproved("extensions.uninstall", uninstallExtension, detail);

    it("checks the extension is still installed, then asks Chrome to uninstall it with its confirmation dialog", async () => {
      const outcome = await run(uninstallDetail(TIDY));

      expect(management.get).toHaveBeenCalledWith(TIDY.id);
      expect(management.uninstall).toHaveBeenCalledWith(TIDY.id, { showConfirmDialog: true });
      expect(management.get.mock.invocationCallOrder[0]).toBeLessThan(management.uninstall.mock.invocationCallOrder[0]);
      expect(outcome).toEqual({
        ok: true,
        result: { contentTrust: "untrusted-page-content", id: TIDY.id, name: "Tab Tidy" },
      });
    });

    it("returns NOT_FOUND and uninstalls nothing when the extension is gone by the time the user approves", async () => {
      installed.delete(TIDY.id);

      const outcome = await run(uninstallDetail(TIDY));

      expect(outcome).toMatchObject({ ok: false, code: "NOT_FOUND" });
      expect(management.uninstall).not.toHaveBeenCalled();
    });

    it("returns USER_REJECTED when the user cancels Chrome's own confirmation dialog", async () => {
      management.uninstall.mockRejectedValue(new Error(`Extension ${TIDY.id} uninstall canceled by user.`));

      const outcome = await run(uninstallDetail(TIDY));

      expect(outcome).toMatchObject({ ok: false, code: "USER_REJECTED" });
    });

    it("reports any other refusal from Chrome as an internal error without leaking it", async () => {
      management.uninstall.mockRejectedValue(new Error("chrome.management.uninstall requires a user gesture."));
      const error = vi.spyOn(console, "error").mockImplementation(() => {});

      const outcome = await run(uninstallDetail(TIDY));

      expect(outcome).toEqual({ ok: false, code: "INTERNAL_ERROR", message: "internal error" });
      expect(error).toHaveBeenCalled();
    });
  });

  // 审批窗口在 Chrome 确认框打开期间被关掉时，窗口里等结论的回调随之消失；这时卸载成功只能从
  // chrome.management.onUninstalled 得知，结论与窗口自己回报的一样。
  describe("observing an uninstall outside the approval window", () => {
    it("concludes the approved uninstall of exactly the extension Chrome reports as uninstalled", () => {
      const tidy: ApprovalRequest = { kind: "extensions.uninstall", detail: uninstallDetail(TIDY) };
      const removal: ApprovalRequest = {
        kind: "bookmarks.remove",
        detail: {
          summary: { items: 1, bookmarks: 1, folders: 0, containedBookmarks: 0, containedFolders: 0 },
          items: [{ id: TIDY.id, type: "bookmark", title: "B", url: "https://b.example/", parentId: "1", path: [] }],
        },
      };

      expect(uninstallObserved(TIDY.id)(tidy)).toEqual({
        ok: true,
        result: { contentTrust: "untrusted-page-content", id: TIDY.id, name: "Tab Tidy" },
      });
      expect(uninstallObserved("someone-else")(tidy)).toBeNull();
      expect(uninstallObserved(TIDY.id)(removal)).toBeNull();
    });
  });
});

function uninstallDetail(ext: chrome.management.ExtensionInfo): ExtensionUninstallDetail {
  return {
    id: ext.id,
    name: ext.name,
    version: ext.version,
    description: ext.description,
    installType: ext.installType,
    enabled: ext.enabled,
  };
}
