import { HandlerError } from "@/background/registry";

// 列表类方法的条数上限（spec 设计决策 9）：默认 100，最多 1000。
export const DEFAULT_LIST_LIMIT = 100;
export const MAX_LIST_LIMIT = 1000;

// listLimit 给出本次最多返回的条数。生成的校验器不检查 maximum，协议声明的上限只能在这里兑现；
// 要在查询浏览器之前调用，越界的请求不产生任何读取。
export function listLimit(limit: number | undefined): number {
  const max = limit ?? DEFAULT_LIST_LIMIT;
  if (max > MAX_LIST_LIMIT) {
    throw new HandlerError("INVALID_REQUEST", `limit must be at most ${MAX_LIST_LIMIT}`);
  }
  return max;
}

// takePage 取已排好序的前 limit 项，并报告是否还有未返回的条目。
export function takePage<T>(items: T[], limit: number): { items: T[]; hasMore: boolean } {
  return { items: items.slice(0, limit), hasMore: items.length > limit };
}
