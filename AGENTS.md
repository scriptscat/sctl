# Repository Guidelines

This file is the entry point for AI coding agents and contributors working on sctl.

> **It is the single source of truth relative to `CLAUDE.md`** — `CLAUDE.md` only contains `@AGENTS.md` and
> re-imports this file; don't split guidance between the two. Everything beyond engineering principles and the
> architecture quick-map is owned by the docs linked below (ownership table in
> [`docs/README.md`](./docs/README.md)) — cross-link them, don't copy their content here.

> **Before writing any code, read [`docs/development.md`](./docs/development.md)** — build and test commands,
> static analysis, environment variables, the version floor, branches, CI, and releases.

> **Before changing package structure, adding a package, or altering dependency direction, read
> [`docs/architecture.md`](./docs/architecture.md)** — process model, directory layout, per-package
> responsibilities, dependency direction.

> **Before changing the envelope, RPC methods, limits, or handshake, read
> [`docs/protocol.md`](./docs/protocol.md)** — [`internal/pkg/protocol/protocol.json`](./internal/pkg/protocol/protocol.json) is the schema authority; generated files
> are updated with `make protocol-generate`, while the doc owns temporal semantics.

> **Before touching authentication, keys, enrollment, or auditing, read
> [`docs/threat-model.md`](./docs/threat-model.md)** — security boundaries, attack surface, accepted
> trade-offs, and the inventory of credentials on disk.

> **Before claiming a change "actually works", read [`docs/verification.md`](./docs/verification.md)** — how to
> write one-shot verification scripts under `e2e/scratch/`, what counts as evidence, and which side effects to
> inspect for surfaces you cannot drive.

> **Before adding, rewriting, or reviewing any documentation, read
> [`docs/doc-maintenance.md`](./docs/doc-maintenance.md)** — ownership rules and truth discipline
> (*if you can't `git grep` it on this branch, don't write it down*).

## Project Overview

sctl is ScriptCat's local control tool: a bridge daemon, an MCP server, and script management commands,
shipped as a single cross-platform binary. Go, built on the [cago](https://github.com/cago-frame/cago)
framework and [cobra](https://github.com/spf13/cobra).

```text
sctl mcp / CLI verbs  ──/control/* HTTP──▶  sctl serve (daemon)  ──WS──▶  ScriptCat extension (approval authority)
internal/client/             internal/daemon/                      internal/pkg/ (shared by both sides)
```

The authority always lives on the extension side: the daemon approves no write on its own — it forwards the
request and blocks until a human decides in the browser. Full process model and package responsibilities are
in [`docs/architecture.md`](./docs/architecture.md).

## Engineering Principles

These are non-negotiable, regardless of what the owning docs say about mechanics. All of them are held by
review today; the one exception is called out in the item itself.

- **Reproduce before you fix.** Reproduce a reported bug yourself and capture the failing evidence, then pin it
  down with a failing test, and only then change code. The order is not negotiable: a fix that starts from an
  assumption often repairs a problem that never existed while burying the real cause deeper. If it doesn't
  reproduce, say so plainly and stop — "it's obviously wrong" is not an exception, and neither is a one-line
  change. How to reproduce and what counts as evidence are in
  [`docs/verification.md`](./docs/verification.md).

- **Tests first.** Write the failing test before the implementation. Test names state **behavior** ("list
  returns exit code 3 when the extension is not connected"), not implementation detail ("calls dispatch"). When
  a test fails, fix the code, not the test. Delete meaningless tests outright — tautologies, tests that only
  assert a mock, pure pass-throughs — but confirm against the source, one by one, that each really protects
  nothing before deleting it.

- **Fix root causes, not symptoms.** No `//nolint` to paper over a diagnostic, no swallowed errors, no empty
  branch added just to silence something. A `//nolint` must name the specific rule and give a reason — that
  much is mechanically enforced by `nolintlint` in `.golangci.yaml` — but whether the reason is a genuine
  exception rather than "make it pass" is still something only review can judge.

- **Validate at boundaries, don't second-guess internally.** Everywhere untrusted data enters — WS envelopes,
  `/control/*` requests, payloads from the extension, command-line arguments — must be validated. Between
  trusted internal layers, add no `if x == nil` fallbacks, no swallowed errors, no "just in case" runtime
  shims. An assumption you cannot hold should panic or return an error rather than be masked by a branch that
  never fires — that branch only makes the bug that *does* fire harder to find.

- **Layer by process role; dependencies point one way.** `client/` (request side) and `daemon/` (guard side) do
  not depend on each other and communicate across processes only through `/control/*`. `internal/pkg/` is the
  shared layer and may only be depended upon from above. `bridge` knows nothing about the HTTP control plane.
  The single exception is the guard side referencing `client/control` one-way. Once a shortcut breaks the
  direction, the two process roles are welded together at compile time and separating them again means a
  rewrite. The current dependency graph and the reason for the exception are in
  [`docs/architecture.md`](./docs/architecture.md).

- **Sensitive files hit disk in exactly one place.** Long-term keys, the control token, and the client store
  all go through `internal/pkg/fsutil.WriteFileAtomic`. A non-atomic write leaves truncated content behind on a
  crash, and a bare `os.WriteFile` also loses the 0600 permission bits. The inventory of credentials on disk is
  in [`docs/threat-model.md`](./docs/threat-model.md).

- **stdout belongs to the CLI alone.** stdout carries `sctl mcp`'s JSON-RPC channel; a single byte written
  there by the daemon side or a library corrupts MCP frames. Diagnostics always go through
  `internal/pkg/logging`, which writes to stderr plus the log files under the data directory — never stdout.

- **Extend through the existing extension points.** A new RPC method lands in `internal/pkg/protocol/protocol.json` first. A new verb
  reuses the `dispatch` / `dispatchBlocking` skeleton in `internal/cli/dispatch.go` instead of re-implementing
  connection, cancellation, and exit-code mapping. A new `/control/*` handler is registered in
  `controlapi.Handler.Register`, on the mux that `internal/daemon/component.go` assembles. Inject dependencies
  through constructors and depend on narrow
  interfaces rather than concrete types — `controlapi.Bridge` is the template for that pattern. Never branch on
  a type string in shared code.

- **Reuse before you rebuild.** `git grep` for an existing helper before writing a new one. Extract a shared
  implementation the second time the same logic appears — one concept, one implementation, so a single fix
  lands everywhere. But don't pre-abstract for hypothetical needs: three repeated lines beat a premature
  generic helper.

- **Stay in scope.** A bug fix touches only the files that bug requires. Correcting a stale comment or an
  incorrect doc line right under your cursor is in scope; drive-by refactors and rename sweeps are not.

- **Comments explain "why"; no dead code.** A comment states a constraint or reason the code cannot express —
  why 0600, why the symlink must be resolved first here. Comments that restate the steps get deleted. Likewise:
  no dead code, no commented-out blocks, no `// removed` markers — git remembers.

- **Documentation doesn't claim what isn't there.** Before asserting that a file, function, or flag exists,
  verify it on the current branch with `git grep` / `git ls-files`. The discipline and the per-claim
  verification table are in [`docs/doc-maintenance.md`](./docs/doc-maintenance.md).

## Before You Commit

```bash
go build ./... && go vet ./... && go test ./... -race && golangci-lint run ./...
```

Never claim "fixed" or "passing" without real output as evidence — see
[`docs/verification.md`](./docs/verification.md).
