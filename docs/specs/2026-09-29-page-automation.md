# sctl Browser 第 3 期：页面自动化

<!-- File: docs/specs/2026-09-29-page-automation.md -->

> Status: Approved
> Owner: sctl maintainers
> Last updated: 2026-09-29

**目标：** AI agent 和 shell 脚本可以通过 sctl 命令行和 MCP，在用户日常使用、已经登录的浏览器里操作任意标签页的页面：生成带元素引用的页面快照，点击、填表、按键、选择、上传、滚动，等待页面条件成立，截图，执行脚本。整体行为接近 Playwright，包括动作前的自动等待。

**不可退化的约束：**

- 第 1 期的全部行为不变，包括 ScriptCat 的密钥和连接、`scripts.*` 命令、MCP 工具和退出码。
- `tabs` / `windows` / `browsers` 命令的行为不变。
- 给 ScriptCat 的生成 TS 文件保持逐字节不变。

本期是五期规划中的第 3 期，依赖第 1 期（已合并）。第 2 期（浏览器数据管理）与本期并行开发，两者互不依赖。

## 问题

1. **sctl 看不到、也操作不了页面内容。** 已验证。
   - 浏览器实例目前只实现了 `tabs.*` 和 `windows.list`（`internal/pkg/protocol/protocol.json` 中 peer 为 `browser` 的方法）。
   - 扩展权限只有 `tabs`、`storage`、`offscreen`（`extension/src/manifest.json`）。
   - AI agent 能打开一个页面，却读不到上面有什么，也点不了任何东西。
2. **扩展无法向 daemon 主动推送消息。** 已验证。
   - daemon 的读循环只处理响应和 `$session.ping`，其余方法一律忽略（`internal/daemon/bridge/conn.go` 的 `readLoop`）。
   - CDP 的页面事件（加载完成、JS 弹框、调试器被断开）没有通道能送到 daemon，自动等待就无从实现。
3. **CLI 每条命令都是独立进程，无处保存跨命令的页面状态。** 已验证。
   - `internal/cli/dispatch.go` 的每次调用都新建到 daemon 的连接，命令结束即退出。
   - 「先快照，再点 `e5`」要求元素引用在两条命令之间保持有效。

## 角色与用户故事

1. 作为通过 `sctl mcp` 接入的 AI agent，我希望拿到一份简洁的、带元素引用的页面快照，再用引用去点击和填写，这样不用猜选择器就能完成用户交给我的网页任务。
2. 作为 shell 脚本作者，我希望用 CSS 选择器直接对页面执行动作，动作前自动等元素就绪，这样脚本不用手写 sleep 也能稳定运行。
3. 作为用户，我希望 AI 在别的标签页工作时，我可以继续用我正在看的标签页，不会被切走。
4. 作为用户，我希望浏览器明确提示「正在被自动化」，并且停止使用后这个提示会自己消失。

## 设计决策

| # | 决策 | 依据与被否方案 |
|---|---|---|
| 1 | 扩展只做通用的 CDP 中转：按标签页转发任意 CDP 命令，并把 CDP 事件和调试器断开的消息推给 daemon。快照、元素引用和自动等待都在 Go 侧实现。 | 第 4 期（调试事件）和第 5 期（给 Playwright 连接的 CDP 端点）都需要原始的 CDP 通道，本期建好可以直接复用；而且 Go 侧逻辑可以直接对着真 Chrome 做集成测试（决策 12）。被否：像 ScriptCat 那样在扩展内实现 DOM agent。那样第 5 期还得另建一条 CDP 通道，自动化算法也只能在 mock 的 `chrome.*` 下测试。 |
| 2 | 页面自动化的状态（每个浏览器实例、每个标签页的元素引用表、调试器附加状态、未处理的 JS 弹框）保存在 daemon 进程的内存里，由 daemon 里一个新的组件负责，通过新的 `/control/page/*` 接口提供给 CLI 和 MCP。 | 见问题 3：只有 daemon 活得比单条命令长。被否：放在 CLI 进程里，无法跨命令保持；只放在 `sctl mcp` 进程里，CLI 就用不了，而且两者会各有一套逻辑。 |
| 3 | 按需附加调试器：某个标签页第一次执行页面命令时附加；该标签页空闲 5 分钟、标签页关闭或执行 `page detach` 时断开。 | 用户决定。被否：每条命令附加后立刻断开，那样提示条会反复闪，也拿不到跨命令的页面事件；一直保持附加，那样提示条会长期挂着。 |
| 4 | 页面命令从不自动切换标签页，也不抢窗口焦点，尽量在后台完成。某个操作在后台标签页上确实无法完成时返回 `PAGE_HIDDEN`，由调用方决定是否加 `--activate` 重试。 | 用户决定。被否：需要时自动切换，会把用户正在看的标签页切走；总是先切换，AI 工作时用户基本没法用这个窗口。 |
| 5 | 最低 Chrome 版本从 116 提到 125，跨域 iframe（独立进程的 iframe）在快照和动作里全面支持。 | 用户决定。`chrome.debugger` 从 Chrome 125 起支持子会话，才能操作独立进程的 iframe（真机探针已在 Chrome 125 上验证，见[真机探针的结论](#真机探针的结论)）。被否：保持 116、按版本分支，那样有两套行为；本期只支持同进程 iframe，那样嵌入式登录框和支付框操作不了。 |
| 6 | 快照基于无障碍树生成，格式与 Playwright 的 aria 快照相近：每行是 `- 角色 "名称" [状态…] [ref=eN]`，按层级缩进。 | 这是 AI agent 最熟悉的页面表示，比 HTML 小一到两个数量级，而且只包含用户看得到、能交互的东西。被否：返回 DOM 或 HTML，体积大、噪声多，AI 不容易找到可操作的元素。 |
| 7 | 动作的目标可以是快照里的引用（`e5`），也可以是 CSS 选择器（`--selector`）。选择器采用严格模式：匹配到多个元素时报错，不会默认取第一个。 | 引用给 AI 用，选择器给 shell 脚本用，因为脚本很难先解析快照再拿引用。严格模式与 Playwright 一致，避免点错元素。被否：实现 Playwright 的整套选择器引擎（`text=`、`role=` 等），工作量大，而引用已经覆盖了按角色和名称定位的需求。 |
| 8 | 动作前的自动等待与 Playwright 一致：元素已挂载、可见、位置稳定、可用（针对需要可用的动作）、并且点击点确实落在它身上。默认超时 10 秒，导航默认超时 30 秒，都可以用 `--timeout` 调整。 | 这是 Playwright 稳定性的核心来源。被否：不等待，结果不稳定；固定 sleep，又慢又不可靠。 |
| 9 | 鼠标和键盘输入通过 CDP 的 Input 域发出，页面收到的是可信事件（`isTrusted` 为真）。 | 很多网站会忽略脚本合成的事件。被否：用 `dispatchEvent` 合成 DOM 事件。 |
| 10 | MCP 按动作拆分工具，一个动作一个工具，名称为 `page_<动作>`。 | 用户决定，与 Playwright MCP 的习惯一致。 |
| 11 | 页面自动化沿用第 1 期决策 3，不做逐次人工审批：持有控制令牌的进程，可以在任意已配对浏览器的任意可调试页面上读取内容、输入和执行脚本。 | 用户在第 1 期已经决定。本期把这项取舍的影响写进威胁模型（见[文档](#文档)）。被否：按站点授权，操作负担大，与定位不符。 |
| 12 | 除了用假 CDP 做单元测试，还提交一组 Go 集成测试：直接连 headless Chrome，对本地 fixture 页面运行快照、引用和自动等待逻辑。本地没有 Chrome 时跳过，CI 安装 Chrome 后运行。 | 用户决定。快照和遮挡判断这类算法，只有对着真实的 Blink 测试才可信。被否：只用假 CDP 单测加一次性真机验证，回归时容易漏。 |

## 目标选择

**浏览器：** 页面命令都属于「操作类」，沿用第 1 期的目标选择规则。

- 恰好 1 个浏览器在线时，直接用它。
- 多个在线时，必须用 `--browser` 或 `SCTL_BROWSER` 指定，否则返回 `BROWSER_AMBIGUOUS`，退出码 3。
- 其余目标错误（`NO_BROWSER_CONNECTED`、`BROWSER_OFFLINE`、`BROWSER_NOT_FOUND`）同第 1 期。

**标签页：**

- 所有页面命令都接受 `--tab <tabId>`，MCP 工具对应可选参数 `tabId`。
- 未指定时，使用该浏览器里最后获得焦点的窗口中的激活标签页。这个标签页在命令开始时确定，之后用户切换标签页也不会改变本次命令的目标。
- 每条页面命令的结果都带上实际操作的 `tabId`，脚本可以据此锁定后续目标。
- 指定的标签页不存在时返回 `NOT_FOUND`，退出码 3。

**同一标签页上的并发：** 同一个标签页上的页面命令按到达顺序串行执行，不同标签页之间可以并行。

## 调试器附加与提示条

- **附加：**
  - *前提：* 目标标签页当前没有被 sctl 附加。
  - *操作：* 对它执行任意页面命令（`page detach` 除外）。
  - *结果：* 扩展附加调试器后执行命令。Chrome 会在浏览器顶部显示「sctl Browser 已开始调试此浏览器」的提示条，扩展无法隐藏它。用户以 `--silent-debugger-extension-api` 启动 Chrome 时不显示，这一点写进文档。
- **无法附加的页面：** 对 `chrome://` 页面、Chrome 应用商店页面、其他扩展的页面，或其他 Chrome 拒绝附加的页面，返回 `PAGE_NOT_AUTOMATABLE`，退出码 3，消息里带上 Chrome 给出的原因。
- **自动断开：** 以下任一情况发生时断开调试器，并清空这个标签页的全部自动化状态。
  - 最近一条页面命令结束后 5 分钟内，没有新的页面命令。
  - 标签页被关闭。
  - 浏览器实例从 daemon 断开。
  - daemon 退出。
  - 最后一个被附加的标签页断开后，提示条由 Chrome 自行移除。
- **手动断开：** `page detach [--tab N | --all]` 断开一个标签页或这个浏览器里的全部标签页。目标没有被附加时，命令也成功，只是什么都不做。
- **用户在提示条上点「取消」：** Chrome 会断开这个扩展的全部调试会话。
  - 正在执行的页面命令返回 `DEBUGGER_DETACHED`，退出码 2。
  - 所有标签页的自动化状态被清空。
  - 下一条页面命令会重新附加，提示条也会重新出现。

## 后台执行

- 页面命令默认在后台执行：不激活标签页，不聚焦窗口，不改变用户正在看的页面。
- **焦点模拟：** 调试器附加期间，sctl 让被附加的页面以为自己可见且有焦点：`document.visibilityState` 为 `visible`，`document.hasFocus()` 为真。
  - 原因：真机探针显示，不这样做时，Chrome 会丢弃发往后台标签页的鼠标和按键事件，截图在 Chrome 125 上不返回，稳定性检测永远等不到下一帧；这样做之后，这些操作都能在后台完成，标签页仍然不被激活。
  - 影响：附加期间，依赖可见性的页面行为会像前台一样运行，例如隐藏时本会暂停的视频、动画和计时器会继续。调试器断开后恢复正常。这一点写进文档。
- 某个操作在后台标签页上确实无法完成时，返回 `PAGE_HIDDEN`，退出码 3。
  - 例如：隐藏标签页的渲染被 Chrome 节流，稳定性检测一直无法完成，或截图得不到有效图像。
  - 消息会写明原因，并提示可以加 `--activate` 重试。
  - 绝不静默返回空白截图，也不会把「未能确认稳定」当作成功。
- **`--activate`：** 命令开始前先激活目标标签页，但不聚焦它所在的窗口。
- 开启焦点模拟后，探针测到的操作（点击、悬停、输入、按键、滚动、稳定性等待、三种截图、eval）都能在后台完成，都不能要求 `--activate`。`PAGE_HIDDEN` 只留给焦点模拟也无效的情况，例如探针未覆盖的最小化窗口、被冻结或丢弃的标签页。
- 单靠 `--activate` 不保证页面渲染：激活到被其他窗口完全遮挡的窗口里，页面在 Chrome 125 上仍是隐藏状态。

## 页面快照

`page snapshot [--root <目标>]` 返回标签页当前文档的无障碍快照。

- **格式：** 每个节点一行，按层级缩进两个空格：`- 角色 "可访问名称" [状态…] [ref=eN]`。
  - **状态：** 有值时才出现，包括 `checked`、`disabled`、`expanded`、`selected`、`pressed`、`level=N`、`required`、`focused`。
  - **表单控件：** 输入框、文本域和下拉框在后面写上当前值，形如 `: "当前值"`。
  - **链接：** 在子行写出 `/url: …`。
  - **文本：** 纯文本节点输出为 `- text: …`。
- **不包含：** 不可见的元素（`display:none`、`visibility:hidden`、`aria-hidden`、尺寸为零），以及没有角色、没有名称的纯布局容器（直接展开它的子节点）。
- **元素引用：** 可交互、或有名称的节点带 `[ref=eN]`。引用在同一个标签页内唯一，跨 iframe 也唯一。
- **iframe：** 显示为 `- iframe "标题"` 节点，其内容展开在它下面。同源、跨域、独立进程的 iframe 都展开，嵌套 iframe 也展开。无法附加的 iframe（例如扩展页面）显示为 `- iframe [unavailable]`。
- **`--root`：** 只输出以指定引用或选择器为根的子树，用于在大页面上缩小范围。这次快照中子树之外的引用不会生成。
- **内容可信度：** 快照中的名称、值、文本、URL 都由网页控制。结果带有 `contentTrust: "untrusted-page-content"`；MCP 工具的描述保持静态文本，沿用第 1 期规则。
- **大小：** 快照文本超过 1 MiB 时返回 `PAYLOAD_TOO_LARGE`，退出码 3，提示用 `--root` 缩小范围。

## 元素引用的有效期

- 对同一个标签页生成新快照后，这个标签页之前的引用全部失效，由新快照的引用取代。
- 以下情况发生后，引用全部失效：
  - 标签页的主文档被替换（导航、刷新、前进后退）。
  - 调试器断开。
  - daemon 重启。
- 某个 iframe 的文档被替换后，这个 iframe 内的引用失效。
- 引用对应的元素已经被页面从 DOM 中移除时，这个引用失效。
- **使用失效的引用：** 返回 `STALE_REF`，退出码 3，提示重新快照。
- **引用不会跨标签页解析：** 在标签页 A 上用标签页 B 快照里的引用，同样返回 `STALE_REF`，不会误操作到 A 上碰巧同名的元素。

## 动作

所有动作都接受 `--tab`、`--browser`、`--activate`、`--timeout`、`-o json`。凡是要指定元素的动作，目标都写成引用（`e5`）或 `--selector <CSS>`，两者恰好给一个。

**选择器：**

- 选择器在主文档里匹配，不会穿透 iframe。要操作 iframe 里的元素，先快照再用引用。
- 在自动等待期间，选择器匹配到 0 个元素会一直等，直到超时。
- 匹配到多于 1 个元素时，立即返回 `TARGET_AMBIGUOUS`，退出码 3，并写明匹配数量。

**自动等待：** 需要元素的动作执行前，都会等元素满足下表的条件。超时返回 `TIMEOUT`，退出码 3，消息写明最后一个未满足的条件，例如「被 `div.modal-backdrop` 遮挡」。

| 条件 | click | hover | fill | select | upload | scroll 到目标 |
|---|---|---|---|---|---|---|
| 已挂载 | ✓ | ✓ | ✓ | ✓ | ✓ | ✓ |
| 可见 | ✓ | ✓ | ✓ | ✓ | | |
| 位置稳定（连续两次测量的边界框不变） | ✓ | ✓ | | | | |
| 可用（没有 disabled） | ✓ | | ✓ | ✓ | ✓ | |
| 可编辑（没有 readonly） | | | ✓ | | | |
| 点击点落在元素自身或其后代上 | ✓ | ✓ | | | | |

执行前会先把元素滚动到可视区域内。

**动作列表：**

| 命令行 | MCP 工具 | 行为 |
|---|---|---|
| `page click <目标> [--button left\|right\|middle] [--count N] [--modifiers Alt,Control,Meta,Shift]` | `page_click` | 在元素可见区域的中心点点击 |
| `page hover <目标>` | `page_hover` | 把鼠标移到元素中心 |
| `page fill <目标> <文本>` | `page_fill` | 清空后填入文本，触发 `input` 和 `change` 事件。<br>适用于 input、textarea 和 contenteditable。<br>checkbox、radio、file 类型的 input 返回 `INVALID_REQUEST`，分别提示用 click 或 upload。 |
| `page type <文本>` | `page_type` | 在当前焦点元素上逐键输入 |
| `page press <按键>` | `page_press` | 按下一个键或组合键，写法同 Playwright，例如 `Enter`、`Control+A`、`Shift+Tab` |
| `page select <目标> <值>...` | `page_select` | 按 value 或可见文本选择 `<select>` 的选项。<br>元素不是 `<select>` 时返回 `INVALID_REQUEST`；选项不存在时返回 `NOT_FOUND`。 |
| `page upload <目标> <文件>...` | `page_upload` | 为 file input 设置文件。<br>命令行把相对路径按当前目录解析为绝对路径；MCP 要求传绝对路径。<br>文件不存在或不可读时返回 `INVALID_REQUEST`。 |
| `page scroll [<目标>] [--dx N] [--dy N]` | `page_scroll` | 有目标时把它滚到可视区域内；无目标时按给定的像素量滚动视口 |
| `page goto <url> [--wait load\|domcontentloaded\|networkidle]`<br>`page back`、`page forward`、`page reload` | `page_navigate`（用参数区分四种导航） | 导航后等待指定的加载状态，默认是 `load`。<br>`networkidle` 指至少 500 ms 内没有进行中的网络请求。<br>DNS 失败、连接被拒这类网络错误返回 `NAVIGATION_FAILED`，退出码 3，带上 Chrome 的错误文本。<br>HTTP 错误状态码不算失败，结果里会带上状态码。<br>没有可后退或前进的历史时返回 `NOT_FOUND`。 |
| `page wait (--text T \| --gone T \| --selector S \| --url P \| --load 状态)` | `page_wait` | 等待条件成立。<br>`--text`：可见文本出现；`--gone`：文本或选择器对应的元素消失；`--selector`：元素可见；`--url`：URL 包含给定子串；`--load`：到达指定加载状态。<br>超时返回 `TIMEOUT`。 |
| `page screenshot [-f 文件] [--full \| <目标>] [--format png\|jpeg] [--quality N]` | `page_screenshot` | 默认截视口；`--full` 截整页；给目标时截元素的边界框。<br>命令行写入 `-f` 指定的文件，未指定时写到当前目录的 `screenshot-<tabId>-<时间戳>.<扩展名>`，并输出文件路径。<br>MCP 直接返回图片内容。<br>编码后的数据超过单帧上限时返回 `PAYLOAD_TOO_LARGE`，提示改用 jpeg 或只截视口。 |
| `page eval <表达式> [<目标>]` | `page_eval` | 在页面主世界里执行 JS 表达式；返回 Promise 时等它 resolve。<br>给了目标时，表达式要写成函数，例如 `el => el.textContent`，元素作为参数传入，iframe 里的元素就在它自己的 frame 中执行。<br>结果能按 JSON 序列化的原样返回，否则返回它的字符串形式。<br>页面抛出的异常返回 `EVAL_ERROR`，退出码 3，带上异常消息。<br>结果同样标记为不可信的页面内容。 |
| `page dialog accept [--text T]` / `page dialog dismiss` | `page_dialog` | 处理当前打开的 JS 弹框（alert、confirm、prompt、beforeunload）。<br>`--text` 是 prompt 的输入内容。<br>没有打开的弹框时返回 `NOT_FOUND`。 |
| `page detach [--all]` | `page_detach` | 见上文[手动断开](#调试器附加与提示条) |

**动作的结果：**

- 每个动作都返回：实际操作的 `tabId`；动作结束时的页面 URL 和标题（标记为不可信内容）；是否发生了导航。
- **点击触发导航：** 点击后 500 ms 内主文档开始导航的，等到 DOMContentLoaded 再返回，受同一个超时限制。
- **动作打开了新标签页：** 例如 `target=_blank` 的链接，结果带上新标签页的 `tabId`，但不会切换过去。

**JS 弹框：**

- 标签页上有未处理的 JS 弹框时，除 `page dialog`、`page detach`、`page screenshot` 以外的页面命令，都返回 `DIALOG_OPEN`，退出码 3，带上弹框的类型和文字（不可信内容）。
- 弹框不会被自动接受或关闭。

**命令行输出：**

- `page snapshot` 默认直接输出快照文本。
- 其余动作默认输出一行摘要：tabId，外加导航后的 URL 或新标签页 ID。
- `-o json` 输出完整的结构化结果。

## 协议与权限

- **新方法：** 新增的浏览器方法、扩展到 daemon 的通知、新错误码，都先加进 `protocol.json` 并标注归属为 `browser`，再按对端生成代码。
  - 通用的 CDP 中转方法。
  - 调试器断开方法。
  - CDP 事件通知和调试器断开通知。
- **daemon 接收通知：** daemon 开始接受浏览器实例发来的这两种通知。ScriptCat 发来的这两种通知一律丢弃，不影响它的连接。不在 `protocol.json` 里的非 `$` 方法，仍按现有规则判为非法帧并断开连接（`internal/pkg/protocolschema/validate.go` 的 `ValidateWireFrame`）。
- **`schemaVersion` 不变。** 给 ScriptCat 的生成文件逐字节不变。
- **中转方法不对 MCP 直接暴露。** 它们只由 daemon 内的自动化组件调用，CLI 也不提供发送原始 CDP 的命令；原始 CDP 留给第 5 期。
- **新增错误码：** 都以 `browser` 为归属，退出码按上文。
  - `STALE_REF`、`TIMEOUT`、`TARGET_AMBIGUOUS`
  - `PAGE_NOT_AUTOMATABLE`、`PAGE_HIDDEN`
  - `DEBUGGER_DETACHED`、`DIALOG_OPEN`
  - `EVAL_ERROR`、`NAVIGATION_FAILED`
- **已有错误码扩展到浏览器：** `PAYLOAD_TOO_LARGE` 的 peers 加上 `browser`。
- **权限：**
  - 清单新增 `debugger` 权限，`minimum_chrome_version` 改为 `125`。
  - 通过「加载已解压的扩展程序」更新时，新权限直接生效。
  - 低于 125 的浏览器无法安装新版扩展。
- **与第 2 期的关系：** 第 2 期同样会修改清单权限和 `protocol.json`，两期各自只加自己的条目；后合并的一期负责合并冲突并重新生成代码。

## 真机探针的结论

实现计划的第一个任务在隔离配置中用 Chrome for Testing 125 和 153（有界面与 headless）做了真机探针，证据在 `e2e/scratch/2026-09-29-page-automation/probe/`（已 gitignore）。以下结论已验证：

- **跨域 iframe：** Chrome 125 上，`chrome.debugger` 通过子会话（`Target.setAutoAttach` 的扁平会话，`sendCommand` 带 `sessionId`）可以读取跨域 iframe 的无障碍树和 DOM、在其中执行脚本、发送可信的鼠标和键盘事件；子会话的事件会带上 `sessionId`。决策 5 成立。主会话的无障碍树不包含跨域 iframe 的内容，子会话上不能截图，只能从顶层截取。
- **后台标签页：** 见[后台执行](#后台执行)里的焦点模拟。
- **headless 与提示条：** headless 的 Chrome 125 不带 `--silent-debugger-extension-api` 时，每次附加都会被立刻当作「用户取消」断开。这只影响经扩展的 headless 验证，不影响直连 Chrome 的 Go 集成测试。

## 不在本期范围

- **原始 CDP：** 通过 CLI 或 MCP 发送原始 CDP 命令，以及给 Playwright 或 Puppeteer 用的 `connectOverCDP` 端点，留到第 5 期。
- **调试数据：** console、异常和网络请求的缓存与查询，留到第 4 期。
- **选择器引擎：** Playwright 的完整选择器引擎（`text=`、`role=`、`>>` 链式写法等），以及选择器穿透 iframe。
- **录制：** 录制用户操作并生成脚本。
- **页面设置：** 网络拦截和改写、设备模拟、地理位置和权限模拟。
- **打印：** 打印成 PDF。
- **弹窗 UI：** 弹窗不新增任何界面，Chrome 的调试提示条就是「正在自动化」的指示。
- **审批：** 页面操作不做逐次人工审批，也不限定站点范围。

## 文档

以下文档在本期一并修改，各自只改自己负责的内容：

- **[architecture.md](../architecture.md)：** daemon 里的页面自动化组件，CDP 中转链路，页面状态放在 daemon 内存里的原因，`/control/page/*`。
- **[protocol.md](../protocol.md)：**
  - 扩展到 daemon 的通知：这是这个方向的第一类业务消息。
  - 中转方法和新错误码。
  - 调试器生命周期的时序。
- **[threat-model.md](../threat-model.md)：**
  - 持有控制令牌的进程可以读取任意可调试页面的内容。
  - 它可以在这些页面上以用户的登录态输入、提交表单和执行脚本。
  - 调试提示条是唯一的可见指示，而且可以用启动参数关闭。
  - 附加期间的焦点模拟会让页面以为自己在前台。
  - 快照和 eval 结果都是不可信内容，存在提示注入风险。
- **[development.md](../development.md)：** 真 Chrome 集成测试的运行方式和跳过条件，以及 CI 里的 Chrome 安装。
- **[mcp.md](../mcp.md) 和两份 README：** 页面命令和 `page_*` 工具，最低 Chrome 125，调试提示条的说明。

## 测试决策

| 测试层 | 验证什么 | 可参照的现有测试 |
|---|---|---|
| Go 自动化组件单元测试，连一个假的 CDP 对端 | 附加和空闲断开的计时；引用表的替换与失效（导航、iframe 导航、断开、新快照）；同一标签页串行执行；弹框状态；各错误码的产生条件 | `internal/daemon/bridge/server_test.go`（模拟对端的写法） |
| Go 真 Chrome 集成测试，直连 headless Chrome，没有 Chrome 时跳过，CI 运行 | 用本地 fixture 页面检查：快照格式与可见性过滤；iframe 展开，包括跨域；每一项自动等待条件，包括遮挡时报出遮挡元素；click、fill、select、upload、press、scroll 的效果；点击触发导航和打开新标签页；`networkidle`；截图三种模式；eval 的返回值和异常；四种 JS 弹框 | 本仓库暂无 |
| bridge 测试 | 浏览器实例发来的 CDP 事件通知和断开通知能送达订阅方；ScriptCat 发来的同名通知被忽略；实例断开会触发页面状态清理 | `internal/daemon/bridge/routing_test.go` |
| 控制 API 测试 | `/control/page/*` 的目标选择（浏览器、标签页、默认激活标签页）以及错误映射 | `internal/daemon/controlapi/routing_test.go` |
| 命令行测试，连接模拟的控制服务 | 每个 `page` 子命令的参数校验（引用和 `--selector` 必须恰好给一个、`upload` 解析相对路径）；表格、快照文本和 JSON 输出；截图写文件；退出码 0、2、3 | `internal/cli/tabs_test.go` |
| MCP 服务测试 | `page_*` 工具已注册，描述是静态文本；截图返回图片内容；可选的 `browser` 和 `tabId` 参数能正确转发 | `internal/client/mcpserver/mcpserver_test.go` |
| 扩展单元测试（Vitest，mock `chrome.*`） | 中转方法按标签页转发命令，支持子会话；事件和断开通知的转发；空闲断开计时；无法附加时的错误翻译 | `extension/src/handlers/index.test.ts` |
| 协议生成检查 | 新方法、通知和错误码只出现在 Go 和浏览器扩展的生成代码里，给 ScriptCat 的那套逐字节不变 | `make protocol-check`、`internal/protocolgen/generate_test.go` |
| 一次性真机验证，放在 `e2e/scratch/`，不提交 | 在隔离的 Chrome 里加载本分支构建的扩展，同时加载 ScriptCat，都配对到临时 daemon，检查以下内容：<br>• 在后台标签页上跑一遍 snapshot、click、fill、wait、screenshot、eval，用户正在看的前台标签页始终不被切走<br>• 提示条的出现和 5 分钟空闲后的消失，以及点「取消」后的 `DEBUGGER_DETACHED`<br>• 跨域 iframe 的表单操作<br>• `PAGE_NOT_AUTOMATABLE`<br>• MCP 工具<br>• ScriptCat 全程正常 | [verification.md](../verification.md) |

提示条的外观由 Chrome 决定，不写自动化测试，只在真机验证时截图确认。

## 未决问题

<!-- 批准前必须为空。 -->
