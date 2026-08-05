# Development

```bash
go build ./...
go vet ./...
go test ./... -race
go build -o sctl ./cmd/sctl && ./sctl version
```

## Language conventions

- Documentation (this directory, `AGENTS.md`, `docs/README.md`) is written in English — it is read mostly by
  coding agents. `docs/README_zh-CN.md` is the user-facing Chinese translation.
- Code comments are written in Simplified Chinese, matching the extension repository.
- The user-facing `README.md` and CLI output are written in English.

## Testing

Tests protect observable contracts, not coverage percentages or the current implementation shape. Before writing
a test, identify:

1. **Contract** — what a caller, CLI user, peer process, or operator can observe;
2. **Trigger** — the input, event, state, or sequence that exercises it;
3. **Outcome** — the returned value, exit code, response, persisted state, audit event, or external call required
   by the contract; and
4. **Regression** — a plausible broken implementation that the test would reject.

If no relevant regression can be named, the proposed test is probably a tautology or an assertion about an
implementation detail. For a reported bug, follow the reproduction-first workflow in
[verification.md](./verification.md#reproduction-is-step-one-of-a-fix), then commit the smallest test that fails
for the confirmed cause before changing production code.

### Choose cases by behavior, not sample count

Start with a representative normal case, then add cases only where the expected outcome or production branch is
different. Consider each boundary that the changed contract actually has:

| Category | Cases to consider |
|---|---|
| Values and collections | empty / one / many; first / last; exactly at a size, count, or time limit; immediately below and above it |
| Invalid or failed input | malformed envelopes or requests, missing required fields, unavailable extension, rejection, permission denial, timeout, cancellation, and partial I/O |
| State and lifecycle | before / after connection or pairing, repeated calls, idempotency, cleanup, expiry, revocation, and stale work completing after newer work |
| Ordering and concurrency | overlapping requests, out-of-order responses, duplicate delivery, disconnect during an operation, and exactly-once effects where promised |
| Compatibility and security | protocol limits, untrusted paths or URLs, authorization scope, credential permissions, and accepted legacy forms where the contract retains them |

This is a selection guide, not a requirement to enumerate every row for every change. If `< limit`, `== limit`,
and `> limit` produce distinct results, cover all three. If ten ordinary strings take the same path, one
representative is normally enough. Empty input deserves a separate case only when emptiness changes behavior.
For a bug fix, preserve a normal-case assertion when the repair could accidentally reject previously supported
input.

Use the narrowest realistic boundary that exposes the contract:

- pure unit tests for parsing, mapping, validation, selection, and state transitions;
- package tests for persistence, bridge lifecycle, rate limiting, audit behavior, retries, and ordering across an
  interface;
- handler or client tests for `/control/*` request validation, authentication, status mapping, and cancellation;
- CLI tests for user-visible output, stderr/stdout separation, and exit codes; and
- real-process verification when the behavior depends on process wiring or the extension. Do not replace that
  boundary with a heavily mocked unit test merely because it is easier to run.

### Assert the contract

- Prefer exact assertions on domain values, HTTP responses, exit codes, files, audit events, and visible output.
  Use truthiness or substring checks only when omitted details are intentionally outside the contract.
- Assert a collaborator call when the call itself is the contract — for example, a write must not occur before
  extension approval, or an audit event must be emitted exactly once. Otherwise assert the resulting behavior,
  not every internal call.
- Test names state the trigger and observable outcome, not the helper used to implement it. One test may contain
  several related assertions for one behavior; do not split each field into a separate setup-heavy test or merge
  unrelated contracts into one scenario.
- Mock external or expensive boundaries and keep the production path under test real. Assert how sctl validates,
  transforms, persists, forwards, or reacts to a mock result — never merely that a mock returned its configured
  value.
- Keep fixtures small enough that the outcome-changing input is obvious. Prefer existing test harnesses and
  helpers over rebuilding daemon, bridge, or control-plane setup in each package.

### Do not write low-value tests

A test earns its maintenance cost when it exercises sctl logic and fails for a meaningful regression. Do not add,
and remove when encountered within the current task's scope:

- tautologies that restate a constant, fixture, or type definition;
- tests whose only assertion is that a configured mock returns its configured value;
- pure pass-through tests with no branch, transformation, validation, side effect, or stable user-visible
  contract between input and output;
- assertions about private helper calls, temporary data structures, or source layout that may change without any
  observable behavior changing;
- repeated examples from the same equivalence class that reject no regression beyond an existing case; or
- static counts and broad snapshots whose changes cannot distinguish a regression from an intentional edit.

Thin tests can still be valuable: protocol drift checks, exact exit-code or permission-bit assertions, error type
identity, serialization compatibility, security blocklists, and the only coverage of a real branch all protect
stable contracts. Judge each test against its production path rather than by line count.

Before deleting or consolidating a test, read the source it covers, search nearby package and integration tests
for the same contract, and name the regression signal that would be lost. A failing or slow test is not
automatically low value: fix production regressions; update a genuinely changed contract; reproduce and remove
the cause of flakes; and move misclassified integration work to the appropriate boundary. Do not weaken a valid
test merely to make the suite pass, and do not turn a focused change into a repository-wide cleanup.

## Static analysis

CI uses [golangci-lint](https://golangci-lint.run) v2 (configuration in `.golangci.yaml`). Installing the same
version locally avoids "green locally, red in CI":

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint fmt ./...   # gofmt + goimports (with this repo's prefix grouping)
golangci-lint run ./...
```

When bumping the version, update `GOLANGCI_LINT_VERSION` in `.github/workflows/test.yaml` to match.

Use the global `--data-dir <path>` flag to override the data directory for keys, tokens, client state, and logs.
Use the global `--listen-address <host:port>` flag to override the loopback listener and control-client target.
Pass the same values to `serve` and every client command that talks to it. How to drive a real daemon in
isolation with these flags, and what evidence makes a change count as
"verified", is owned by [verification.md](./verification.md).

## Version floor

The extension checks `daemonVersion >= versions.minDaemonVersion` (currently `0.1.0`). A plain `go build`
produces `0.0.0-dev`, which is **below the floor and will be rejected as "too old" and disconnected by the
extension**. For smoke tests and integration against the real extension, build with the version injected (or
use a release binary):

```bash
go build -ldflags "-X github.com/scriptscat/sctl/internal/cli.Version=0.1.0" -o sctl ./cmd/sctl
```

That version is delivered to the extension in `hello.daemonVersion`.

## Branches and CI

| Trigger | What runs |
|---|---|
| Every PR (any target branch) | `lint` + native Linux/macOS/Windows `test` (`build`, `vet`, `-race`) + protocol generation and exact paired-ScriptCat drift |
| push to `main` / `release/**` | Same as above |
| push tag `v*` | Reuses the full test gate first; only builds and publishes once it passes |

## Releases

The version comes from the tag; the whole release flow is driven by `.github/workflows/release.yaml`:

```bash
git tag -a v0.1.0 -m "v0.1.0"     # use v0.1.0-rc.1 for prereleases; the workflow marks them automatically
git push origin v0.1.0
```

It produces 6 artifacts (darwin/linux/windows × amd64/arm64) plus `checksums.txt`, and generates build
provenance for the checksum file. The release is **created as a draft**; publish it manually on GitHub after
reviewing the changelog.

Artifacts are built with `-trimpath` and use the commit time as the archive member timestamp, so rebuilding
the same tag is byte-for-byte reproducible. Version, commit, and build time are injected into `internal/cli`
via `-ldflags` and are visible through `sctl version`.

Cross-compilation, packaging, checksums, and the GitHub Release upload all happen inside that workflow.

## Protocol source and generation

`internal/pkg/protocol/protocol.json` is the only maintained source for the extension-facing RPC contract. Run
`make protocol-generate` after changing it. The Go and TypeScript bindings, native TypeScript validators, and
ScriptCat copies are generated artifacts and must be updated in the same cross-repository change.
`make protocol-sync-scriptcat` updates the adjacent checkout; override `SCRIPTCAT_DIR` when it lives elsewhere.

`make protocol-check` verifies that every checked-in artifact is reproducible from the source. The generator also
validates the protocol definition before emitting code, so invalid method bindings and unsupported schema shapes
fail generation.
