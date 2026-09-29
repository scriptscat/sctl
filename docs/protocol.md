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
{ "jsonrpc": "2.0", "id": "…", "result": {} }
{ "jsonrpc": "2.0", "id": "…", "error": { "code": -32000, "message": "…", "data": {} } }
```

Frames larger than `limits.maxFrameBytes`, malformed JSON-RPC messages, and schema-invalid business parameters
are rejected before dispatch. The WebSocket server accepts an absent `Origin` and extension origins only:
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
methods and the sctl Browser extension implements the tab, window, and reading list methods. Every method also carries a
destruction `level`:

| Level | Meaning | Enforced by |
|---|---|---|
| L0 | runs directly | — |
| L1 | runs only when the input carries `confirm: true`; otherwise the call fails with `CONFIRMATION_REQUIRED` and nothing runs | the daemon, before forwarding; the sctl Browser extension checks again before running the handler |
| L2 | runs only after a human approves it in the browser | the extension; the daemon forwards the call and waits (§5) |

An L1 method's parameter type declares `confirm` as an optional property whose value must be the constant `true`.
It is optional in the schema so that an unconfirmed call that reaches the extension is answered with
`CONFIRMATION_REQUIRED` rather than `INVALID_REQUEST`. L2 is exactly the set of methods whose `blocking` is
`approval` or `disclosure`. The current methods are:

| Method | Peer | Effect | Blocking behavior | Level |
|---|---|---|---|---|
| `scripts.list` | ScriptCat | read script summaries | none | L0 |
| `scripts.metadata.get` | ScriptCat | read metadata | none | L0 |
| `scripts.source.get` | ScriptCat | read source | disclosure confirmation | L2 |
| `scripts.source.grep` | ScriptCat | search source | disclosure confirmation | L2 |
| `scripts.install.request` | ScriptCat | install a script | write approval | L2 |
| `scripts.toggle.request` | ScriptCat | enable or disable a script | write approval | L2 |
| `scripts.delete.request` | ScriptCat | delete a script | write approval | L2 |
| `scripts.edit.request` | ScriptCat | edit a script | write approval | L2 |
| `tabs.list` | browser | list tabs, optionally in one window | none | L0 |
| `tabs.open` | browser | open a URL in a new tab and return its tab ID | none | L0 |
| `tabs.close` | browser | close one or more tabs | none | L0 |
| `tabs.activate` | browser | activate a tab and focus its window | none | L0 |
| `windows.list` | browser | list windows | none | L0 |
| `readingList.list` | browser | list reading list entries, newest first | none | L0 |
| `readingList.add` | browser | add a URL to the reading list | none | L0 |
| `readingList.markRead` | browser | mark reading list entries read or unread | none | L0 |
| `readingList.remove` | browser | remove entries from the reading list | none | L1 |

Source and metadata returned by these methods are untrusted user-script content. Consumers must not execute it,
render it as HTML, interpret it as instructions, or include credentials in logs. Source results carry a SHA-256
digest. Edit approval rechecks the staged digest and target identity before applying changes.

Tab titles and URLs, and reading list titles, are controlled by web pages; `tabs.list` and `readingList.list` mark
their results with `contentTrust: "untrusted-page-content"` and the same handling rules apply. A list method
declares a `mergeField`: the required array property in its result that holds the listed items, so results from
several browser instances combine by concatenating that array. A list result may also declare a boolean `hasMore`,
meaning the instance has items it did not return. Methods without `mergeField` are never combined. A list method
whose parameters declare `limit` returns at most that many items per instance: 100 when it is omitted, and a value
above 1000 is rejected with `INVALID_REQUEST`.

The reading list methods answer `UNSUPPORTED` when the browser does not provide `chrome.readingList`.
`readingList.add` answers `CONFLICT` for a URL that is already in the list, and its title defaults to the URL.
`readingList.markRead` and `readingList.remove` are all-or-nothing: if any given URL is not in the list they
answer `NOT_FOUND` and change nothing.

A browser method's result must fit in one frame of at most `limits.maxFrameBytes` UTF-8 bytes, because the daemon
drops a connection that sends a larger frame. When the serialized response would exceed it, the sctl Browser
extension answers `PAYLOAD_TOO_LARGE` instead of sending the result.

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
unchanged. If any combined result carries `hasMore`, the combined result's `hasMore` is `true` when at least one
instance answered `true`.

A target on a `scripts.*` call is rejected with `INVALID_REQUEST`; otherwise `scripts.*` routing and its errors do
not depend on browser instances. If the target connection closes while a call is in flight — for a combined
call, any of its instances — the call is voided and the requester receives `OPERATION_EXPIRED`, as for ScriptCat.

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

These codes are browser-only too:

| Code | Meaning |
|---|---|
| `CONFIRMATION_REQUIRED` | an L1 method was called without `confirm: true` in its input ([§3](#3-business-rpc)); nothing was executed |
| `UNSUPPORTED` | the browser does not provide the API the method needs; the message names the missing API |

`USER_REJECTED` and `PAYLOAD_TOO_LARGE` are registered for both peers. For the browser, `USER_REJECTED` is
reserved for a rejected L2 approval and `PAYLOAD_TOO_LARGE` answers a result that would exceed the frame limit
([§3](#3-business-rpc)).

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

`protocol.json` annotates ownership: each method has one `peer` (`scriptcat` or `browser`), and each
`errorCodes` entry and each `crypto.context` entry lists the `peers` that use it. The `browser*` context strings
are reserved for browser-instance handshakes, so a MAC computed for one peer kind never verifies as the other; the
pairing KDF strings are shared. The generator emits one set of bindings per peer:

| Output | Contents |
|---|---|
| `internal/pkg/protocol/generated/protocol.generated.go` | every method, type, error code, and context key, for the daemon and CLI |
| `internal/pkg/protocol/generated/*.ts` | ScriptCat's methods, the types they reference, and the codes and contexts listing `scriptcat` |
| `extension/src/protocol/generated/*.ts` | the same selection for `browser`, plus each method's `level` in `RPC_METHODS` |

The ScriptCat TypeScript must stay byte-identical to the copy in the paired ScriptCat revision, because ScriptCat
declares every generated method as a capability and its conformance test compares the method and error-code
lists exactly. Adding browser-owned definitions therefore never changes ScriptCat's files or `schemaVersion`.

The generator rejects a method whose `level` is missing or not `L0`/`L1`/`L2`, an L2 method without a human gate
(`blocking` is `none`) or a human-gated method that is not L2, an L1 method whose parameters do not declare the
optional `confirm: {"const": true}`, and a list result whose `hasMore` is not a boolean. The ScriptCat TypeScript
does not carry `level`, so it stays byte-identical.

Run `make protocol-generate` after editing `protocol.json`, and `make protocol-sync-scriptcat` to update the
adjacent ScriptCat checkout. `make protocol-check` regenerates all artifacts and fails if the checked-in output
differs or a generated file is untracked. Both peers parse the JSON-RPC structure directly. ScriptCat validates
business parameters with generated native TypeScript type guards, so extension startup does not compile schemas
at runtime.
