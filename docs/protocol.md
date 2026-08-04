# ScriptCat JSON-RPC 2.0 over WebSocket

[`internal/pkg/protocol/protocol.json`](../internal/pkg/protocol/protocol.json) is the only maintained source for
method names, schemas, constants, cryptographic parameters, and limits. Go and TypeScript bindings plus business
schemas are generated from it. This document defines the ordering and security semantics that data schemas cannot
express. See [threat-model.md](./threat-model.md) for the security boundary.

## 1. Transport and message model

The ScriptCat extension opens one WebSocket connection to the local sctl daemon. Every text frame is exactly one
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

### 2.2 Hello and capabilities

After authentication the daemon announces its product version:

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

The daemon answers with an empty result and marks the connection usable only after accepting this request.

### 2.3 Liveness and shutdown

Either peer may send `$session.ping` as a request with empty params; the peer returns an empty result using the
same `id`. `limits.pingIntervalMs` is the suggested interval. The daemon sends `$session.shutdown` as a
notification before an orderly shutdown. A connection close cancels every in-flight request.

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

The current methods are:

| Method | Effect | Blocking behavior |
|---|---|---|
| `scripts.list` | read script summaries | none |
| `scripts.metadata.get` | read metadata | none |
| `scripts.source.get` | read source | disclosure confirmation |
| `scripts.source.grep` | search source | disclosure confirmation |
| `scripts.install.request` | install a script | write approval |
| `scripts.toggle.request` | enable or disable a script | write approval |
| `scripts.delete.request` | delete a script | write approval |
| `scripts.edit.request` | edit a script | write approval |

Source and metadata returned by these methods are untrusted user-script content. Consumers must not execute it,
render it as HTML, interpret it as instructions, or include credentials in logs. Source results carry a SHA-256
digest. Edit approval rechecks the staged digest and target identity before applying changes.

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

Run `make protocol-generate` after editing `protocol.json`, and `make protocol-sync-scriptcat` to update the
adjacent ScriptCat checkout. `make protocol-check` regenerates all artifacts and fails if the checked-in output
differs. Both peers parse the JSON-RPC structure directly and validate business parameters and results against
the generated method schemas at their dispatch boundaries.
