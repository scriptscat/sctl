# 浏览器数据管理

每组都接受 `--browser <name|ID 前缀>`。读取结果建议用 `-o json`；列表默认 `--limit 100`（范围 1–1000）。

**多项操作是原子的**：`tabs move/pin/mute/reload`、`windows close`、`bookmarks move` 等要么全部成功，要么都不生效。

时间参数（`--since/--until`）可以是 RFC 3339 时间，也可以是「多久以前」，如 `7d`、`12h`、`30m`。

## 标签页、窗口、标签组

| 命令 | 说明 |
|---|---|
| `tabs list [--window W]` | 列出标签页：tabId、windowId、active、pinned、groupId、title、url |
| `tabs open <url> [--background] [--window W]` | 打开新标签页，打印 tabId。默认激活；`--background` 不激活 |
| `tabs close <tabId>...` | 关闭（`recent restore` 可以恢复） |
| `tabs activate <tabId>` | 激活并聚焦其窗口 |
| `tabs move <tabId>... [--window W] [--index I]` | 移动到窗口和位置，`-1` 表示末尾 |
| `tabs pin/unpin/mute/unmute <tabId>...` | 固定/取消固定、静音/取消静音 |
| `tabs reload <tabId>... [--bypass-cache]` | 重新加载 |
| `tabs duplicate <tabId>` | 复制，打印新的 tabId |
| `windows list` / `windows open [<url>...] [--state S]` / `windows close <id>...` / `windows focus <id>` / `windows state <id> normal\|minimized\|maximized\|fullscreen` | 窗口 |
| `groups list [--window W]` / `groups create <tabId>... [--title T] [--color C]` / `groups add <groupId> <tabId>...` / `groups edit <groupId> [--title] [--color] [--collapse\|--expand]` / `groups ungroup <tabId>...` | 标签组。颜色可选 grey、blue、red、yellow、green、pink、purple、cyan、orange |

## 书签与阅读列表

| 命令 | 说明 |
|---|---|
| `bookmarks list [--folder ID] [--recursive]` | 列出文件夹的子项，默认列顶层文件夹 |
| `bookmarks search <query>` | 按标题和 URL 搜索，显示文件夹路径 |
| `bookmarks add <url> [--title] [--folder ID] [--index I]` | 默认添加到「其他书签」 |
| `bookmarks mkdir <title> [--folder ID]` | 新建文件夹 |
| `bookmarks move <id>... --folder ID [--index I]` | 移动 |
| `bookmarks edit <id> [--title] [--url]` | 修改 |
| `bookmarks rm <id>...` | 删除（文件夹连同内容），**需要用户在浏览器里批准**，命令会阻塞等待 |
| `reading-list list [--read\|--unread]` / `add <url> [--title]` / `mark-read <url>... [--unread]` / `rm <url>... --yes` | 阅读列表 |

## 历史、最近关闭、下载

| 命令 | 说明 |
|---|---|
| `history search [text] [--since] [--until]` | 从新到旧搜索 |
| `history visits <url>` | 一个 URL 的每次访问 |
| `history rm <url>... --yes` | 删除这些 URL 的全部访问记录 |
| `history clear [--since] [--until] --yes` | 不给时间范围时清除**全部**历史，务必先和用户确认范围 |
| `recent list` / `recent restore [<sessionId>]` | 最近关闭的标签页和窗口（Chrome 最多保留 25 个）；不给 sessionId 时恢复最近关闭的那个 |
| `downloads list [--state in_progress\|complete\|interrupted] [--query 文本]` | 下载列表 |
| `downloads start <url> [--filename 相对路径]` | 下载到浏览器的默认下载目录，不覆盖已有文件 |
| `downloads pause/resume/show <id>` | 暂停、继续；`show` 在系统文件管理器里显示文件 |
| `downloads cancel <id> --yes` / `erase <id>... --yes` / `delete-file <id> --yes` | 取消下载；`erase` 只移除列表记录；`delete-file` 删除磁盘上的文件 |

## Cookie 与浏览数据

| 命令 | 说明 |
|---|---|
| `cookies list [--url U\|--domain D] [--name N]` | 列出 Cookie（含分区 Cookie）。不加过滤条件时列出所有站点 |
| `cookies get --url U --name N` | 不存在时退出码 3 |
| `cookies set --url U --name N --value V [--domain] [--path] [--expires RFC3339] [--secure] [--http-only] [--same-site lax\|strict\|no_restriction]` | 不给 `--expires` 时为会话 Cookie |
| `cookies rm --url U --name N --yes` | 删除一个 |
| `cookies clear (--domain D \| --all) --yes` | 删除某个域及其子域，或全部 Cookie |
| `browsing-data clear --types t1,t2 [--since] [--origin https://x]... --yes` | 可选类型：cache、cacheStorage、cookies、downloads、fileSystems、formData、history、indexedDB、localStorage、serviceWorkers、webSQL。`--origin` 只对 cache、cacheStorage、cookies、fileSystems、indexedDB、localStorage、serviceWorkers、webSQL 有效。不管理密码 |

Cookie 值是敏感信息：只在用户需要时展示，不要写进日志或文件。

## 扩展

| 命令 | 说明 |
|---|---|
| `extensions list` | 已安装的扩展和应用 |
| `extensions enable <id>` | 启用 |
| `extensions disable <id> --yes` | 不能禁用 sctl Browser 自己和企业策略安装的扩展。禁用 ScriptCat 会使它与 daemon 断开 |
| `extensions uninstall <id>` | **需要用户先在 sctl Browser 的审批窗口批准，再在 Chrome 自己的确认框里确认** |

## 浏览器实例

| 命令 | 说明 |
|---|---|
| `browsers` / `browsers list` | 已配对的实例：name、ID、online、产品与版本 |
| `browsers forget <name\|id>` | 删除该实例的配对密钥并断开它，之后需要重新配对。只在用户明确要求时执行 |
