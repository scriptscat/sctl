# sctl

[English](../README.md) | [简体中文](./README_zh-CN.md)

sctl 用于将 AI 客户端和命令行工作流连接到
[ScriptCat](https://github.com/scriptscat/scriptcat) 浏览器扩展,以及它自己的 **sctl Browser**
浏览器扩展。单个跨平台二进制同时提供本地桥接 daemon、stdio MCP Server、脚本管理命令和浏览器控制命令。

```text
AI 客户端 ── stdio MCP ──▶ sctl mcp ── 本地控制 API ──▶ sctl serve ── WebSocket ──▶ ScriptCat
CLI ─────────────────────────────────────────────────────────▲
```

最终控制权仍在 ScriptCat：源码披露和所有写操作均受扩展中的策略与确认界面控制。

## 功能

- 将 ScriptCat 操作与浏览器标签页/窗口控制暴露为可发现、具有 Schema 类型的 MCP 工具，另有 10 个按领域合并的浏览器工具
  （`bookmarks`、`reading_list`、`tabs_manage`、`tab_groups`、`history`、`recently_closed`、`downloads`、`cookies`、
  `browsing_data`、`extensions`），用 `action` 参数选择操作。
- 列出脚本并读取元数据或源码，支持按行读取和源码搜索。
- 通过浏览器确认请求安装、基于内容锚点的编辑、启用/禁用和删除。
- 在一个或多个已配对的 sctl Browser 实例上列出、打开、关闭、激活、移动、固定、静音、刷新、复制标签页,以及列出、打开、关闭、聚焦窗口和修改窗口状态。
- 在已配对的 sctl Browser 实例上列出、创建、编辑和解散标签组。
- 在已配对的 sctl Browser 实例上列出、添加、标记已读或未读、移除阅读列表条目。
- 在已配对的 sctl Browser 实例上列出、搜索、添加、移动和编辑书签与书签文件夹，并在该浏览器里批准后删除它们。
- 搜索和清除历史记录，恢复最近关闭的标签页和窗口，管理下载，读取和修改 Cookie，清除浏览数据，以及列出、启用、禁用扩展或在批准后卸载扩展。
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

在 ScriptCat 中启用**外部接入**，然后输入 `connect` 打印的一次性配对码。

若还要配对 **sctl Browser** 扩展(标签页/窗口控制),从 [GitHub Releases](https://github.com/scriptscat/sctl/releases)
下载 `sctl-browser-extension-<version>.zip` 并解压,在浏览器的扩展管理页把解压后的目录作为"已解压的扩展程序"加载,
打开其弹窗并输入 `sctl connect` 打印的一次性配对码;一个码只能配对一个扩展,若已被 ScriptCat 用掉,就再运行一次
`connect`。完整步骤(含浏览器的"开发者模式"开关)见[`mcp.md`](./mcp.md#4-enroll-scriptcat-and-sctl-browser)(英文)。

随后将 AI 客户端配置为启动：

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
| `sctl connect` | 打开一次性接入窗口,供 ScriptCat 或 sctl Browser 配对。 |
| `sctl mcp [--name <label>]` | 通过 stdio MCP 提供 ScriptCat 与 sctl Browser 工具。 |
| `sctl status` | 查看 daemon 和扩展的连接状态。 |
| `sctl get [<uuid>]` | 列出脚本或读取单个脚本。 |
| `sctl grep <uuid> <query>` | 搜索单个脚本的源码。 |
| `sctl install <url\|file>` | 请求安装脚本。 |
| `sctl edit <uuid>` | 请求基于内容锚点编辑源码。 |
| `sctl enable <uuid>` / `sctl disable <uuid>` | 请求修改启用状态。 |
| `sctl delete <uuid>` | 请求删除脚本。 |
| `sctl browsers [list]` / `sctl browsers forget <name\|id>` | 列出已配对的 sctl Browser 实例，或忘记其中一个。 |
| `sctl tabs list\|open\|close\|activate` | 在已配对的 sctl Browser 实例上列出、打开、关闭或激活标签页。 |
| `sctl tabs move\|pin\|unpin\|mute\|unmute\|reload\|duplicate` | 移动、固定、静音、刷新或复制标签页（多个 ID 全有或全无）。 |
| `sctl windows list` | 列出已配对的 sctl Browser 实例上的窗口。 |
| `sctl windows open\|close\|focus\|state` | 打开、关闭、聚焦窗口或修改窗口状态。 |
| `sctl groups list\|create\|add\|edit\|ungroup` | 列出、创建、加入标签页、编辑（标题、颜色、折叠）或解散标签组。 |
| `sctl reading-list list\|add\|mark-read\|rm` | 在已配对的 sctl Browser 实例上列出、添加、标记已读或未读、移除阅读列表条目。 |
| `sctl history search\|visits\|rm\|clear` | 搜索历史、列出某个 URL 的访问记录、按 URL 删除历史，或按时间范围清除历史。 |
| `sctl browsing-data clear` | 按类型、时间和来源清除缓存、Cookie、存储等浏览数据。 |
| `sctl recent list\|restore` | 列出最近关闭的标签页和窗口，或恢复其中一项（不给会话 ID 时恢复最近关闭的一项）。 |
| `sctl downloads list\|start\|pause\|resume\|cancel\|erase\|delete-file\|show` | 在已配对的 sctl Browser 实例上列出、开始、暂停、继续、取消下载，删除下载记录，或从磁盘删除已下载的文件。 |
| `sctl cookies list\|get\|set\|rm\|clear` | 在已配对的 sctl Browser 实例上列出（含分区 Cookie）、读取、设置或删除 Cookie；Cookie 值原样返回，不打码。 |
| `sctl bookmarks list\|search\|add\|mkdir\|move\|edit\|rm` | 在已配对的 sctl Browser 实例上列出、搜索、添加、移动、编辑或删除书签和书签文件夹。 |
| `sctl extensions list\|enable\|disable\|uninstall` | 在已配对的 sctl Browser 实例上列出、启用、禁用或卸载扩展和应用；禁用 ScriptCat 会断开它与 daemon 的连接。 |

运行 `sctl --help` 或 `sctl <command> --help` 查看用法和参数。写操作会阻塞，直到用户在
ScriptCat 中批准、拒绝或关闭确认流程；浏览器控制命令按设计没有审批步骤、立即执行(参见
[`threat-model.md`](./threat-model.md))。当多个实例同时在线时，`tabs`、`windows`、`groups`、`reading-list`、`bookmarks`、`history`、`browsing-data`、`recent`、`downloads`、`cookies` 与 `extensions` 可用
`--browser <name|id>`（或环境变量 `SCTL_BROWSER`）指定目标实例。破坏性的浏览器操作需要显式确认：
`reading-list rm`、`history rm`、`history clear`、`browsing-data clear`，`downloads cancel`、`erase`、`delete-file`，`cookies rm`、`clear`，以及 `extensions disable` 必须加 `--yes`（MCP 传 `confirm: true`），否则什么都不执行，退出码为 3。
`bookmarks rm <id>...` 则需要人工审批：浏览器打开审批窗口，命令一直等待；书签删除后退出码为 0，被拒绝或关闭窗口为 1，
5 分钟内无人处理或按 Ctrl-C 为 2，批准前书签已发生变化为 3。
`extensions uninstall <id>` 同样需要在审批窗口里批准，点「卸载」后 Chrome 还会弹出自己的确认框：扩展卸载后退出码为 0，
被拒绝、关闭窗口或在 Chrome 确认框里取消为 1，5 分钟内无人处理或按 Ctrl-C 为 2，ID 不存在、目标是 sctl Browser 自己或企业策略安装的扩展为 3。
带 `--limit` 的命令默认最多返回 100 条，可用 `--limit` 提到 1000 条（`recent list` 为 25 条，即 Chrome 保留的上限）；还有更多条目时在 stderr 提示。`--since`、`--until` 接受 RFC 3339 时间，或 `7d`、`12h`、`30m` 这样的「多久以前」。

## 许可证

GPL-3.0，与 ScriptCat 相同。参见 [LICENSE](../LICENSE)。
