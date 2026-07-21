# Architecture

## Process model

```text
MCP client (Claude/Codex…) ─ stdio ─→ sctl mcp ─┐ (local internal connection: loopback control API)
CLI verbs (sctl scripts list / install …)───────┤
                                                ▼
                          sctl serve (daemon; WS listens on 127.0.0.1:8643 only)
                                                ▲ WebSocket (extension dials in + mutual HMAC handshake)
                          ScriptCat browser extension (authority for approval and authorization)
```

`sctl mcp` and the CLI verbs are **separate processes** from the resident `sctl serve`. They talk over the
`/control/*` HTTP/JSON control API on the daemon's listener — same port as the extension's WS surface, separate
path, authenticated with the 0600 control token the daemon writes. When a frontend finds no daemon running it
spawns one detached. The bind race between several frontends starting cold is resolved by "if the bind fails,
connect to the instance that won".

The authority always lives on the extension side: the daemon approves no write on its own — it forwards the
request and blocks until a human decides in the browser. Details in [threat-model.md](./threat-model.md).

## Directory layout

`internal/` is grouped by **process role**: `daemon/` is the guard side (`sctl serve`), `client/` is the
request side (`sctl mcp` and the CLI verbs), and the flat top-level packages are shared by both by definition.
The layering convention is borrowed from [cago](https://github.com/cago-frame/cago) — `configs/`,
`internal/pkg/`, and store playing the repository role.

```text
cmd/sctl/main.go            # cobra entry point (unwraps ExitError → os.Exit)
configs/config.yaml         # cago config (bridge.address etc.; falls back to built-in defaults if absent)

internal/cli/               # subcommand definitions; spans both sides, hence top level
  cli.go                    #   root command, global flags, JSON output helpers
  serve.go                  #   bootstraps the cago app and mounts the daemon Component
  mcp.go                    #   sctl mcp / sctl mcp pair
  pair.go status.go version.go
  scripts.go write.go       #   read verbs / write verbs
  dispatch.go               #   action forwarding and bridge error → exit code mapping

internal/daemon/            # ── sctl serve side ──
  component.go              #   cago Component: assembles listener + bridge + controlapi
  bridge/                   #   WS service core
    server.go               #     Server struct, Serve, handshake admission, connection registry
    conn.go                 #     single connection: handshake, read loop, send
    call.go                 #     action forwarding, pending-call table, bridge.cancel
    pairing.go              #     extension pairing window and MCP client pairing
    clients.go              #     client revocation and client.sync broadcast
    envelope.go             #     envelope, payload structs, error codes
  controlapi/               #   /control/* handlers (controller role), depends on the narrow Bridge interface
  auth/                     #   mutual HMAC handshake, pairing-code derivation (HKDF), key delivery (AES-GCM)
  store/                    #   0600 persistence of long-term keys / client tokens (repository role)
  ratelimit/                #   per-key sliding-window rate limiting

internal/client/            # ── sctl mcp / CLI verb side ──
  control/                  #   control API client, shared DTOs, control token, detached auto-spawn
  mcpserver/                #   go-sdk stdio MCP server: 6 tools, scope filtering, progress while waiting
  identity/                 #   cached paired identity for an sctl mcp instance (0600)

internal/pkg/               # ── shared by both sides ──
  protocol/                 #   protocol.json itself + embedded parsing
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
