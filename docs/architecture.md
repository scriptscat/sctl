# 架构

## 进程模型

```text
MCP 客户端(Claude/Codex…)─ stdio ─→ sctl mcp ─┐(本机内部连接:loopback 控制 API)
CLI 动词(sctl scripts list / install …)───────┤
                                               ▼
                          sctl serve(daemon,WS 仅监听 127.0.0.1:8643)
                                               ▲ WebSocket(扩展主动连接 + 双向 HMAC 握手)
                          ScriptCat 浏览器扩展(审批与授权的权威端)
```

`sctl mcp` / CLI 动词与常驻 `sctl serve` 是**独立进程**,经 daemon listener 上的 `/control/*`
HTTP/JSON 控制 API 通信(与扩展 WS 面同端口、独立路径,凭 daemon 写下的 0600 控制令牌鉴权)。
发现 daemon 未运行时自动以 detached 方式拉起;多个前端冷启动的绑定竞态按「绑定失败即连接既有实例」处理。

权威始终在扩展侧:daemon 不自行批准任何写操作,只转发请求并阻塞等待浏览器里的人工决策。
详见 [threat-model.md](./threat-model.md)。

## 目录结构

`internal/` 按**进程角色**分组:`daemon/` 是守卫侧(`sctl serve`),`client/` 是请求侧
(`sctl mcp` 与 CLI 动词),顶层扁平的包按定义即两侧共享。分层约定借 [cago](https://github.com/cago-frame/cago)
(`configs/`、`internal/pkg/`、store 承担 repository 角色)。

```text
cmd/sctl/main.go            # cobra 入口(解包 ExitError → os.Exit)
configs/config.yaml         # cago 配置(bridge.address 等;缺文件时退回内置默认)

internal/cli/               # 子命令定义;横跨两侧,故留在顶层
  cli.go                    #   root 命令、全局标志、JSON 输出helper
  serve.go                  #   引导 cago 应用并挂上 daemon Component
  mcp.go                    #   sctl mcp / sctl mcp pair
  pair.go status.go version.go
  scripts.go write.go       #   读动词 / 写动词
  dispatch.go               #   动作转发与 bridge 错误 → 退出码映射

internal/daemon/            # ── sctl serve 侧 ──
  component.go              #   cago Component:组装 listener + bridge + controlapi
  bridge/                   #   WS 服务核心
    server.go               #     Server 结构、Serve、握手接入、连接注册表
    conn.go                 #     单连接:握手、读循环、发送
    call.go                 #     action 转发、挂起调用表、bridge.cancel
    pairing.go              #     扩展配对窗口与 MCP 客户端配对
    clients.go              #     客户端撤销与 client.sync 广播
    envelope.go             #     信封、payload 结构、错误码
  controlapi/               #   /control/* 处理器(controller 角色),依赖窄 Bridge 接口
  auth/                     #   双向 HMAC 握手、配对码派生(HKDF)、密钥下发(AES-GCM)
  store/                    #   长期密钥 / 客户端令牌的 0600 落盘(repository 角色)
  ratelimit/                #   按 key 滑动窗口限流

internal/client/            # ── sctl mcp / CLI 动词侧 ──
  control/                  #   控制 API 客户端、共享 DTO、控制令牌、detached 自动拉起
  mcpserver/                #   go-sdk stdio MCP server:6 工具、scope 过滤、等待期 progress
  identity/                 #   sctl mcp 实例的已配对身份缓存(0600)

internal/pkg/               # ── 两侧共享 ──
  protocol/                 #   protocol.json 本体 + 内嵌解析
  audit/                    #   守卫侧安全事件(Event 类型跨控制 API,故在共享层)
  paths/                    #   数据目录与派生路径
  logging/                  #   统一 zap 日志(恒走 stderr)
  fsutil/                   #   原子写文件
```

## 依赖方向

`cli` → `daemon`(仅 `serve`)与 `client`;`daemon/controlapi` → `daemon/bridge`,反向不成立
—— 控制 API 只通过 `controlapi.Bridge` 这个窄接口看到守卫,`bridge` 对 HTTP 路径一无所知,
路由在 `internal/daemon/component.go` 里组装。共享 DTO 放在 `client/control`,由 `controlapi`
单向引用。

另外两条跨包约定:敏感文件只经 `internal/pkg/fsutil` 落盘,stdout 只属于 `internal/cli`
—— 后者是因为 stdout 被 `sctl mcp` 的 JSON-RPC 独占。这些约定目前由评审守住。
