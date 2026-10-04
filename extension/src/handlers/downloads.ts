import { HandlerError, type HandlerRegistry, type RpcHandler } from "@/background/registry";
import { needs } from "./api";
import { listLimit, takePage } from "./list";

// chrome 的下载开始时间是 ISO 字符串；协议用毫秒整数。
function startMillis(item: chrome.downloads.DownloadItem): number {
  return Date.parse(item.startTime);
}

// chrome.downloads.download 的 filename 相对于默认下载目录；绝对路径（含 Windows 盘符与反斜杠开头）和 ".." 段
// 会逃出该目录，必须在调用 chrome 之前拒绝。
function requireRelativeFilename(filename: string): void {
  const absolute = /^[/\\]/.test(filename) || /^[a-zA-Z]:/.test(filename);
  const escapes = filename.split(/[/\\]/).includes("..");
  if (absolute || escapes) {
    throw new HandlerError("INVALID_REQUEST", "filename must be a relative path without .. segments");
  }
}

function requireUrl(url: string): void {
  try {
    new URL(url);
  } catch {
    throw new HandlerError("INVALID_REQUEST", `${url} is not a valid URL`);
  }
}

async function requireDownload(id: number): Promise<chrome.downloads.DownloadItem> {
  const [found] = await chrome.downloads.search({ id });
  if (!found) {
    throw new HandlerError("NOT_FOUND", `no download ${id}`);
  }
  return found;
}

// chrome 对不可执行的操作（例如暂停已完成的下载）只给出拒绝原因；把原因交给调用方。
async function surfaceRejection<T>(run: () => Promise<T>): Promise<T> {
  try {
    return await run();
  } catch (error) {
    throw new HandlerError("INVALID_REQUEST", error instanceof Error ? error.message : String(error));
  }
}

const handleList: RpcHandler<"downloads.list"> = async (params) => {
  const limit = listLimit(params.limit);
  // 多取一条，用来判断是否还有未返回的条目。
  const query: chrome.downloads.DownloadQuery = { orderBy: ["-startTime"], limit: limit + 1 };
  if (params.state !== undefined) {
    query.state = params.state;
  }
  if (params.query !== undefined) {
    query.query = [params.query];
  }
  const found = await chrome.downloads.search(query);
  const page = takePage(found, limit);
  return {
    // 文件名和 URL 由网页或服务器控制，原样返回。
    contentTrust: "untrusted-page-content",
    hasMore: page.hasMore,
    items: page.items.map((item) => ({
      id: item.id,
      url: item.url,
      filename: item.filename,
      state: item.state,
      bytesReceived: item.bytesReceived,
      totalBytes: item.totalBytes,
      startTime: startMillis(item),
      exists: item.exists,
    })),
  };
};

const handleStart: RpcHandler<"downloads.start"> = async (params) => {
  requireUrl(params.url);
  const options: chrome.downloads.DownloadOptions = {
    url: params.url,
    // 从不覆盖已有文件，也从不弹出另存为对话框。
    conflictAction: "uniquify",
    saveAs: false,
  };
  if (params.filename !== undefined) {
    requireRelativeFilename(params.filename);
    options.filename = params.filename;
  }
  const id = await surfaceRejection(() => chrome.downloads.download(options));
  return { id };
};

const handlePause: RpcHandler<"downloads.pause"> = async (params) => {
  await requireDownload(params.id);
  await surfaceRejection(() => chrome.downloads.pause(params.id));
  return { id: params.id };
};

const handleResume: RpcHandler<"downloads.resume"> = async (params) => {
  await requireDownload(params.id);
  await surfaceRejection(() => chrome.downloads.resume(params.id));
  return { id: params.id };
};

const handleCancel: RpcHandler<"downloads.cancel"> = async (params) => {
  await requireDownload(params.id);
  await surfaceRejection(() => chrome.downloads.cancel(params.id));
  return { id: params.id };
};

const handleShow: RpcHandler<"downloads.show"> = async (params) => {
  await requireDownload(params.id);
  chrome.downloads.show(params.id);
  return { id: params.id };
};

const handleErase: RpcHandler<"downloads.erase"> = async (params) => {
  const ids = [...new Set(params.ids)];
  for (const id of ids) {
    await requireDownload(id);
  }
  for (const id of ids) {
    await chrome.downloads.erase({ id });
  }
  return { ids };
};

const handleDeleteFile: RpcHandler<"downloads.deleteFile"> = async (params) => {
  const download = await requireDownload(params.id);
  if (download.state !== "complete") {
    throw new HandlerError("INVALID_REQUEST", `download ${params.id} is ${download.state}, not complete`);
  }
  await surfaceRejection(() => chrome.downloads.removeFile(params.id));
  return { id: params.id };
};

export function registerDownloadsHandlers(registry: HandlerRegistry): void {
  registry.register("downloads.list", needs("downloads", handleList));
  registry.register("downloads.start", needs("downloads", handleStart));
  registry.register("downloads.pause", needs("downloads", handlePause));
  registry.register("downloads.resume", needs("downloads", handleResume));
  registry.register("downloads.cancel", needs("downloads", handleCancel));
  registry.register("downloads.erase", needs("downloads", handleErase));
  registry.register("downloads.deleteFile", needs("downloads", handleDeleteFile));
  registry.register("downloads.show", needs("downloads", handleShow));
}
