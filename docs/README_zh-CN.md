# sctl

[English](../README.md) | [简体中文](./README_zh-CN.md)

sctl 用于将 AI 客户端和命令行工作流连接到
[ScriptCat](https://github.com/scriptscat/scriptcat) 浏览器扩展。单个跨平台二进制同时提供本地桥接
daemon、stdio MCP Server 和脚本管理命令。

```text
AI 客户端 ── stdio MCP ──▶ sctl mcp ── 本地控制 API ──▶ sctl serve ── WebSocket ──▶ ScriptCat
CLI ─────────────────────────────────────────────────────────▲
```

最终控制权仍在 ScriptCat：源码披露和所有写操作均受扩展中的策略与确认界面控制。

## 功能

- 将 ScriptCat 操作暴露为可发现、具有 Schema 类型的 MCP 工具。
- 列出脚本并读取元数据或源码，支持按行读取和源码搜索。
- 通过浏览器确认请求安装、基于内容锚点的编辑、启用/禁用和删除。
- 在仅监听回环地址的 WebSocket 上使用 JSON-RPC 2.0 和双向认证。
- 单二进制交付，不依赖浏览器自动化或 Native Messaging Host。

## 快速开始

使用一条命令安装最新版本 — macOS / Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
```

或 Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
```

安装脚本会下载与你平台对应的连字符命名发布包 `sctl-<version>-<os>-<arch>.<ext>`，用 `checksums.txt`
校验其 sha256，并安装 `sctl` 到 `~/.local/bin`（macOS/Linux）或 `%LOCALAPPDATA%\sctl\bin`（Windows）。
`SCTL_VERSION` 用于固定版本，`SCTL_INSTALL_DIR` 用于覆盖安装目录。若安装目录不在 `PATH` 中，安装脚本会
打印将 `PATH` 加入该目录的确切提示 — 它不会替你修改 shell 配置或用户 PATH。

也可以从 [GitHub Releases](https://github.com/scriptscat/sctl/releases) 手动下载
`sctl-<version>-<os>-<arch>.<ext>` 归档并放入 `PATH`，或从源码构建；普通源码构建以 `0.0.0-dev` 标识自身。

选择一个绝对路径作为数据目录，并为所有 sctl 进程设置环境变量：

```bash
export SCTL_DATA_DIR=/absolute/path/to/sctl-data

# 终端 1：保持 daemon 运行
sctl serve

# 终端 2：首次接入，然后验证连接
sctl connect
sctl status
```

在 ScriptCat 中启用**外部接入**，然后输入 `connect` 打印的一次性配对码。随后将 AI
客户端配置为启动：

```text
/absolute/path/to/sctl mcp --name my-ai-client
```

`sctl mcp` 不会启动 daemon；它与 `sctl serve` 必须解析到相同的数据目录；覆盖默认监听地址时，
还必须使用相同的 `--listen-address <host:port>`。客户端 JSON、验证方式、
安全说明和故障排查参见[完整 MCP 安装指南](./mcp.md)。

## 命令

| 命令 | 用途 |
|---|---|
| `sctl serve` | 运行本地桥接 daemon。 |
| `sctl connect` | 打开一次性 ScriptCat 接入窗口。 |
| `sctl mcp [--name <label>]` | 通过 stdio MCP 提供 ScriptCat 工具。 |
| `sctl status` | 查看 daemon 和扩展的连接状态。 |
| `sctl get [<uuid>]` | 列出脚本或读取单个脚本。 |
| `sctl grep <uuid> <query>` | 搜索单个脚本的源码。 |
| `sctl install <url\|file>` | 请求安装脚本。 |
| `sctl edit <uuid>` | 请求基于内容锚点编辑源码。 |
| `sctl enable <uuid>` / `sctl disable <uuid>` | 请求修改启用状态。 |
| `sctl delete <uuid>` | 请求删除脚本。 |

运行 `sctl --help` 或 `sctl <command> --help` 查看用法和参数。写操作会阻塞，直到用户在
ScriptCat 中批准、拒绝或关闭确认流程。

## 许可证

GPL-3.0，与 ScriptCat 相同。参见 [LICENSE](../LICENSE)。
