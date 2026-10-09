# 原始 CDP 与 Playwright/Puppeteer 端点

## 单条命令 `sctl cdp send`

```sh
sctl cdp send <Domain.method> [--params '<JSON 对象>'] [--tab N] [--browser B] [--timeout 10s]
sctl cdp send Page.getNavigationHistory --tab $TAB
sctl cdp send Runtime.evaluate --params '{"expression":"document.title","returnByValue":true}' --tab $TAB
```

- **发送方式**：走 sctl 自己的调试会话，只发给顶层页面（不发给跨进程 iframe），不返回事件。附加、排队、空闲断开都与 `page` 命令相同。
- **结果**：Chrome 的原始结果，外加 `tabId` 和 `contentTrust`；默认是缩进的 JSON。
- **错误**：
  - 方法名不是 `Domain.method` 形式，或 `--params` 不是 JSON 对象：`INVALID_REQUEST`。
  - Chrome 拒绝或不认识这条命令：`INVALID_REQUEST`，带 Chrome 自己的错误。
  - 结果超过 4 MiB：`PAYLOAD_TOO_LARGE`。
  - 超时：`TIMEOUT`。
- **会被拒绝、不发给 Chrome 的命令**：`Page.disable`、`Runtime.disable`、`Network.disable`、`Log.disable`、`Emulation.setFocusEmulationEnabled`、`Target.setAutoAttach`、`Target.detachFromTarget`。
- **其余命令的副作用由你负责还原**：
  - 持续生效的设置（如 `Emulation.setDeviceMetricsOverride`、`Network.setExtraHTTPHeaders`）会影响之后的 page 命令，用完要撤销。
  - `Fetch.enable` 之后，这个标签页的所有请求都会挂住，因为原始命令收不到事件，没有人处理被暂停的请求。用 `Fetch.disable` 恢复。
  - `Debugger.enable` 加 `Debugger.pause` 会冻结页面，用 `Debugger.resume` 恢复。
  - 也可以 `sctl page detach --tab N`，之后重新附加。
- **弹框期间照常发送**，所以可以用 `Page.handleJavaScriptDialog`。弹框期间被 Chrome 阻塞的命令会等到超时。

先查是否已有 `page`/`debug` 命令能做到；只有缺失的能力才用 `cdp send`。

## 端点（给 Playwright / Puppeteer）

```sh
sctl cdp endpoint            # 创建或显示：打印两个地址、是否有客户端、何时失效
sctl cdp endpoint -o json    # 取 .endpoint.httpUrl / .endpoint.wsUrl
sctl cdp status              # 查看
sctl cdp close               # 立即失效并断开客户端；没有端点时也成功
```

**安全**：

- 地址里带随机密钥，地址本身就是凭据。连上的客户端不需要审批，就能完全控制这个浏览器所有可附加的标签页（读页面、读 Cookie、执行脚本、以用户身份发请求）。
- 不要把地址写进会被提交或分享的文件，也不要发到外部。
- 用完运行 `sctl cdp close`。

**连接**：Playwright 用 `httpUrl`，使用浏览器自带的上下文（用户的配置和登录状态），不要新建上下文：

```js
const { chromium } = require("playwright");
const browser = await chromium.connectOverCDP(process.env.SCTL_CDP_HTTP); // http://127.0.0.1:8643/cdp/<secret>
const context = browser.contexts()[0];
const page = await context.newPage();          // 或从 context.pages() 里挑一个
await page.goto("https://example.com");
await browser.close();                         // 只断开：浏览器和标签页都保留
```

Puppeteer 用 `wsUrl`，并且必须传 `defaultViewport: null`，否则会改变用户标签页的尺寸：

```js
const puppeteer = require("puppeteer-core");
const browser = await puppeteer.connect({ browserWSEndpoint: process.env.SCTL_CDP_WS, defaultViewport: null });
const [page] = await browser.pages();
await page.goto("https://example.com");
await browser.disconnect();
```

**行为**：

- **独占**：同一时间只能有一个客户端，第二个连接得到 409。客户端连着时，这个浏览器上的 `sctl page`/`debug`/`cdp send` 返回 `ENDPOINT_CONNECTED`；`tabs`、书签等其他命令照常可用。
- **连上时**：sctl 先结束录制、关闭已知的弹框，再断开自己附加的标签页，把它们交给客户端。
- **客户端看到的标签页**：所有可附加的标签页，不含 `chrome://`、扩展页和应用商店；连接期间新开的标签页也会出现。后台标签页开了焦点模拟，行为与前台一致。
- **断开时**（包括 `browser.close()`、进程退出、`cdp close`）：sctl 关闭弹框、断开调试器，标签页（包括客户端新开的）全部保留，sctl 命令恢复可用。失效之前，同一个地址可以再次连接。
- **失效**：`cdp close`、daemon 退出、连续 60 分钟没有客户端、浏览器被 `sctl browsers forget`。
- **不支持**（返回点名该功能的 CDP 错误）：新建浏览器上下文（`browser.newContext()` / `createBrowserContext()`）、授予权限、窗口尺寸与位置、忽略证书错误、Service Worker 目标。
- **其他限制**：
  - 下载事件收不到，下载按浏览器自己的设置进行。
  - Chrome 125 上读取 Cookie 会被 Chrome 拒绝，较新的版本可以。
  - 超过 4 MiB 的单个事件会被丢弃。
