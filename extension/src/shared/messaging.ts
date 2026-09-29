// chrome.runtime 消息的请求/应答约定：应答包成信封，处理方的失败会在调用方变成被拒绝的 Promise，
// 而不是一个看似成功的 undefined。
type Reply = { ok: true; value: unknown } | { ok: false; error: string };

type MessageListener = (message: unknown, sender: unknown, sendResponse: (reply: unknown) => void) => boolean | void;

export interface RuntimeLike {
  sendMessage(message: unknown): Promise<unknown>;
  onMessage: {
    addListener(listener: MessageListener): void;
    removeListener(listener: MessageListener): void;
  };
}

export type Target = "background" | "offscreen" | "popup";

function isTargeted(message: unknown, target: Target): message is { target: Target; type: string } {
  return typeof message === "object" && message !== null && (message as { target?: unknown }).target === target;
}

// 只处理发给 target 的消息；同一条消息会送达每个扩展页面，其余页面必须不作应答。
export function listen<M extends { target: Target; type: string }>(
  runtime: RuntimeLike,
  target: M["target"],
  handler: (message: M) => Promise<unknown>,
): void {
  runtime.onMessage.addListener((message, _sender, sendResponse) => {
    if (!isTargeted(message, target)) {
      return false;
    }
    handler(message as M).then(
      (value) => sendResponse({ ok: true, value } satisfies Reply),
      (error: unknown) => {
        console.error(`failed to handle ${message.type}`, error);
        sendResponse({ ok: false, error: error instanceof Error ? error.message : String(error) } satisfies Reply);
      },
    );
    return true;
  });
}

// 状态广播不等待应答：弹窗通常没有打开，此时 Chrome 以"没有接收方"拒绝这条消息，这正是预期情况。
export function broadcast<M extends { target: Target; type: string }>(runtime: RuntimeLike, message: M): void {
  runtime.sendMessage(message).catch(() => undefined);
}

export async function request<R>(runtime: RuntimeLike, message: { target: Target; type: string }): Promise<R> {
  const reply = (await runtime.sendMessage(message)) as Reply | undefined;
  if (!reply) {
    throw new Error(`no ${message.target} context answered ${message.type}`);
  }
  if (!reply.ok) {
    throw new Error(reply.error);
  }
  return reply.value as R;
}
