# Documentation Maintenance

> **Read this before adding, rewriting, or reviewing any documentation** — [`AGENTS.md`](../AGENTS.md),
> everything under `docs/`, the Markdown in `.github/`, and any package-local README that may appear later.
> Don't work from a fixed list: run `git ls-files '*.md'` to discover the current set. This guide has two
> jobs: keep the doc set **organized** (links resolve, the index is current, facts aren't duplicated), and
> keep every claim **true for the current branch**.

## One fact, one owning doc

Each fact — a port, an exit code, a version number — is expanded in exactly **one** doc; everywhere else
cross-links to it. A fact copied into two places will drift, and usually only surfaces once someone acts on
the wrong copy. The ownership table is in [README.md](./README.md), which doubles as the index.

When you move a fact, move it to the doc that **owns** it and cross-link — don't copy. When you rename or move
a file, update the index and every doc referencing it **in the same change**.

## Truth discipline

**If you can't `git grep` it on the current branch, don't write it down.**

Always verify with git-aware commands:

```bash
git grep -n "someSymbol"          # not rg
git ls-files 'internal/**/*.go'   # not ls / find
git ls-tree -r --name-only HEAD
```

`rg` / `ls` / `find` also match **untracked** local files, so work that hasn't landed yet looks as though it
already shipped — exactly the fourth failure mode above. Anything not yet landed either stays in its own
branch's docs or is explicitly marked as planned.

When code and docs change in the same PR, checking against the old `HEAD` is not enough — check against **the
tree that will exist after the change lands**. Re-check after a rebase or conflict resolution: resolving a
conflict can quietly reintroduce a stale fact or drop a doc update.

## How to verify each kind of claim

When a doc makes one of these claims, verify it accordingly. Every count must be **enumerated** from the
authoritative source, never recalled from memory or copied from earlier prose.

| Claim in the docs | Verify with |
|---|---|
| A source file / directory exists | `git ls-files --error-unmatch internal/daemon/component.go` |
| A function / type exists **under that exact name** | `git grep -n 'func WriteFileAtomic' -- internal` — renames are the #1 source of drift |
| Dependency direction between packages | `go list -f '{{.ImportPath}} {{join .Imports " "}}' ./...` |
| Protocol constants (port, timeouts, TTLs, limits, method set) | `internal/pkg/protocol/protocol.json` is the authority; docs only explain semantics |
| The version floor | `grep minDaemonVersion internal/pkg/protocol/protocol.json` |
| "N MCP tools" | Count the `methods` keys in `internal/pkg/protocol/protocol.json`, not comments in the MCP implementation |
| "N release artifacts" | The build matrix in `.github/workflows/release.yaml` |
| Toolchain versions | `GOLANGCI_LINT_VERSION` in `.github/workflows/test.yaml` must match [development.md](./development.md) |
| CLI subcommands / flags | `git grep -n 'Use:' -- internal/cli`, or just `./sctl --help` |
| Exit codes | The `exitOK` / `exitRejected` / `exitVoided` / `exitError` constants in `internal/cli/cli.go` |
| Which jobs CI runs | The `jobs:` section of `.github/workflows/test.yaml` |
| A relative link's target exists | `git ls-files --error-unmatch <target>` — only tracked files count |

When you introduce a new kind of concrete claim — another version number, another mirrored file — add a row to
the table above. A claim type missing from the table will eventually lie without anyone noticing.

## Cross-document consistency

The same rule must not appear in two docs with **different** conditions. For any rule you touch, establish
which doc owns it, when it triggers, what it requires, and whether it has a documented exception. When you
change an upstream rule, confirm that the carve-out a downstream doc grants for it still holds.

Absolute phrasing ("always", "must", "never", "all") most often hides an unwritten exception. When you see it,
first confirm the absolute really has no exception, then decide whether to keep or loosen it — some absolutes
are deliberate non-negotiables.

## When you find a discrepancy

The **code on the current branch** wins; change the doc to match it. The exception is when the code is
genuinely wrong — then fix the code and say so in the description.

Delete stale content **outright**; don't stack corrections on top of it. A line saying "note: the sentence
above no longer applies" only leaves the next reader distrusting both. Don't preserve an old value as history
with wording like "used to be" or "kept for compatibility", and don't demote it to a comment — git remembers.

## Honest completion claims

Only say "fully checked", "verified", or "all fixed" when the evidence actually covers that scope. The
accurate summary is usually narrower: which docs got a fact check and what backs it, versus which ones only
got a structural pass or were deliberately left alone. A check you couldn't complete gets stated, not quietly
skipped.
