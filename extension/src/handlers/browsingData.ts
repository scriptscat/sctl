import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { needs } from "./api";

// chrome.browsingData 的 origins 只对 cookies、存储和缓存有效（@types/chrome RemovalOptions.origins）；
// history、downloads、formData 没有来源维度，与 origins 同用会被 chrome 笼统拒绝，这里先翻译成 INVALID_REQUEST。
const ORIGIN_FILTERABLE = new Set([
  "cache",
  "cacheStorage",
  "cookies",
  "fileSystems",
  "indexedDB",
  "localStorage",
  "serviceWorkers",
  "webSQL",
]);

// chrome 要求 origins 是不带路径的 http/https 来源。
function requireOrigin(origin: string): void {
  let parsed: URL;
  try {
    parsed = new URL(origin);
  } catch {
    throw new HandlerError("INVALID_REQUEST", `${origin} is not a valid origin`);
  }
  if ((parsed.protocol !== "http:" && parsed.protocol !== "https:") || parsed.origin !== origin) {
    throw new HandlerError("INVALID_REQUEST", `${origin} is not an http or https origin without a path`);
  }
}

const handleClear: RpcHandler<"browsingData.clear"> = async (params) => {
  const options: chrome.browsingData.RemovalOptions = { since: params.since ?? 0 };
  if (params.origins !== undefined) {
    const unsupported = params.types.filter((type) => !ORIGIN_FILTERABLE.has(type));
    if (unsupported.length > 0) {
      throw new HandlerError(
        "INVALID_REQUEST",
        `origins cannot be combined with ${unsupported.join(", ")}: only ${[...ORIGIN_FILTERABLE].join(", ")} can be cleared per origin`,
      );
    }
    params.origins.forEach(requireOrigin);
    options.origins = params.origins as [string, ...string[]];
  }
  const dataToRemove: chrome.browsingData.DataTypeSet = {};
  for (const type of params.types) {
    dataToRemove[type] = true;
  }
  await chrome.browsingData.remove(options, dataToRemove);
  return { types: params.types };
};

export function registerBrowsingDataHandlers(registry: HandlerRegistry): void {
  registry.register("browsingData.clear", needs("browsingData", handleClear));
}
