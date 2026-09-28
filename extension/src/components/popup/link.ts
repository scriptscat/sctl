import type { ConnectionStatus } from "@/shared/state";

// 连线的样式表示状态（docs/specs 扩展弹窗）：
// 绿色实线=已连接，琥珀色流动虚线=重连中，红色长虚线=认证被拒，灰色稀疏点线=未配对（含配对中/配对失败/连不上）。
export type Link = "idle" | "ok" | "warn" | "bad";

export function linkFor(status: ConnectionStatus): Link {
  switch (status) {
    case "reconnecting":
      return "warn";
    case "rejected":
      return "bad";
    case "connected":
      return "ok";
    default:
      return "idle";
  }
}
