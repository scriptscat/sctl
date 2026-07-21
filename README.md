# sctl

ScriptCat 的本地控制工具:桥接 daemon、MCP server 与脚本管理命令,单二进制跨平台分发。基于 [cago](https://github.com/cago-frame/cago) 框架 + [cobra](https://github.com/spf13/cobra)。

> ⚠️ 开发中(WIP)。协议见 [`PROTOCOL.md`](./PROTOCOL.md),安全边界见 [`THREAT-MODEL.md`](./THREAT-MODEL.md);`protocol.json` 为常量镜像,权威副本在 [scriptcat 主仓库](https://github.com/scriptscat/scriptcat)。

```text
MCP 客户端(Claude/Codex…)─ stdio ─→ sctl mcp ─┐(本机内部连接:loopback 控制 API)
CLI 动词(sctl scripts list / install …)───────┤
                                               ▼
                          sctl serve(daemon,WS 仅监听 127.0.0.1:8643)
                                               ▲ WebSocket(扩展主动连接 + 双向 HMAC 握手)
                          ScriptCat 浏览器扩展(审批与授权的权威端)
```

`sctl mcp` / CLI 动词与常驻 `sctl serve` 是**独立进程**,经 daemon listener 上的 `/control/*` HTTP/JSON 控制 API 通信(与扩展 WS 面同端口、独立路径,凭 daemon 写下的 0600 控制令牌鉴权)。发现 daemon 未运行时自动以 detached 方式拉起;多个前端冷启动的绑定竞态按「绑定失败即连接既有实例」处理。

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

## 结构(cago 布局)

```text
cmd/sctl/main.go        # cobra 入口(解包 ExitError → os.Exit)
internal/cli/           # 子命令定义;serve 引导 cago 应用,其余为控制 API 客户端
internal/bridge/        # 桥接 WS server(cago Component)+ 本机控制 API 处理器(control.go)
internal/control/       # 前端 → daemon 控制 API 客户端、DTO、控制令牌、detached 自动拉起
internal/mcpserver/     # go-sdk stdio MCP server:6 工具、scope 过滤、等待期 progress
internal/mcpidentity/   # sctl mcp 实例的已配对身份缓存(0600)
internal/auth/          # 双向 HMAC 握手、配对码派生、长期密钥 / 客户端 token 存储
internal/protocol/      # 内嵌 protocol.json 解析
internal/fsutil/        # 原子写文件
internal/ratelimit/     # 按 key 滑动窗口限流
configs/config.yaml     # cago 配置(bridge.address 等;缺文件时退回内置默认)
embed.go                # go:embed protocol.json
```

## 开发

```bash
go build ./...
go vet ./...
go test ./... -race
go build -o sctl ./cmd/sctl && ./sctl version
```

`SCTL_DATA_DIR` 覆盖数据目录(密钥/令牌/客户端存储/日志);`SCTL_BRIDGE_ADDR` 同时覆盖 daemon 绑定地址与前端连接地址(自定义端口 / 多实例、隔离测试用)。

### 本机冒烟(不依赖真实浏览器扩展)

daemon 侧的全链路(配对 / 握手 / list / 源码披露 / 安装审批 / 断开作废 / 撤销)由单测覆盖并可 `-race` 跑;要手动驱动 daemon,可在隔离数据目录与端口上起 serve、再用 CLI 动词打:

```bash
export SCTL_DATA_DIR=$(mktemp -d) SCTL_BRIDGE_ADDR=127.0.0.1:18643
./sctl serve &                 # 起 daemon(写下 control.token)
./sctl status                  # 扩展未连接时如实报告
./sctl scripts list            # 无扩展连接 → 「扩展未连接」错误(退出码 3)
```

真实浏览器扩展的端到端冒烟(配对→list→源码披露→安装审批→直接允许→撤销→kill switch)见主仓库 `docs/verification.md`,需扩展侧 PR 就绪并构建后进行。

> **版本门槛**:扩展会校验 `daemonVersion >= versions.minDaemonVersion`(当前 `0.1.0`)。默认 `go build` 的版本是 `0.0.0-dev`,**低于门槛会被扩展判为「版本过旧」并断开**。冒烟/联调请用注入版本的构建(或 release 二进制):
>
> ```bash
> go build -ldflags "-X github.com/scriptscat/sctl/internal/cli.Version=0.1.0" -o sctl ./cmd/sctl
> ```
>
> 该版本经 `hello.daemonVersion` 下发给扩展。

## License

计划与主仓库一致(GPL-3.0),发布前确认。
