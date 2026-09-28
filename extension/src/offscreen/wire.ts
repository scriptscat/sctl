// daemon 发来的帧是不可信输入：先校验 JSON-RPC 2.0 形状（docs/protocol.md §1），再交给连接状态机。
export interface JsonRpcMessage {
  jsonrpc: "2.0";
  id?: string;
  method?: string;
  params?: Record<string, unknown>;
  result?: Record<string, unknown>;
  error?: { code: number; message: string; data?: { code?: string } };
}

export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function assertOnlyFields(value: Record<string, unknown>, allowed: readonly string[]): void {
  const unexpected = Object.keys(value).find((field) => !allowed.includes(field));
  if (unexpected) {
    throw new Error(`unexpected JSON-RPC field ${unexpected}`);
  }
}

function isNonEmptyString(value: unknown): value is string {
  return typeof value === "string" && value.length > 0;
}

export function decodeFrame(frame: string): JsonRpcMessage {
  const value: unknown = JSON.parse(frame);
  if (!isRecord(value) || value.jsonrpc !== "2.0") {
    throw new Error("not a JSON-RPC 2.0 message");
  }
  if (value.method !== undefined) {
    if (!isNonEmptyString(value.method) || (value.id !== undefined && !isNonEmptyString(value.id))) {
      throw new Error("invalid JSON-RPC request");
    }
    if (value.params !== undefined && !isRecord(value.params)) {
      throw new Error("invalid JSON-RPC params");
    }
    assertOnlyFields(value, ["jsonrpc", "id", "method", "params"]);
    return value as unknown as JsonRpcMessage;
  }
  const hasResult = value.result !== undefined;
  const hasError = value.error !== undefined;
  if (!isNonEmptyString(value.id) || hasResult === hasError) {
    throw new Error("invalid JSON-RPC response");
  }
  if (hasResult && !isRecord(value.result)) {
    throw new Error("invalid JSON-RPC result");
  }
  if (hasError) {
    const error = value.error;
    if (!isRecord(error) || !Number.isInteger(error.code) || typeof error.message !== "string") {
      throw new Error("invalid JSON-RPC error");
    }
    if (
      error.data !== undefined &&
      !(isRecord(error.data) && (error.data.code === undefined || typeof error.data.code === "string"))
    ) {
      throw new Error("invalid JSON-RPC error data");
    }
  }
  assertOnlyFields(value, ["jsonrpc", "id", "result", "error"]);
  return value as unknown as JsonRpcMessage;
}
