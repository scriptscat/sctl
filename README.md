# sctl

[English](./README.md) | [简体中文](./README_zh-CN.md)

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
- Uses JSON-RPC 2.0 over a loopback-only WebSocket with mutual authentication.
- Ships as one binary; no browser automation or Native Messaging host is required.

## Quick start

If a binary for your platform is available on
[GitHub Releases](https://github.com/scriptscat/sctl/releases), install it on `PATH`. Contributor source builds
must follow the [version-floor instructions](./docs/development.md#version-floor); a plain `0.0.0-dev` build is
rejected by the extension.

Choose one absolute data directory and pass it to every sctl process:

```bash
# Terminal 1: keep the daemon running
sctl --data-dir /absolute/path/to/sctl-data serve

# Terminal 2: enroll once, then verify the connection
sctl --data-dir /absolute/path/to/sctl-data connect
sctl --data-dir /absolute/path/to/sctl-data status
```

Enable **External Access** in ScriptCat and enter the one-time code printed by `connect`.
Then configure the AI client to launch:

```text
/absolute/path/to/sctl --data-dir /absolute/path/to/sctl-data mcp --name my-ai-client
```

`sctl mcp` does not start the daemon. It and `sctl serve` must use the same `--data-dir`. See the
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

## Documentation

- [MCP installation](./docs/mcp.md) — installation, enrollment, client configuration, and troubleshooting.
- [Architecture](./docs/architecture.md) — process model and package responsibilities.
- [Protocol](./docs/protocol.md) — extension ↔ daemon JSON-RPC 2.0 protocol.
- [Threat model](./docs/threat-model.md) — trust boundaries, credentials, approval, and auditing.
- [Contributor documentation](./docs/README.md) — development, verification, and documentation index.

## License

GPL-3.0, the same license as ScriptCat. See [LICENSE](./LICENSE).
