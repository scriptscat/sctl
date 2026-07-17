# sctl

ScriptCat 的本地控制工具:桥接 daemon、MCP server 与脚本管理命令,单二进制跨平台分发。基于 [cago](https://github.com/cago-frame/cago) 框架 + [cobra](https://github.com/spf13/cobra)。

> ⚠️ 开发中(WIP)。协议见 [`PROTOCOL.md`](./PROTOCOL.md);`protocol.json` 为常量镜像,权威副本在 [scriptcat 主仓库](https://github.com/scriptscat/scriptcat)。

```text
MCP 客户端(Claude/Codex…)─ stdio ─→ sctl mcp ─┐
CLI 动词(sctl scripts list / install …)───────┤
                                               ▼
                          sctl serve(daemon,WS 仅监听 127.0.0.1:8643)
                                               ▲ WebSocket(扩展主动连接)
                          ScriptCat 浏览器扩展(审批与授权的权威端)
```

## 子命令

| 命令 | 说明 | 状态 |
|---|---|---|
| `sctl serve` | 运行桥接 daemon(cago 应用) | 骨架(空跑,未监听) |
| `sctl mcp` | stdio MCP server(自动拉起 serve) | 未实现 |
| `sctl pair` | 生成一次性配对码 | 未实现 |
| `sctl status` | 连接状态 | 未实现 |
| `sctl scripts list / info / source` | 读脚本 | 未实现 |
| `sctl install / enable / disable / rm` | 写操作(阻塞等待浏览器人工确认) | 未实现 |
| `sctl version` | 版本与协议信息 | ✅ |

## 结构(cago 布局)

```text
cmd/sctl/main.go        # cobra 入口
internal/cli/           # 子命令定义;serve 引导 cago 应用
internal/bridge/        # 桥接 WS server(cago Component)
internal/protocol/      # 内嵌 protocol.json 解析
configs/config.yaml     # cago 配置(bridge.address 等)
embed.go                # go:embed protocol.json
```

## 开发

```bash
go test ./...
go build -o sctl ./cmd/sctl
./sctl version
```

License:计划与主仓库一致(GPL-3.0),发布前确认。
