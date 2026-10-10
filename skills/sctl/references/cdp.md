# Raw CDP and the Playwright/Puppeteer endpoint

## One command: `sctl cdp send`

```sh
sctl cdp send <Domain.method> [--params '<JSON object>'] [--tab N] [--browser B] [--timeout 10s]
sctl cdp send Page.getNavigationHistory --tab $TAB
sctl cdp send Runtime.evaluate --params '{"expression":"document.title","returnByValue":true}' --tab $TAB
```

Reach for it only when no `page` or `debug` command does the job.

- **How it is sent:** on sctl's own debugger session, to the top-level page only (not to cross-process iframes),
  and the events a command causes are not returned. Attaching, queuing and the idle detach work as for `page`.
- **Result:** Chrome's raw result object plus `tabId` and `contentTrust`, as indented JSON.
- **Errors:**
  - a method that is not `Domain.method`, or `--params` that is not a JSON object → `INVALID_REQUEST`;
  - Chrome rejecting or not knowing the command → `INVALID_REQUEST` carrying Chrome's own error;
  - a result over 4 MiB → `PAYLOAD_TOO_LARGE`; running out of time → `TIMEOUT`.
- **Refused without being sent**, because they would break state sctl depends on: `Page.disable`,
  `Runtime.disable`, `Network.disable`, `Log.disable`, `Emulation.setFocusEmulationEnabled`,
  `Target.setAutoAttach`, `Target.detachFromTarget`.
- **Everything else is sent as is, and undoing its effects is your job:**
  - A persistent setting such as `Emulation.setDeviceMetricsOverride` or `Network.setExtraHTTPHeaders` keeps
    affecting later page commands. Restore it when you are done.
  - After `Fetch.enable` every request of the tab hangs: events are not returned, so nothing answers the paused
    requests. Recover with `Fetch.disable`.
  - `Debugger.enable` plus `Debugger.pause` freezes the page. Recover with `Debugger.resume`.
  - Or run `sctl page detach --tab N` and let the next command attach again.
- **Dialogs do not block it**, so `Page.handleJavaScriptDialog` works. A command Chrome itself blocks while a
  dialog is open waits until the time limit.

## Endpoint for Playwright and Puppeteer

```sh
sctl cdp endpoint            # create or show: both addresses, client state, expiry
sctl cdp endpoint -o json    # read .endpoint.httpUrl and .endpoint.wsUrl
sctl cdp status              # show it, or say there is none
sctl cdp close               # expire it now and disconnect the client; fine when there is none
```

**The address is a password.** It carries a random secret, and whoever has it gets unapproved, full control of
every tab Chrome lets a debugger attach to in that browser: reading pages and cookies, running scripts, sending
requests as the signed-in user. Pass it to the script through an environment variable, keep it out of committed
or shared files, and run `sctl cdp close` when the work is done.

With Playwright, connect to `httpUrl` and use the browser's own context — the user's profile and logins — instead
of creating one:

```js
const { chromium } = require("playwright");
const browser = await chromium.connectOverCDP(process.env.SCTL_CDP_HTTP); // http://127.0.0.1:8643/cdp/<secret>
const context = browser.contexts()[0];
const page = await context.newPage();          // or pick one of context.pages()
await page.goto("https://example.com");
await browser.close();                         // disconnects only: the browser and every tab stay open
```

With Puppeteer, connect to `wsUrl` and pass `defaultViewport: null`, or it resizes the user's tabs to its default
viewport:

```js
const puppeteer = require("puppeteer-core");
const browser = await puppeteer.connect({ browserWSEndpoint: process.env.SCTL_CDP_WS, defaultViewport: null });
const [page] = await browser.pages();
await page.goto("https://example.com");
await browser.disconnect();
```

What to expect:

- **Exclusive.** One client at a time; a second connection gets HTTP 409. While a client is connected,
  `sctl page`, `debug` and `cdp send` on that browser fail with `ENDPOINT_CONNECTED`; `tabs`, `bookmarks` and the
  other groups keep working.
- **On connect,** sctl ends its recordings, dismisses dialogs it knows about and detaches its own tabs, then hands
  the tabs to the client.
- **Visible tabs:** every attachable tab — not `chrome://` pages, extension pages or the Chrome Web Store —
  including ones opened while connected. Attached tabs get focus emulation, so background tabs behave like the
  active one.
- **On disconnect** (`browser.close()`, the process exiting, `sctl cdp close`), sctl dismisses open dialogs and
  detaches; every tab stays open, including ones the client opened, and sctl's own commands work again. The same
  address can reconnect until it expires.
- **Expiry:** `sctl cdp close`, the daemon exiting, 60 minutes without a connected client, or
  `sctl browsers forget`.
- **Not supported** (answered with a CDP error naming the feature): new browser contexts
  (`browser.newContext()`, `createBrowserContext()`), granting permissions, window size and position, ignoring
  certificate errors, Service Worker targets.
- **Other limits:** no download events (downloads follow the browser's own settings); on Chrome 125 Chrome itself
  refuses cookie reads; a single event over 4 MiB is dropped.
