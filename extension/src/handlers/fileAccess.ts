import { errorMessage, HandlerError } from "@/background/registry";
import type { ErrorCode } from "@/shared/messages";

// 从商店安装的扩展默认关着「允许访问文件网址」，这时 Chrome 拒绝 file:// 网址只说 "Cannot attach to this target."
// 之类的话，看不出原因。Chrome 拒绝涉及 file:// 网址的操作后调用它：确实是这个开关关着，就换成说清原因和打开位置的错误；
// 不是的话什么也不做，调用方照常报告 Chrome 的原话。
export async function explainFileAccessRefusal(url: unknown, code: ErrorCode): Promise<void> {
  if (typeof url !== "string" || !url.startsWith("file:") || (await chrome.extension.isAllowedFileSchemeAccess())) {
    return;
  }
  throw new HandlerError(
    code,
    `sctl Browser is not allowed to access file:// URLs: turn on "Allow access to file URLs" on its details page ` +
      `(chrome://extensions/?id=${chrome.runtime.id}), then retry`,
  );
}

// chrome.tabs.create / chrome.windows.create 校验过窗口之后仍拒绝，原因在调用方给的网址（javascript: 等 Chrome 不肯打开的
// 网址、没有文件访问权限时的 file://），所以带着原因作为 INVALID_REQUEST 返回，而不是落成看不出原因的 INTERNAL_ERROR。
export async function openRefused(urls: readonly string[], error: unknown): Promise<HandlerError> {
  await explainFileAccessRefusal(
    urls.find((url) => url.startsWith("file:")),
    "INVALID_REQUEST",
  );
  return new HandlerError("INVALID_REQUEST", errorMessage(error));
}
