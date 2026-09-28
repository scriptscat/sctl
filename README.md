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

- Exposes ScriptCat operations and browser tab/window control as discoverable, schema-typed MCP tools.
- Lists scripts and reads metadata or source, including line windows and source search.
- Requests installation, content-anchored editing, enable/disable, and deletion through browser approval.
- Lists, opens, closes, and activates tabs and lists windows across one or more paired sctl Browser instances.
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
popup and enter the same one-time code from `sctl connect`. Full steps, including the browser's "developer
mode" toggle, are in [`docs/mcp.md`](./docs/mcp.md#4-enroll-scriptcat-and-sctl-browser).

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
| `sctl connect` | Open a one-time ScriptCat enrollment window. |
| `sctl mcp [--name <label>]` | Serve ScriptCat tools over stdio MCP. |
| `sctl status` | Show daemon and extension connection status. |
| `sctl get [<uuid>]` | List scripts or read one script. |
| `sctl grep <uuid> <query>` | Search one script's source. |
| `sctl install <url\|file>` | Request script installation. |
| `sctl edit <uuid>` | Request a content-anchored source edit. |
| `sctl enable <uuid>` / `sctl disable <uuid>` | Request an enabled-state change. |
| `sctl delete <uuid>` | Request script deletion. |
| `sctl browsers [list]` / `sctl browsers forget <name\|id>` | List paired sctl Browser instances, or forget one. |
| `sctl tabs list\|open\|close\|activate` | List, open, close, or activate tabs on a paired sctl Browser instance. |
| `sctl windows list` | List windows on a paired sctl Browser instance. |

Run `sctl --help` or `sctl <command> --help` for usage and flags. Write operations block
until the user approves, rejects, or closes the confirmation flow in ScriptCat; browser control commands run
immediately with no approval step (see [`docs/threat-model.md`](./docs/threat-model.md)). `tabs` and `windows`
accept `--browser <name|id>` (or `SCTL_BROWSER`) to pick an instance when more than one is paired.

## License

GPL-3.0, the same license as ScriptCat. See [LICENSE](./LICENSE).
