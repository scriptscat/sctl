# sctl

ScriptCat 的本地控制工具:桥接 daemon、MCP server 与脚本管理命令,单二进制跨平台分发。基于 [cago](https://github.com/cago-frame/cago) 框架 + [cobra](https://github.com/spf13/cobra)。

> ⚠️ 开发中(WIP)。

```text
MCP 客户端(Claude/Codex…)─ stdio ─→ sctl mcp ─┐(本机内部连接:loopback 控制 API)
CLI 动词(sctl scripts list / install …)───────┤
                                               ▼
                          sctl serve(daemon,WS 仅监听 127.0.0.1:8643)
                                               ▲ WebSocket(扩展主动连接 + 双向 HMAC 握手)
                          ScriptCat 浏览器扩展(审批与授权的权威端)
```

`sctl mcp` / CLI 动词与常驻 `sctl serve` 是**独立进程**,经 daemon listener 上的 `/control/*` HTTP/JSON 控制 API 通信,未运行时自动拉起。详见 [`docs/architecture.md`](./docs/architecture.md)。

## 子命令

| 命令 | 说明 |
|---|---|
| `sctl serve` | 运行桥接 daemon(cago 应用;WS 仅监听 loopback,并挂载控制 API) |
| `sctl mcp [--name <label>]` | stdio MCP server;加载已配对身份、按 scope 过滤工具后提供服务(未运行时自动拉起 serve) |
| `sctl mcp pair [--name <label>]` | 交互式配对该 MCP 实例:打印核对码,等待扩展批准,缓存铸造出的身份 |
| `sctl pair` | 生成一次性配对码,与扩展建立互信 |
| `sctl status` | daemon 与扩展连接状态,附守卫侧安全事件摘要(不自动拉起 daemon) |
| `sctl scripts list / info <uuid> / source <uuid>` | 读脚本(`source` 输出到 stdout,可重定向) |
| `sctl install <url\|file>` | 请求安装脚本(URL 或本地文件;本地文件上送 staged code) |
| `sctl enable / disable / rm <uuid>` | 写操作,阻塞等待浏览器人工确认 |
| `sctl version` | 版本与协议信息 |

全局标志:`--json`(结构化输出,供脚本消费)、`--log-level`(日志始终走 stderr;`sctl mcp` 的 stdout 由 MCP 协议独占)。

### 写操作阻塞语义与退出码

写动词与 MCP 写工具**阻塞**至用户在浏览器确认页决策。请求方 **Ctrl-C** 会切断连接 → daemon 向扩展发 `bridge.cancel` 作废该操作。CLI 退出码:

| 码 | 含义 |
|---|---|
| 0 | 批准 / 成功 |
| 1 | 用户拒绝(`USER_REJECTED`) |
| 2 | 作废(超时 `OPERATION_EXPIRED` / Ctrl-C 取消 / 扩展断开) |
| 3 | 其他错误(校验失败、`NOT_FOUND`、`INSUFFICIENT_SCOPE`、连接失败…) |

## 文档

| 文档 | 内容 |
|---|---|
| [AGENTS.md](./AGENTS.md) | 工程原则、架构速查、动手之前先读什么 |
| [docs/README.md](./docs/README.md) | 全部文档索引与归属表 |
| [docs/architecture.md](./docs/architecture.md) | 进程模型、目录结构、各包职责、依赖方向 |
| [docs/protocol.md](./docs/protocol.md) | 扩展 ↔ daemon 的 WS 桥接协议 |
| [docs/threat-model.md](./docs/threat-model.md) | 安全边界与取舍 |
| [docs/development.md](./docs/development.md) | 构建、测试、静态检查、环境变量、版本门槛 |
| [docs/verification.md](./docs/verification.md) | 怎么确认改动真的能用 |

## License

GPL-3.0,与扩展主仓库一致。全文见 [LICENSE](./LICENSE)。
