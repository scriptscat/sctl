# Development

```bash
go build ./...
go vet ./...
go test ./... -race
go build -o sctl ./cmd/sctl && ./sctl version
```

## Language conventions

- Documentation (this directory, `AGENTS.md`, `docs/README.md`) is written in English — it is read mostly by
  coding agents.
- Code comments are written in Simplified Chinese, matching the extension repository.
- The user-facing `README.md` and all CLI output stay in Simplified Chinese.

## Static analysis

CI uses [golangci-lint](https://golangci-lint.run) v2 (configuration in `.golangci.yaml`). Installing the same
version locally avoids "green locally, red in CI":

```bash
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2
golangci-lint fmt ./...   # gofmt + goimports (with this repo's prefix grouping)
golangci-lint run ./...
```

When bumping the version, update `GOLANGCI_LINT_VERSION` in `.github/workflows/test.yaml` to match.

## Environment variables

| Variable | Effect |
|---|---|
| `SCTL_DATA_DIR` | Overrides the data directory (keys / tokens / client store / logs) |
| `SCTL_BRIDGE_ADDR` | Overrides both the daemon's bind address and the frontend's connect address (custom port, multiple instances, isolated testing) |

How to drive a real daemon in isolation with these two variables, and what evidence makes a change count as
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
| Every PR (any target branch) | `lint` + `test` (`-race`) + `protocol-drift` |
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

## Protocol single source

`internal/pkg/protocol/protocol.json` mirrors the extension's copy and is compiled into the binary with
`go:embed`. CI's `protocol-drift` job fetches the authoritative copy from the extension repository and diffs
it byte for byte; both sides must be updated together.

The job reads that copy from the `main` branch of `scriptscat/scriptcat` by default. Until the extension-side
bridge lands there, point the repository variable `EXT_PROTOCOL_REF` at the branch that carries it — otherwise
the fetch fails and the job goes red on every PR.
