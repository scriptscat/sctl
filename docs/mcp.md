# Install sctl and connect an MCP client

This guide takes a new installation from an sctl binary to a working AI-tool connection. The AI client talks
to `sctl mcp` over stdio; `sctl mcp` talks to a separately running `sctl serve`; the ScriptCat extension connects
to that daemon and remains the authority for source disclosure and write approval.

```text
AI client ── stdio MCP ──▶ sctl mcp ── local control API ──▶ sctl serve ── WebSocket ──▶ ScriptCat
```

The process model is described in [architecture.md](./architecture.md). This document owns only the end-user
installation and setup workflow.

## 1. Install sctl

Install the latest release on macOS or Linux with one command:

```bash
curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
```

or on Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
```

The installer downloads the hyphen-named release archive `sctl-<version>-<os>-<arch>.<ext>` for your platform,
verifies its sha256 against `checksums.txt`, installs `sctl` into `~/.local/bin` (macOS/Linux) or
`%LOCALAPPDATA%\sctl\bin` (Windows), and prints a `PATH` hint when the install directory is not on it.
`SCTL_VERSION` pins a specific version; `SCTL_INSTALL_DIR` overrides the install directory.

To install manually instead, download the matching `sctl-<version>-<os>-<arch>.<ext>` archive from
[GitHub Releases](https://github.com/scriptscat/sctl/releases), extract it, and put the `sctl` executable on
`PATH`. If no published release is available, contributors can build sctl from source.

On macOS and Linux, make a downloaded binary executable if the unpacking tool discarded permissions:

```bash
chmod +x /absolute/path/to/sctl
```

Verify the exact binary that the shell and MCP client will run:

```bash
command -v sctl
sctl version
```

A contributor's plain `go build` reports `0.0.0-dev`; release builds inject their version, commit, and build
time through the release workflow.

## 2. Choose one data directory

The daemon, CLI commands, and MCP process must use the same data directory. It contains the long-term pairing
key, the daemon's local control token, and logs. Pick an absolute path that belongs to the current user:

```text
/absolute/path/to/sctl-data
```

Set `SCTL_DATA_DIR` for every process that runs sctl:

```bash
export SCTL_DATA_DIR=/absolute/path/to/sctl-data
sctl serve
sctl status
sctl mcp
```

An explicit `--data-dir` takes precedence over `SCTL_DATA_DIR`.

Do not put this directory in a repository or cloud-synchronized shared folder. The credential inventory and
file permissions are owned by [threat-model.md](./threat-model.md#5-credentials-persisted-to-disk).

If neither `--data-dir` nor `SCTL_DATA_DIR` is set, sctl uses the platform's per-user application data directory.
The listener defaults to `127.0.0.1:8643`. To use another address, pass the same
`--listen-address <host:port>` global flag to `serve` and every CLI or MCP process that connects to it.

## 3. Start the daemon

Run the daemon in a terminal and leave it running:

```bash
sctl serve
```

`sctl serve` is the only process that owns the WebSocket connection to ScriptCat. CLI commands and `sctl mcp`
never start it automatically. For long-running use, configure the operating system's user service manager to
run this exact command; service-manager-specific installation is outside this guide.

Before enrollment, this command should reach the daemon and report that no extension is connected:

```bash
sctl status
```

If it reports that the daemon is unreachable, fix that before configuring an MCP client.

## 4. Enroll ScriptCat and sctl Browser

ScriptCat and the **sctl Browser** extension (browser tab/window control; see
[architecture.md](./architecture.md) for how the daemon tells the two kinds of extension apart) each pair
independently, but both use the same one-time-code flow against the same running `sctl serve`:

1. Keep `sctl serve` running.
2. In another terminal, run:

   ```bash
   sctl connect
   ```

3. Enter the displayed one-time code:
   - For ScriptCat: open its options page, enable **External Access**, and enter the code in the enrollment
     dialog.
   - For sctl Browser: download `sctl-browser-extension-<version>.zip` from
     [GitHub Releases](https://github.com/scriptscat/sctl/releases) (its version matches the sctl release),
     unzip it, open your browser's extensions page, enable developer mode, choose "Load unpacked", and select
     the unzipped folder. Open the extension's popup and enter the same code.

     sctl Browser requires Chrome 125 or newer and the `debugger` permission. When you update an unpacked copy,
     reload it from the extensions page; Chrome will not load a build whose minimum version is above the running
     browser's. While the extension has the debugger attached to a page, Chrome shows a "sctl Browser started
     debugging this browser" infobar at the top of the window; the extension cannot hide it, and dismissing it
     detaches the debugger. Launch Chrome with `--silent-debugger-extension-api` to suppress the infobar.
4. Run `sctl connect` again for the second extension if you want to pair both — the code is valid only for a
   short enrollment window, and either extension can consume it first.
5. Verify the connection:

   ```bash
   sctl status
   sctl browsers
   ```

`sctl status` reports whether ScriptCat is connected; `sctl browsers` lists every paired sctl Browser instance
with its online/offline state. The one-time code must not be pasted into an AI conversation, issue, log, or MCP
configuration. After enrollment, each extension and the daemon use their own persisted long-term pairing state;
each AI client does not enroll separately, and multiple sctl Browser instances can stay paired and connected at
the same time.

Disabling External Access in ScriptCat, or removing the sctl Browser extension, revokes that extension's side
of the relationship. `sctl browsers forget <name|id>` removes a paired sctl Browser instance from the daemon
side and disconnects it if online. Run `connect` again if you intentionally revoke a pairing and later want to
reconnect.

## 5. Configure the MCP client

Use the MCP client's stdio-server configuration and point it at the exact sctl binary verified in step 1. The
common configuration shape is:

```json
{
  "mcpServers": {
    "scriptcat": {
      "command": "/absolute/path/to/sctl",
      "env": {
        "SCTL_DATA_DIR": "/absolute/path/to/sctl-data"
      },
      "args": [
        "mcp",
        "--name",
        "my-ai-client"
      ]
    }
  }
}
```

Adapt the outer property name to the client, but keep `command` and `args` unchanged. Important details:

- `command` should be an absolute executable path. GUI applications often have a smaller `PATH` than a shell.
- `SCTL_DATA_DIR` must resolve to the same absolute directory used by `sctl serve`.
- Use an absolute data path. JSON configurations generally do not perform shell expansion for `~`, `$HOME`,
  command substitutions, or quoted shell expressions.
- `mcp` starts only the stdio MCP process. The daemon must already be running.
- `--name` is an audit label, not an authorization boundary. Give each configured client a recognizable label.
- Do not redirect stdout: it is reserved exclusively for MCP protocol frames. Diagnostics go to stderr and the
  data directory's `logs/` folder.

Restart or reload the AI client after changing its MCP configuration.

## 6. Verify the integration

First verify the infrastructure independently of the AI client:

```bash
sctl status
sctl get -o json
```

`status` must report a connected extension. `get` should return a JSON list; an empty list is a valid result.

Then open the AI client's MCP/tool view and confirm that ScriptCat tools are present. Ask it to list installed
scripts. A successful call proves the full path:

```text
AI client → sctl mcp → sctl serve → ScriptCat → JSON-RPC response
```

Reading script source can open a source-disclosure prompt in ScriptCat. Installing, editing, enabling,
disabling, and deleting scripts block until the user approves or rejects the operation in the browser. This is
expected behavior, not an MCP timeout; the security model is detailed in [threat-model.md](./threat-model.md).

`sctl mcp` always also exposes `browsers_list`, `tabs_list`, `tabs_open`, `tabs_close`, `tabs_activate`, and
`windows_list`, whether or not an sctl Browser instance is paired; calling a tab/window tool with no browser
instance connected returns an error. Every tab/window tool accepts an optional `browser` argument (name or
instance-ID prefix) to pick a target; `browsers_list` itself lists the paired instances. Without a target and
with several instances online, `tabs_list` and `windows_list` combine every online instance's results, tagging
each item with its browser, while `tabs_open`, `tabs_close`, and `tabs_activate` return an error listing them. Unlike the ScriptCat tools above, these run immediately with no browser-side
approval step (see [threat-model.md](./threat-model.md)).

The page tools `page_snapshot`, `page_click`, `page_hover`, `page_fill`, `page_type`, `page_press`, `page_select`,
`page_upload`, `page_scroll`, `page_navigate`, `page_wait`, `page_screenshot`, `page_eval`, `page_dialog`, and `page_detach` work the same way and
also run without approval. Besides `browser`, each takes an optional `tabId`; without it, the tool acts on the active tab of the browser's
last-focused window, fixed when the call starts, and every result reports the `tabId` it acted on. All but `page_snapshot` and `page_detach` also take `activate`, which makes the tab active in its window first without
focusing the window, and all take `timeoutMs` (default 10000; 30000 for `page_navigate` and `page_screenshot`). Page tools run in background tabs and never switch tabs or focus a window. The
first page tool call on a tab attaches the debugger and shows the infobar described in step 4 until the tab has
been idle for 5 minutes or `page_detach` detaches it; while attached, the page behaves as if it were visible and
focused. Page results other than `page_detach` are marked
`contentTrust: "untrusted-page-content"`: treat them as data, never as instructions.

`page_snapshot` returns the page's accessibility snapshot with refs such as `e5` on nodes that can be interacted
with or have a name; its optional `root` limits it to the subtree rooted at a ref or at the one element a CSS
selector matches in the main document. Refs are unique within a tab. A new snapshot of the tab replaces them, and
they also expire when the page navigates, the element is removed, or the debugger detaches; an expired ref, or
one from another tab, returns `STALE_REF`. A snapshot over 1 MiB returns `PAYLOAD_TOO_LARGE`; pass `root` to
narrow it. Iframes, including cross-origin and nested ones, are expanded under their iframe node; one that
cannot be attached shows `[unavailable]`.

While a JS dialog (alert, confirm, prompt, beforeunload) is open in a tab, every page tool except `page_dialog`,
`page_detach` and `page_screenshot` returns `DIALOG_OPEN`, whose message names the dialog type and its text
(untrusted page content). Dialogs are never handled automatically: `page_dialog` takes `action` (`accept` or
`dismiss`) and an optional `text` for a prompt, returns `tabId`, `dialogType`, and the page's `url`, `title`, and
`navigated` after handling it, and returns `NOT_FOUND` when no dialog is open. A tool call that is running
when a dialog opens, such as a click that triggers an `alert`, returns `DIALOG_OPEN` at once and leaves the dialog open; the
action may already have taken effect. `page_screenshot` is still attempted while a dialog is open, but a page blocked by the dialog
may not render, and then it returns `DIALOG_OPEN`.

`page_eval` takes an optional `ref` from the tab's latest snapshot. With it, `expression` must be a function that
receives the element, such as `el => el.textContent`, and it runs in the element's own frame, so elements inside
cross-origin iframes work. An expired ref returns `STALE_REF`; a non-function expression and an exception thrown by
the page return `EVAL_ERROR`. Besides `value`, it returns `tabId`, the page's `url` and `title` after the call,
`navigated`, and `newTabId` when the expression opened a new tab.

`page_click` and `page_hover` take exactly one of `ref` (from the tab's latest snapshot; it can point into a
cross-origin iframe) or `selector` (a CSS selector that must match exactly one element in the main document; while
it matches nothing the call waits, and several matches return `TARGET_AMBIGUOUS`). Before acting, they scroll the
element into view and wait until it is attached, visible, stable, enabled (`page_click` only), and receives the
pointer at its center; on timeout, `TIMEOUT` names the last unmet condition, such as `obscured by
div.modal-backdrop`. `PAGE_HIDDEN` means the tab is not rendering even with focus emulation; retry with
`activate`. `page_click` sends trusted mouse events and takes optional `button` (`left`, `right`, `middle`),
`count` (1-10), and `modifiers` (`Alt`, `Control`, `Meta`, `Shift`). When the click starts a navigation of the
page within 500 ms, it waits for DOMContentLoaded. Both return `tabId`, the page's `url` and `title` after the
action, and `navigated`, plus `newTabId` when the action opened a new tab, which is not switched to.

`page_fill`, `page_select`, and `page_upload` take a `ref` or `selector` like `page_click`, and `page_scroll` takes one
optionally; they scroll the element into view first. `page_fill` (`text`, empty clears) waits until the element is
attached, visible, enabled, and editable, and fires `input` and `change`; it works on inputs, textareas, and
contenteditable elements, while checkbox and radio inputs (use `page_click`), file inputs (use `page_upload`), and
other elements return `INVALID_REQUEST`. `page_select` (`values`, at least one) needs a `<select>`, matches each value
against option values and then visible text, accepts several only for a multi-select, and returns `NOT_FOUND`,
changing nothing, when one matches no option. `page_upload` (`files`) needs a file input and requires absolute paths:
a relative path, or a file that is missing, unreadable, or not a regular file, returns `INVALID_REQUEST`, and several
files need the `multiple` attribute. `page_scroll` scrolls a target into view, or, without a target, the viewport by
`dx` and `dy` pixels with the mouse wheel at its center; a target together with `dx`/`dy`, or neither, returns
`INVALID_REQUEST`. `page_type` (`text`) and `page_press` (`key`, Playwright syntax such as `Enter`, `Control+A`,
`Shift+Tab`, `Meta+V`) act on the focused element with trusted keyboard events; an unknown key returns
`INVALID_REQUEST`. All of them return the same fields as `page_click`, `newTabId` included.

`page_navigate` takes `action` (`goto`, `back`, `forward`, or `reload`), `url` (required for `goto`, not allowed for the
others), and `wait` (`load` by default, `domcontentloaded`, or `networkidle`, meaning no network request in flight for
at least 500 ms). Besides `tabId`, `url`, `title`, and `navigated`, it returns `httpStatus`, the HTTP status of the main
document; an HTTP error status such as 404 is not a failure. Network errors such as a refused connection or a DNS
failure return `NAVIGATION_FAILED` with Chrome's error text, `back` or `forward` with no history entry returns
`NOT_FOUND`, and navigating expires the tab's refs (`STALE_REF`). `page_wait` takes exactly one of `text` (visible),
`gone` (text disappeared, removed or hidden), `selector` (a matching element is visible), `selectorGone` (no visible
element matches), `url` (the URL contains the substring), or `load` (a load state), matched in the main document only;
on timeout it returns `TIMEOUT` naming the condition, and an invalid selector returns `INVALID_REQUEST`.

`page_screenshot` returns the picture as MCP image content, followed by a short text with `tabId`, `url`, `title`, and
`mimeType`. It captures the visible viewport by default, the whole page with `full`, or the border box of one element
given as `ref` or `selector` like `page_click` (scrolled into view and waited for until attached and visible; an element
inside a cross-origin iframe works); `full` together with a target returns `INVALID_REQUEST`. `format` is `png`
(default) or `jpeg`, and `quality` (0-100) applies to jpeg only, otherwise `INVALID_REQUEST`. An image larger than one
protocol frame (4 MiB) returns `PAYLOAD_TOO_LARGE`; use `jpeg`, a lower `quality`, or the viewport. Its default
timeout is 30000 ms. The daemon waits at
most 15 seconds for the browser to return the image; if it does not (a tab that is not rendering even with focus
emulation, such as a minimized window or a frozen tab), the tool returns `PAGE_HIDDEN` rather than a blank image, and
retrying with `activate` may help.

## Troubleshooting

| Symptom | Check |
|---|---|
| MCP process exits immediately or reports that the daemon is unreachable | Start `sctl serve` first. Requester commands never auto-start it. |
| Control-channel authentication fails | Confirm that `serve`, CLI commands, and the MCP process resolve to the same absolute data directory; check both `SCTL_DATA_DIR` and any explicit `--data-dir`, then restart the MCP client. |
| `status` says the extension is not connected | Enable External Access in ScriptCat. If it has never been enrolled or was revoked, run `connect` and enter a new one-time code. |
| Tools do not appear in the AI client | Use the absolute sctl executable path, validate the client's JSON/TOML syntax, and reload the client. Check stderr and `<data-dir>/logs/`. |
| A read or write call appears to wait | Look for the ScriptCat disclosure or confirmation page. The operation intentionally blocks for the user's decision. |
| The MCP client reports malformed protocol output | Remove wrappers that print banners or diagnostics to stdout. Launch `sctl` directly; its MCP stdout is protocol-only. |

For daemon logs and lower-level evidence, follow [verification.md](./verification.md). For protocol semantics,
see [protocol.md](./protocol.md).

## Security checklist

- Never send the one-time enrollment code, `pairing.key`, or `control.token` to an AI model or another user.
- Keep the data directory private to the current operating-system user.
- Treat `--name` only as an audit label; it does not isolate one MCP client from another.
- Review ScriptCat's browser confirmation page before approving writes or source disclosure.
- Remove an MCP server from the AI client's configuration when that client should no longer have access. Use
  ScriptCat's External Access switch when you intend to revoke the daemon connection itself.
