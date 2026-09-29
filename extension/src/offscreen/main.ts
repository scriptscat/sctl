import type { ConnectionConfig, OffscreenCommand, OffscreenEvent, RpcReply, StateBroadcast } from "@/shared/messages";
import { broadcast, listen, request } from "@/shared/messaging";
import { Connection } from "./connection";

const toBackground = <R>(event: OffscreenEvent) => request<R>(chrome.runtime, event);

const connection = new Connection({
  createSocket(url, events) {
    const socket = new WebSocket(url);
    socket.onopen = () => events.open();
    // daemon 只发文本帧；其他类型交给解码器当作畸形帧处理。
    socket.onmessage = (event: MessageEvent) => events.message(typeof event.data === "string" ? event.data : "");
    socket.onclose = (event) => events.close(event.code);
    return socket;
  },
  timers: {
    setTimeout: (fn, ms) => window.setTimeout(fn, ms),
    clearTimeout: (id) => window.clearTimeout(id),
    now: () => Date.now(),
  },
  onState: (state) => broadcast(chrome.runtime, { target: "popup", type: "state", state } satisfies StateBroadcast),
  persistKey: (key, name) => toBackground({ target: "background", type: "paired", key, name }),
  persistName: (name) => toBackground({ target: "background", type: "renamed", name }),
  dispatch: (method, input, context) =>
    toBackground<RpcReply>({ target: "background", type: "rpc", method, input, context }),
  cancel: (requestId) => toBackground({ target: "background", type: "rpcCancel", requestId }),
  disconnected: (connection) => toBackground({ target: "background", type: "disconnected", connection }),
});

// offscreen 文档不能访问扩展存储，配置由 service worker 读出后交给它。
const started = toBackground<ConnectionConfig>({ target: "background", type: "offscreenReady" }).then((config) =>
  connection.start(config),
);
// 启动失败时之后的每条命令也会以同一错误失败，这里先记录一次原因。
started.catch((error: unknown) => console.error("failed to start the connection", error));

// 命令可能在配置到达之前就来了，等连接启动后再处理。
listen<OffscreenCommand>(chrome.runtime, "offscreen", async (command) => {
  await started;
  switch (command.type) {
    case "getState":
      return connection.getState();
    case "pair":
      return connection.pair(command.code);
    case "rename":
      return connection.rename(command.name);
    case "retryNow":
      return connection.retryNow();
    case "forget":
      return connection.forget();
    case "setAddress":
      return connection.setAddress(command.address);
    case "settle":
      return connection.settle(command.requestId, command.outcome);
  }
});
