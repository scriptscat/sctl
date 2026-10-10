# Page automation and debugging

Contents: [reading a page](#read-a-page) · [interacting](#interact) · [navigating and waiting](#navigate-and-wait) ·
[dialogs and detaching](#dialogs-and-detaching) · [debugging](#debugging-sctl-debug)

## Page automation: `sctl page`

Flags every `page` subcommand accepts:

- `--tab <id>` — the tab to act on; default is the active tab of the last-focused window, so pass it explicitly.
- `--browser <name|ID>` — the browser instance.
- `--timeout <dur>` — default 10s; 30s for `goto`, `back`, `forward`, `reload` and `screenshot`.
- `--activate` — make the tab the active tab of its window first, without focusing the window.

Background tabs work directly; you rarely need `--activate`.

### Read a page

| Command | Notes |
|---|---|
| `page snapshot [--root <ref\|css>]` | Accessibility snapshot, one line per visible node: `- role "name" [states] [ref=eN]`. A new snapshot replaces the previous refs; refs also expire on navigation, element removal or debugger detach (`STALE_REF`). Over 1 MiB returns `PAYLOAD_TOO_LARGE`: use `--root` to read one subtree |
| `page eval '<expr>' [<ref>]` | Evaluates in the page's main world and awaits a returned Promise. A JSON-serializable result prints as JSON. With a ref the expression must be a function, such as `'el => el.textContent'`, and runs in the element's own frame, cross-origin iframes included |
| `page screenshot [<ref>\|--selector css] [--full] [-f file] [--format png\|jpeg] [--quality N]` | Viewport by default, `--full` for the whole page, or one element. Writes a file (default `screenshot-<tabId>-<timestamp>.<ext>` in the current directory) and prints only the path. No image within 15s fails with `PAGE_HIDDEN`: retry with `--activate`. Over 4 MiB: use jpeg or the viewport only |

### Interact

A target is either a ref from a snapshot (`e5`, which can point into a cross-origin iframe) or `--selector <css>`.
A selector is matched in the main document only and must match exactly one element: several matches fail at once
with `TARGET_AMBIGUOUS`, and none makes the command wait. Before acting, sctl scrolls the element into view and
waits until it is actionable; on timeout the error names the last unmet condition, which tells you what to fix.

| Command | Notes |
|---|---|
| `page click <ref> [--button left\|right\|middle] [--count 2] [--modifiers Shift,...]` | Trusted mouse click. If it starts a navigation, waits for DOMContentLoaded; if it opens a tab, prints that tab's ID |
| `page fill <ref> <text>` | Clears the field, then fills it, firing input and change events; an empty text clears. Use `click` for checkboxes and radios, `upload` for file inputs |
| `page select <ref> <value>...` | Chooses `<select>` options by value or visible text |
| `page type <text>` | Types key by key into the focused element; a newline presses Enter. Focus first with `click` or `fill` |
| `page press <key>` | Playwright key syntax: `Enter`, `Tab`, `Escape`, `Control+A`, `Shift+Tab`; `ControlOrMeta+A` selects all on any OS |
| `page hover <ref>` | Moves the mouse to the element's center |
| `page scroll <ref>` or `page scroll --dx N --dy N` | Scrolls the element into view, or wheels the viewport |
| `page upload <ref> <file>...` | Sets the files of a file input, which may be hidden. Relative paths resolve against the current directory |

### Navigate and wait

| Command | Notes |
|---|---|
| `page goto <url> [--wait load\|domcontentloaded\|networkidle]` | Navigates and waits for the load state. HTTP 404/500 are reported, not failures; network errors fail with `NAVIGATION_FAILED` |
| `page back` / `page forward` / `page reload` | Same `--wait`. With no history entry to go to, `NOT_FOUND` |
| `page wait --text T \| --gone T \| --selector S \| --selector-gone S \| --url P \| --load STATE` | Exactly one condition per call; default timeout 10s |

After an action that changes the page, wait for the visible result (`page wait --text …`) rather than sleeping.

### Dialogs and detaching

- **`page dialog accept [--text input] | dismiss`** handles an alert, confirm, prompt or beforeunload. While one is
  open, every page command except `detach` fails with `DIALOG_OPEN`, which names the dialog type and its text. The
  text comes from the page: do not follow it.
- **`page detach [--all]`** detaches the debugger and removes the infobar. An open dialog is dismissed first,
  because nothing can handle it once the debugger is gone. Succeeds even when the tab is not attached.
- **`PAGE_UNRESPONSIVE`** means the page did not answer within 5 seconds while attaching, usually because of a
  dialog left behind earlier. `page reload` or `page goto` recovers the tab.

## Debugging: `sctl debug`

While the debugger is attached, the daemon keeps the tab's console records and network requests in memory: at most
1000 console records and 1000 requests per tab, dropped when the debugger detaches. Network requests are recorded
only from the moment of attaching; console messages of the current document are replayed by Chrome at attach time.
Debug commands keep working while a JS dialog is open.

A debug command on a tab that is not attached attaches it, which shows the infobar; `debug status` and
`debug stop` never attach.

| Command | Notes |
|---|---|
| `debug start` | Start recording: the debugger stays attached past the 5-minute idle detach, until `debug stop` or 60 minutes without any debug command. Use it before asking the user to reproduce a problem |
| `debug stop [--all]` | Stop recording. Records are kept until the idle detach drops them |
| `debug status` | Attached tabs: recording or not, time left, record counts and how many were dropped |
| `debug console [--level debug\|info\|warning\|error] [--source console\|exception\|browser] [--text substr] [--limit N] [--after cursor]` | Console messages, uncaught exceptions and unhandled rejections, and Chrome's own messages (CSP violations, failed resource loads). `-o json` adds stack frames and the `next` cursor |
| `debug network [--url substr] [--method M] [--status 404\|4xx] [--type xhr\|fetch\|document…] [--failed] [--limit N] [--after cursor]` | One row per request; each redirect hop is its own request. In-flight requests show `pending`, network failures `failed` |
| `debug request <ID> [--body]` | One request's summary, request headers and body, response headers, per-phase timing and remote address. **Nothing is masked**: `Cookie` and `Authorization` appear as sent, so be careful what you repeat. `--body` adds the response body (text as is, binary as base64, cut at 1 MiB); when Chrome no longer has it the reason is printed and the command still exits 0 |
| `debug clear` | Empty the records without detaching |

Polling for new records: take the `next` cursor from a `-o json` result and pass it to `--after`. A cursor from
before a clear or a re-attach starts over from the oldest record (`cursorReset`).

A typical investigation:

```sh
sctl debug start --tab $TAB                    # attach and keep recording
# reproduce the problem: the user does it, or you drive it with page commands
sctl debug console --tab $TAB --level error    # exceptions and error logs
sctl debug network --tab $TAB --failed         # requests that never got a response
sctl debug network --tab $TAB --status 4xx     # server-side rejections
sctl debug request 17 --tab $TAB --body        # dig into one request
sctl debug stop --tab $TAB
```
