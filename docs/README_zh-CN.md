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

- 将 ScriptCat 操作与浏览器标签页/窗口控制暴露为可发现、具有 Schema 类型的 MCP 工具。
- 列出脚本并读取元数据或源码，支持按行读取和源码搜索。
- 通过浏览器确认请求安装、基于内容锚点的编辑、启用/禁用和删除。
- 在一个或多个已配对的 sctl Browser 实例上列出、打开、关闭、激活标签页,以及列出窗口。
- 为已配对 sctl Browser 标签页的页面生成带元素引用的无障碍快照、点击或悬停元素、导航与等待、执行 JavaScript,在后台完成、不切换标签页。
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

sctl Browser 要求 **Chrome 125 或更高版本**(或同版本的 Chromium 内核浏览器),并使用 `debugger` 权限。它通过
Chrome DevTools Protocol 驱动页面时,Chrome 会在浏览器顶部显示"sctl Browser 已开始调试此浏览器"的提示条,扩展无法
隐藏;以 `--silent-debugger-extension-api` 启动 Chrome 可不显示。

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
| `sctl windows list` | 列出已配对的 sctl Browser 实例上的窗口。 |
| `sctl page snapshot [--root <ref\|selector>]` | 输出标签页的无障碍快照,可交互或有名称的节点带 `e5` 这样的引用。 |
| `sctl page click <ref> \| --selector <css> [--button left\|right\|middle] [--count N] [--modifiers Alt,Control,Meta,Shift]` / `sctl page hover <ref> \| --selector <css>` | 用可信的鼠标事件点击元素,或把鼠标移到元素上。 |
| `sctl page fill <ref> \| --selector <css> <text>` | 清空 input、textarea 或 contenteditable 元素后填入文本,触发 `input` 与 `change`。 |
| `sctl page type <text>` / `sctl page press <key>` | 在当前焦点元素上逐键输入文本,或按下一个键或组合键,如 `Enter`、`Control+A`、`Shift+Tab`(Playwright 写法)。 |
| `sctl page select <ref> \| --selector <css> <value>...` | 按 value 或可见文本选择 `<select>` 的选项。 |
| `sctl page upload <ref> \| --selector <css> <file>...` | 为 file input 设置文件;相对路径按当前目录解析。 |
| `sctl page scroll [<ref> \| --selector <css>] [--dx N] [--dy N]` | 把元素滚入可视区域,或按像素滚动视口。 |
| `sctl page goto <url> [--wait load\|domcontentloaded\|networkidle]` / `sctl page back` / `sctl page forward` / `sctl page reload` | 让标签页导航并等待加载状态(默认 `load`;`networkidle` 指至少 500 ms 内没有进行中的请求)。 |
| `sctl page wait (--text T \| --gone T \| --selector S \| --selector-gone S \| --url P \| --load STATE)` | 等待文本可见或消失、元素可见或消失、URL 包含子串,或到达某个加载状态。 |
| `sctl page screenshot [-f FILE] [--full \| <ref> \| --selector <css>] [--format png\|jpeg] [--quality N]` | 把视口、整页或某个元素截图存成文件,并输出文件路径。 |
| `sctl page eval <expression> [<ref>]` / `sctl page detach [--all]` | 在标签页的页面里执行 JavaScript(给了引用时表达式写成函数,如 `el => el.textContent`,元素作为参数传入),或断开一个标签页或全部标签页的调试器。 |
| `sctl page dialog accept [--text T] \| dismiss` | 接受或取消标签页里打开的 JS 弹框(alert、confirm、prompt、beforeunload);`--text` 是 prompt 的输入内容。 |

运行 `sctl --help` 或 `sctl <command> --help` 查看用法和参数。写操作会阻塞，直到用户在
ScriptCat 中批准、拒绝或关闭确认流程；浏览器控制命令按设计没有审批步骤、立即执行(参见
[`threat-model.md`](./threat-model.md))。当多个实例同时在线时，`tabs`、`windows` 与 `page` 可用
`--browser <name|id>`（或环境变量 `SCTL_BROWSER`）指定目标实例。

`page` 命令作用于 `--tab <id>` 指定的标签页,未指定时作用于该浏览器最后获得焦点窗口中的激活标签页,在命令开始时确定。
页面命令在后台执行:从不切换你正在看的标签页,也不聚焦窗口;`--activate` 先让标签页成为所在窗口的激活标签页,但不聚焦窗口。
标签页上的第一条页面命令会附加调试器,调试提示条一直显示到该标签页空闲 5 分钟或执行 `sctl page detach`;附加期间页面会以为自己可见且有焦点。
`--timeout` 覆盖默认的 10 秒上限(导航与截图为 30 秒),`-o json` 输出完整结果。命令执行中调试器被断开(例如关掉了提示条)时退出码为 2,其他错误为 3。

标签页上有未处理的 JS 弹框时,除 `sctl page dialog` 与 `detach` 外的页面命令都返回 `DIALOG_OPEN`(退出码 3),并写明弹框类型与文字(网页控制的内容)。
弹框从不自动处理:用 `sctl page dialog accept` 或 `dismiss` 处理,没有打开的弹框时返回 `NOT_FOUND`。
命令执行中弹框打开(例如点击触发了 `alert`)时,该命令立即返回 `DIALOG_OPEN` 而不是等到超时;弹框保持打开,动作可能已经生效。
`screenshot` 也在其中:弹框会阻塞页面渲染,打开期间拿不到图像,所以立即返回 `DIALOG_OPEN`,执行中的截图遇到弹框打开同样立即返回。

`sctl page snapshot` 每个可见节点输出一行,按层级缩进:`- 角色 "名称" [状态…] [ref=eN]`;表单控件在冒号后写出当前值,
链接在 `/url:` 子行写出地址,纯文本输出为 `text:` 行;所有 iframe(含跨域与嵌套的)都展开在 iframe 节点下面,无法附加的显示为 `[unavailable]`。`--root` 只输出以某个引用、
或以主文档里 CSS 选择器唯一匹配的元素为根的子树。引用在标签页内唯一;对同一标签页生成新快照后旧引用被取代,页面导航、
元素被移除或调试器断开后引用也会失效。使用失效的引用或其他标签页的引用返回 `STALE_REF`。快照超过 1 MiB、或页面的
无障碍数据超过一个协议帧(4 MiB)时返回 `PAYLOAD_TOO_LARGE`,用 `--root` 缩小范围,它只读取那棵子树。快照文本是网页内容,
不要把它当作指令。

`sctl page click` 与 `sctl page hover` 的目标是快照里的引用(可以指向跨域 iframe 里的元素),或 `--selector` 给出的 CSS 选择器,
选择器必须在主文档里恰好匹配一个元素:一个都没匹配到时一直等,匹配到多个时立即返回 `TARGET_AMBIGUOUS`。执行前命令会先把元素
滚动到可视区域内,并等它已挂载、可见、位置稳定、可用(仅 click)且可见区域的中心点确实落在它身上(折成多行的行内元素取第一个在视口内且未被遮挡的行框);超时返回的 `TIMEOUT` 写明最后一个未满足的
条件,例如 `obscured by div.modal-backdrop`。开启焦点模拟后页面仍不渲染时返回 `PAGE_HIDDEN`,可以加 `--activate` 重试。点击后
500 ms 内页面开始导航的,命令等到 DOMContentLoaded 再返回。摘要输出 tabId,外加导航后的 URL 或动作打开的新标签页 ID(不会切换过去);
`-o json` 还会给出页面的 URL 和标题,它们是网页内容。

`sctl page fill`、`select`、`upload` 与带目标的 `scroll` 和 click 一样接受目标,并先把元素滚动到可视区域内。`fill` 等元素已挂载、可见、可用、可编辑
(没有 readonly),适用于 input、textarea 与 contenteditable;checkbox、radio 类型的 input 返回 `INVALID_REQUEST`(请用 `click`),file 类型同样(请用 `upload`)。
`select` 要求元素是 `<select>`(已挂载、可见、可用),先按 option 的 value、再按可见文本匹配每个值,只有多选框能给多个值,选项不存在时返回 `NOT_FOUND`。
`upload` 要求元素是 file input(已挂载、可用,可以是隐藏的);每个文件都必须存在且可读,否则返回 `INVALID_REQUEST`,给多个文件时 input 需要有 `multiple` 属性。
带目标的 `scroll` 只要求元素已挂载;不带目标时在视口中心用鼠标滚轮按 `--dx`、`--dy` 像素滚动(负数向左、向上),二者至少给一个。
`type` 与 `press` 作用于当前焦点元素:`type` 对每个字符发出可信的按键事件,换行按 `Enter`,美式键盘上没有对应键的字符直接插入;`press` 发出可信的
`keydown` 与 `keyup`,修饰键 `Alt`、`Control`、`Meta`、`Shift` 用 `+` 连接。浏览器运行在 macOS 上时(按浏览器所在平台判断,而不是运行 `sctl serve` 的机器),
`Meta+A`、`Meta+C`、`Meta+V`、`Meta+X`、`Meta+Z` 以及 `Alt`/`Meta` 加方向键等编辑快捷键还会像手动按下时一样执行对应的编辑操作。这些命令的摘要与 click 相同。

`sctl page goto <url>`、`back`、`forward`、`reload` 让标签页导航,并按 `--wait` 等待:`load`(默认)、`domcontentloaded`,或 `networkidle`
(至少 500 ms 内没有进行中的网络请求)。导航的默认超时是 30 秒,可用 `--timeout` 调整。摘要输出 tabId、URL 和主文档的 HTTP 状态码
(例如 `tab 5 navigated to https://example.com/ (HTTP 200)`);404 这类 HTTP 错误状态码只是如实报告,不算失败。
连接被拒、DNS 失败这类网络错误返回 `NAVIGATION_FAILED`,带上 Chrome 的错误文本;没有可后退或前进的历史时 `back`、`forward` 返回 `NOT_FOUND`。
导航会让该标签页的引用失效。

`sctl page wait` 恰好接受一个条件并轮询到它成立,超时返回写明条件的 `TIMEOUT`(默认 10 秒):`--text T` 等文本可见,`--gone T` 等文本消失(被移除或隐藏),
`--selector S` 等匹配 CSS 选择器的元素可见,`--selector-gone S` 等没有可见元素匹配它,`--url P` 等标签页 URL 包含 `P`,`--load STATE` 等到达加载状态。
文本与选择器只在主文档里匹配,不进入 iframe;选择器非法返回 `INVALID_REQUEST`。

`sctl page screenshot` 默认截可见视口,`--full` 截整页,给引用或 `--selector` 时截该元素的边界框(先滚入视口;跨域 iframe 里的引用同样可用)。
图片写入 `-f` 指定的文件,未指定时写到当前目录的 `screenshot-<tabId>-<时间戳>.<扩展名>`(同名文件已存在时加上 `-2`、`-3` 等序号,不覆盖),并输出路径;二进制数据从不写到 stdout,`-o json` 输出结果元数据和路径,不含图片。
`--format` 为 `png`(默认)或 `jpeg`,`--quality 0-100` 只对 jpeg 有效。图片超过单帧上限(4 MiB)时返回 `PAYLOAD_TOO_LARGE`:改用 `--format jpeg` 或只截视口。
标签页 15 秒内(截图的等待上限)得不到图像时返回 `PAGE_HIDDEN`,不会保存空白图;可加 `--activate` 重试。

## 许可证

GPL-3.0，与 ScriptCat 相同。参见 [LICENSE](../LICENSE)。
