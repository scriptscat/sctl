# sctl Browser 第 2 期：浏览器数据管理

<!-- File: docs/specs/2026-09-29-browser-data.md -->

> Status: Approved
> Owner: sctl maintainers
> Last updated: 2026-09-29

**目标：** AI agent 和 shell 脚本可以通过 sctl 命令行和 MCP，管理用户日常浏览器里的数据，范围包括：书签、阅读列表、标签页/窗口/标签组、历史记录、最近关闭、下载、Cookie、浏览数据和扩展。破坏性操作按破坏程度分级设门槛，其中删除书签必须由人在浏览器里批准。

**不可退化的约束：**

- 第 1 期的全部行为不变，包括 ScriptCat 的密钥和连接、`scripts.*` 命令、MCP 工具和退出码。
- 现有 `tabs` / `windows` / `browsers` 命令的行为和输出不变，只允许在 JSON 输出里新增字段。
- 给 ScriptCat 的生成 TS 文件保持逐字节不变。

本期是五期规划中的第 2 期，依赖第 1 期（已合并）。第 3 期（页面自动化）与本期并行开发，两者互不依赖。

## 问题

1. **浏览器里的个人数据无法被脚本或 AI 整理。** 已验证。
   - 浏览器实例目前只实现了 `tabs.list/open/close/activate` 和 `windows.list`（`internal/pkg/protocol/protocol.json` 中 peer 为 `browser` 的方法）。
   - 扩展权限只有 `tabs`、`storage`、`offscreen`（`extension/src/manifest.json`）。
   - 用户想让 AI 帮忙整理书签、清理历史、归档标签页，目前做不到。
2. **浏览器侧没有任何人工审批机制。** 已验证。
   - 人工审批目前只存在于 ScriptCat 的写操作中：协议里 `USER_REJECTED` 的 peers 只有 `scriptcat`，CLI 把它映射为退出码 1（`internal/cli/cli.go` 的 `exitRejected`）。
   - sctl Browser 扩展没有审批界面，第 1 期的浏览器方法全部直接执行。

## 角色与用户故事

1. 作为通过 `sctl mcp` 接入的 AI agent，我希望能搜索、整理书签和阅读列表，按主题给标签页分组，查询历史和下载，这样可以替用户完成日常整理。
2. 作为 shell 脚本作者，我希望每种数据都有对象加动作形式的命令，输出表格或 JSON，退出码可预期，这样可以写定期清理的脚本。
3. 作为用户，我希望删除书签这种我在意、又没法撤销的操作，必须经我在浏览器里点头才会执行，而且一次批量删除只需要批准一次。
4. 作为用户，我希望其他破坏性操作至少需要调用方显式确认，这样脚本写错参数时不会误删。

## 设计决策

| # | 决策 | 依据与被否方案 |
|---|---|---|
| 1 | 破坏性操作分三级：**L0 直接执行**、**L1 显式确认**（命令行 `--yes`，MCP `confirm: true`）、**L2 人工审批**（在浏览器里批准）。L2 只用于删除书签，一次请求可以包含多个书签或文件夹，只需审批一次。原本拟定为 L2 的其他操作都降为 L1。 | 用户决定。被否：全部不设门槛，误操作没有任何缓冲；全部走弹窗审批，操作负担大，也违背「持令牌即可操作」的定位。 |
| 2 | L1 的确认由 daemon 在转发前检查，扩展在执行前再检查一次。缺少确认时返回 `CONFIRMATION_REQUIRED`，不执行任何操作。 | 只在 CLI 检查的话，MCP 和直接调用 `/control/*` 就能绕过。 |
| 3 | 卸载扩展属于 L1，同时 Chrome 自己一定会弹出确认框，不再叠加 sctl 的审批窗口。 | Chrome 在一个扩展卸载另一个扩展时，总会显示自带的确认框（外观事实，见[未验证事实](#已知的未验证事实)），它本身就是人工确认。 |
| 4 | 审批窗口是扩展打开的一个独立小窗口，同时在工具栏图标上显示待审批数量的角标，弹窗里也有入口。 | 请求到达时，用户可能没在看工具栏；MV3 的弹窗不能由扩展随时打开。被否：只显示角标，容易被忽略，请求会在 5 分钟后过期；复用 ScriptCat 的审批页，这与「独立于 ScriptCat」的决定冲突。 |
| 5 | 审批的等待、拒绝、超时和取消，沿用 ScriptCat 写操作已有的语义：拒绝返回 `USER_REJECTED`（退出码 1），5 分钟未处理返回 `OPERATION_EXPIRED`（退出码 2），请求方取消后请求作废。 | 协议 §5 和 CLI 的 `dispatchBlocking` 已经实现了这套语义，调用方的脚本不需要学新规则。 |
| 6 | 协议按操作逐个定义方法（例如 `bookmarks.search`、`bookmarks.remove`），命令行也按操作提供子命令；MCP 则按领域合并成 10 个工具，每个工具用 `action` 参数区分操作。 | MCP 的合并方式由用户决定。协议保持逐个操作定义，这样路由、校验、审计和分级都按单个方法来做。被否：协议也按领域合并，那样分级和参数校验只能在方法内部再分支。 |
| 7 | 最低 Chrome 版本保持 116。阅读列表在运行时检测：浏览器不提供 `chrome.readingList` 时返回 `UNSUPPORTED`。 | `chrome.readingList` 从 Chrome 120 起才有，Edge 上是否提供尚未确认。第 3 期会把最低版本提到 125，那是第 3 期的决定，本期不依赖它。 |
| 8 | Cookie 值原样返回，不做打码。 | 用户在第 1 期已决定可以不考虑那么多安全问题。这项影响写进威胁模型：持有控制令牌的进程可以读到所有站点的登录态。被否：默认打码、另加开关显示原值，这会增加一层 AI 很少用得上的交互。 |
| 9 | 列表类方法默认最多返回 100 条，最多可以用 `--limit` 提到 1000 条；结果里带上 `hasMore`，表示是否还有未返回的条目。结果超过单帧 4 MiB 时返回 `PAYLOAD_TOO_LARGE`。 | 历史和 Cookie 很容易有上万条，一次全量返回会超过单帧上限（`protocol.json` 的 `limits.maxFrameBytes` 为 4194304），也会占满 AI 的上下文。 |

## 破坏级别

| 级别 | 行为 | 包含的操作 |
|---|---|---|
| L0 直接执行 | 与第 1 期相同 | 所有读操作<br>新建和移动书签、编辑书签的标题和 URL、新建书签文件夹<br>添加阅读列表条目、标记已读或未读<br>标签页和窗口整理（移动、固定、静音、刷新、复制，打开、关闭、聚焦窗口，改窗口状态）<br>标签组的所有操作<br>恢复最近关闭的标签页或窗口<br>开始、暂停、继续下载，在文件管理器中显示下载文件<br>设置 Cookie<br>启用扩展 |
| L1 显式确认 | 命令行必须加 `--yes`，MCP 必须传 `confirm: true`，否则返回 `CONFIRMATION_REQUIRED`（退出码 3），什么都不执行 | 删除单条历史 URL，按时间范围删除或清空历史<br>删除单个 Cookie，按域名或全部批量删除 Cookie<br>清除浏览数据<br>取消下载，删除下载记录，从磁盘删除已下载的文件<br>移出阅读列表<br>禁用扩展，卸载扩展（Chrome 另外还会弹出自己的确认框） |
| L2 人工审批 | 在浏览器里批准后才执行，见[删除书签的审批](#删除书签的审批) | 删除书签：单个书签、空文件夹、非空文件夹（连同其中全部内容），可以在一次请求里批量删除 |

## 通用规则

- **目标选择：** 沿用第 1 期规则。
  - 所有命令都接受 `--browser` 和 `SCTL_BROWSER`；MCP 工具都有可选的 `browser` 参数。
  - 列表和搜索类操作在多个浏览器在线、又没指定目标时，汇总所有在线浏览器的结果：表格多一列 BROWSER，JSON 的每一项带上浏览器名称和 ID。
  - 其余操作在多个浏览器在线时必须指定目标，否则返回 `BROWSER_AMBIGUOUS`。
- **输出：** 所有命令都支持 `-o json`，默认输出表格。
- **不存在的对象：** 书签、标签页、窗口、标签组、下载、扩展、会话的 ID 不存在，或者阅读列表 URL 不在列表中时，返回 `NOT_FOUND`，退出码 3。
- **全有或全无：** 接受多个 ID 的操作先确认全部存在，只要有一个不存在，就一个都不执行。这与第 1 期的 `tabs close` 一致。
- **不可信内容：** 书签标题和 URL、历史标题、下载文件名、Cookie 名称和值、扩展名称和描述、标签组标题，都由网页或第三方控制。
  - 它们只作为结构化数据返回，结果带有 `contentTrust: "untrusted-page-content"`。
  - MCP 工具的描述保持静态文本。
  - 审批窗口只把它们当纯文本渲染。
- **浏览器不支持：** 浏览器不提供某个领域所需的 API 时，这个领域的操作返回 `UNSUPPORTED`，退出码 3，消息写明缺少的 API。

## 各领域的命令

每一行的 MCP 工具就是这个领域合并后的工具，用 `action` 参数取表中的动作名。级别标注在动作后面，没有标注的都是 L0。

### 书签（MCP：`bookmarks`）

- `bookmarks list [--folder ID] [--recursive]`
  - 列出一个文件夹的直接子项；不给 `--folder` 时列出根下的顶层文件夹（书签栏、其他书签等）。
  - `--recursive` 返回整棵子树。这种情况不受 100 条的默认上限限制，但仍受单帧大小限制。
  - 每项包含：ID、类型（书签或文件夹）、标题、URL、父文件夹 ID、在父文件夹中的位置、添加时间；文件夹还有子项数量。
- `bookmarks search <关键词> [--limit N]`：按标题和 URL 搜索，结果带上每一项的文件夹路径。
- `bookmarks add <url> [--title T] [--folder ID] [--index I]`：默认加到「其他书签」，输出新书签 ID。
- `bookmarks mkdir <标题> [--folder ID] [--index I]`：新建文件夹，输出其 ID。
- `bookmarks move <ID>... --folder ID [--index I]`：移动书签或文件夹，全有或全无。
- `bookmarks edit <ID> [--title T] [--url U]`：文件夹不能设置 URL，否则返回 `INVALID_REQUEST`。
- `bookmarks rm <ID>...`（L2）：删除书签或文件夹，文件夹连同其全部内容一起删除。见下节。

根节点和浏览器内置的顶层文件夹（书签栏、其他书签、移动设备书签）不能被移动、编辑或删除，否则返回 `INVALID_REQUEST`。

### 阅读列表（MCP：`reading_list`）

- `reading-list list [--unread | --read] [--limit N]`：每项包含 URL、标题、是否已读、添加时间、更新时间。
- `reading-list add <url> [--title T]`：标题默认用 URL。URL 已经在列表中时返回 `CONFLICT`。
- `reading-list mark-read <url>... [--unread]`：标记为已读，加 `--unread` 时标记为未读。
- `reading-list rm <url>...`（L1）：移出阅读列表。

### 标签页、窗口和标签组

**标签页和窗口整理（MCP：`tabs_manage`）：**

- `tabs move <tabId>... [--window N] [--index I]`：`--index -1` 表示移到末尾。
- `tabs pin` / `tabs unpin <tabId>...`
- `tabs mute` / `tabs unmute <tabId>...`
- `tabs reload <tabId>... [--bypass-cache]`
- `tabs duplicate <tabId>`：输出新标签页 ID。
- `windows open [<url>...] [--state normal|minimized|maximized|fullscreen]`：输出新窗口 ID。
- `windows close <windowId>...`：关闭的窗口可以用 `recent restore` 恢复。
- `windows focus <windowId>`
- `windows state <windowId> <状态>`：设置窗口状态，取值同 `windows open`。

**标签组（MCP：`tab_groups`）：**

- `groups list [--window N]`：每项包含组 ID、窗口 ID、标题、颜色、是否折叠、标签页数量。
- `groups create <tabId>... [--title T] [--color C]`：把这些标签页编成一个新组，输出组 ID。
  - 颜色取 Chrome 支持的九种：grey、blue、red、yellow、green、pink、purple、cyan、orange。
  - 这些标签页必须在同一个窗口里，否则返回 `INVALID_REQUEST`。
- `groups add <groupId> <tabId>...`：把标签页加入已有的组。
- `groups edit <groupId> [--title T] [--color C] [--collapse | --expand]`
- `groups ungroup <tabId>...`：把标签页移出所在的组。组里最后一个标签页移出后，这个组由 Chrome 自动删除。

**现有输出的扩展：** 第 1 期的 `tabs list` 在 JSON 输出里新增 `groupId` 字段，不在组中的标签页为 `-1`。表格输出不变。

### 历史记录（MCP：`history`）

- `history search [关键词] [--since 时间] [--until 时间] [--limit N]`
  - 不给时间范围时，搜索全部历史。
  - 每项包含 URL、标题、最后访问时间、访问次数。
  - 时间可以写成 RFC 3339 格式，也可以写成相对时长，例如 `7d`、`12h`。
- `history visits <url>`：列出这个 URL 的每一次访问：访问时间、来源类型。
- `history rm <url>...`（L1）：删除这些 URL 的全部访问记录。
- `history clear [--since 时间] [--until 时间]`（L1）：删除时间范围内的全部历史；两个参数都不给时清空全部历史。

### 最近关闭（MCP：`recently_closed`）

- `recent list [--limit N]`：列出最近关闭的标签页和窗口，按关闭时间倒序排列。
  - 每项包含会话 ID、类型、关闭时间、标题和 URL；窗口类型的项还有其中的标签页数量。
  - Chrome 最多提供 25 条。
- `recent restore [<会话 ID>]`：恢复一项；不给 ID 时恢复最近关闭的一项。输出恢复出来的标签页或窗口的 ID。

### 下载（MCP：`downloads`）

- `downloads list [--state in_progress|complete|interrupted] [--query 关键词] [--limit N]`
  - 每项包含：下载 ID、URL、本地文件路径、状态、已接收和总字节数、开始时间、文件是否还在磁盘上。
  - 按开始时间倒序排列。
- `downloads start <url> [--filename 相对路径]`
  - 保存到浏览器的默认下载目录，输出下载 ID。
  - 文件名冲突时自动改名，从不覆盖已有文件。
  - `--filename` 不能是绝对路径，也不能包含 `..`，否则返回 `INVALID_REQUEST`。
- `downloads pause` / `downloads resume <下载 ID>`
- `downloads cancel <下载 ID>`（L1）
- `downloads erase <下载 ID>...`（L1）：只删除下载记录，不删除文件。
- `downloads delete-file <下载 ID>`（L1）：从磁盘删除已下载完成的文件，下载记录保留。下载尚未完成时返回 `INVALID_REQUEST`。
- `downloads show <下载 ID>`：在系统文件管理器里显示这个文件。

### Cookie（MCP：`cookies`）

- `cookies list [--url U | --domain D] [--name N] [--limit N]`
  - 每项包含名称、值、域名、路径、过期时间、Secure、HttpOnly、SameSite、是否为会话 Cookie。
  - 不给任何过滤条件时列出所有站点的 Cookie，受条数上限约束。
- `cookies get --url U --name N`：不存在时返回 `NOT_FOUND`。
- `cookies set --url U --name N --value V [--domain D] [--path P] [--secure] [--http-only] [--same-site no_restriction|lax|strict] [--expires 时间]`
  - 不给 `--expires` 时设为会话 Cookie。
  - Chrome 拒绝写入时返回 `INVALID_REQUEST`，并带上原因。
- `cookies rm --url U --name N`（L1）
- `cookies clear (--domain D | --all)`（L1）：删除这个域名及其子域名下的全部 Cookie，或者删除全部 Cookie。输出删除的条数。

### 浏览数据（MCP：`browsing_data`）

- `browsing-data clear --types 类型,... [--since 时间] [--origin O...]`（L1）
  - **类型：** 可选 `cache`、`cacheStorage`、`cookies`、`downloads`、`fileSystems`、`formData`、`history`、`indexedDB`、`localStorage`、`serviceWorkers`、`webSQL`。
  - **密码不在其中：** 本期不管理密码。
  - **时间范围：** 不给 `--since` 时清除全部时间范围。
  - **`--origin`：** 把清除范围限定到这些来源，只对支持按来源清除的类型有效。和不支持的类型一起用时，返回 `INVALID_REQUEST`。

### 扩展（MCP：`extensions`）

- `extensions list`：列出已安装的扩展和应用，每项包含 ID、名称、版本、是否启用、类型、安装方式，以及能否被禁用。
- `extensions enable <ID>`
- `extensions disable <ID>`（L1）
- `extensions uninstall <ID>`（L1）：Chrome 会弹出自己的卸载确认框。
  - 用户在 Chrome 的确认框里取消时，返回 `USER_REJECTED`，退出码 1。
- **限制：**
  - 不能对 sctl Browser 自己执行 disable 或 uninstall，否则返回 `INVALID_REQUEST`。
  - 企业策略安装、不允许禁用的扩展，按 Chrome 给出的原因返回 `INVALID_REQUEST`。
  - 禁用 ScriptCat 是允许的，但后果是它与 daemon 的连接会断开。命令的帮助文本里要写明这一点。

## 删除书签的审批

- **发起：**
  - *前提：* 目标浏览器在线。
  - *操作：* 调用方执行 `bookmarks rm <ID>...`，或者调用 MCP 的 `bookmarks` 工具，`action` 为 `remove`。
- **审批前的校验，任何一项不满足都直接报错，不打开审批窗口：**
  - 每个 ID 都存在，否则返回 `NOT_FOUND`。
  - 其中没有根节点或内置顶层文件夹，否则返回 `INVALID_REQUEST`。
  - 一次最多 500 个 ID，否则返回 `INVALID_REQUEST`。
  - 如果某个 ID 位于同时被删除的文件夹之内，只算一次，不重复计数。
- **等待：** 命令行输出「等待在浏览器 <名称> 中批准……」并一直阻塞；MCP 调用同样阻塞，直到有结果。
- **审批窗口：** 扩展打开审批窗口（已经打开时，新请求在窗口里排队），同时更新工具栏角标。窗口显示：
  - 浏览器实例的名称。
  - 请求方的标签。这个标签是请求方自己报的，窗口上写明「未经验证」。
  - 收到请求的时间，以及距离自动拒绝的倒计时。
  - **影响摘要：** 例如「将删除 3 项：2 个书签、1 个文件夹（含 37 个书签、4 个子文件夹）」。
  - **逐项列表：** 书签显示标题、URL 和所在文件夹的路径；文件夹显示它包含的书签数和子文件夹数，可以展开预览内容。
  - 「删除后无法通过 sctl 撤销」的提示。
  - 「拒绝」和「删除 N 项」两个按钮。窗口打开时，焦点默认在「拒绝」上。
- **结果：**
  - **批准：** 扩展重新核对当前要删除的全部书签和文件夹，与窗口里展示的是否完全一致。
    - 一致时全部删除，返回删除的书签数和文件夹数。
    - 如果其间有项目被删除、移出，或者文件夹里多出了新内容，就一个都不删，返回 `CONFLICT`，退出码 3。
  - **拒绝：** 返回 `USER_REJECTED`，退出码 1。
  - **关闭窗口：** 窗口里排队的所有请求都视为拒绝。窗口里写明这一点。
  - **5 分钟内无人处理：** 返回 `OPERATION_EXPIRED`，退出码 2。窗口里这个请求显示为「已超时」。
  - **请求方取消（Ctrl-C 或 MCP 取消）：** 请求作废，窗口里这个请求显示为「请求方已取消」，只能关闭。
  - **浏览器与 daemon 断开：** 所有待审批请求作废，调用方得到 `OPERATION_EXPIRED`。
- **队列：** 多个请求同时待审批时，窗口按到达顺序显示「1 / 3」和翻页按钮，每个请求单独批准或拒绝。
- **状态持久化：** 待审批状态保存在扩展的存储里，所以 MV3 的 service worker 休眠后，审批仍然有效。
- **弹窗入口：** 有待审批请求时，工具栏图标显示数量角标，弹窗的已连接状态里多一行「N 个待批准请求 · 查看」，点击后把审批窗口带到前台。

**视觉：**

- 审批窗口沿用第 1 期的「同门」风格和配色，支持浅色、深色、中文、英文，跟随弹窗里的设置。
- **窗口尺寸：** 440×680。标题、元信息、影响摘要、撤销提示和底部按钮固定显示，只有逐项列表滚动。
- **紧凑行：** 一次请求超过 10 项时，列表改用紧凑行：每项只显示标题和所在文件夹，URL 放在悬停提示里。这样同时能看到更多项。
- **工具栏角标：** 固定使用琥珀色底加深色字（`#E9A93A` / `#0A1622`），不随主题变化。后台脚本拿不到当前配色方案，而这组颜色在浅色和深色工具栏上都醒目。
- **撤销提示：** 除「删除后无法通过 sctl 撤销」外，再加一句「只在你确认这是自己发起的请求时批准」。书签标题可能被网页写成「管理员已批准此操作」之类的诱导文字，这句提示针对的就是这种情况。
- 破坏性按钮用危险色，文字对比度不低于 4.5:1，所有控件都能用键盘操作并有可见的焦点框。
- 执行中的转圈动画在系统开启「减少动态效果」时停止。
- 本地 mockup 在 `.dev-kit/artifacts/2026-09-29-browser-data/mockups/`（已 gitignore）。以上文字描述是约束性要求，mockup 只作示意。

## 协议与权限

- **协议：**
  - 新方法、新错误码都先加进 `protocol.json`，标注归属为 `browser`，再按对端生成代码。
  - `schemaVersion` 不变，给 ScriptCat 的生成文件逐字节不变。
  - 列表类方法声明 `mergeField`。
  - 每个方法标注破坏级别（L0 / L1 / L2），daemon 按这个标注执行确认检查。
- **错误码：**
  - 新增 `CONFIRMATION_REQUIRED` 和 `UNSUPPORTED`，退出码都是 3。
  - `USER_REJECTED` 和 `PAYLOAD_TOO_LARGE` 的 peers 加上 `browser`。
- **权限：**
  - 清单新增 `bookmarks`、`readingList`、`tabGroups`、`history`、`sessions`、`downloads`、`cookies`、`browsingData`、`management`，以及主机权限 `<all_urls>`（读写 Cookie 需要）。
  - 通过「加载已解压的扩展程序」更新时，新权限直接生效。
- **与第 3 期的关系：** 第 3 期同样会修改清单和 `protocol.json`，两期各自只加自己的条目；后合并的一期负责解决冲突并重新生成代码。

## 已知的未验证事实

以下三条来自 Chrome 公开文档，还没在本仓库验证过。实现计划的第一个任务是写一个真机探针来确认它们。

- **卸载确认框：** 调用 `chrome.management.uninstall` 卸载其他扩展时，Chrome 是否总会弹出自带的确认框，以及能否在没有用户手势的情况下调用。
  - 如果调用需要用户手势，`extensions uninstall` 就无法由 sctl 发起，需要回到本 spec 重新决定，不能在实现里自行改变行为。
- **Edge 的阅读列表：** Edge 是否提供 `chrome.readingList`。这只影响 Edge 上是否返回 `UNSUPPORTED`，规则本身已经定好。
- **Cookie 的来源范围：** 用 `<all_urls>` 主机权限时，`chrome.cookies.getAll` 能否返回所有站点的 Cookie，包括分区 Cookie。
  - 分区 Cookie 如果拿不到，就在文档里写明，不做额外处理。

## 不在本期范围

- **不管理的数据：** 密码、自动填充、同步设置、其他设备上的会话、浏览器设置、站点权限、搜索引擎。
- **不支持的操作：**
  - 下载时覆盖已有文件，以及弹出「另存为」对话框。
  - 撤销删除：书签删除后无法通过 sctl 恢复。
- **审批范围：** 除删除书签以外的人工审批，以及按领域或站点授权。
- **页面内容：** 页面自动化相关的一切，属于第 3 期。

## 文档

以下文档在本期一并修改，各自只改自己负责的内容：

- **[architecture.md](../architecture.md)：** 破坏级别的检查点（daemon 检查，扩展再检查），以及浏览器侧审批窗口在进程模型中的位置。
- **[protocol.md](../protocol.md)：**
  - 新方法表，以及每个方法的破坏级别。
  - `CONFIRMATION_REQUIRED`、`UNSUPPORTED`，以及 `USER_REJECTED` 扩展到浏览器。
  - 浏览器侧审批的时序，沿用 §5。
- **[threat-model.md](../threat-model.md)：**
  - 持有控制令牌的进程可以读取全部书签、历史和 Cookie 值（即所有站点的登录态），可以执行 L0 和 L1 操作。
  - L2 只保护书签删除。
  - 审批窗口里的请求方标签是自报的。
- **[mcp.md](../mcp.md) 和两份 README：** 新命令，10 个按领域合并的 MCP 工具，破坏级别说明。

## 测试决策

| 测试层 | 验证什么 | 可参照的现有测试 |
|---|---|---|
| 扩展单元测试（Vitest，mock `chrome.*`） | 每个领域的处理逻辑：参数到 `chrome.*` 调用的映射，`NOT_FOUND`、`INVALID_REQUEST`、`UNSUPPORTED` 的翻译，全有或全无；L1 在扩展侧的二次确认检查；书签删除的审批状态机（批准、拒绝、关闭窗口、超时、请求方取消、断开、执行前内容已变化返回 `CONFLICT`）以及待审批状态在 service worker 重启后的恢复 | `extension/src/handlers/index.test.ts` |
| 审批窗口和弹窗组件测试（Vitest + Testing Library） | 审批窗口的每个状态在中英文下都能正确渲染；默认焦点在「拒绝」上；不可信文本按纯文本渲染；弹窗里待审批入口的显示条件 | `extension/src/popup/App.test.ts` |
| 控制 API 测试 | L1 缺少确认时返回 `CONFIRMATION_REQUIRED`，并且不向扩展转发；列表类方法的汇总；L2 调用阻塞直到得到结果 | `internal/daemon/controlapi/routing_test.go` |
| 命令行测试，连接模拟的控制服务 | 每个新子命令的参数校验和输出；`--yes` 的传递；时间参数的解析；退出码 0、1、2、3 | `internal/cli/tabs_test.go` |
| MCP 服务测试 | 10 个领域工具已注册，描述是静态文本；`action` 能分发到对应的方法；`confirm` 和可选的 `browser` 能正确转发 | `internal/client/mcpserver/mcpserver_test.go` |
| 协议生成检查 | 新方法和错误码只出现在 Go 和浏览器扩展的生成代码里，给 ScriptCat 的那套逐字节不变；每个方法都标注了破坏级别 | `make protocol-check`、`internal/protocolgen/generate_test.go` |
| 一次性真机验证，放在 `e2e/scratch/`，不提交 | 在隔离的 Chrome 配置里预置虚构的书签、历史、下载和 Cookie，加载本分支构建的扩展和 ScriptCat，都配对到临时 daemon，检查以下内容：<br>• 每个领域至少跑一遍读和写<br>• L1 不加 `--yes` 被拒、加了之后成功<br>• 书签删除审批的批准、拒绝、关闭窗口、超时、Ctrl-C 取消，以及批量删除<br>• 卸载扩展时 Chrome 自己的确认框<br>• MCP 工具<br>• ScriptCat 全程正常<br>全程绝不使用用户日常的浏览器配置 | [verification.md](../verification.md) |

审批窗口的视觉效果不写自动化测试，收尾时截图检查。截图要覆盖两种外观、两种语言、焦点框、减少动态效果。

## 未决问题

<!-- 批准前必须为空。 -->
