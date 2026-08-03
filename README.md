# sctl

ScriptCat's local control tool: a bridge daemon, an MCP server, and script management commands, shipped as a
single cross-platform binary. Built on the [cago](https://github.com/cago-frame/cago) framework and
[cobra](https://github.com/spf13/cobra).

> ⚠️ Work in progress.

```text
MCP client (Claude/Codex…) ─ stdio ─→ sctl mcp ─┐ (local internal connection: loopback control API)
CLI verbs (sctl get / edit / install …)─────────┤
                                                ▼
                          sctl serve (daemon; WS listens on 127.0.0.1:8643 only)
                                                ▲ WebSocket (extension dials in + mutual HMAC handshake)
                          ScriptCat browser extension (authority for approval and authorization)
```

`sctl mcp` and the CLI verbs are **separate processes** from the resident `sctl serve`; they talk to it over
the `/control/*` HTTP/JSON control API on the daemon's listener, and spawn one when none is running. Details
in [`docs/architecture.md`](./docs/architecture.md).

## Subcommands

| Command | What it does |
|---|---|
| `sctl serve` | Run the bridge daemon (a cago app; the WS server binds loopback only and mounts the control API) |
| `sctl connect` | Generate a one-time pairing code and set up external access with the extension (needed once; the CLI and every MCP agent inherit that trust afterwards) |
| `sctl mcp [--name <label>]` | stdio MCP server; inherits trust through external access and exposes every script tool (starts `serve` if it is not running; `--name` is an audit label only) |
| `sctl status` | Daemon and extension connection status, with a summary of guard-side security events (does not start the daemon) |
| `sctl get [<uuid>]` | List installed scripts, or show one script |
| `sctl grep <uuid> <query>` | Search one script's source and print matching lines with their line numbers |
| `sctl install <url\|file>` | Request installing a script (URL or local file; a local file is sent as staged code) |
| `sctl edit <uuid>` | Request a content-anchored edit of a script's source |
| `sctl enable <uuid>` / `sctl disable <uuid>` | Request enabling / disabling a script |
| `sctl delete <uuid>` | Request deleting a script (alias: `del`) |
| `sctl version` | Version and protocol information |

`get`, `grep`, `edit`, `enable`, `disable` and `delete` also accept an optional resource word — `scripts`,
`script` or `sc` — in front of the uuid, so `sctl get sc <uuid>` and `sctl get <uuid>` are the same command.

Global flags:

- `-o` / `--output` — `table` (the default human-readable format), `json`, or `source`. `-o source` is only
  valid for `sctl get <uuid>`, where it writes the raw code to stdout with no trailing newline so it can be
  redirected into a `.user.js` file.
- `--log-level` — `debug|info|warn|error`. Logs go to stderr and to `logs/` under the data directory, never to
  stdout; `sctl mcp`'s stdout is claimed by the MCP protocol.

### Reading and editing source

`sctl get <uuid> -o source` accepts `--lines A-B` (1-based, inclusive) to fetch a line window instead of the
whole file.

`sctl grep` matches a **literal substring** by default — `*`, `?`, `.` and friends match themselves; pass `-E`
to compile the query as a regular expression. `-i` ignores case, `-C N` prints N lines of context, and `-m N`
stops after N matches. Finding nothing is not an error: the exit code stays 0 and stdout is empty.

`sctl edit` replaces text anchored by content, never by line number. Give the edits either as a JSON array of
`{oldText, newText, replaceAll?}` via `-f <file>` (`-` reads stdin), or as repeated `--replace` / `--with`
pairs, whose values accept a leading `@path` to read the value from a file (`@@` escapes a literal leading
`@`). Each `oldText` is matched literally and must occur exactly once unless `--replace-all` is given; edits
apply in order, so each one searches the result of the previous one, and an empty replacement deletes the
matched text. At most **100 edits** per request. The source is never uploaded and never read first — only the
edits are sent.

### Blocking semantics and exit codes for write operations

Write verbs and MCP write tools **block** until the user decides on the confirmation page in the browser.
**Ctrl-C** in the requester cuts the connection, and the daemon then sends `bridge.cancel` to the extension to
void the operation. CLI exit codes:

| Code | Meaning |
|---|---|
| 0 | Approved / success |
| 1 | The user rejected it (`USER_REJECTED`) |
| 2 | Voided (timed out with `OPERATION_EXPIRED` / cancelled with Ctrl-C / the extension disconnected) |
| 3 | Any other error (validation failure, `NOT_FOUND`, connection failure, …) |

## Documentation

- [`AGENTS.md`](./AGENTS.md) — the entry point for contributors and AI agents: engineering principles, the
  architecture quick-map, and which doc to read before which kind of change.
- [`docs/README.md`](./docs/README.md) — the index of every doc, doubling as the ownership table
  (architecture, protocol, threat model, development, verification, doc maintenance).

The index is maintained in `docs/README.md` alone; it is not copied here, so the two cannot drift.

## License

GPL-3.0, same as the main extension repository. Full text in [LICENSE](./LICENSE).
