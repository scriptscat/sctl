# Verification

Green unit tests only prove **the behaviors you asserted** — not that the change actually works. This document
owns the latter: before saying "fixed" or "it works", you need real output as evidence.

## Order: cheap signals first, then drive the real thing

```bash
go build ./... && go vet ./...          # nothing else matters if it doesn't compile
go test ./... -race
golangci-lint run ./...
```

Only once this set passes is it worth spending time starting processes. Doing it the other way round is just
debugging compile errors through a slow feedback loop.

## Driving a real daemon

`SCTL_DATA_DIR` and `SCTL_BRIDGE_ADDR` (defined in
[development.md](./development.md#environment-variables)) let you start a daemon on an isolated data directory
and port without touching the one you use day to day:

```bash
go build -o sctl ./cmd/sctl
export SCTL_DATA_DIR=$(mktemp -d) SCTL_BRIDGE_ADDR=127.0.0.1:18643
./sctl serve &                 # start the daemon (writes control.token)
./sctl status                  # should report truthfully that no extension is connected
./sctl scripts list            # no extension connected → "extension not connected" error, exit code 3
```

Cover **the boundaries that motivated the change**: if you touched exit-code mapping, exercise every code; if
you touched the pairing window, walk the expiry path too. Running only the happy path is not verification.

Mind the version floor before integrating with a real extension (see
[development.md](./development.md#version-floor)): the `0.0.0-dev` produced by a plain `go build` is below
`minDaemonVersion` and the extension will reject it as too old and disconnect.

## One-shot end-to-end verification, not a new test suite

One-shot verification scripts and their evidence go under the git-ignored `e2e/scratch/`, one directory per
scenario, and are discarded after use:

```text
e2e/scratch/<scenario>/
├── run.sh                    # one-shot driver script
├── report.md                 # what was run, what was observed, the conclusion
└── *.log                     # raw output
```

- **Don't** run the entire heavy test suite just to check one thing, and **don't** casually leave a scratch
  script behind as a permanent case.
- A behavior genuinely worth guarding long-term is a separate decision: judge it once, then commit it as a
  **proper** test. It is not a by-product of verification.
- `e2e/scratch/` never enters version control. Reference the conclusions from your local `report.md` in the PR
  or the conversation; don't commit or paste raw logs.

## Reproduction is step one of a fix

The order for fixing a bug is: first write a script under `e2e/scratch/` that reproduces it reliably
(**proving the bug exists**), then narrow that into a failing committed test, and only then change code.

A scratch reproduction **cannot** replace the failing test you are going to commit — it only answers "is this
real?", while the committed test answers "will it happen again?".

If it doesn't reproduce, say so plainly and stop. Fixing a problem that was never confirmed to exist usually
just buries the real cause deeper.

## Surfaces you can't drive: watch their side effects

You cannot click through the browser-extension side, but every decision it makes leaves a trace on the daemon
side. The observable outlets are:

- `./sctl status` — whether the extension is connected, how many clients are paired, a summary of recent
  security events;
- audit events (`internal/pkg/audit`) — handshakes, pairing, revocation, and rejections are all recorded;
- stderr logs with `--log-level` turned up;
- on-disk state under `$SCTL_DATA_DIR` (keys, client store).

The full chain that needs a human clicking in the browser (pair → list → source disclosure → install approval
→ revoke → kill switch) is cross-repository integration work. It waits until the extension-side build is ready
and then follows the verification doc in the main repository; on this side, check the side effects above first.

## Before claiming completion

- Report the commands you ran and their real output, not "it should be fine";
- If a test failed, say it failed and attach the output; if you skipped a step, say which one;
- For what you genuinely finished and verified, state it plainly, without vague hedging.
