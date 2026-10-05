# Architecture

## Process model

```text
MCP client (Claude/Codex…) ─ stdio ─→ sctl mcp ─┐ (authenticated control API)
CLI verbs (sctl get / edit / install / browsers / tabs / windows / page / debug …) ┤
                                                ▼
                          sctl serve (daemon; defaults to 127.0.0.1:8643)
                                                ▲ WebSocket (each extension dials in + mutual HMAC handshake)
                     ScriptCat browser extension (authority for write approval and source disclosure)
                     sctl Browser extension, one or more paired instances (browser control; approval window for L2; CDP relay)
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

Every method carries a destruction level (L0 / L1 / L2, see [protocol.md](./protocol.md#3-business-rpc)). The
L1 confirmation is checked twice: `controlapi` rejects an L1 call whose input lacks `confirm: true` with
`CONFIRMATION_REQUIRED` before `bridge` forwards anything, and the sctl Browser extension's handler registry
(`extension/src/background/registry.ts`) checks again before running the handler. The daemon never decides L2 —
it forwards the call and blocks, as for ScriptCat's gates. The L2 gate lives entirely in the sctl Browser extension:
its service worker validates the request, queues it in `chrome.storage.session` (so it survives the worker being
suspended), badges the toolbar icon, and opens its own approval window (`extension/src/approval/`), a separate popup
window of the extension. The offscreen document holds back the JSON-RPC answer until the window's decision is carried
out, tells the daemon with `$/approvalPending` once a request has entered the queue, and forwards the daemon's
cancellations and its own disconnects to the queue ([protocol.md](./protocol.md#5-cancellation-and-approval)).
`bridge` hands that notification to the waiting call through `Request.OnPending`; when the requester set
`reportPending` on `/control/call`, `controlapi` writes an interim `{"pending":true}` line ahead of the result, which
is when the CLI prints its waiting line and `sctl mcp` starts its progress notifications.

### Page automation

`sctl page` and `sctl debug` commands and the `page_*` and `debug_*` MCP tools reach the daemon through `/control/page`, not `/control/call`: a
page action is not one extension method but a sequence of Chrome DevTools Protocol (CDP) commands decided in Go.
The page automation component (`internal/daemon/page`) resolves the browser and tab, then drives the tab through
the extension's internal relay methods ([protocol.md](./protocol.md#32-internal-methods)):

```text
sctl page / page_* ─/control/page─▶ controlapi ─▶ page.Manager ─▶ bridge ─WS─▶ sctl Browser ─chrome.debugger─▶ tab
                                                      ▲                            │
                                                      └── debugger.detached ◀──────┘ (BrowserListener)
```

The extension only relays CDP commands, events, and detach notices; the page logic lives in Go so it can be
tested against a fake CDP. Page state — which tabs are attached, their idle
timers, and the per-tab queue that runs commands on one tab in arrival order — lives in the daemon's memory,
because the daemon is the only process that outlives a single command. When the daemon attaches and detaches a
tab is described in [protocol.md](./protocol.md#32-internal-methods).

The same component keeps the debug records of each attached tab (`sctl debug`, `debug_*`). Every attach creates a
fresh per-tab state (`page.Tab`) that holds two ring buffers, at most 1000 console records and 1000 network requests,
each also capped at 32 MiB of record content (a record is page-controlled and can approach one 4 MiB frame) with the
oldest dropped first; attach hooks enable the `Runtime`, `Log`, and `Network` domains on the tab's top-level session (`Network` with a small
`maxPostDataSize`, so a large request body cannot push an event over the frame limit), and the CDP events the
extension relays are converted into records in the bridge read loop. Request and response bodies are not buffered:
`debug request` reads them on demand through `debugger.body`, which cuts them to 1 MiB in the extension. Cross-process
iframes are attached automatically with `waitForDebuggerOnStart`, so a new one starts paused: a background task of the
tab enables the same domains on its child session and then always resumes it, because event handlers run in the bridge
read loop and must not send commands. Since the buffers belong to the per-tab state, every path that detaches the
debugger — idle detach, `page detach`, a `debugger.detached` notice, the browser instance going away, a failed attach
— drops them, and before the daemon itself detaches a tab it ends and waits for that tab's background tasks so none of
them re-attaches it. Debug records never leave the daemon's memory.

Recording (`debug start`/`stop`/`status`) is also per-tab state. A recording tab gets no idle-detach timer after its
commands; instead a 60-minute timer, restarted by every debug command on the tab, ends the recording through the tab's
queue and re-arms the idle timer, with a sequence number discarding a timer that fired late. The daemon tells the
extension through `debugger.record` so its own idle fallback leaves the tab alone too. Because recording lives in the
per-tab state, every detach path ends it.

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
  page*.go                  #   sctl page snapshot|click|hover|fill|type|press|select|upload|scroll|goto|back|forward|
                            #   reload|wait|screenshot|eval|dialog|detach (reach /control/page through dispatchPage)
  debug.go                  #   sctl debug start|stop|status|console|network|request|clear (also through dispatchPage)
  resource.go               #   the optional scripts|script|sc resource word shared by those verbs
  dispatch.go               #   action forwarding and bridge error → exit code mapping

internal/daemon/            # ── sctl serve side ──
  component.go              #   cago Component: assembles listener + bridge + page + controlapi
  bridge/                   #   WS service core
    server.go               #     Server struct, Serve, Origin whitelist, handshake admission, connection registry
    conn.go                 #     single connection: handshake, read loop, send
    call.go                 #     action forwarding, pending-call table, JSON-RPC cancellation
    pairing.go              #     extension enrollment window (out-of-band code → key K)
    envelope.go             #     envelope, payload structs, error codes
  controlapi/               #   /control/* handlers (controller role), depends on the narrow Bridge and Page interfaces
  page/                     #   page automation: Manager (target tab, per-tab queue, attach and idle detach),
                             #     page actions, per-tab debug record buffers, the bridge-backed CDP implementation
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
  src/handlers/             #   browser method implementations, one module per domain (chrome.tabs, chrome.bookmarks,
                             #     chrome.debugger, …)
  src/approval/             #   L2 approval window UI (bookmark deletion, extension uninstall)
  src/popup/                #   popup UI: pairing, rename, forget, daemon address
  src/protocol/generated/   #   browser-only generated protocol TS (see protocol.md §6)
```

## Dependency direction

`cli` → `daemon` (for `serve` only) and `client`. `daemon/controlapi` → `daemon/page` → `daemon/bridge`, never
the reverse: the control API sees the guard only through the narrow `controlapi.Bridge` and `controlapi.Page`
interfaces, `page` reaches browsers only through its narrow `page.CDP` interface, and `bridge` knows
nothing about HTTP paths or page automation — `component.go` registers the page `Manager` as the bridge's
`BrowserListener`. The `/control/*` routes are registered by `controlapi.Handler.Register`, on the mux
`internal/daemon/component.go` assembles and hands to `bridge.Server.Serve` (which owns only `/`). The shared
DTOs live in `client/control` and are referenced one-way by `controlapi`.

Two further cross-package conventions: sensitive files reach disk only through `internal/pkg/fsutil`, and
stdout belongs to `internal/cli` alone — the latter because stdout is claimed exclusively by `sctl mcp`'s
JSON-RPC channel. Both are held by review today.
