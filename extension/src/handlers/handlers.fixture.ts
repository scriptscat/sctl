// 处理函数测试的替身：不涉及调试器中转的测试用它注册全部处理函数，中转既不发通知也不持久化。
import type { HandlerRegistry } from "@/background/registry";
import { DebuggerRelay } from "./debugger";
import { registerHandlers } from "./index";

export function registerAllHandlers(registry: HandlerRegistry): void {
  registerHandlers(
    registry,
    new DebuggerRelay(() => undefined, { get: () => Promise.resolve({}), set: () => Promise.resolve() }),
  );
}
