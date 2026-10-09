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
methods and the sctl Browser extension implements the tab, window, debugger, reading list, and bookmark methods. Every method also carries a
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
| `tabs.current` | browser, internal | return the active tab of the last-focused normal window | none | L0 |
| `tabs.select` | browser, internal | make a tab the active tab of its window without focusing the window | none | L0 |
| `windows.list` | browser | list windows | none | L0 |
| `debugger.send` | browser, internal | send one Chrome DevTools Protocol command to a tab | none | L0 |
| `debugger.detach` | browser, internal | detach the debugger from one tab, or from every tab | none | L0 |
| `debugger.record` | browser, internal | mark a tab as recording, exempting it from the extension's idle fallback, or clear the mark | none | L0 |
| `debugger.body` | browser, internal | read the request or response body of a recorded network request, cut at 1 MiB | none | L0 |
| `debugger.targets` | browser, internal | list the tabs a debugger can attach to, with their DevTools target IDs | none | L0 |
| `debugger.userAgent` | browser, internal | return the browser's real `navigator.userAgent` | none | L0 |
| `debugger.open` | browser, internal | open a tab for the raw CDP endpoint, marked endpoint-owned, and return its tab and target IDs | none | L0 |
| `debugger.close` | browser, internal | close a tab for the raw CDP endpoint | none | L0 |
| `debugger.own` | browser, internal | mark a tab as endpoint-owned, exempting it from the extension's idle fallback, or clear the mark | none | L0 |
| `tabs.move` | browser | move tabs to a window and position (`index` -1 is the end) | none | L0 |
| `tabs.pin` | browser | pin tabs | none | L0 |
| `tabs.unpin` | browser | unpin tabs | none | L0 |
| `tabs.mute` | browser | mute tabs | none | L0 |
| `tabs.unmute` | browser | unmute tabs | none | L0 |
| `tabs.reload` | browser | reload tabs, optionally bypassing the cache | none | L0 |
| `tabs.duplicate` | browser | duplicate a tab and return the new tab ID | none | L0 |
| `windows.open` | browser | open a window, optionally with URLs and a state, and return its window ID | none | L0 |
| `windows.close` | browser | close windows | none | L0 |
| `windows.focus` | browser | focus a window | none | L0 |
| `windows.state` | browser | set a window's state | none | L0 |
| `tabGroups.list` | browser | list tab groups, optionally in one window | none | L0 |
| `tabGroups.create` | browser | group tabs of one window into a new group and return its group ID | none | L0 |
| `tabGroups.add` | browser | add tabs to an existing group | none | L0 |
| `tabGroups.edit` | browser | change a group's title, color, or collapsed state | none | L0 |
| `tabGroups.ungroup` | browser | remove tabs from their groups | none | L0 |
| `readingList.list` | browser | list reading list entries, newest first | none | L0 |
| `readingList.add` | browser | add a URL to the reading list | none | L0 |
| `readingList.markRead` | browser | mark reading list entries read or unread | none | L0 |
| `readingList.remove` | browser | remove entries from the reading list | none | L1 |
| `history.search` | browser | search history by text and time range, newest first | none | L0 |
| `history.visits` | browser | list each visit of one URL, newest first | none | L0 |
| `history.remove` | browser | delete every visit of the given URLs from history | none | L1 |
| `history.clear` | browser | delete history in a time range, or all history | none | L1 |
| `browsingData.clear` | browser | clear browsing data of the given types | none | L1 |
| `recent.list` | browser | list recently closed tabs and windows, newest first | none | L0 |
| `recent.restore` | browser | restore a closed tab or window, or the most recently closed | none | L0 |
| `downloads.list` | browser | list downloads, newest first | none | L0 |
| `downloads.start` | browser | start a download into the default download directory | none | L0 |
| `downloads.pause` | browser | pause a download | none | L0 |
| `downloads.resume` | browser | resume a paused download | none | L0 |
| `downloads.show` | browser | show a download's file in the system file manager | none | L0 |
| `downloads.cancel` | browser | cancel an in-progress download | none | L1 |
| `downloads.erase` | browser | remove download records, keeping the files | none | L1 |
| `downloads.deleteFile` | browser | delete a completed download's file from disk, keeping its record | none | L1 |
| `cookies.list` | browser | list cookies, partitioned ones included | none | L0 |
| `cookies.get` | browser | read one cookie | none | L0 |
| `cookies.set` | browser | set a cookie | none | L0 |
| `cookies.remove` | browser | delete one cookie | none | L1 |
| `cookies.clear` | browser | delete the cookies of a domain, or all cookies | none | L1 |
| `extensions.list` | browser | list installed extensions and apps | none | L0 |
| `extensions.enable` | browser | enable an extension | none | L0 |
| `extensions.disable` | browser | disable an extension | none | L1 |
| `extensions.uninstall` | browser | uninstall an extension, after Chrome's own confirmation dialog too | write approval | L2 |
| `bookmarks.list` | browser | list a folder's children, or a whole subtree | none | L0 |
| `bookmarks.search` | browser | search bookmarks by title and URL, with folder paths | none | L0 |
| `bookmarks.add` | browser | add a bookmark | none | L0 |
| `bookmarks.mkdir` | browser | create a bookmark folder | none | L0 |
| `bookmarks.move` | browser | move bookmarks or folders into a folder | none | L0 |
| `bookmarks.edit` | browser | change a bookmark's title or URL, or a folder's title | none | L0 |
| `bookmarks.remove` | browser | delete bookmarks and folders, each folder with its contents | write approval | L2 |

Source and metadata returned by these methods are untrusted user-script content. Consumers must not execute it,
render it as HTML, interpret it as instructions, or include credentials in logs. Source results carry a SHA-256
digest. Edit approval rechecks the staged digest and target identity before applying changes.

Tab titles and URLs, tab group titles, reading list titles, history titles and URLs, recently closed titles and URLs, download file names and URLs, cookie names and values, and bookmark titles and URLs are controlled by web pages, and extension names by their authors; `tabs.list`, `tabGroups.list`, `readingList.list`, `history.search`, `recent.list`, `downloads.list`, `cookies.list`, `cookies.get`, `cookies.set`, `bookmarks.list`, `bookmarks.search`, `extensions.list`, and `extensions.uninstall` mark
their results with `contentTrust: "untrusted-page-content"` and the same handling rules apply. A list method
declares a `mergeField`: the required array property in its result that holds the listed items, so results from
several browser instances combine by concatenating that array. A list result may also declare a boolean `hasMore`,
meaning the instance has items it did not return. Methods without `mergeField` are never combined. A list method
whose parameters declare `limit` returns at most that many items per instance: 100 when it is omitted, and a value
above 1000 is rejected with `INVALID_REQUEST`.

Every method of a data domain answers `UNSUPPORTED`, naming the missing API, when the browser does not provide that
domain's `chrome.*` namespace (`bookmarks`, `readingList`, `tabGroups`, `history`, `sessions`, `downloads`, `cookies`,
`browsingData`, `management`). Chrome 125, the extension's minimum version, provides all of them; another Chromium
browser may not.
`readingList.add` answers `CONFLICT` for a URL that is already in the list, and its title defaults to the URL.
`readingList.markRead` and `readingList.remove` are all-or-nothing: if any given URL is not in the list they
answer `NOT_FOUND` and change nothing.

The tab and window management methods (`tabs.move`, `tabs.pin`, `tabs.unpin`, `tabs.mute`, `tabs.unmute`,
`tabs.reload`, `tabs.duplicate`, `windows.open`, `windows.close`, `windows.focus`, `windows.state`) are L0 and are
all-or-nothing for several IDs: every tab or window is checked first, and an unknown one answers `NOT_FOUND` with
nothing changed. `tabs.move` without `index` (or with `-1`) moves to the end of the window and answers `NOT_FOUND`
for an unknown target `windowId`. A window `state` other than `normal`, `minimized`, `maximized`, or `fullscreen`
answers `INVALID_REQUEST`, as does an `index` below -1.

The tab group methods (`tabGroups.*`) are L0 and need the `tabGroups` permission. `tabs.list` items carry `groupId`
(`-1` when the tab is in no group). `tabGroups.list` marks its result `contentTrust: "untrusted-page-content"`
because group titles are page- or user-controlled. `tabGroups.create` requires every tab in the same window and
answers `INVALID_REQUEST` otherwise; a `color` other than `grey`, `blue`, `red`, `yellow`, `green`, `pink`, `purple`,
`cyan`, or `orange` answers `INVALID_REQUEST`, as does a `tabGroups.edit` that changes nothing. An unknown tab or group
answers `NOT_FOUND` and changes nothing (all-or-nothing). When `tabGroups.ungroup` removes a group's last tab, the
browser deletes the group.

History times are integer milliseconds since the epoch (`startTime`, `endTime`, `lastVisitTime`, `visitTime`); a `startTime` after `endTime`
answers `INVALID_REQUEST`. `history.search` without a time range searches all history (`startTime` 0), returns items newest first
with URL, title, last visit time and visit count, and applies `limit` and `hasMore`; `history.visits` lists the visits of one URL
the same way, with the transition type. `history.remove` and `history.clear` are L1 and need the `history` permission:
`history.remove` takes valid URLs only (`INVALID_REQUEST` and nothing deleted otherwise, unknown URLs are ignored), and `history.clear`
deletes all history when neither time is given, otherwise the range with an open start at 0 and an open end at now.

`browsingData.clear` is L1 and needs the `browsingData` permission. `types` is a non-empty list drawn from `cache`, `cacheStorage`,
`cookies`, `downloads`, `fileSystems`, `formData`, `history`, `indexedDB`, `localStorage`, `serviceWorkers`, and `webSQL`; passwords are
excluded and any other value answers `INVALID_REQUEST`. `since` (milliseconds) defaults to 0, all time. `origins` (bare http or https
origins) limits the clearing to those origins and is only valid when every type is one of `cache`, `cacheStorage`, `cookies`,
`fileSystems`, `indexedDB`, `localStorage`, `serviceWorkers`, `webSQL`, the types `chrome.browsingData` can filter by origin;
combining it with `downloads`, `formData`, or `history` answers `INVALID_REQUEST` and nothing is cleared.

`recent.list` returns at most 25 items (Chrome's retention limit; `limit` is 1 to 25, default 25), newest first, each with session ID, `type` (`tab` or `window`), `closedTime` (milliseconds; Chrome reports seconds and the extension converts), title and URL, and `tabCount` for windows, and reports `hasMore`. `recent.restore` reopens the session with the given `sessionId`, or the most recently closed one when none is given, and returns `tabId` or `windowId`; an unknown session answers `NOT_FOUND`.

The download methods (`downloads.*`) need the `downloads` permission and identify a download by its integer ID; an unknown ID
answers `NOT_FOUND`. `downloads.list` returns downloads newest first (by start time), each with ID, URL, local file path
(`filename`), `state` (`in_progress`, `complete`, `interrupted`), `bytesReceived`, `totalBytes` (`-1` when unknown), `startTime`
(milliseconds) and `exists` (whether the file is still on disk); it accepts `state`, `query` and `limit` and reports `hasMore`.
`downloads.start` saves into the browser's default download directory with `conflictAction` `uniquify` and no save-as dialog, so an
existing file is never overwritten; an optional `filename` must be relative and free of `..` segments, and an absolute path
(including a leading backslash or a drive letter) or a `..` segment answers `INVALID_REQUEST` without starting anything. Chrome's own
refusal of `pause`, `resume`, `cancel` or `start` (for example pausing a finished download) is surfaced as `INVALID_REQUEST`. `downloads.cancel`,
`downloads.erase` and `downloads.deleteFile` are L1. `downloads.erase` takes several IDs, is all-or-nothing, and removes only the records.
`downloads.deleteFile` removes only the file of a `complete` download and keeps the record; any other state answers `INVALID_REQUEST`.

The cookie methods (`cookies.*`) need the `cookies` permission and the host permission `<all_urls>`. `cookies.list` calls
`chrome.cookies.getAll` with `partitionKey: {}`: without it Chrome omits partitioned (CHIPS) cookies, and the empty key returns
all of them (verified on a real browser). Each item has name, value (returned unmasked), domain, path, `expires` (milliseconds; absent
for a session cookie), `secure`, `httpOnly`, `sameSite` (`no_restriction`, `lax`, `strict`, `unspecified`), `session`, and
`partitionTopLevelSite` for a partitioned cookie; it accepts `url` or `domain` (mutually exclusive, else `INVALID_REQUEST`; `domain`
includes subdomains), `name` and `limit`, and reports `hasMore`. `cookies.get` answers `NOT_FOUND` when no cookie matches, and prefers
a non-partitioned cookie over a partitioned one of the same name. `cookies.set` without `expires` creates a session cookie; a cookie
Chrome refuses to store answers `INVALID_REQUEST` carrying Chrome's reason. `cookies.remove` and `cookies.clear` are L1: `remove`
deletes the one cookie `cookies.get` would return for the same `url` and `name` and answers `NOT_FOUND` when nothing matches, `clear` takes exactly one of `domain` (with subdomains) and `all: true` (else
`INVALID_REQUEST`), and both return `deleted`, the number of cookies removed; partitioned cookies are removed with their own
partition key.

The extension methods (`extensions.*`) need the `management` permission. `extensions.list` returns every installed
extension and app with its ID, name, version, `enabled`, `type`, `installType` (as `chrome.management` reports it:
`normal`, `development`, `sideload`, `admin` or `other`) and `mayDisable`. `extensions.enable` and `extensions.disable`
return the ID and the new `enabled` state; a refusal from Chrome answers `INVALID_REQUEST` carrying Chrome's reason.
`extensions.disable` is L1 and `extensions.uninstall` is L2. Neither accepts the sctl Browser extension's own ID or an
extension installed by enterprise policy (`installType` `admin` or `mayDisable` false); both answer `INVALID_REQUEST`.
Disabling ScriptCat is allowed and disconnects it from the daemon. An unknown ID answers `NOT_FOUND`, and
`extensions.uninstall` runs all of these checks before asking for approval. Its result is the uninstalled extension's
ID and name; how the approval continues into Chrome's own dialog is in [§5](#5-cancellation-and-approval).

Bookmark IDs are the browser's own. An unknown ID answers `NOT_FOUND`. `bookmarks.list` returns a folder's direct
children (the root's children, the built-in top-level folders, when no folder is given) and applies `limit`; with
`recursive` it returns the whole subtree as a flat depth-first list linked by `parentId`, ignores `limit`, and is
bounded only by the frame limit. `bookmarks.search` adds `path`, the titles of the enclosing folders from the
outermost down. The root and the built-in top-level folders cannot be moved or edited, a URL cannot be set on a
folder, and a folder cannot move into itself or its own descendant; each answers `INVALID_REQUEST`, as does an
`index` past the end of the target folder. `bookmarks.move` is all-or-nothing: every check runs before the first
node moves. `bookmarks.remove` takes at most 500 IDs and checks all of them before asking for approval: an unknown ID
answers `NOT_FOUND`, and more than 500 IDs, the root, or a built-in top-level folder answers `INVALID_REQUEST`. An
ID inside another listed folder is deleted with that folder and counted once. Its result lists the deleted IDs and
the number of bookmarks and folders deleted, contents included.

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
detached. `debugger.record` input is `{tabId, recording}` and its result `{recording}` echoes the state now in
force: a recording tab is exempt from the extension's 10-minute idle fallback below, and stopping re-arms the fallback
from that moment. Starting to record a tab that is not attached answers `DEBUGGER_DETACHED` (the daemon always
attaches first); stopping one that is not attached succeeds and does nothing. CDP params and results are
open objects: the schema checks only that they are JSON objects, and the frame limit still applies.

`debugger.body` input is `{tabId, sessionId?, requestId, part}`: `requestId` is a CDP network request ID seen on the
tab's top-level session or on the child session `sessionId`, and `part` is `request` or `response`. The extension
reads the whole body in the browser (`Network.getRequestPostData` or `Network.getResponseBody`) and cuts it to its
first 1 MiB before replying, because Chrome returns a body only whole and a body over one frame could not cross
`debugger.send` at all. The result is `{body, base64Encoded, size, truncated}`: text bodies are cut at a UTF-8
character boundary, binary bodies (`base64Encoded: true`) are cut in decoded bytes and re-encoded, and `size` is the
original size in bytes. When Chrome no longer keeps the body the call still succeeds, with only `unavailable`:
`navigated` (the page navigated away, which drops the bodies of all earlier requests), `noData` (the request is in
flight or failed, has no body, or the page never read it), `evicted` (over Chrome's buffer, about 20 MB per resource,
or pushed out by later responses), or `noPostData` (no request body). Any other CDP error answers `INVALID_REQUEST`
with CDP's message. Like `debugger.record` it never attaches: a tab that is not attached answers `DEBUGGER_DETACHED`.
Both CDP commands block until a JavaScript dialog open in the tab closes, so the daemon does not send
`debugger.body` while it knows a dialog is open, and stops waiting for one when a dialog opens meanwhile.

`tabs.current` input is `{}`; its result `{tabId, windowId}` is the active tab of the last-focused window of type
`normal`. The daemon asks for it once when a page command names no tab: the last-focused window stays the user's
browser window while they type in a terminal, when no browser window has focus at all. It answers `NOT_FOUND`
when no normal window is open. `tabs.select` input is `{tabId}`; it makes that tab the active tab of its window
without focusing the window, unlike `tabs.activate`, and answers `NOT_FOUND` for an unknown tab. Its result is
`{tabId, windowId}`.

The daemon drives the debugger lifecycle. A page command on a tab the daemon has not attached sends
`Emulation.setFocusEmulationEnabled {enabled: true}` through `debugger.send` first; that first send makes the
extension attach. If any of the commands that set up the attach gets no answer within 5 seconds, the daemon gives
the attach up, sends `debugger.detach` for the tab, and fails the command with `PAGE_UNRESPONSIVE`. For `page goto`
and `page reload` it first tries to recover: on a fresh attach it sends only `Page.navigate` or `Page.reload` — on a
page held by a leftover dialog Chrome still answers that as the first command, and the navigation closes the dialog —
sends `debugger.detach`, and then attaches again and runs the navigation as usual, so the page loads twice. If that
command is answered with an error (for example Chrome rejecting an invalid URL), the daemon still sends
`debugger.detach` and fails the command with that error instead of `PAGE_UNRESPONSIVE`. The daemon then treats the tab as attached until it sends `debugger.detach` — after 5 minutes
without a page command on the tab, or on `page detach` — or until the extension reports `debugger.detached` for
it, or the instance disconnects. `debug start` sends `debugger.record {recording: true}` after attaching, and the
daemon then does not detach the tab for idleness; `debug stop`, or 60 minutes without a debug command on the tab,
sends `debugger.record {recording: false}` and restarts the 5-minute idle timer. A detach ends recording on both sides
without a `debugger.record`. Page commands on the same tab run one at a time in arrival order. A
`debugger.detached` notification for a tab, or the instance disconnecting, fails the command running on that tab
with `DEBUGGER_DETACHED`, and the next page command attaches again. The extension keeps a fallback of its own: a tab
with no `debugger.send` or `debugger.body` for 10 minutes is detached and reported as `debugger.detached` with reason `idle_timeout`, so
the infobar does not stay up if the daemon stops driving it, and when its connection to the daemon closes it detaches
every tab without notifying. `debugger.detach` for a tab whose attach is still in flight waits for that attach and
then detaches it. A JavaScript dialog that opened while the debugger was attached and is still open when the debugger
detaches can no longer be handled by any later debugger session, and blocks every command that would attach one
again; only navigating or reloading the page closes it. So before either side detaches a tab — the daemon on
`page detach`, its idle detach, or when it gives an attach up; the extension on any `debugger.detach`, its 10-minute
fallback, or its connection closing — it sends `Page.handleJavaScriptDialog {accept: false}` to the tab if it has seen
`Page.javascriptDialogOpening` there without a matching `Page.javascriptDialogClosed`. A failed dismissal does not stop
the detach. A detach Chrome starts itself, such as the user cancelling the infobar, can still leave such a dialog
behind. The extension records its attached tabs, which of them are recording, and which have an open dialog, in `chrome.storage.session`, because Chrome keeps a
debugger attached when the MV3 service worker restarts: after a restart it keeps driving the recorded tabs that are
still attached, and reports each one that no longer is as `debugger.detached` with reason `target_closed`. A tab that is still attached
keeps its recording state across the restart; any detach of a tab clears it.

`cdp send` is a `/control/page` action (`cdp.send`, input `{method, params?}`), not a protocol method: the daemon
sends the command to the tab's top-level session with `debugger.send`, after the same target selection, attach and
per-tab queue as other page commands. `method` must have the form `Domain.method` and `params` must be an object,
otherwise the daemon answers `INVALID_REQUEST` without attaching. It also answers `INVALID_REQUEST` without sending
anything for the commands that would break state the daemon depends on: `Page.disable`, `Runtime.disable`,
`Network.disable`, `Log.disable` (page automation and the debug records need those domains enabled),
`Emulation.setFocusEmulationEnabled` (commands on background tabs depend on the focus emulation), and
`Target.setAutoAttach` and `Target.detachFromTarget` (the daemon uses them to discover, pause and release
out-of-process iframes). Every other command is sent as is, also while a JS dialog is open. The result is Chrome's
result object with `tabId` and `contentTrust` added; a command Chrome rejects answers `INVALID_REQUEST` carrying
Chrome's error text, a result over the 4 MiB frame limit `PAYLOAD_TOO_LARGE`, and a command that does not finish in
time `TIMEOUT`. Events the command causes are not returned.

The raw CDP endpoint uses five more internal methods. `debugger.targets` returns `{targets: [{tabId, targetId, title, url}]}`:
the `page` targets that have a tab, from `chrome.debugger.getTargets`, without `chrome://` and other browser-internal
pages, extension pages (`chrome-extension://`), DevTools pages, and the Chrome Web Store, none of which Chrome lets a
debugger attach to; `targetId` is the DevTools target ID. `debugger.userAgent` returns `{userAgent}`. `debugger.open`
takes `{url, background?}`, opens the tab in the last-focused window (not activated when `background` is true), marks it
endpoint-owned before returning, and answers `{tabId, targetId}` (or `NOT_FOUND`, leaving the opened tab unmarked, when
Chrome lists no target for it); it exists apart from `tabs.open` because it must
return the target ID and set the mark in one step, and `debugger.close` `{tabId}` is its counterpart, answering
`NOT_FOUND` for a missing tab. `debugger.own` `{tabId, owned}` marks or unmarks any tab, attached or not, which the
daemon uses for tabs the endpoint's client attaches to. The extension's 10-minute fallback never detaches an
endpoint-owned tab, the same as a recording one; the mark is kept in `chrome.storage.session` and survives a
service-worker restart for tabs that still exist. Any detach of the tab, closing it, `debugger.own {owned: false}`, or
the connection to the daemon closing clears the mark, and the fallback timer restarts.

### 3.3 Extension notifications

An sctl Browser instance sends notifications to the daemon. They are the only business messages in that
direction. A notification has no `id` and no response, and its `params` is the notification's type directly —
there is no `input` wrapper or `clientId`:

| Notification | Params | Sent when |
|---|---|---|
| `debugger.event` | `{tabId, sessionId?, method, params?}` | the debugger attached to `tabId` receives a CDP event; `sessionId` names the child session that produced it |
| `debugger.tabCreated` | `{tabId, targetId, title, url}` | a tab is created and is already listed as an attachable target; a tab whose target is not listed yet is reported by `debugger.tabUpdated` once it is |
| `debugger.tabUpdated` | `{tabId, targetId, title, url}` | an attachable tab's URL, title or load status changes; a tab that is not attachable is never reported, including one that navigates to such a page |
| `debugger.tabRemoved` | `{tabId}` | any tab is closed, including tabs that were never reported |
| `debugger.detached` | `{tabId, reason}` | Chrome detaches the debugger from `tabId` (`chrome.debugger.onDetach`), where `reason` is Chrome's detach reason, such as `target_closed` or `canceled_by_user`; or the extension's 10-minute fallback detaches it, with reason `idle_timeout`; or, after a service-worker restart, a tab it had attached is no longer attached, with reason `target_closed` |

The daemon validates a notification against its schema like any other frame; a notification that carries an
`id` or fails its schema is an invalid frame. Notifications with these names from ScriptCat are valid frames
that the daemon drops without affecting the ScriptCat connection.

### 3.4 Raw CDP endpoint

The raw CDP endpoint is not part of the JSON-RPC protocol: it is an HTTP and WebSocket surface the daemon serves on
its own listener under `/cdp/`, for Playwright `chromium.connectOverCDP` and Puppeteer `connect`. A control-token
holder creates it through `/control/cdp/endpoint` (`sctl cdp endpoint`, MCP `cdp_endpoint`); creating it for a browser
that is not online fails with the target errors of [§3.1](#31-routing-and-target-selection), such as `BROWSER_OFFLINE`
or `NO_BROWSER_CONNECTED`. Each browser instance has at most one endpoint, and creating it again returns the same
addresses:

| Address | For |
|---|---|
| `http://<host>/cdp/<secret>` | Playwright `chromium.connectOverCDP` |
| `ws://<host>/cdp/<secret>/devtools/browser/<id>` | Puppeteer `connect({browserWSEndpoint})` |

`<host>` is the address the requester used to reach the daemon; when that is a host name rather than an IP literal or
`localhost`, `sctl cdp endpoint` and `sctl cdp status` answer `INVALID_REQUEST` instead of an address the `Host` check
below would refuse. `<secret>` is 128 random bits from `crypto/rand`,
hex-encoded, kept only in the daemon's memory; `<id>` is a random ID in the shape of a Chrome browser target ID. The
address is the credential: a client presents nothing else. `GET /cdp/<secret>/json/version`, with or without a trailing
slash, answers Chrome's JSON shape — `Browser` (the `Chrome/<version>` product from the User-Agent), `Protocol-Version`
`1.3`, `User-Agent` from `debugger.userAgent`, and `webSocketDebuggerUrl`, the WebSocket address built from the
request's `Host` — or `503` when the browser cannot answer. Every request under `/cdp/` is checked in this order:

1. `Host` must be an IP literal or `localhost` (with any port), otherwise `403`: a page that rebinds its own domain
   to 127.0.0.1 still sends that domain as `Host`.
2. A request carrying any `Origin` header is refused with `403`, like Chrome's own remote debugging by default:
   Playwright and Puppeteer in Node send none, while a browser always stamps one. Extension and DevTools origins are
   refused as well.
3. A wrong secret, an unknown path, and a WebSocket path whose `<id>` does not match all answer the same `404`, so a
   response never tells whether an endpoint exists.

One client at a time: while one is connected, or being handed the tabs, another WebSocket request is refused with
`409` saying a client is already connected; a request to the WebSocket address that is not a WebSocket upgrade
answers `400` before anything else happens. A WebSocket request first makes the page automation component hand
the browser's tabs over (architecture.md, [Raw CDP endpoint](./architecture.md#raw-cdp-endpoint)); if that fails —
for example the browser is offline — the request is refused with `503` and the reason, and nothing changes. Then the
connection is upgraded; a client message may be at most `limits.maxFrameBytes`. Before the first command the client's
session sends to a tab, the daemon marks the tab endpoint-owned with `debugger.own`; a tab opened through
`debugger.open` is already marked. The daemon answers the client's CDP itself for browser-level
commands (`Browser.*`, `Target.*`), forwards commands sent on an attached tab's session to that tab and returns the
tab's events on the same session, and answers a feature it cannot provide (new browser contexts, permissions, window
size and position, ignoring certificate errors) with a CDP error that names the unsupported feature.

The client is disconnected when it closes the connection or the connection drops, when the session ends itself, on
`sctl cdp close`, when the endpoint expires, when the browser instance disconnects, and when the daemon exits. The
daemon sends a close frame — `1000` when the session ended itself, `1001` for the other daemon-side reasons, `1013`
when the client fell more than 4096 browser notifications behind — and then, in order: dismisses with
`Page.handleJavaScriptDialog {accept: false}` every JS dialog it saw open (`Page.javascriptDialogOpening` without a
matching `Page.javascriptDialogClosed`) on a tab the client attached, on the session that reported it; sends
`debugger.detach` for each tab the client attached that Chrome has not detached or closed meanwhile; sends
`debugger.own {owned: false}` for each tab it marked; and gives the tabs back to sctl's page commands. No tab is
closed, including tabs the client opened. A failed step is logged and does not stop the next one.

The endpoint expires — its addresses answer `404` from then on — on `sctl cdp close` (which answers once the client's
cleanup is done, and succeeds when there is no endpoint), when the daemon exits, after 60 minutes without a connected
client, and when the browser instance is forgotten with `sctl browsers forget`, whether it is online or not. The
60 minutes count from creation and again from each disconnect, and do not run while a client is connected. Until it
expires, the same address can connect again after a client disconnects. A browser instance disconnecting closes the
client's connection but does not expire the endpoint: once the instance is back, the same address works again, while
a connection attempt while it is offline fails with `503`. `sctl cdp status` and `sctl cdp close` resolve the browser
like any browser command, except that a paired browser named with `--browser` (or `browser`) may be offline, so its
address can be inspected and revoked before it reconnects; without a named browser they still need exactly one
online browser.

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
| `PAGE_UNRESPONSIVE` | the page did not answer within 5 seconds while the debugger was being attached, possibly because of a JavaScript dialog left open after an earlier debugger detached; reloading or navigating the page recovers it |
| `EVAL_ERROR` | an evaluated expression threw in the page |
| `NAVIGATION_FAILED` | a navigation failed with a network error |
| `ENDPOINT_CONNECTED` | a raw CDP endpoint client is connected and owns the browser's tabs, so sctl's own page, debug and `cdp send` commands are refused until it disconnects |

`USER_REJECTED` and `PAYLOAD_TOO_LARGE` are registered for both peers. For the browser, `USER_REJECTED` is
reserved for a rejected L2 approval (including an uninstall cancelled in Chrome's own dialog, §5) and `PAYLOAD_TOO_LARGE` answers a result that would exceed the frame limit
([§3](#3-business-rpc)); page automation also returns it when a snapshot or screenshot exceeds its size limit.

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

The sctl Browser extension gates its L2 methods the same way, in its own approval window:

1. The offscreen document receives the request and hands it, with its request `id`, `params.clientId`, and the
   connection it arrived on, to the service worker. The service worker runs the method's checks first; a failing
   check is answered at once and opens no window.
2. Otherwise the request joins the approval queue, persisted in `chrome.storage.session`, and the service worker
   tells the offscreen document that the answer is deferred. The answer is not held open on that message, because
   the service worker can be stopped while the user decides. The offscreen document then tells the daemon that the
   request is waiting for a human, on the connection the request arrived on:

   ```json
   { "jsonrpc": "2.0", "method": "$/approvalPending", "params": { "id": "<original request id>" } }
   ```

   A request that fails its checks in step 1, or that the daemon cancelled before it was queued, gets no such
   notification. The daemon passes it to the waiting requester at most once, and ignores it for an unknown request,
   one sent on another connection or by a ScriptCat connection, or a method that does not wait for a human. Requesters tell the user they are waiting only after it arrives: the CLI prints
   its waiting line and `sctl mcp` starts sending progress then. Only the sctl Browser extension sends it
   (`sessionMethods` lists it for the browser peer alone); for ScriptCat's gates, which send nothing similar,
   requesters say they are waiting as soon as they call.
3. The user's decision reaches the offscreen document as a separate command carrying the request `id`, and the
   offscreen document sends it as the JSON-RPC response on the connection the request arrived on. Approval
   re-checks the target against what the window showed and answers `CONFLICT`, changing nothing, if it differs.
   Rejection, or closing the window while requests are queued, answers `USER_REJECTED`.
4. The extension expires a request itself shortly before `limits.writeDecisionTtlMs` after it arrived and answers
   `OPERATION_EXPIRED`, so the window can tell a timeout from a cancellation. `$/cancelRequest` for a queued request
   voids it without a response. When the connection closes, every request queued from it is voided, and the daemon
   answers the requester `OPERATION_EXPIRED` (§3.1).
5. `extensions.uninstall` is carried out by the approval window, not the service worker: Chrome refuses
   `chrome.management.uninstall` without a user gesture (verified on a real browser). Approving marks the request as
   executing, and the window, still handling the click, checks the extension is installed (`NOT_FOUND` otherwise,
   nothing done) and calls the uninstall, which opens Chrome's own confirmation dialog. Confirming there answers the
   result; cancelling answers `USER_REJECTED`. The window reports the outcome to the service worker, which answers as
   in step 3; the request stays executing across a service worker restart. While Chrome's dialog is open the deadline
   keeps running: a `$/cancelRequest`, the deadline, or a closed connection voids the request for the requester
   (`OPERATION_EXPIRED`) without withdrawing the dialog, whose outcome the window still shows. Closing the approval
   window while the dialog is open rejects the other queued requests as usual but leaves the uninstall executing: the
   window can no longer report the dialog's outcome, so the service worker learns a confirmed uninstall from
   `chrome.management.onUninstalled` and answers the result; a dialog cancelled after the window closed cannot be
   observed, and the request is answered `OPERATION_EXPIRED` at its deadline.

## 6. Generation and conformance

`protocol.json` annotates ownership: each method and each entry of `notifications` has one `peer` (`scriptcat`
or `browser`), and each `sessionMethods` entry, each `errorCodes` entry and each `crypto.context` entry lists the
`peers` that use it. The `browser*` context strings are reserved for browser-instance handshakes, so a MAC computed
for one peer kind never verifies as the other; the pairing KDF strings are shared. A notification's name may not
start with `$` or reuse a method name, because both travel in the JSON-RPC `method` field. `internal` on a method is
daemon-side metadata and does not reach the TypeScript output. The generator emits one set of bindings per peer:

| Output | Contents |
|---|---|
| `internal/pkg/protocol/generated/protocol.generated.go` | every method, notification, type, error code, and context key, for the daemon and CLI |
| `internal/pkg/protocol/generated/*.ts` | ScriptCat's methods, the types they reference, and the session methods, codes and contexts listing `scriptcat` |
| `extension/src/protocol/generated/*.ts` | the same selection for `browser`, plus each method's `level` in `RPC_METHODS`, its notifications and the types they reference |

The ScriptCat TypeScript must stay byte-identical to the copy in the paired ScriptCat revision, because ScriptCat
declares every generated method as a capability and its conformance test compares the method and error-code
lists exactly. Adding browser-owned definitions therefore never changes ScriptCat's files or `schemaVersion`.

The generator rejects a method whose `level` is missing or not `L0`/`L1`/`L2`, an L2 method without a human gate
(`blocking` is `none`) or a human-gated method that is not L2, an L1 method whose parameters do not declare the
optional `confirm: {"const": true}`, a `mergeField` on a method that waits for a human, and a list result whose
`hasMore` is not a boolean. The ScriptCat TypeScript does not carry `level`, so it stays byte-identical.

Run `make protocol-generate` after editing `protocol.json`, and `make protocol-sync-scriptcat` to update the
adjacent ScriptCat checkout. `make protocol-check` regenerates all artifacts and fails if the checked-in output
differs or a generated file is untracked. Both peers parse the JSON-RPC structure directly. ScriptCat validates
business parameters with generated native TypeScript type guards, so extension startup does not compile schemas
at runtime.
