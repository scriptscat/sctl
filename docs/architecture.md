# Architecture

## Process model

```text
MCP client (Claude/Codex…) ─ stdio ─→ sctl mcp ─┐ (local internal connection: loopback control API)
CLI verbs (sctl get / edit / install …)─────────┤
                                                ▼
                          sctl serve (daemon; loopback-only WS listener)
                                                ▲ WebSocket (extension dials in + mutual HMAC handshake)
                          ScriptCat browser extension (authority for approval and authorization)
```

`sctl mcp` and the CLI verbs are **separate processes** from `sctl serve`. They talk over the
`/control/*` HTTP/JSON control API on the daemon's listener — same port as the extension's WS surface, separate
path, authenticated with the control token described in [threat-model.md](./threat-model.md#4-the-control-channel-internal-local-connection-in-detail).
Frontends never start `serve`: run it
explicitly in the foreground or let an external system service manager own its lifecycle.

The authority always lives on the extension side: the daemon approves no write on its own — it forwards the
request and blocks until a human decides in the browser. Details in [threat-model.md](./threat-model.md).

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
  store/                    #   persistence of the long-term key K (repository role)
  ratelimit/                #   per-key sliding-window rate limiting

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
