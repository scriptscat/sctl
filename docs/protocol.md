# ScriptCat JSON-RPC 2.0 over WebSocket

[`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json) is the only maintained source for
method names, schemas, constants, cryptographic parameters, and limits. Go and TypeScript bindings plus business
schemas are generated from it. This document defines the ordering and security semantics that data schemas cannot
express. See [threat-model.md](./threat-model.md) for the security boundary.

## 1. Transport and message model

Each peer extension — ScriptCat, and every paired sctl Browser instance — opens its own WebSocket connection to
the sctl daemon. Every text frame is exactly one
JSON-RPC 2.0 message and includes `"jsonrpc": "2.0"`. Requests and responses correlate through `id`;
notifications omit `id`. Batch requests are not supported.

The standard message forms are:

```json
{ "jsonrpc": "2.0", "id": "…", "method": "scripts.list", "params": {} }
{ "jsonrpc": "2.0", "method": "$session.shutdown", "params": {} }
{ "jsonrpc": "2.0", "method": "debugger.event", "params": { "tabId": 7, "method": "Page.loadEventFired", "params": {} } }
{ "jsonrpc": "2.0", "id": "…", "result": {} }
{ "jsonrpc": "2.0", "id": "…", "error": { "code": -32000, "message": "…", "data": {} } }
```

Business requests travel from the daemon to an extension; business notifications
([§3.3](#33-extension-notifications)) travel from an sctl Browser instance to the daemon. Frames larger than
`limits.maxFrameBytes`, malformed JSON-RPC messages, schema-invalid business or notification parameters, and
methods that neither start with `$` nor appear in `protocol.json` are rejected before dispatch; the daemon closes
the connection that sent one. The WebSocket server accepts an absent `Origin` and extension origins only:
`chrome-extension://`, `moz-extension://`, and `safari-web-extension://`.

## 2. Session lifecycle

```text
connect
  → $session.authenticate request
  → authentication response
  → $session.authenticated notification
  → $session.hello notification
  → $session.capabilities request
  → capabilities response
  → business requests
  → $session.shutdown notification or connection close
```

### 2.1 Authentication

The daemon starts authentication with a request:

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "method": "$session.authenticate",
  "params": { "nonceD": "<lowercase hex>" }
}
```

For an enrolled session, the extension answers the same `id`:

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "result": {
    "mode": "session",
    "nonceE": "<lowercase hex>",
    "hmac": "HMAC(K, context.sessionExt || nonceD || nonceE)"
  }
}
```

The daemon verifies the HMAC in constant time and proves possession of the same key with a notification:

```json
{
  "jsonrpc": "2.0",
  "method": "$session.authenticated",
  "params": {
    "hmac": "HMAC(K, context.sessionDaemon || nonceE || nonceD)"
  }
}
```

During first-time pairing, the extension uses `mode: "pairing"` and the one-time pairing code to derive the MAC
key with the KDF parameters in `protocol.json`. After verification, the daemon encrypts the persistent session
key with the derived encryption key and includes `{ciphertext, iv}` as `params.key` in
`$session.authenticated`. Pairing codes expire after `limits.extPairingCodeTtlMs`; authentication must complete
within `limits.authTimeoutMs`. Nonces are fresh for every connection.

The extension stores the session key in extension-local storage. The daemon stores its copy in a user-only file.
Disabling External Access deletes the extension copy and closes the connection, requiring enrollment again.

#### Browser instances

An sctl Browser instance identifies itself in the authentication response; a response without `peer` is
ScriptCat. The instance ID is 32 lowercase hex digits, generated randomly when the extension is installed:

```json
{
  "mode": "session",
  "nonceE": "<lowercase hex>",
  "hmac": "HMAC(K_instance, context.browserSessionExt || instanceId || nonceD || nonceE)",
  "peer": { "kind": "browser", "instanceId": "<32 lowercase hex>" }
}
```

The daemon answers with `HMAC(K_instance, context.browserSessionDaemon || instanceId || nonceE || nonceD)`.
Pairing uses `context.browserPairExt` / `context.browserPairDaemon` with the same `instanceId` term, keyed by the
pairing-code MAC key; key derivation and delivery are the same as for ScriptCat. The `browser*` contexts bind the
peer kind and the `instanceId` term binds the instance, so a MAC recorded for one instance or for ScriptCat never
verifies as another. An unknown `kind` or a malformed `instanceId` fails the handshake.

Each instance has its own session key, issued by its pairing; pairing a browser never touches ScriptCat's key.
The daemon selects the key by `instanceId`, so a session handshake from an instance that is not paired — or that
has been forgotten on the daemon side — fails like any other handshake failure.

### 2.2 Hello and capabilities

After authentication the daemon announces its product version for diagnostics. The extension does not use it
as a compatibility gate:

```json
{
  "jsonrpc": "2.0",
  "method": "$session.hello",
  "params": { "daemonVersion": "0.1.0" }
}
```

The extension then declares the generated schema it uses and the business methods it implements:

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "method": "$session.capabilities",
  "params": {
    "schemaVersion": "1.0.0",
    "methods": ["scripts.list", "scripts.toggle.request"]
  }
}
```

The daemon registers the connection before it answers with an empty result, so business requests may follow that
result immediately. A connection is usable only after this request is accepted.
A new ScriptCat connection replaces the previous ScriptCat connection; a browser instance's new connection
replaces only that instance's previous connection.

A browser instance also declares its name and self-reported product details:

```json
"params": {
  "schemaVersion": "1.0.0",
  "methods": ["tabs.list", "tabs.open"],
  "peer": {
    "name": "chrome-3f2a",
    "product": "Chrome",
    "productVersion": "129.0.6668.58",
    "extensionVersion": "0.1.0"
  }
}
```

`name` is 1–32 lowercase letters, digits, and `-`; the other fields are optional printable text of at most 64
bytes. A missing or invalid `peer` closes the connection. On its first pairing an instance proposes its default
name, the browser brand followed by `-` and the first four hex digits of its instance ID; renaming means
reconnecting with the new name. The daemon's registry is authoritative: names are unique across every paired
instance, online or offline. When another paired instance holds the name, the daemon answers the capabilities
request with application error `CONFLICT`, keeps the instance's previous name, and closes the connection; a
first pairing refused this way is not persisted.

### 2.3 Liveness and shutdown

Either peer may send `$session.ping` as a request with empty params; the peer returns an empty result using the
same `id`. The daemon actively sends one every `limits.pingIntervalMs` and closes the connection if
the response is not received within another interval. The daemon sends `$session.shutdown` as a notification
before an orderly shutdown. A connection close cancels every in-flight request.

## 3. Business RPC

The daemon sends each method listed in `protocol.json` directly as the JSON-RPC method. `params.input` is the
method's generated parameter type. `params.clientId` is a self-reported audit label only and is never used for
authorization.

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "method": "scripts.toggle.request",
  "params": {
    "clientId": "sctl-cli",
    "input": { "uuid": "…", "enable": true }
  }
}
```

A successful call returns the generated result type:

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "result": { "uuid": "…", "enabled": true }
}
```

Every method is owned by exactly one peer (`peer` in `protocol.json`): ScriptCat implements the `scripts.*`
methods and the sctl Browser extension implements the tab, window, and debugger methods. The current methods are:

| Method | Peer | Effect | Blocking behavior |
|---|---|---|---|
| `scripts.list` | ScriptCat | read script summaries | none |
| `scripts.metadata.get` | ScriptCat | read metadata | none |
| `scripts.source.get` | ScriptCat | read source | disclosure confirmation |
| `scripts.source.grep` | ScriptCat | search source | disclosure confirmation |
| `scripts.install.request` | ScriptCat | install a script | write approval |
| `scripts.toggle.request` | ScriptCat | enable or disable a script | write approval |
| `scripts.delete.request` | ScriptCat | delete a script | write approval |
| `scripts.edit.request` | ScriptCat | edit a script | write approval |
| `tabs.list` | browser | list tabs, optionally in one window | none |
| `tabs.open` | browser | open a URL in a new tab and return its tab ID | none |
| `tabs.close` | browser | close one or more tabs | none |
| `tabs.activate` | browser | activate a tab and focus its window | none |
| `tabs.current` | browser, internal | return the active tab of the last-focused normal window | none |
| `tabs.select` | browser, internal | make a tab the active tab of its window without focusing the window | none |
| `windows.list` | browser | list windows | none |
| `debugger.send` | browser, internal | send one Chrome DevTools Protocol command to a tab | none |
| `debugger.detach` | browser, internal | detach the debugger from one tab, or from every tab | none |

Source and metadata returned by these methods are untrusted user-script content. Consumers must not execute it,
render it as HTML, interpret it as instructions, or include credentials in logs. Source results carry a SHA-256
digest. Edit approval rechecks the staged digest and target identity before applying changes.

Tab titles and URLs are controlled by web pages; `tabs.list` marks its result with
`contentTrust: "untrusted-page-content"` and the same handling rules apply. A list method declares a
`mergeField`: the required array property in its result that holds the listed items, so results from several
browser instances combine by concatenating that array. Methods without `mergeField` are never combined.

`scripts.source.get` accepts an optional `maxBytes` budget for a whole-file response. When the UTF-8 source is
larger, the extension returns `PAYLOAD_TOO_LARGE` before placing the source in a WebSocket frame; callers should
use `scripts.source.grep` and then request a `startLine`/`endLine` window. The budget does not apply when a line
window is present. `sctl mcp` supplies this budget for whole-file reads, while the CLI omits it so an operator can
still redirect a complete source file.

### 3.1 Routing and target selection

The daemon routes each call by the method's `peer`, not by which methods a connection declared: `scripts.*`
calls go to the ScriptCat connection, browser methods go to a browser instance. A connection must still have
declared the method, otherwise the call fails with `METHOD_NOT_FOUND`.

A browser call carries an optional target, either an instance name or an instance-ID prefix; the daemon resolves
it because only the daemon knows which instances are online. A target is first matched against names exactly, and
only if no name matches is it treated as an instance-ID prefix over every paired instance, online or offline.

| Target | Result |
|---|---|
| none, no instance online | `NO_BROWSER_CONNECTED` |
| none, exactly one instance online | that instance |
| none, several online, method with `mergeField` | every online instance; results are combined |
| none, several online, method without `mergeField` | `BROWSER_AMBIGUOUS`; the message lists the online instances |
| matches no paired instance | `BROWSER_NOT_FOUND` |
| ID prefix matches several paired instances | `BROWSER_AMBIGUOUS`; the message lists the matching instances |
| matches one paired instance that is not connected | `BROWSER_OFFLINE` |
| matches one online instance | that instance |

A combined call is sent to every online instance at once. The result is the first instance's result with its
`mergeField` array replaced by the concatenation of every instance's array, in instance-name order, and each
item gains a `browser` object naming its source: `{"id": "<instance ID>", "name": "<instance name>"}`. An
instance that answers `NOT_FOUND` — for example, it has no window with the requested ID — contributes no items;
the call fails with `NOT_FOUND` only when every instance answers it. Any other failing instance fails the whole
call; partial results are never returned. A call routed to a single instance returns that instance's result
unchanged.

A target on a `scripts.*` call is rejected with `INVALID_REQUEST`; otherwise `scripts.*` routing and its errors do
not depend on browser instances. If the target connection closes while a call is in flight — for a combined
call, any of its instances — the call is voided and the requester receives `OPERATION_EXPIRED`, as for ScriptCat.

### 3.2 Internal methods

A method marked `internal` in `protocol.json` is reserved for components inside the daemon. `/control/call`
answers it with `INVALID_REQUEST` exactly as for an unknown method, and `sctl mcp` registers no tool for it,
so a control-token holder cannot send it directly. The `debugger.*` methods are internal because they relay raw
Chrome DevTools Protocol (CDP) traffic with the user's signed-in browser state. `tabs.current` and `tabs.select`
are internal because they exist only to serve the daemon's page automation, which reaches callers through
`/control/page` instead.

`debugger.send` input is `{tabId, sessionId?, method, params?}`: `method` and `params` are the CDP command, sent to
the tab's top-level debugger session, or to the child session `sessionId` — the `sessionId` of a CDP
`Target.attachedToTarget` event, used for out-of-process iframes. Its result is `{result}`, the CDP command's
result object unchanged. `debugger.send` answers `PAGE_NOT_AUTOMATABLE` with Chrome's reason when Chrome refuses to
attach, `NOT_FOUND` for an unknown tab, `INVALID_REQUEST` with CDP's message when the CDP command itself fails, and
`DEBUGGER_DETACHED` when the debugger detaches while the command runs. `debugger.detach` input is `{tabId?}`: with
`tabId` it detaches that tab, without it every tab the instance has attached; its result `{tabIds}` lists the tabs it
detached. CDP params and results are
open objects: the schema checks only that they are JSON objects, and the frame limit still applies.

`tabs.current` input is `{}`; its result `{tabId, windowId}` is the active tab of the last-focused window of type
`normal`. The daemon asks for it once when a page command names no tab: the last-focused window stays the user's
browser window while they type in a terminal, when no browser window has focus at all. It answers `NOT_FOUND`
when no normal window is open. `tabs.select` input is `{tabId}`; it makes that tab the active tab of its window
without focusing the window, unlike `tabs.activate`, and answers `NOT_FOUND` for an unknown tab. Its result is
`{tabId, windowId}`.

The daemon drives the debugger lifecycle. A page command on a tab the daemon has not attached sends
`Emulation.setFocusEmulationEnabled {enabled: true}` through `debugger.send` first; that first send makes the
extension attach. The daemon then treats the tab as attached until it sends `debugger.detach` — after 5 minutes
without a page command on the tab, or on `page detach` — or until the extension reports `debugger.detached` for
it, or the instance disconnects. Page commands on the same tab run one at a time in arrival order. A
`debugger.detached` notification for a tab, or the instance disconnecting, fails the command running on that tab
with `DEBUGGER_DETACHED`, and the next page command attaches again. The extension keeps a fallback of its own: a tab
with no `debugger.send` for 10 minutes is detached and reported as `debugger.detached` with reason `idle_timeout`, so
the infobar does not stay up if the daemon stops driving it, and when its connection to the daemon closes it detaches
every tab without notifying. `debugger.detach` for a tab whose attach is still in flight waits for that attach and
then detaches it. The extension records its attached tabs in `chrome.storage.session`, because Chrome keeps a
debugger attached when the MV3 service worker restarts: after a restart it keeps driving the recorded tabs that are
still attached, and reports each one that no longer is as `debugger.detached` with reason `target_closed`.

### 3.3 Extension notifications

An sctl Browser instance sends notifications to the daemon. They are the only business messages in that
direction. A notification has no `id` and no response, and its `params` is the notification's type directly —
there is no `input` wrapper or `clientId`:

| Notification | Params | Sent when |
|---|---|---|
| `debugger.event` | `{tabId, sessionId?, method, params?}` | the debugger attached to `tabId` receives a CDP event; `sessionId` names the child session that produced it |
| `debugger.detached` | `{tabId, reason}` | Chrome detaches the debugger from `tabId` (`chrome.debugger.onDetach`), where `reason` is Chrome's detach reason, such as `target_closed` or `canceled_by_user`; or the extension's 10-minute fallback detaches it, with reason `idle_timeout`; or, after a service-worker restart, a tab it had attached is no longer attached, with reason `target_closed` |

The daemon validates a notification against its schema like any other frame; a notification that carries an
`id` or fails its schema is an invalid frame. Notifications with these names from ScriptCat are valid frames
that the daemon drops without affecting the ScriptCat connection.

## 4. Errors

Protocol errors use the JSON-RPC standard codes. Application failures use the server-defined code `-32000` and
put the stable domain code in `error.data.code`:

```json
{
  "jsonrpc": "2.0",
  "id": "…",
  "error": {
    "code": -32000,
    "message": "The user rejected the operation",
    "data": {
      "code": "USER_REJECTED",
      "operationId": "…"
    }
  }
}
```

The generated `errorCodes` list is authoritative for domain codes. Messages are human-readable diagnostics;
callers branch on the numeric JSON-RPC code and `data.code`, not on message text.

These codes are reserved for browser target selection failures ([§3.1](#31-routing-and-target-selection)):

| Code | Meaning |
|---|---|
| `NO_BROWSER_CONNECTED` | a browser method was called while no browser instance is online |
| `BROWSER_OFFLINE` | the target names a paired browser instance that is not connected |
| `BROWSER_NOT_FOUND` | the target matches no paired browser instance; `/control/browsers/forget` returns it too when the name or ID matches none |
| `BROWSER_AMBIGUOUS` | the target instance-ID prefix matches more than one paired instance, or a method without `mergeField` was called without a target while several instances are online |

These codes are reserved for page automation on a browser instance:

| Code | Meaning |
|---|---|
| `STALE_REF` | an element reference is no longer valid, or belongs to another tab's snapshot |
| `TIMEOUT` | an automatic wait or an explicit wait condition did not hold in time |
| `TARGET_AMBIGUOUS` | a selector matches more than one element |
| `PAGE_NOT_AUTOMATABLE` | Chrome refuses to attach the debugger to the page, for example a `chrome://` page or another extension's page |
| `PAGE_HIDDEN` | the operation cannot complete on a background tab even with focus emulation |
| `DEBUGGER_DETACHED` | the debugger detached while a page command was running |
| `DIALOG_OPEN` | an unhandled JavaScript dialog blocks the page |
| `EVAL_ERROR` | an evaluated expression threw in the page |
| `NAVIGATION_FAILED` | a navigation failed with a network error |

`PAYLOAD_TOO_LARGE` is shared: ScriptCat returns it for an oversized source read (§3), and page automation
returns it when a snapshot or screenshot exceeds its size limit.

## 5. Cancellation and approval

Write and source-disclosure requests remain pending until the user decides. If the requester disconnects,
cancels, or times out, the daemon sends the standard cancellation notification used by JSON-RPC tooling:

```json
{
  "jsonrpc": "2.0",
  "method": "$/cancelRequest",
  "params": { "id": "<original request id>" }
}
```

The extension invalidates the operation and sends no response for it. Approval and cancellation are serialized
and effective once, so a cancelled operation cannot later execute. A late response is ignored by the daemon.
The extension persists pending approval state because an MV3 service worker can sleep; the decision event sends
the JSON-RPC response through the offscreen WebSocket owner.

## 6. Generation and conformance

`protocol.json` annotates ownership: each method and each entry of `notifications` has one `peer` (`scriptcat`
or `browser`), and each `errorCodes` entry and each `crypto.context` entry lists the `peers` that use it. The
`browser*` context strings are reserved for browser-instance handshakes, so a MAC computed for one peer kind never
verifies as the other; the pairing KDF strings are shared. A notification's name may not start with `$` or reuse a
method name, because both travel in the JSON-RPC `method` field. `internal` on a method is daemon-side metadata
and does not reach the TypeScript output. The generator emits one set of bindings per peer:

| Output | Contents |
|---|---|
| `internal/pkg/protocol/generated/protocol.generated.go` | every method, notification, type, error code, and context key, for the daemon and CLI |
| `internal/pkg/protocol/generated/*.ts` | ScriptCat's methods, the types they reference, and the codes and contexts listing `scriptcat` |
| `extension/src/protocol/generated/*.ts` | the same selection for `browser`, plus its notifications and the types they reference |

The ScriptCat TypeScript must stay byte-identical to the copy in the paired ScriptCat revision, because ScriptCat
declares every generated method as a capability and its conformance test compares the method and error-code
lists exactly. Adding browser-owned definitions therefore never changes ScriptCat's files or `schemaVersion`.

Run `make protocol-generate` after editing `protocol.json`, and `make protocol-sync-scriptcat` to update the
adjacent ScriptCat checkout. `make protocol-check` regenerates all artifacts and fails if the checked-in output
differs or a generated file is untracked. Both peers parse the JSON-RPC structure directly. ScriptCat validates
business parameters with generated native TypeScript type guards, so extension startup does not compile schemas
at runtime.
