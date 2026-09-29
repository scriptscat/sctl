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

Later browser domains are exposed as one tool per domain instead of one tool per operation. `reading_list` manages
the browser's reading list: its required `action` argument selects `list`, `add`, `mark-read`, or `rm`, and the
remaining arguments belong to that action. The tool description lists which arguments each action takes;
arguments that the chosen action does not take are rejected before anything is sent. It accepts the same optional
`browser` argument: with several instances online and no target, `list` combines every online instance while the
other actions return an error listing them. A browser without the reading list API answers `UNSUPPORTED`.

`bookmarks` manages bookmarks the same way, with `action` set to `list`, `search`, `add`, `mkdir`, `move`, `edit`,
or `remove`. `list` takes an optional `folder` and `recursive`; `search` takes `query`; results carry
`contentTrust: "untrusted-page-content"` because bookmark titles and URLs come from web pages. The root and the
browser's built-in top-level folders cannot be moved, edited, or removed, and a folder cannot be given a URL
(`INVALID_REQUEST`); `move` changes nothing if any ID is unknown (`NOT_FOUND`) or invalid. `remove` deletes up to
500 bookmarks or folders, each folder with its contents, only after a person approves the request in the browser.

`tabs_manage` rearranges tabs and windows, with `action` set to `move`, `pin`, `unpin`, `mute`, `unmute`, `reload`,
`duplicate`, `windows-open`, `windows-close`, `windows-focus`, or `windows-state`. It runs immediately with no
confirmation. `duplicate` returns the new tab ID and `windows-open` the new window ID; actions taking several IDs
change nothing if any ID is unknown (`NOT_FOUND`), and a `state` other than `normal`, `minimized`, `maximized`, or
`fullscreen` is `INVALID_REQUEST`.

`tab_groups` manages tab groups, with `action` set to `list`, `create`, `add`, `edit`, or `ungroup`. It runs
immediately with no confirmation, and results are marked `contentTrust: "untrusted-page-content"` because group titles
are page- or user-controlled. `create` puts `tabIds` (all in one window, otherwise `INVALID_REQUEST`) into a new group
with an optional `title` and `color` and returns its group ID; `add` adds `tabIds` to `groupId`; `edit` changes the
`title`, `color`, or `collapsed` state; `ungroup` removes `tabIds` from their groups, and the browser deletes a group
whose last tab leaves. `color` is one of `grey`, `blue`, `red`, `yellow`, `green`, `pink`, `purple`, `cyan`, `orange`
(anything else is `INVALID_REQUEST`), and an unknown tab or group is `NOT_FOUND` with nothing changed.
`tabs_list` items also carry `groupId` (`-1` when ungrouped).

Browser operations carry a destruction level. Most run immediately. A few are destructive enough to need explicit
confirmation — currently only the reading list's `rm`: it runs only when the call passes `confirm: true`, and
without it the daemon answers `CONFIRMATION_REQUIRED` and nothing runs. The command-line equivalent is `--yes`.
Deleting bookmarks needs human approval instead: the sctl Browser extension opens an approval window, and the call
waits — sending progress notifications like the ScriptCat write tools — until the user approves (`CONFLICT` and
nothing deleted if the bookmarks changed meanwhile), rejects or closes the window (`USER_REJECTED`), or nobody
decides within 5 minutes (`OPERATION_EXPIRED`). Cancelling the call voids the request.

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
