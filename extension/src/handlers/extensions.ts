import { type ApprovalHandler, HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";

async function requireExtension(id: string): Promise<chrome.management.ExtensionInfo> {
  try {
    return await chrome.management.get(id);
  } catch {
    throw new HandlerError("NOT_FOUND", `no extension or app with id ${id}`);
  }
}

// 禁用或卸载自己会切断 sctl 与这个浏览器的连接，调用方也就拿不到结果。
function refuseSelf(id: string, action: "disable" | "uninstall"): void {
  if (id === chrome.runtime.id) {
    throw new HandlerError("INVALID_REQUEST", `sctl Browser cannot ${action} itself`);
  }
}

// 企业策略强制安装的扩展 Chrome 不许用户禁用或卸载；mayDisable 与 installType 就是 Chrome 给出的原因。
function refuseManaged(ext: chrome.management.ExtensionInfo, action: "disabled" | "uninstalled"): void {
  if (ext.installType === "admin" || !ext.mayDisable) {
    throw new HandlerError(
      "INVALID_REQUEST",
      `Chrome does not let the user change extension ${ext.id}: it is installed by enterprise policy ` +
        `(installType ${ext.installType}, mayDisable ${ext.mayDisable}) and cannot be ${action}`,
    );
  }
}

// setEnabled 被拒绝时（例如权限升级后需要用户重新授权）原样交出 Chrome 的理由。
async function setEnabled(id: string, enabled: boolean): Promise<{ id: string; enabled: boolean }> {
  try {
    await chrome.management.setEnabled(id, enabled);
  } catch (error) {
    throw new HandlerError("INVALID_REQUEST", error instanceof Error ? error.message : String(error));
  }
  return { id, enabled };
}

const handleList: RpcHandler<"extensions.list"> = async () => {
  const all = await chrome.management.getAll();
  return {
    // 名称由扩展作者决定，原样返回。
    contentTrust: "untrusted-page-content",
    items: all.map((ext) => ({
      id: ext.id,
      name: ext.name,
      version: ext.version,
      enabled: ext.enabled,
      type: ext.type,
      installType: ext.installType,
      mayDisable: ext.mayDisable,
    })),
  };
};

const handleEnable: RpcHandler<"extensions.enable"> = async (params) => {
  await requireExtension(params.id);
  return setEnabled(params.id, true);
};

const handleDisable: RpcHandler<"extensions.disable"> = async (params) => {
  refuseSelf(params.id, "disable");
  refuseManaged(await requireExtension(params.id), "disabled");
  return setEnabled(params.id, false);
};

// Chrome 确认框里点了取消时 uninstall 以「Extension <id> uninstall canceled by user.」拒绝（T1 真机探针）。
const CANCELED_BY_USER = /uninstall canceled by user/;

// 卸载扩展（L2）。execute 只能在审批窗口里、借「卸载」按钮的点击执行：没有用户手势时 Chrome 直接拒绝，
// 也不会弹出它自己的确认框（T1 真机探针）。
export const uninstallExtension: ApprovalHandler<"extensions.uninstall"> = {
  async prepare(params) {
    refuseSelf(params.id, "uninstall");
    const ext = await requireExtension(params.id);
    refuseManaged(ext, "uninstalled");
    return {
      id: ext.id,
      name: ext.name,
      version: ext.version,
      description: ext.description,
      installType: ext.installType,
      enabled: ext.enabled,
    };
  },
  async execute(detail) {
    // 从弹出审批窗口到用户点击之间，扩展可能已被别处卸载：那样什么都不做。
    await requireExtension(detail.id);
    try {
      await chrome.management.uninstall(detail.id, { showConfirmDialog: true });
    } catch (error) {
      if (error instanceof Error && CANCELED_BY_USER.test(error.message)) {
        throw new HandlerError("USER_REJECTED", "the uninstall was cancelled in the browser's confirmation dialog");
      }
      throw error;
    }
    return { contentTrust: "untrusted-page-content", id: detail.id, name: detail.name };
  },
  inWindow: true,
};

export function registerExtensionsHandlers(registry: HandlerRegistry): void {
  registry.register("extensions.list", handleList);
  registry.register("extensions.enable", handleEnable);
  registry.register("extensions.disable", handleDisable);
  registry.registerApproval("extensions.uninstall", uninstallExtension);
}
