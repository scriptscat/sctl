# sctl

[English](./README.md) | [简体中文](./docs/README_zh-CN.md)

sctl connects AI clients and command-line workflows to the
[ScriptCat](https://github.com/scriptscat/scriptcat) browser extension. One cross-platform binary provides a
local bridge daemon, a stdio MCP server, and script-management commands.

```text
AI client ── stdio MCP ──▶ sctl mcp ── local control API ──▶ sctl serve ── WebSocket ──▶ ScriptCat
CLI ────────────────────────────────────────────────────────▲
```

ScriptCat remains the authority: source disclosure and every write request are governed by the policies and
confirmation UI in the extension.

## Features

- Exposes ScriptCat operations as discoverable, schema-typed MCP tools.
- Lists scripts and reads metadata or source, including line windows and source search.
- Requests installation, content-anchored editing, enable/disable, and deletion through browser approval.
- Uses JSON-RPC 2.0 over a WebSocket with mutual authentication; the listener defaults to loopback.
- Ships as one binary; no browser automation or Native Messaging host is required.

## Quick start

If a binary for your platform is available on
[GitHub Releases](https://github.com/scriptscat/sctl/releases), install it on `PATH`. If no release is available,
contributors can build sctl from source; plain source builds identify themselves as `0.0.0-dev`.

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

Run `sctl --help` or `sctl <command> --help` for usage and flags. Write operations block
until the user approves, rejects, or closes the confirmation flow in ScriptCat.

## License

GPL-3.0, the same license as ScriptCat. See [LICENSE](./LICENSE).
