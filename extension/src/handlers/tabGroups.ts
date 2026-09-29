import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { requireAllTabs, requireTab, requireTabGroup, requireWindow } from "./targets";

const GROUP_COLORS = ["grey", "blue", "red", "yellow", "green", "pink", "purple", "cyan", "orange"];

// 扩展侧不再校验协议 schema，颜色取值在这里兑现；先于任何 chrome.* 调用，失败时什么都不改。
function requireColor(color: string | undefined): chrome.tabGroups.Color | undefined {
  if (color !== undefined && !GROUP_COLORS.includes(color)) {
    throw new HandlerError("INVALID_REQUEST", `invalid group colour ${JSON.stringify(color)}`);
  }
  return color as chrome.tabGroups.Color | undefined;
}

// chrome.tabs.group/ungroup 的类型要求至少一个 ID；协议的 minItems: 1 已在 daemon 边界保证。
type TabIds = [number, ...number[]];

const handleList: RpcHandler<"tabGroups.list"> = async (params) => {
  const query = params.windowId === undefined ? {} : { windowId: params.windowId };
  if (params.windowId !== undefined) {
    await requireWindow(params.windowId);
  }
  const [groups, tabs] = await Promise.all([chrome.tabGroups.query(query), chrome.tabs.query(query)]);
  const counts = new Map<number, number>();
  for (const tab of tabs) {
    counts.set(tab.groupId, (counts.get(tab.groupId) ?? 0) + 1);
  }
  return {
    contentTrust: "untrusted-page-content",
    groups: groups.map((group) => ({
      groupId: group.id,
      windowId: group.windowId,
      // 标题由网页或用户控制，原样返回，只作为数据。
      title: group.title ?? "",
      color: group.color,
      collapsed: group.collapsed,
      tabCount: counts.get(group.id) ?? 0,
    })),
  };
};

const handleCreate: RpcHandler<"tabGroups.create"> = async (params) => {
  const color = requireColor(params.color);
  const tabIds = [...new Set(params.tabIds)] as TabIds;
  const windowIds = new Set<number>();
  for (const tabId of tabIds) {
    windowIds.add((await requireTab(tabId)).windowId);
  }
  if (windowIds.size > 1) {
    throw new HandlerError("INVALID_REQUEST", "tabs must all be in the same window to be grouped");
  }
  const groupId = await chrome.tabs.group({ tabIds, createProperties: { windowId: [...windowIds][0] } });
  const update: chrome.tabGroups.UpdateProperties = {
    ...(params.title === undefined ? {} : { title: params.title }),
    ...(color === undefined ? {} : { color }),
  };
  if (Object.keys(update).length > 0) {
    await chrome.tabGroups.update(groupId, update);
  }
  return { groupId };
};

const handleAdd: RpcHandler<"tabGroups.add"> = async (params) => {
  await requireTabGroup(params.groupId);
  const tabIds = (await requireAllTabs(params.tabIds)) as TabIds;
  await chrome.tabs.group({ groupId: params.groupId, tabIds });
  return { groupId: params.groupId, tabIds };
};

const handleEdit: RpcHandler<"tabGroups.edit"> = async (params) => {
  const color = requireColor(params.color);
  const update: chrome.tabGroups.UpdateProperties = {
    ...(params.title === undefined ? {} : { title: params.title }),
    ...(color === undefined ? {} : { color }),
    ...(params.collapsed === undefined ? {} : { collapsed: params.collapsed }),
  };
  if (Object.keys(update).length === 0) {
    throw new HandlerError("INVALID_REQUEST", "nothing to change: give title, color or collapsed");
  }
  await requireTabGroup(params.groupId);
  await chrome.tabGroups.update(params.groupId, update);
  return { groupId: params.groupId };
};

const handleUngroup: RpcHandler<"tabGroups.ungroup"> = async (params) => {
  const tabIds = (await requireAllTabs(params.tabIds)) as TabIds;
  // 组里最后一个标签页移出后，Chrome 会自动删除这个组。
  await chrome.tabs.ungroup(tabIds);
  return { tabIds };
};

export function registerTabGroupHandlers(registry: HandlerRegistry): void {
  registry.register("tabGroups.list", handleList);
  registry.register("tabGroups.create", handleCreate);
  registry.register("tabGroups.add", handleAdd);
  registry.register("tabGroups.edit", handleEdit);
  registry.register("tabGroups.ungroup", handleUngroup);
}
