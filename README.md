# sctl

[English](./README.md) | [简体中文](./docs/README_zh-CN.md)

sctl puts the browser you are using under the control of the command line, AI clients, and scripts. One
cross-platform binary runs a local bridge daemon that reaches
[ScriptCat](https://github.com/scriptscat/scriptcat) for userscript management and its own **sctl Browser**
extension for tabs, browser data, page automation, and debugging.

```text
sctl <command> ───────────────┐
AI client ── MCP ── sctl mcp ──┴─ control API ─▶ sctl serve ─┬─ WebSocket ─▶ ScriptCat      userscripts
Playwright / Puppeteer ────── CDP endpoint ────▶             └─ WebSocket ─▶ sctl Browser   tabs, data, pages, debugging
```

Who decides: ScriptCat stays the authority for its scripts, so reading source and every write wait for approval
in the browser. Browser control, page automation, debugging, and raw CDP run immediately, by design; only
deleting bookmarks and uninstalling an extension wait for approval, and other destructive commands need `--yes`.
Read [`docs/threat-model.md`](./docs/threat-model.md) before exposing sctl to anything you do not trust.

## Features

- **ScriptCat userscripts** — list, read metadata and source (line windows, search), install, edit by content
  anchors, enable, disable, delete.
- **Tabs and windows** — list, open, close, activate, move, pin, mute, reload, duplicate, and group tabs across
  one or more paired browsers.
- **Browser data** — bookmarks, reading list, history, recently closed tabs, downloads, cookies, browsing data,
  and extensions.
- **Page automation** — accessibility snapshots with element refs, click, hover, fill, type, select, upload,
  scroll, navigate, wait, screenshot, and run JavaScript, in background tabs without switching the one you are on.
- **Debugging** — console messages, uncaught exceptions, browser messages, and network requests with headers and
  bodies.
- **Raw CDP** — send one Chrome DevTools Protocol command, or open an endpoint that Playwright `connectOverCDP`
  and Puppeteer `connect` attach to.
- **One binary** — JSON-RPC 2.0 over a mutually authenticated WebSocket, loopback by default; no Native Messaging
  host.

## Install

macOS and Linux:

```bash
curl -fsSL https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.sh | sh
```

Windows PowerShell:

```powershell
irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex
```

The installer verifies the release archive's sha256 and installs `sctl` into `~/.local/bin` (macOS/Linux) or
`%LOCALAPPDATA%\sctl\bin` (Windows). Pinning a version, manual downloads, and building from source are covered in
[`docs/mcp.md`](./docs/mcp.md#1-install-sctl).

## Quick start

1. **Start the daemon** and leave it running:

   ```bash
   sctl serve
   ```

2. **Pair the extensions.** `sctl connect` prints a one-time code; each code pairs one extension, so run it once
   per extension.

   - **ScriptCat:** enable **External Access** in its options and enter the code.
   - **sctl Browser:** download `sctl-browser-extension-<version>.zip` from
     [GitHub Releases](https://github.com/scriptscat/sctl/releases), unzip it, load the folder as an unpacked
     extension, and enter a code in its popup. It needs **Chrome 125 or newer** (or a Chromium browser of that
     version).

3. **Check the connection:**

   ```bash
   sctl status      # daemon and ScriptCat
   sctl browsers    # paired sctl Browser instances
   ```

Every sctl process must use the same data directory and listener address; the defaults work when they all run as
the same user. The step-by-step guide, including `SCTL_DATA_DIR`, `--listen-address`, and the debugging infobar
Chrome shows while a page is being driven, is [`docs/mcp.md`](./docs/mcp.md).

## Ways to use it

**Command line.** Every capability is a command; add `-o json` for scripts.

```bash
sctl tabs list
sctl page snapshot --tab 123
sctl debug console --tab 123 --level error
sctl get                      # ScriptCat scripts
```

Behavior, exit codes, and confirmations are in [`docs/cli.md`](./docs/cli.md).

**AI clients over MCP.** Configure the client to launch `/absolute/path/to/sctl mcp --name my-ai-client`.
`sctl mcp` does not start the daemon. Client configuration, the tool reference, and troubleshooting are in
[`docs/mcp.md`](./docs/mcp.md).

**Agent skill.** To have an agent drive the command line instead of MCP, install the skill in
[`skills/sctl/`](./skills/sctl/SKILL.md). For Claude Code, link it from a clone of this repository:

```sh
ln -s "$PWD/skills/sctl" ~/.claude/skills/sctl
```

**Playwright and Puppeteer.** `sctl cdp endpoint` prints addresses that existing scripts connect to, keeping the
browser's logins; see [`docs/cli.md`](./docs/cli.md#endpoint-for-playwright-and-puppeteer).

## Commands

| Group | What it does |
|---|---|
| `sctl serve` / `connect` / `status` / `mcp` | Run the daemon, pair an extension, show connection status, serve MCP over stdio. |
| `sctl get` / `grep` / `install` / `edit` / `enable` / `disable` / `delete` | Manage ScriptCat userscripts. |
| `sctl browsers` | List paired sctl Browser instances, or forget one. |
| `sctl tabs` / `windows` / `groups` | Tabs, windows, and tab groups. |
| `sctl bookmarks` / `reading-list` | Bookmarks and the reading list. |
| `sctl history` / `recent` / `downloads` | History, recently closed tabs and windows, downloads. |
| `sctl cookies` / `browsing-data` / `extensions` | Cookies, browsing data, installed extensions. |
| `sctl page` | Automate a page: snapshot, click, fill, navigate, wait, screenshot, evaluate. |
| `sctl debug` | Record and read a tab's console and network requests. |
| `sctl cdp` | Send a raw CDP command, or open an endpoint for Playwright and Puppeteer. |

`sctl <command> --help` lists every subcommand and flag; [`docs/cli.md`](./docs/cli.md) is the full reference.

## Documentation

- [`docs/cli.md`](./docs/cli.md) — command-line reference.
- [`docs/mcp.md`](./docs/mcp.md) — installation, pairing, MCP client setup, and the MCP tool reference.
- [`docs/threat-model.md`](./docs/threat-model.md) — what sctl protects and what it deliberately does not.
- [`docs/README.md`](./docs/README.md) — index of the contributor docs (architecture, protocol, development).

## License

GPL-3.0, the same license as ScriptCat. See [LICENSE](./LICENSE).
