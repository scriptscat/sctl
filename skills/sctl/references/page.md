# 页面自动化与调试

## 页面自动化 `sctl page`

公共参数（每个 `page` 子命令都可用）：

- `--tab <id>`：目标标签页，默认为最近聚焦窗口的活动标签页；
- `--browser <name|ID>`：目标浏览器；
- `--timeout <dur>`：默认 10s，`goto/back/forward/reload/screenshot` 默认 30s；
- `--activate`：先把标签页设为所在窗口的活动标签页，但不聚焦窗口。

后台标签页也能直接操作。

### 读页面

| 命令 | 说明 |
|---|---|
| `page snapshot [--root <ref\|css>]` | 可访问性快照，每行 `- role "name" [states] [ref=eN]`。新快照替换旧 ref；导航、元素移除、调试器断开后 ref 失效（`STALE_REF`）。超过 1 MiB 返回 `PAYLOAD_TOO_LARGE`，用 `--root` 取子树 |
| `page eval '<expr>' [<ref>]` | 在页面主世界执行表达式，Promise 会被等待。可序列化的结果打印为 JSON。带 ref 时表达式须是函数，如 `'el => el.textContent'`，在元素所在 frame 里执行（含跨源 iframe） |
| `page screenshot [<ref>\|--selector css] [--full] [-f file] [--format png\|jpeg] [--quality N]` | 默认截视口，`--full` 截整页，给 ref 或 selector 时截元素。图片写文件（默认 `screenshot-<tabId>-<时间>.<ext>`），只打印路径。15 秒没出图返回 `PAGE_HIDDEN`，加 `--activate` 重试；超过 4 MiB 改用 jpeg 或只截视口 |

### 交互

目标可以是快照 ref（`e5`，能指向跨源 iframe 里的元素），也可以是 `--selector <css>`（只在主文档里找，必须恰好匹配一个，匹配多个时立即报 `TARGET_AMBIGUOUS`，一个都没有时会等待）。动作前自动滚动到可见，并等待元素可操作；超时时错误信息会写出最后一个没满足的条件。

| 命令 | 说明 |
|---|---|
| `page click <ref> [--button left\|right\|middle] [--count 2] [--modifiers Shift,...]` | 可信鼠标点击。触发导航时会等到 DOMContentLoaded；打开新标签页时打印它的 ID |
| `page fill <ref> <text>` | 清空后填入，触发 input/change 事件；空串表示清空。复选框、单选框用 click，文件输入用 upload |
| `page select <ref> <value>...` | 按 value 或可见文本选 `<select>` 选项 |
| `page type <text>` | 往当前焦点元素逐键输入，换行会按 Enter；需要先用 click 或 fill 聚焦 |
| `page press <key>` | Playwright 语法：`Enter`、`Tab`、`Escape`、`Control+A`、`Shift+Tab`；`ControlOrMeta+A` 跨平台全选 |
| `page hover <ref>` | 鼠标移到元素中心 |
| `page scroll <ref>` 或 `page scroll --dx N --dy N` | 把元素滚动到可见，或用滚轮滚动视口 |
| `page upload <ref> <file>...` | 设置文件输入的文件（可以是隐藏的输入框）；相对路径按当前目录解析 |

### 导航与等待

| 命令 | 说明 |
|---|---|
| `page goto <url> [--wait load\|domcontentloaded\|networkidle]` | 导航并等待加载状态。HTTP 404/500 不算失败，会报告状态码；网络错误返回 `NAVIGATION_FAILED` |
| `page back` / `page forward` / `page reload` | 同样支持 `--wait`；没有可后退/前进的记录时报 `NOT_FOUND` |
| `page wait --text T \| --gone T \| --selector S \| --selector-gone S \| --url P \| --load STATE` | 每次只给一个条件，默认 10s |

### 弹框与断开

- **`page dialog accept [--text 输入] | dismiss`**：处理 alert、confirm、prompt、beforeunload。弹框打开时，除了 detach 以外的 page 命令都返回 `DIALOG_OPEN`，错误里会写出弹框类型和文字（不可信）。
- **`page detach [--all]`**：断开调试器、收起提示条；有弹框时先关闭它。标签页没附加时也成功。
- **`PAGE_UNRESPONSIVE`**：附加时 5 秒内没有响应，常见原因是之前留下的弹框。用 `page reload` 或 `page goto` 恢复。

## 调试 `sctl debug`

调试器附加期间，daemon 在内存里记录 console 和网络请求，每个标签页最多 1000 条 console 记录和 1000 个请求。调试器断开时记录清空。网络请求只从附加时开始记录。console 会在附加时由 Chrome 回放当前文档已有的消息。弹框打开时 debug 命令照常可用。

| 命令 | 说明 |
|---|---|
| `debug start` | 开始录制：调试器一直附加，不受 5 分钟空闲断开影响，直到 `debug stop` 或 60 分钟没有任何 debug 命令。适合让用户先复现问题、再回来查 |
| `debug stop [--all]` | 停止录制；记录保留，5 分钟空闲后断开时清空 |
| `debug status` | 已附加的标签页：是否在录制、剩余时间、记录条数与丢弃条数 |
| `debug console [--level debug\|info\|warning\|error] [--source console\|exception\|browser] [--text 子串] [--limit N] [--after 游标]` | console 消息、未捕获异常、未处理的 Promise 拒绝，以及 Chrome 自己的消息（CSP 违规、资源加载失败）。`-o json` 里有堆栈和 next 游标 |
| `debug network [--url 子串] [--method M] [--status 404\|4xx] [--type xhr\|fetch\|document…] [--failed] [--limit N] [--after 游标]` | 请求列表：重定向的每一跳各一条；进行中的显示 pending，失败的显示 failed |
| `debug request <ID> [--body]` | 单个请求的头、体、各阶段耗时、远端地址。**头和体不打码**，Cookie、Authorization 原样显示，给用户展示前注意隐私。`--body` 还返回响应体（文本原样、二进制 base64，截断到 1 MiB）；取不到时写明原因，退出码仍为 0 |
| `debug clear` | 清空记录，不断开调试器 |

增量查询：先 `-o json` 拿结果里的 `next` 游标，下次传 `--after <next>`。游标属于已清空或重新附加之前的缓存时，会从最早的记录重新开始（`cursorReset`）。

典型排查：

```sh
sctl debug start --tab $TAB          # 附加并开始录制
# 让用户在页面上复现问题，或用 page 命令复现
sctl debug console --tab $TAB --level error
sctl debug network --tab $TAB --status 4xx
sctl debug network --tab $TAB --failed
sctl debug request 17 --tab $TAB --body
sctl debug stop --tab $TAB
```
