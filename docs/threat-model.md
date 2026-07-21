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
- the **long-term shared key K** between the extension and the daemon (derived from and delivered via a
  one-time pairing code, never in plaintext over the wire);
- the **control token** between the local frontend (`sctl mcp` / CLI verbs) and the daemon (written to a 0600
  file once the daemon has bound its port; only same-user processes can read it).

**The second gate, present throughout:** every write operation (install / toggle / delete) and every source
disclosure is ultimately decided by **human approval in the browser**; even if a malicious local process gets
the control token and calls sctl, the write still needs the user to press approve on the extension's
confirmation page.

## 2. Attack surface and countermeasures

| Threat | Countermeasure | Residual risk |
|---|---|---|
| A web page connects straight to the daemon with `new WebSocket("ws://127.0.0.1:8643")` | A connection must complete the mutual HMAC handshake before it can send or receive any business message; a connection without credentials necessarily fails at the challenge-response step and is disconnected on the 5s timeout **with no reason echoed back** (close 1008). **No Origin check** — a non-browser process can forge any Origin, so the handshake itself is the only gate. Failed attempts are recorded in the daemon-side audit (§6) | A page can probe that the port is open |
| A web page impersonates the local frontend with `fetch("http://127.0.0.1:8643/control/…")` | Apart from `/control/health`, every control API requires an `X-Sctl-Control-Token` header, compared in constant time against the daemon's 0600 token; a web page cannot read that file, so it gets a 401 and the action never runs at all | Port / health information can be probed (see below) |
| A local process grabs 8643 to impersonate the daemon, or connects in while impersonating the extension | **Mutual** HMAC-SHA-256 challenge-response between the extension and the daemon ([protocol.md](./protocol.md) §3.1); the long-term key K comes from a one-time pairing code and never travels in plaintext; nonces are regenerated per connection, so replays are useless | See the "malicious same-user process" row |
| An MCP client (agent) exceeds its privileges | Interactive pairing per client (an 8-character code checked on both ends), only the SHA-256 of the token is stored, least-privilege scopes, `tools/list` filtered by scope (an ungranted tool is never registered at all), per-client read/write rate limits, single-client revocation plus a global kill switch. The daemon holds the authoritative token store; the extension's mirror is for the UI and the second check | A dynamic scope change needs a reconnect as a fallback (a known v1 deferral) |
| Write operations are abused (installing a malicious script / bulk deletion) | Two-phase confirmation plus a TOCTOU re-check at the moment of approval (staged `contentHash`, target `existingCodeHash`, client not revoked), plus newly installed scripts disabled by default; calls are purely blocking, so a requester disconnect voids them. "Always-allow" is an explicit security-downgrade switch (amber warning in the UI) | Under "always-allow" a write is no longer confirmed by a human — the user takes that risk |
| Source code leaks | Source disclosure is a separate scope, and the first disclosure per client and per script needs human approval (reading is a privacy matter and is not exempted by the write policy); script-controlled text is always returned as structured data (`contentTrust: untrusted-user-script-source`) and must never be concatenated into a tool description | The CLI (`sctl scripts source`) is exempt from disclosure — see below |
| The port's existence is found by scanning | Accepted: the extension is the client and cannot read a discovery file, so the default port 8643 has to be fixed; authentication is the backstop | The open port is visible |
| A malicious same-user process reads the daemon key file / control token (0600) | **Explicit non-goal** — a process holding the user's full privileges is already past the capability boundary of any local scheme | See the explicit non-goals in §3 |

## 3. Explicit non-goals

- **A malicious local process with the user's full privileges**: it can read `pairing.key` / `control.token`
  (both 0600) and can ptrace this user's processes. No purely local scheme can stop it; browser-side human
  approval of write operations is the only mitigation still in effect (unless the user turned on
  "always-allow").
- **The disclosure exemption for `sctl scripts source`**: the CLI is typed by the user in their own terminal,
  and any process able to run sctl can also read the key file, so exempting the CLI from disclosure adds no
  attack surface. Source reads by MCP clients (through `sctl mcp`) are **not** exempt and trigger disclosure
  approval as usual.
- **The built-in `sctl-cli` identity**: CLI verbs run with the full scope set and without pairing; they do not
  appear in the extension's "paired clients" list and cannot be revoked individually (their lifecycle is that
  of the bridge toggle). The trust it carries is exactly "holding the control token = a same-user process";
  write operations still go through browser approval.
- **Plaintext `ws://` and remote access**: loopback only; the daemon refuses to bind a non-loopback address.
  Remote `wss://` (TLS plus server identity) is a separate later design and is not implemented in v1.

## 4. The control channel (internal local connection) in detail

`sctl mcp` / CLI verbs and the daemon are **separate processes** that talk over the `/control/*` HTTP/JSON API
on the daemon's listener (same port as the extension WS surface, separate path). Security properties:

- **The only transport gate is the control token**: the daemon generates and writes the 0600 token file only
  **after** it has successfully bound the port (the loser of a port race does not write, so it cannot
  overwrite the winner's file); once the frontend sees a 200 from `/control/health` it knows the token is
  ready.
- **Two identity dimensions**: the control token (always required, proving same-user) plus an optional MCP
  client token (supplying it constrains the call to that client's scopes and makes it revocable; omitting it
  means the built-in `sctl-cli` full identity).
- **No exemption for writes**: the control token only proves "same user on this host", it does not bypass
  browser-side human approval. Even holding the control token, a malicious local process still needs the user
  to press approve in the extension before a write happens (unless "always-allow" is on).
- **The health check is unauthenticated**: it returns only `{ok, version}` and leaks no key, script, or client
  information; it is equivalent to the already-accepted risk that an open port is probeable.
- **Disconnect voids the request**: the frontend request (HTTP connection) drops → the daemon's request ctx is
  cancelled → `bridge.cancel` is sent to the extension to void the in-flight write operation.

## 5. Credentials persisted to disk

| File | Mode | Contents | Impact if leaked |
|---|---|---|---|
| `<dataDir>/pairing.key` | 0600 | The extension's long-term pairing key K (hex) | Allows impersonating the extension when connecting to the daemon; still bound by write approval |
| `<dataDir>/control.token` | 0600 | The local control-channel token (regenerated on every serve start) | Allows impersonating the local frontend to call the control API; writes still need browser approval |
| `<dataDir>/clients.json` | 0600 | MCP client records; only the SHA-256 of a token is stored | The token plaintext cannot be recovered; clientId/scope/timestamps are readable |
| `<dataDir>/mcp-clients/<name>.json` | 0600 | The paired identity cached by one `sctl mcp` instance (including the token plaintext) | Allows impersonating that MCP client (limited to its scopes, revocable from the extension) |

Three iron rules: **a token's plaintext never goes over the wire, never enters a log, never enters a URL**;
audit events **never record** a token, source code, or a URL containing credentials; keys are always persisted
to disk as 0600 in a 0700 directory and written atomically (temp file plus rename, no window with overly wide
permissions).

## 6. Daemon-side audit

The authoritative audit store lives on the **extension side** (the audit view in the settings page): what a
paired client did is determined by that record.

The daemon side only fills in the part the extension **cannot see** — events that were blocked before an
extension session was ever established and therefore leave no extension-side record at all:

| Event | Trigger |
|---|---|
| `handshake.failed` | Handshake HMAC verification failed / the 5s timeout elapsed / a non-`auth.response` message was sent during the handshake |
| `pairing.failed` | The pairing handshake HMAC failed, or there was no valid pairing code |
| `pairing.rate_limited` | Pairing attempts exceeded 5 per minute |
| `request.rate_limited` | A client's read/write requests went over the limit and were rejected before being forwarded to the extension |
| `handshake.ok` / `client.revoked` | A session was established / a client was revoked |

To view: `sctl status` prints one summary line aggregated by type, and `sctl status --json` outputs the full
events.

Boundary: the queryable store is **in memory only** (a fixed-capacity ring buffer, cleared whenever the daemon
restarts). Every event is additionally emitted once as a structured `warn` log line, so it also lands in
`<dataDir>/logs/sctl.log` (0600 in a 0700 directory) with the rest of the daemon's logs. What keeps that safe
is the closed event field set — time / type / client identifier / reason category, with no outlet for an
arbitrary payload — which is what enforces the iron rules of §5 on both outlets.
