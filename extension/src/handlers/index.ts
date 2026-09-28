import type { HandlerRegistry } from "@/background/registry";

// 标签页与窗口方法的处理函数在这里注册；注册了哪些方法，扩展就向 daemon 声明哪些能力。
export function registerHandlers(_registry: HandlerRegistry): void {}
