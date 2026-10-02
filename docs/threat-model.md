# sctl Threat Model (bridge daemon + local control API)

> Status: kept in sync with the sctl v0.1 daemon/CLI/MCP implementation. The authority for constants is
> [`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json), and the protocol semantics
> are in [`protocol.md`](./protocol.md). This document covers only the security boundary and its trade-offs.

## 1. Positioning and overall trade-offs

sctl exposes a WebSocket listener protected by a mutual authentication handshake. It defaults to
`127.0.0.1:8643`, while an explicit `--listen-address` may bind another interface. This avoids new browser
permissions and a Native Messaging host installer while preserving single-binary distribution.

**There are only two trust anchors:**
- the **long-term shared keys** between each peer extension and the daemon, each established once by
  **enrollment**: the daemon prints a one-time code in the terminal (never over the wire), the user types it into
  the extension (for ScriptCat, its 「外部接入 / External Access」 page), and the key is derived and delivered
  under that code. ScriptCat holds one key K; every sctl Browser instance holds its own
  instance key, so pairing, renaming, or forgetting a browser never affects ScriptCat's K. Trust is **flat** —
  after enrollment the CLI and every MCP agent inherit trust through the extension ↔ daemon channels and never
  enroll again; there is no per-client pairing, token, scope, or revocation.
- the **control token** between the local frontend (`sctl mcp` / CLI verbs) and the daemon (written to a
  user-only file once the daemon has bound its port: POSIX mode 0600 or a protected current-user DACL on
  Windows).

**The second gate, present throughout:** every write operation (install / edit / toggle / delete) and every source
disclosure is ultimately decided by **human approval in the browser**, applied identically to the CLI and to
MCP; even if a malicious local process gets the control token and calls sctl, the write still needs the user to
press approve on the extension's confirmation page (unless the corresponding global policy is set to
"always-allow").

**Browser control has no human gate, by design — except L2.** The `sctl browsers` / `tabs` / `windows` commands
and the other L0 and L1 browser operations, with their MCP tools, are neither approved per operation nor limited to
particular tabs: any holder of the control token can list, open, close, and activate tabs and list windows in
**every** paired sctl Browser instance, immediately, and can read all of its bookmarks, reading list, history,
downloads, and cookie values (the login state of every site, below). This is a deliberate trade for low operating friction; the
control token (same user on this host) is the only gate. The L1 destruction level
([protocol.md](./protocol.md#3-business-rpc)) is not a security boundary either: it only makes the caller state
`confirm: true` in the call input so a destructive call is not made by accident, and any control-token holder can
supply it — a control-token holder can perform every L0 and L1 operation.

The L2 level is the one browser-side human gate, and it covers only deleting bookmarks (`bookmarks.remove`) and
uninstalling an extension (`extensions.uninstall`): the sctl Browser extension opens its own approval window and
does nothing until a person approves there. Bookmark deletion then re-checks that the bookmarks still match what the
window showed. An uninstall is carried out from the approving click itself, because Chrome refuses
`chrome.management.uninstall` without a user gesture, and Chrome then asks again in its own confirmation dialog, so it
takes two human confirmations. Every other browser operation stays ungated, including disabling an extension (L1):
a control-token holder can disable any extension that Chrome lets the user disable — ScriptCat included, which cuts
ScriptCat off from the daemon — but not sctl Browser itself or an extension installed by enterprise policy. The
requester label attached to each queued request is the call's self-reported `clientId` and proves nothing, and a
page-controlled bookmark title or an author-controlled extension name or description in the window can claim
anything; the only safe rule is to approve a request only when you know you started it.

**Cookie values are returned unmasked.** `cookies list` and `cookies get` (MCP `cookies`) return every cookie value as the
browser holds it, HttpOnly and Secure cookies included, with no masking and no opt-in switch: a cookie value is a site's login
state. Any control-token holder can therefore read the login state of every site in a paired sctl Browser instance, and
`cookies set`, `rm` and `clear` let it plant or delete cookies; none of these is human-gated (L0 and L1 only). The extension
holds the host permission `<all_urls>` for this. `cookies list` also covers partitioned (CHIPS) cookies: a real-browser probe
showed that `chrome.cookies.getAll` without a partition key omits them and that the empty key `partitionKey: {}` returns all of
them, so the extension always passes it and the partitioned login state is exposed the same way.

**Page automation carries the same trade, with a larger reach.** The `sctl page` commands and `page_*` MCP tools
attach Chrome's debugger to a tab through the sctl Browser extension's `debugger` permission, again with no
per-operation approval and no per-site limit. A control-token holder can therefore:
- read the content of any page Chrome lets the debugger attach to, in any paired instance;
- type into, click, and submit forms in those pages with trusted input events, and run scripts in them
  (`page eval`) — and so act with the user's signed-in session: read what the page can read, submit what the page
  can submit;
- do this in background tabs the user is not looking at.

Chrome's "sctl Browser started debugging this browser" infobar is the only visible sign that a page is attached,
and launching Chrome with `--silent-debugger-extension-api` hides it. While a tab is attached, the daemon turns
on focus emulation for it, so the page believes it is visible and focused: timers, animations, and media that a
hidden page would pause keep running, and a page that checks visibility or focus cannot tell it is in the
background. The emulation ends when the debugger detaches. Everything a page returns — snapshots, `page eval`
results, JS dialog text, page URLs and titles — is untrusted page content (`contentTrust: untrusted-page-content`): it can carry prompt
injection aimed at the agent reading it, and must be treated as data, never as instructions.

## 2. Attack surface and countermeasures

| Threat | Countermeasure | Residual risk |
|---|---|---|
| A web page connects straight to the daemon with `new WebSocket("ws://127.0.0.1:8643")` | An **Origin whitelist** rejects any connection whose `Origin` is present and not an extension origin (`chrome-extension://` etc.) — a cheap pre-filter that a browser page cannot get past (the browser stamps Origin, page JS cannot forge it). Beyond that, a connection must complete the mutual HMAC handshake before it can send or receive any business message; without credentials it fails the challenge-response and is disconnected on the 5s timeout **with no reason echoed back** (close 1008). A non-browser process can forge any Origin, so the handshake remains the real gate. Both rejections are recorded in the daemon-side audit (§6) | A page can probe that the port is open |
| A web page impersonates the local frontend with `fetch("http://127.0.0.1:8643/control/…")` | Apart from `/control/health`, every control API requires an `X-Sctl-Control-Token` header, compared in constant time against the daemon's user-only token; a web page cannot read that file, so it gets a 401 and the action never runs at all | Port / health information can be probed (see below) |
| A local process grabs 8643 to impersonate the daemon, or connects in while impersonating the extension | **Mutual** HMAC-SHA-256 challenge-response between the extension and the daemon ([protocol.md](./protocol.md#21-authentication)); long-term keys come from a one-time enrollment code and never travel in plaintext; nonces are regenerated per connection, so replays are useless. A browser instance's MAC also binds its peer kind and instance ID, so a recorded MAC cannot be replayed as ScriptCat or as another instance | See the "malicious same-user process" row |
| A process that reaches the daemon requests a privileged action | Flat trust deliberately grants any control-token holder full read/list and the ability to *request* writes; the gate is not per-client authorization but the **per-operation human gate**: writes need browser approval and source reads need disclosure approval, both keyed by script (extension session). There is no per-client scope or request-frequency limit | Any process that obtains the control token has the same capabilities; write requests remain browser-gated unless always-allow is enabled, while browser control (tabs, windows, tab groups, reading list, bookmarks, history, recently closed, downloads, cookies, browsing data, and extensions in every paired sctl Browser instance) and page automation (reading pages and running scripts in them) are not gated at all except for L2 bookmark deletion and extension uninstall, which need approval in that browser |
| Write operations are abused (installing a malicious script / bulk deletion) | Two-phase confirmation plus a TOCTOU re-check at the moment of approval (staged `contentHash`, target `existingCodeHash`); calls are purely blocking, so a requester disconnect voids them. The install page's own enable toggle decides the enabled state (installs are usable immediately, like a normal install). "Always-allow" is an explicit security-downgrade switch (amber warning in the UI) | Under "always-allow" a write is no longer confirmed by a human — the user takes that risk |
| Source code leaks | Source disclosure is gated by its own **source-read policy** (approval by default), applied to the CLI and MCP alike — the CLI is **not** exempt; reading is a privacy matter and is not covered by the write policy. Script-controlled text is always returned as structured data (`contentTrust: untrusted-user-script-source`) and must never be concatenated into a tool description | Under a "always-allow" source-read policy, reads are no longer confirmed — the user takes that risk |
| The port's existence is found by scanning | Accepted: the extension is the client and cannot read a discovery file, so the default port 8643 has to be fixed; authentication is the backstop | The open port is visible |
| A malicious same-user process reads the daemon key file / control token (0600) | **Explicit non-goal** — a process holding the user's full privileges is already past the capability boundary of any local scheme | See the explicit non-goals in §3 |

## 3. Explicit non-goals

- **A malicious local process with the user's full privileges**: it can read `pairing.key` / `browsers.json` /
  `control.token` (all 0600) and can ptrace this user's processes. No purely local scheme can stop it; browser-side human
  approval of write operations is the only mitigation still in effect (unless the user turned on
  "always-allow"); browser control and page automation have no such gate, so it can drive every paired browser and
  every debuggable page in it freely.
- **Per-client isolation**: flat trust intentionally drops per-agent tokens, scopes, and individual
  revocation. Any same-user process that holds the control token has the same capabilities (full read/list,
  request writes). Revocation collapses to per-peer switches — discarding ScriptCat's K, or forgetting a browser
  instance on the daemon, which deletes its key and registry entry, disconnects it, and makes its next handshake
  fail — or removing the server from an agent's own MCP config. This is the deliberate trade for single-enrollment simplicity; the per-operation
  human gates (write approval, source disclosure) are what still bound damage.
- **`clientId` as an authorization input**: the label a request carries (`sctl-cli`, `scriptcat-<name>`, or an
  MCP client's self-reported `clientInfo.name`) is unauthenticated and forgeable. It is used for audit
  attribution and never gates anything; the sctl Browser approval queue carries it as the requester label, which
  is equally unverified.
- **Transport confidentiality and remote client isolation**: the protocol uses plaintext `ws://`, has no TLS
  server identity, and has no per-remote-client credentials. The default loopback binding confines that risk to
  the host. Explicitly binding another interface exposes metadata and source traffic to that network and must be
  treated as an operator-selected security downgrade. Native `wss://` is not implemented in v1.

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
  on). Browser control is the exception: the control token alone drives every paired sctl Browser instance
  (§1).
- **The health check is unauthenticated**: it returns only `{ok, version}` and leaks no key, script, or client
  information; it is equivalent to the already-accepted risk that an open port is probeable.
- **Disconnect voids the request**: the frontend request (HTTP connection) drops → the daemon's request ctx is
  cancelled → `$/cancelRequest` is sent to the extension to void the in-flight write operation.

## 5. Credentials persisted to disk

| File | Access | Contents | Impact if leaked |
|---|---|---|---|
| `<dataDir>/pairing.key` | POSIX 0600; protected current-user DACL on Windows | ScriptCat's long-term key K (hex), established by enrollment | Allows impersonating ScriptCat when connecting to the daemon; still bound by write approval |
| `<dataDir>/browsers.json` | POSIX 0600; protected current-user DACL on Windows | The registry of paired sctl Browser instances: instance ID, unique name, each instance's long-term key (hex), and last self-reported product and versions | Allows impersonating any paired browser instance when connecting to the daemon |
| `<dataDir>/control.token` | POSIX 0600; protected current-user DACL on Windows | The local control-channel token (regenerated on every serve start) | Allows impersonating the local frontend to call the control API; writes still need browser approval, while tabs and windows in every paired sctl Browser instance can be controlled directly |

Three iron rules: **a token's plaintext never goes over the wire, never enters a log, never enters a URL**;
audit events **never record** a token, source code, or a URL containing credentials; keys are written atomically
through a restricted temporary file (POSIX 0600, or a protected DACL granting only the current Windows user)
before rename, with no window where the credential itself is broadly readable.

## 6. Daemon-side audit

The authoritative audit store lives on the **extension side** (the audit view via the extension's existing
logger, `component: external-access`): what an accepted request did is determined by that record.

The daemon side only fills in the part the extension **cannot see** — events that were blocked before an
extension session was ever established and therefore leave no extension-side record at all:

| Event | Trigger |
|---|---|
| `origin.rejected` | A WS connection was rejected by the Origin whitelist (present, non-extension Origin) |
| `handshake.failed` | Handshake HMAC verification failed / the 5s timeout elapsed / an invalid response to `$session.authenticate` was sent / a browser instance that is not paired (or was forgotten) tried a session handshake |
| `pairing.failed` | The enrollment handshake HMAC failed, or there was no valid enrollment code |
| `pairing.rate_limited` | Enrollment attempts exceeded 5 per minute |
| `handshake.ok` | A session was established |

Events for a connection that declared a well-formed browser instance identity carry `client: browser:<instanceId>`;
ScriptCat events leave `client` empty.

To view: `sctl status` prints one summary line aggregated by type, and `sctl status -o json` outputs the full
events.

Boundary: the queryable store is **in memory only** (a fixed-capacity ring buffer, cleared whenever the daemon
restarts). Every event is additionally emitted once as a structured `warn` log line, so it also lands in
`<dataDir>/logs/sctl.log` (0600 in a 0700 directory) with the rest of the daemon's logs. What keeps that safe
is the closed event field set — time / type / client identifier / reason category, with no outlet for an
arbitrary payload — which is what enforces the iron rules of §5 on both outlets.
