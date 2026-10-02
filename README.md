# sctl

[English](./README.md) | [简体中文](./docs/README_zh-CN.md)

sctl connects AI clients and command-line workflows to the
[ScriptCat](https://github.com/scriptscat/scriptcat) browser extension and to its own **sctl Browser** browser
extension. One cross-platform binary provides a local bridge daemon, a stdio MCP server, and script-management
and browser-control commands.

```text
AI client ── stdio MCP ──▶ sctl mcp ── local control API ──▶ sctl serve ── WebSocket ──▶ ScriptCat
CLI ────────────────────────────────────────────────────────▲
```

ScriptCat remains the authority: source disclosure and every write request are governed by the policies and
confirmation UI in the extension.

## Features

- Exposes ScriptCat operations and browser tab/window control as discoverable, schema-typed MCP tools, plus ten
  per-domain browser tools (`bookmarks`, `reading_list`, `tabs_manage`, `tab_groups`, `history`, `recently_closed`,
  `downloads`, `cookies`, `browsing_data`, `extensions`) that pick the operation with an `action` argument.
- Lists scripts and reads metadata or source, including line windows and source search.
- Requests installation, content-anchored editing, enable/disable, and deletion through browser approval.
- Lists, opens, closes, activates, moves, pins, mutes, reloads, and duplicates tabs, and lists, opens, closes, focuses, and resizes windows across one or more paired sctl Browser instances.
- Lists, creates, edits, and dissolves tab groups on a paired sctl Browser instance.
- Lists, adds, marks read or unread, and removes reading list entries on a paired sctl Browser instance.
- Lists, searches, adds, moves, and edits bookmarks and bookmark folders on a paired sctl Browser instance, and
  deletes them after approval in that browser.
- Searches and clears history, restores recently closed tabs and windows, manages downloads, reads and changes
  cookies, clears browsing data, and lists, enables, disables, or (after approval) uninstalls extensions.
- Uses JSON-RPC 2.0 over a WebSocket with mutual authentication; the listener defaults to loopback.
- Ships as one binary; no browser automation or Native Messaging host is required.

## Quick start

Install the latest release with one command — macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
```

or Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
```

The installer downloads the hyphen-named release archive `sctl-<version>-<os>-<arch>.<ext>` for your platform,
verifies its sha256 against `checksums.txt`, and installs `sctl` into `~/.local/bin` (macOS/Linux) or
`%LOCALAPPDATA%\sctl\bin` (Windows). `SCTL_VERSION` pins a specific version; `SCTL_INSTALL_DIR` overrides the
install directory. If the install directory is not on your `PATH`, the installer prints the exact `PATH` hint
for your platform — it never edits your shell profile or user PATH for you.

Or download a `sctl-<version>-<os>-<arch>.<ext>` archive manually from
[GitHub Releases](https://github.com/scriptscat/sctl/releases) and put it on `PATH`, or build sctl from source;
plain source builds identify themselves as `0.0.0-dev`.

Choose one absolute data directory and export it for every sctl process:

```bash
export SCTL_DATA_DIR=/absolute/path/to/sctl-data

# Terminal 1: keep the daemon running
sctl serve

# Terminal 2: enroll once, then verify the connection
sctl connect
sctl status
```

Enable **External Access** in ScriptCat and enter the one-time code printed by `connect`.

To also pair the **sctl Browser** extension (tab/window control), download
`sctl-browser-extension-<version>.zip` from [GitHub Releases](https://github.com/scriptscat/sctl/releases),
unzip it, and load the unzipped folder as an unpacked extension from your browser's extensions page. Open its
popup and enter a one-time code from `sctl connect`; a code pairs only one extension, so run `connect` again if
ScriptCat already used it. Full steps, including the browser's "developer mode" toggle, are in
[`docs/mcp.md`](./docs/mcp.md#4-enroll-scriptcat-and-sctl-browser).

Then configure the AI client to launch:

```text
/absolute/path/to/sctl mcp --name my-ai-client
```

`sctl mcp` does not start the daemon. It and `sctl serve` must resolve to the same data directory and, when
overriding the default listener, use the same `--listen-address <host:port>`. See the
[complete MCP installation guide](./docs/mcp.md) for client JSON, verification, security notes, and
troubleshooting.

## Commands

| Command | Purpose |
|---|---|
| `sctl serve` | Run the local bridge daemon. |
| `sctl connect` | Open a one-time enrollment window for ScriptCat or sctl Browser. |
| `sctl mcp [--name <label>]` | Serve ScriptCat and sctl Browser tools over stdio MCP. |
| `sctl status` | Show daemon and extension connection status. |
| `sctl get [<uuid>]` | List scripts or read one script. |
| `sctl grep <uuid> <query>` | Search one script's source. |
| `sctl install <url\|file>` | Request script installation. |
| `sctl edit <uuid>` | Request a content-anchored source edit. |
| `sctl enable <uuid>` / `sctl disable <uuid>` | Request an enabled-state change. |
| `sctl delete <uuid>` | Request script deletion. |
| `sctl browsers [list]` / `sctl browsers forget <name\|id>` | List paired sctl Browser instances, or forget one. |
| `sctl tabs list\|open\|close\|activate` | List, open, close, or activate tabs on a paired sctl Browser instance. |
| `sctl tabs move\|pin\|unpin\|mute\|unmute\|reload\|duplicate` | Move, pin, mute, reload, or duplicate tabs (several IDs are all-or-nothing). |
| `sctl windows list` | List windows on a paired sctl Browser instance. |
| `sctl windows open\|close\|focus\|state` | Open, close, focus, or change the state of windows. |
| `sctl groups list\|create\|add\|edit\|ungroup` | List, create, fill, edit (title, color, collapse), or dissolve tab groups. |
| `sctl reading-list list\|add\|mark-read\|rm` | List, add, mark read or unread, or remove reading list entries on a paired sctl Browser instance. |
| `sctl history search\|visits\|rm\|clear` | Search history, list a URL's visits, delete URLs from history, or clear history by time range. |
| `sctl browsing-data clear` | Clear cache, cookies, storage and other browsing data by type, time, and origin. |
| `sctl recent list\|restore` | List recently closed tabs and windows, or restore one (the most recent when no session ID is given). |
| `sctl downloads list\|start\|pause\|resume\|cancel\|erase\|delete-file\|show` | List, start, pause, resume, cancel, erase, or delete the file of downloads on a paired sctl Browser instance. |
| `sctl cookies list\|get\|set\|rm\|clear` | List (partitioned cookies included), read, set, or delete cookies on a paired sctl Browser instance; values are returned unmasked. |
| `sctl bookmarks list\|search\|add\|mkdir\|move\|edit\|rm` | List, search, add, move, edit, or delete bookmarks and bookmark folders on a paired sctl Browser instance. |
| `sctl extensions list\|enable\|disable\|uninstall` | List, enable, disable, or uninstall extensions and apps on a paired sctl Browser instance; disabling ScriptCat disconnects it from the daemon. |

Run `sctl --help` or `sctl <command> --help` for usage and flags. Write operations block
until the user approves, rejects, or closes the confirmation flow in ScriptCat; browser control commands run
immediately with no approval step (see [`docs/threat-model.md`](./docs/threat-model.md)). `tabs`, `windows`, `groups`,
`reading-list`, `bookmarks`, `history`, `browsing-data`, `recent`, `downloads`, `cookies`, and `extensions` accept `--browser <name|id>` (or `SCTL_BROWSER`) to pick an instance when more than one is online.
Destructive browser operations need explicit confirmation: `reading-list rm`, `history rm`, `history clear`, `browsing-data clear`, `downloads cancel`, `erase` and `delete-file`, `cookies rm` and `clear`, and `extensions disable` run only with `--yes` (MCP:
`confirm: true`); without it nothing runs and the command exits with code 3. `bookmarks rm <id>...` needs human
approval instead: the browser opens an approval window and the command waits, exiting 0 once the bookmarks are
deleted, 1 when the request is rejected or the window is closed, 2 when nobody decides within 5 minutes or you
press Ctrl-C, and 3 when the bookmarks changed before approval. `extensions uninstall <id>` is approved the same way,
and clicking Uninstall in the window then opens Chrome's own confirmation dialog: the command exits 0 once the
extension is uninstalled, 1 when the request is rejected, the window is closed, or the uninstall is cancelled in Chrome's dialog, 2 when nobody decides within
5 minutes or you press Ctrl-C, and 3 for an unknown ID, sctl Browser itself, or an extension installed by policy. Commands that take `--limit` return at
most 100 items by default, and up to 1000 with `--limit` (`recent list`: 25, Chrome's retention limit), and note on
stderr when more remain. `--since` and `--until` accept an RFC 3339 time or a duration ago such as `7d`, `12h`, or `30m`.

## License

GPL-3.0, the same license as ScriptCat. See [LICENSE](./LICENSE).
