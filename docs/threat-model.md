# sctl Threat Model (bridge daemon + local control API)

> Status: kept in sync with the sctl v0.1 daemon/CLI/MCP implementation. The authority for constants is
> [`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json), and the protocol semantics
> are in [`protocol.md`](./protocol.md). This document covers only the security boundary and its trade-offs.

## 1. Positioning and overall trade-offs

sctl replaces the **"no listener"** design of the Native Messaging rewrite with a **"loopback WS listener plus
a mutual authentication handshake"**. The selling point deliberately traded away is "no TCP listener on the
host at all"; what it buys is zero new browser permissions, no installer, and single-binary distribution. The
boundary therefore has to be rebuilt head-on: for a malicious web page the situation moves from "no entry
point" to "can see that the port is open, and gets disconnected when the handshake fails".

**There are only two trust anchors:**
- the **long-term shared key K** between the extension and the daemon, established once by **enrollment**: the
  daemon prints a one-time code in the terminal (never over the wire), the user types it into the extension's
  「外部接入 / External Access」 page, and K is derived and delivered under that code. Trust is **flat** — after
  enrollment the CLI and every MCP agent inherit K through the extension ↔ daemon channel and never enroll
  again; there is no per-client pairing, token, scope, or revocation.
- the **control token** between the local frontend (`sctl mcp` / CLI verbs) and the daemon (written to a 0600
  file once the daemon has bound its port; only same-user processes can read it).

**The second gate, present throughout:** every write operation (install / toggle / delete) and every source
disclosure is ultimately decided by **human approval in the browser**, applied identically to the CLI and to
MCP; even if a malicious local process gets the control token and calls sctl, the write still needs the user to
press approve on the extension's confirmation page (unless the corresponding global policy is set to
"always-allow").

## 2. Attack surface and countermeasures

| Threat | Countermeasure | Residual risk |
|---|---|---|
| A web page connects straight to the daemon with `new WebSocket("ws://127.0.0.1:8643")` | An **Origin whitelist** rejects any connection whose `Origin` is present and not an extension origin (`chrome-extension://` etc.) — a cheap pre-filter that a browser page cannot get past (the browser stamps Origin, page JS cannot forge it). Beyond that, a connection must complete the mutual HMAC handshake before it can send or receive any business message; without credentials it fails the challenge-response and is disconnected on the 5s timeout **with no reason echoed back** (close 1008). A non-browser process can forge any Origin, so the handshake remains the real gate. Both rejections are recorded in the daemon-side audit (§6) | A page can probe that the port is open |
| A web page impersonates the local frontend with `fetch("http://127.0.0.1:8643/control/…")` | Apart from `/control/health`, every control API requires an `X-Sctl-Control-Token` header, compared in constant time against the daemon's 0600 token; a web page cannot read that file, so it gets a 401 and the action never runs at all | Port / health information can be probed (see below) |
| A local process grabs 8643 to impersonate the daemon, or connects in while impersonating the extension | **Mutual** HMAC-SHA-256 challenge-response between the extension and the daemon ([protocol.md](./protocol.md) §3.1); the long-term key K comes from a one-time enrollment code and never travels in plaintext; nonces are regenerated per connection, so replays are useless | See the "malicious same-user process" row |
| A local process (CLI or MCP agent) that reaches the daemon requests a privileged action | Flat trust deliberately grants any control-token holder full read/list and the ability to *request* writes; the gate is not per-client authorization but the **per-operation human gate**: writes need browser approval and source reads need disclosure approval, both keyed by script (extension session), plus read/write rate limits. There is no per-client scope to escalate | Any same-user process that can run sctl can list scripts and request writes (writes still gated); an accepted trade-off for single-enrollment simplicity — see §3 |
| Write operations are abused (installing a malicious script / bulk deletion) | Two-phase confirmation plus a TOCTOU re-check at the moment of approval (staged `contentHash`, target `existingCodeHash`); calls are purely blocking, so a requester disconnect voids them. The install page's own enable toggle decides the enabled state (installs are usable immediately, like a normal install). "Always-allow" is an explicit security-downgrade switch (amber warning in the UI) | Under "always-allow" a write is no longer confirmed by a human — the user takes that risk |
| Source code leaks | Source disclosure is gated by its own **source-read policy** (approval by default), applied to the CLI and MCP alike — the CLI is **not** exempt; reading is a privacy matter and is not covered by the write policy. Script-controlled text is always returned as structured data (`contentTrust: untrusted-user-script-source`) and must never be concatenated into a tool description | Under a "always-allow" source-read policy, reads are no longer confirmed — the user takes that risk |
| The port's existence is found by scanning | Accepted: the extension is the client and cannot read a discovery file, so the default port 8643 has to be fixed; authentication is the backstop | The open port is visible |
| A malicious same-user process reads the daemon key file / control token (0600) | **Explicit non-goal** — a process holding the user's full privileges is already past the capability boundary of any local scheme | See the explicit non-goals in §3 |

## 3. Explicit non-goals

- **A malicious local process with the user's full privileges**: it can read `pairing.key` / `control.token`
  (both 0600) and can ptrace this user's processes. No purely local scheme can stop it; browser-side human
  approval of write operations is the only mitigation still in effect (unless the user turned on
  "always-allow").
- **Per-client isolation**: flat trust intentionally drops per-agent tokens, scopes, and individual
  revocation. Any same-user process that holds the control token has the same capabilities (full read/list,
  request writes). Revocation collapses to the global kill switch (discard K) or removing the server from an
  agent's own MCP config. This is the deliberate trade for single-enrollment simplicity; the per-operation
  human gates (write approval, source disclosure) are what still bound damage.
- **`clientId` as an authorization input**: the label a request carries (`sctl-cli`, `scriptcat-<name>`, or an
  MCP client's self-reported `clientInfo.name`) is unauthenticated and forgeable. It is used only for audit
  attribution and is never shown on an approval screen or used to gate anything.
- **Plaintext `ws://` and remote access**: loopback only; the daemon refuses to bind a non-loopback address.
  Remote `wss://` (TLS plus server identity) is a separate later design and is not implemented in v1.

## 4. The control channel (internal local connection) in detail

`sctl mcp` / CLI verbs and the daemon are **separate processes** that talk over the `/control/*` HTTP/JSON API
on the daemon's listener (same port as the extension WS surface, separate path). Security properties:

- **The only transport gate is the control token**: the daemon generates and writes the 0600 token file only
  **after** it has successfully bound the port (the loser of a port race does not write, so it cannot
  overwrite the winner's file); once the frontend sees a 200 from `/control/health` it knows the token is
  ready.
- **One identity dimension**: the control token (always required, proving same-user) is the whole of it. A
  request may carry an optional `X-Sctl-Client` label, but that only sets the audit attribution — it grants
  and constrains nothing.
- **No exemption for writes**: the control token only proves "same user on this host", it does not bypass
  browser-side human approval. Even holding the control token, a malicious local process still needs the user
  to press approve in the extension before a write happens (unless the corresponding "always-allow" policy is
  on).
- **The health check is unauthenticated**: it returns only `{ok, version}` and leaks no key, script, or client
  information; it is equivalent to the already-accepted risk that an open port is probeable.
- **Disconnect voids the request**: the frontend request (HTTP connection) drops → the daemon's request ctx is
  cancelled → `bridge.cancel` is sent to the extension to void the in-flight write operation.

## 5. Credentials persisted to disk

| File | Mode | Contents | Impact if leaked |
|---|---|---|---|
| `<dataDir>/pairing.key` | 0600 | The extension's long-term key K (hex), established by enrollment | Allows impersonating the extension when connecting to the daemon; still bound by write approval |
| `<dataDir>/control.token` | 0600 | The local control-channel token (regenerated on every serve start) | Allows impersonating the local frontend to call the control API; writes still need browser approval |

Three iron rules: **a token's plaintext never goes over the wire, never enters a log, never enters a URL**;
audit events **never record** a token, source code, or a URL containing credentials; keys are always persisted
to disk as 0600 in a 0700 directory and written atomically (temp file plus rename, no window with overly wide
permissions).

## 6. Daemon-side audit

The authoritative audit store lives on the **extension side** (the audit view via the extension's existing
logger, `component: local-access`): what an accepted request did is determined by that record.

The daemon side only fills in the part the extension **cannot see** — events that were blocked before an
extension session was ever established and therefore leave no extension-side record at all:

| Event | Trigger |
|---|---|
| `origin.rejected` | A WS connection was rejected by the Origin whitelist (present, non-extension Origin) |
| `handshake.failed` | Handshake HMAC verification failed / the 5s timeout elapsed / a non-`auth.response` message was sent during the handshake |
| `pairing.failed` | The enrollment handshake HMAC failed, or there was no valid enrollment code |
| `pairing.rate_limited` | Enrollment attempts exceeded 5 per minute |
| `request.rate_limited` | A client's read/write requests went over the limit and were rejected before being forwarded to the extension |
| `handshake.ok` | A session was established |

To view: `sctl status` prints one summary line aggregated by type, and `sctl status --json` outputs the full
events.

Boundary: the queryable store is **in memory only** (a fixed-capacity ring buffer, cleared whenever the daemon
restarts). Every event is additionally emitted once as a structured `warn` log line, so it also lands in
`<dataDir>/logs/sctl.log` (0600 in a 0700 directory) with the rest of the daemon's logs. What keeps that safe
is the closed event field set — time / type / client identifier / reason category, with no outlet for an
arbitrary payload — which is what enforces the iron rules of §5 on both outlets.
