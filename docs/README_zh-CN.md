# sctl

[English](../README.md) | [简体中文](./README_zh-CN.md)

sctl 让命令行、AI 客户端和脚本控制你正在使用的浏览器。单个跨平台二进制运行一个本地桥接 daemon，一边连接
[ScriptCat](https://github.com/scriptscat/scriptcat) 管理用户脚本，一边连接它自己的 **sctl Browser**
扩展来控制标签页、浏览器数据、页面自动化与调试。

```text
sctl <命令> ─────────────────────┐
AI 客户端 ── MCP ── sctl mcp ────┴─ 控制 API ─▶ sctl serve ─┬─ WebSocket ─▶ ScriptCat      用户脚本
Playwright / Puppeteer ───── CDP 端点 ────────▶            └─ WebSocket ─▶ sctl Browser   标签页、数据、页面、调试
```

谁说了算：脚本的控制权仍在 ScriptCat，读取源码和所有写操作都要在浏览器里批准。浏览器控制、页面自动化、调试和原始
CDP 按设计立即执行；只有删除书签和卸载扩展需要批准，其他破坏性命令需要 `--yes`。把 sctl 暴露给任何不可信的对象之前，
请先读[威胁模型](./threat-model.md)（英文）。

## 功能

- **ScriptCat 用户脚本**：列出，读取元数据与源码（按行读取、搜索），安装，基于内容锚点编辑，启用、禁用、删除。
- **标签页与窗口**：在一个或多个已配对的浏览器上列出、打开、关闭、激活、移动、固定、静音、刷新、复制标签页，
  以及管理标签组。
- **浏览器数据**：书签、阅读列表、历史记录、最近关闭、下载、Cookie、浏览数据和扩展。
- **页面自动化**：带元素引用的无障碍快照，点击、悬停、填写、输入、选择、上传、滚动、导航、等待、截图、执行
  JavaScript；在后台标签页完成，不切换你正在看的标签页。
- **调试**：控制台消息、未捕获的异常、浏览器消息，以及带请求头和请求体、响应头和响应体的网络请求。
- **原始 CDP**：发送单条 Chrome DevTools Protocol 命令，或开一个端点供 Playwright `connectOverCDP` 与 Puppeteer
  `connect` 连接。
- **单二进制**：在双向认证的 WebSocket 上使用 JSON-RPC 2.0，默认只监听回环地址；不需要 Native Messaging Host。

## 安装

macOS 和 Linux：

```bash
curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
```

Windows PowerShell：

```powershell
irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
```

安装脚本校验发布归档的 sha256，并把 `sctl` 安装到 `~/.local/bin`（macOS/Linux）或
`%LOCALAPPDATA%\sctl\bin`（Windows）。固定版本、手动下载和从源码构建见
[`mcp.md`](./mcp.md#1-install-sctl)（英文）。

## 快速开始

1. **启动 daemon** 并保持运行：

   ```bash
   sctl serve
   ```

2. **配对扩展。** `sctl connect` 打印一次性配对码；一个码只能配对一个扩展，每个扩展各运行一次。

   - **ScriptCat：** 在选项页启用 **External Access** 并输入配对码。
   - **sctl Browser：** 从 [GitHub Releases](https://github.com/scriptscat/sctl/releases) 下载
     `sctl-browser-extension-<version>.zip` 并解压，把解压后的目录作为“已解压的扩展程序”加载，在其弹窗里输入配对码。
     要求 **Chrome 125 或更高版本**（或同版本的 Chromium 内核浏览器）。

3. **检查连接：**

   ```bash
   sctl status      # daemon 与 ScriptCat
   sctl browsers    # 已配对的 sctl Browser 实例
   ```

每个 sctl 进程必须使用相同的数据目录和监听地址；它们都以同一用户运行时，默认值即可。完整步骤（包括
`SCTL_DATA_DIR`、`--listen-address`，以及驱动页面时 Chrome 显示的调试提示条）见 [`mcp.md`](./mcp.md)（英文）。

## 使用方式

**命令行。** 每项能力都是一条命令；脚本里加 `-o json`。

```bash
sctl tabs list
sctl page snapshot --tab 123
sctl debug console --tab 123 --level error
sctl get                      # ScriptCat 脚本
```

行为、退出码与确认规则见 [`cli.md`](./cli.md)（英文）。

**AI 客户端（MCP）。** 把客户端配置为启动 `/absolute/path/to/sctl mcp --name my-ai-client`。`sctl mcp` 不会启动
daemon。客户端配置、工具参考和故障排查见 [`mcp.md`](./mcp.md)（英文）。

**Agent skill。** 想让 AI agent 直接使用命令行而不是 MCP，可以安装 [`skills/sctl/`](../skills/sctl/SKILL.md) 里的
skill。在 Claude Code 中，于本仓库的克隆目录里把它链接过去：

```sh
ln -s "$PWD/skills/sctl" ~/.claude/skills/sctl
```

**Playwright 与 Puppeteer。** `sctl cdp endpoint` 打印的地址可供现有脚本直接连接，并保留浏览器的登录状态；见
[`cli.md`](./cli.md#endpoint-for-playwright-and-puppeteer)（英文）。

## 命令

| 命令组 | 用途 |
|---|---|
| `sctl serve` / `connect` / `status` / `mcp` | 运行 daemon、配对扩展、查看连接状态、通过 stdio 提供 MCP。 |
| `sctl get` / `grep` / `install` / `edit` / `enable` / `disable` / `delete` | 管理 ScriptCat 用户脚本。 |
| `sctl browsers` | 列出已配对的 sctl Browser 实例，或忘记其中一个。 |
| `sctl tabs` / `windows` / `groups` | 标签页、窗口和标签组。 |
| `sctl bookmarks` / `reading-list` | 书签和阅读列表。 |
| `sctl history` / `recent` / `downloads` | 历史记录、最近关闭的标签页和窗口、下载。 |
| `sctl cookies` / `browsing-data` / `extensions` | Cookie、浏览数据、已安装的扩展。 |
| `sctl page` | 页面自动化：快照、点击、填写、导航、等待、截图、执行脚本。 |
| `sctl debug` | 记录并读取标签页的控制台和网络请求。 |
| `sctl cdp` | 发送原始 CDP 命令，或为 Playwright 与 Puppeteer 开端点。 |

`sctl <命令> --help` 列出全部子命令和参数；完整参考见 [`cli.md`](./cli.md)（英文）。

## 文档

- [`cli.md`](./cli.md)：命令行参考（英文）。
- [`mcp.md`](./mcp.md)：安装、配对、MCP 客户端配置与 MCP 工具参考（英文）。
- [`threat-model.md`](./threat-model.md)：sctl 保护什么、有意不保护什么（英文）。
- [`README.md`](./README.md)：贡献者文档索引（架构、协议、开发）。

## 许可证

GPL-3.0，与 ScriptCat 相同。参见 [LICENSE](../LICENSE)。
