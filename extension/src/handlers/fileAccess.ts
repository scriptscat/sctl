import { HandlerError } from "@/background/registry";
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
