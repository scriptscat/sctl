# Architecture

## Process model

```text
MCP client (Claude/Codex…) ─ stdio ─→ sctl mcp ─┐ (authenticated control API)
CLI verbs (sctl get / edit / install / browsers / tabs / windows …) ┤
                                                ▼
                          sctl serve (daemon; defaults to 127.0.0.1:8643)
                                                ▲ WebSocket (each extension dials in + mutual HMAC handshake)
                     ScriptCat browser extension (authority for write approval and source disclosure)
                     sctl Browser extension, one or more paired instances (tab/window control)
```

`sctl mcp` and the CLI verbs are **separate processes** from `sctl serve`. They talk over the
`/control/*` HTTP/JSON control API on the daemon's listener — same port as the extensions' WS surface, separate
path, authenticated with the control token described in [threat-model.md](./threat-model.md#4-the-control-channel-internal-local-connection-in-detail).
Frontends never start `serve`: run it
explicitly in the foreground or let an external system service manager own its lifecycle.

The daemon accepts two kinds of extension connection at once: exactly one active ScriptCat connection (a new
one replaces the previous one), and any number of paired `sctl Browser` instances connecting simultaneously,
each identified by its own instance ID. A call is routed by which peer kind owns its method (scripts.\* to
ScriptCat, tabs.\*/windows.\* to a resolved browser instance) — see
[protocol.md](./protocol.md#31-routing-and-target-selection). For ScriptCat's methods, the daemon approves no
write on its own — it forwards the request and blocks until a human decides in the browser, and source reads
go through the same disclosure gate. Browser control methods carry no such human gate by design: any
control-token holder can drive a paired browser instance immediately. Details in
[threat-model.md](./threat-model.md).

## Directory layout

`internal/` is grouped by **process role**: `daemon/` is the guard side (`sctl serve`), `client/` is the
request side (`sctl mcp` and the CLI verbs), `cli/` wires commands to those roles, and `pkg/` is shared.
The layering convention is borrowed from [cago](https://github.com/cago-frame/cago), with `internal/pkg/` as
the shared layer and store playing the repository role.

```text
cmd/sctl/main.go            # cobra entry point (unwraps ExitError → os.Exit)

internal/cli/               # subcommand definitions; spans both sides, hence top level
  cli.go                    #   root command, global flags (-o/--output, --log-level), output helpers
  serve.go                  #   bootstraps the cago app and mounts the daemon Component
  mcp.go                    #   sctl mcp (serves all tools; --name is an audit label)
  connect.go status.go version.go
  get.go grep.go            #   read verbs
  edit.go write.go          #   write verbs (edit / install / enable / disable / delete)
  browsers.go               #   sctl browsers [list] / sctl browsers forget <name|id>
  tabs.go windows.go        #   sctl tabs list|open|close|activate, sctl windows list
  resource.go               #   the optional scripts|script|sc resource word shared by those verbs
  dispatch.go               #   action forwarding and bridge error → exit code mapping

internal/daemon/            # ── sctl serve side ──
  component.go              #   cago Component: assembles listener + bridge + controlapi
  bridge/                   #   WS service core
    server.go               #     Server struct, Serve, Origin whitelist, handshake admission, connection registry
    conn.go                 #     single connection: handshake, read loop, send
    call.go                 #     action forwarding, pending-call table, JSON-RPC cancellation
    pairing.go              #     extension enrollment window (out-of-band code → key K)
    envelope.go             #     envelope, payload structs, error codes
  controlapi/               #   /control/* handlers (controller role), depends on the narrow Bridge interface
  auth/                     #   mutual HMAC handshake, enrollment-code derivation (HKDF), key delivery (AES-GCM)
  store/                    #   persistence (repository role): ScriptCat's long-term key K, plus the
                             #     browsers.json registry of paired sctl Browser instances and their own
                             #     per-instance keys
  ratelimit/                #   enrollment-attempt rate limiting

internal/client/            # ── sctl mcp / CLI verb side ──
  control/                  #   control API client, shared DTOs, control token
  mcpserver/                #   go-sdk stdio MCP server: all tools (flat trust), progress while waiting

internal/pkg/               # ── shared by both sides ──
  protocol/                 #   protocol.json authority plus generated language and business-schema artifacts
  protocolschema/           #   JSON-RPC parsing and business-schema validation at the untrusted WS boundary
  audit/                    #   daemon-side security events (the Event type crosses the control API, hence shared)
  paths/                    #   data directory and derived paths
  logging/                  #   unified zap logging (stderr + <dataDir>/logs/*.log, never stdout)
  fsutil/                   #   atomic file writes

extension/                  # ── sctl Browser, the second extension kind (MV3, pnpm/Vite/React) ──
  src/background/           #   service worker: identity and settings storage, message routing, method dispatch
  src/offscreen/            #   holds the WebSocket and connection state, pairing and session handshake, retry/backoff
  src/handlers/             #   tabs.*/windows.* method implementations (chrome.tabs / chrome.windows)
  src/popup/                #   popup UI: pairing, rename, forget, daemon address
  src/protocol/generated/   #   browser-only generated protocol TS (see protocol.md §6)
```

## Dependency direction

`cli` → `daemon` (for `serve` only) and `client`. `daemon/controlapi` → `daemon/bridge`, never the reverse:
the control API sees the guard only through the narrow `controlapi.Bridge` interface and `bridge` knows
nothing about HTTP paths. The `/control/*` routes are registered by `controlapi.Handler.Register`, on the mux
`internal/daemon/component.go` assembles and hands to `bridge.Server.Serve` (which owns only `/`). The shared
DTOs live in `client/control` and are referenced one-way by `controlapi`.

Two further cross-package conventions: sensitive files reach disk only through `internal/pkg/fsutil`, and
stdout belongs to `internal/cli` alone — the latter because stdout is claimed exclusively by `sctl mcp`'s
JSON-RPC channel. Both are held by review today.
