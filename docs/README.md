# sctl 文档

入口页是仓库根的 [AGENTS.md](../AGENTS.md):工程原则与"动手之前先读什么"。这里是全部文档的索引。

## 归属表

每条事实只在它的归属文档里展开,其他地方一律交叉链接过来(理由见 [doc-maintenance.md](./doc-maintenance.md))。

| 文档 | 独占的事实 |
|---|---|
| [architecture.md](./architecture.md) | 进程模型、目录结构、各包职责、依赖方向 |
| [protocol.md](./protocol.md) | 扩展 ↔ daemon 的 WS 桥接协议(信封、握手、动作、限值) |
| [threat-model.md](./threat-model.md) | 安全边界、攻击面与取舍 |
| [development.md](./development.md) | 构建与测试命令、静态检查、环境变量、版本门槛、分支/CI/发布 |
| [verification.md](./verification.md) | 怎么确认一个改动"真的能用":证据、一次性脚本、复现 |
| [doc-maintenance.md](./doc-maintenance.md) | 文档的真实性纪律与落地前校验块 |

## 单一事实源

- 协议常量的唯一权威是 [`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json);
  它是[扩展主仓库](https://github.com/scriptscat/scriptcat)那份的镜像,CI 的 `protocol-drift` job 逐字节比对两者。
