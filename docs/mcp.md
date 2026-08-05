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

If a published archive for your operating system and architecture is available on
[GitHub Releases](https://github.com/scriptscat/sctl/releases), extract it and put the `sctl` executable on
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

## 4. Enroll ScriptCat once

1. Open ScriptCat's options page and enable **External Access**.
2. Keep `sctl serve` running.
3. In another terminal, run:

   ```bash
   sctl connect
   ```

4. Enter the displayed one-time code in ScriptCat's External Access enrollment dialog.
5. Verify the connection:

   ```bash
   sctl status
   ```

The status output must say that the extension is connected. The one-time code is valid only for the enrollment
window and must not be pasted into an AI conversation, issue, log, or MCP configuration. After enrollment, the
extension and daemon use the persisted long-term pairing state; each AI client does not enroll separately.

Disabling External Access in ScriptCat revokes the extension side of this relationship. Run `connect` again if
you intentionally revoke it and later want to reconnect.

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
