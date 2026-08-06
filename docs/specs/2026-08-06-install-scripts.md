# Install scripts for sctl

<!-- File: docs/specs/2026-08-06-install-scripts.md -->

> Status: Draft
> Owner: sctl maintainers
> Last updated: 2026-08-06

**Objective:** Let a user install the current sctl binary with one copy-paste command per platform
(`curl | sh` on macOS/Linux, `irm | iex` on Windows) instead of manually downloading, verifying and
extracting a GitHub Release archive, and unify release artifact naming on a single
`<name>-<version>-<os>-<arch>.<ext>` pattern across CI, the published v0.1.0 release, scripts and docs.

**Hard invariant:** The installed binary must be byte-identical to the released artifact, and the installer
must never complete after a checksum mismatch.

## Problem

1. **No one-line install path.** `README.md` "Quick start" and `docs/mcp.md` step 1 both tell a new user to
   visit GitHub Releases, download the archive for their platform, extract it and put `sctl` on `PATH` by
   hand. There is no script (verified: `git ls-files | grep -i install` returns nothing).
2. **Checksum verification is not in the happy path.** The release already publishes `checksums.txt`
   (`.github/workflows/release.yaml`) and signs it with build provenance, but a manual installer is left to
   remember to verify it; a script can make verification mandatory.
3. **Artifact naming is inconsistent and will only spread.** `release.yaml` builds
   `sctl_${VERSION}_${GOOS}_${{ matrix.arch }}` (underscores), and the published v0.1.0 release carries six
   `sctl_0.1.0_*` archives plus `checksums.txt` (verified via `gh release view v0.1.0`). A `curl | sh`
   installer hard-codes this pattern, so today's underscore name would be baked into every install URL and
   doc reference unless the naming is changed now, while the release set is still small (v0.1.0).
4. **The public ScriptCat docs still teach manual install.** The user-facing installation guide on
   scriptcat.org — `docs/use/external-access.md` (Chinese, default locale) and its `i18n/en/…` and
   `i18n/ru/…` mirrors — tells users to download, extract and put `sctl` on `PATH` by hand. Once the
   one-line installer exists, this guide must advertise it in all three locales or the docs contradict the
   new install path.

## Actors and user stories

1. As a new macOS/Linux user, I want to run one `curl … | sh` command, so that sctl lands on my `PATH` and
   is ready for `sctl serve` / `sctl mcp` without reading release instructions.
2. As a new Windows user, I want an equivalent `irm … | iex` command, so that I do not need to manually
   unzip the release and juggle `sctl.exe`.
3. As a user on a fixed or older version, I want to set an environment variable, so that I can install a
   specific release instead of always the latest.
4. As a maintainer, I want the script to verify the archive checksum against the published `checksums.txt`,
   so that a corrupted or tampered download never becomes the installed binary.

## Design decisions

| # | Decision | Basis and rejected option |
|---|---|---|
| 1 | Scripts live in a new `scripts/` directory and are fetched from `raw.githubusercontent.com/scriptscat/sctl/main/scripts/…` | Keeps the repo root clean. Rejected: root-level `install.sh` — user explicitly asked not to put scripts at the root; separate `install/` directory — `scripts/` is the common convention. |
| 2 | POSIX `install.sh` for macOS/Linux plus `install.ps1` for Windows, same download/verify/extract/install flow | Windows users deserve the same one-liner; both artifacts are already published. Rejected: POSIX only — user chose to include Windows. |
| 3 | Default install dir `~/.local/bin` (POSIX) / `%LOCALAPPDATA%\sctl\bin` (Windows); `SCTL_INSTALL_DIR` overrides | User-level, no sudo, matches sctl's single-user tool. Rejected: `/usr/local/bin` — requires sudo and widens the failure surface. |
| 4 | Default version = latest release via the GitHub `releases/latest` API; `SCTL_VERSION` overrides (leading `v` accepted) | Users install the current release by default and can pin. Rejected: fixed-version-only — worse first-run experience for the common case. |
| 5 | Archive sha256 must match the matching line in that release's `checksums.txt`, or the install aborts | The release already publishes and attests `checksums.txt`; making it mandatory completes the trust chain described in the release workflow. |
| 6 | Install via temp file + atomic rename; an existing binary is overwritten | Idempotent re-runs and no half-written binary. |
| 7 | The script prints a PATH hint but never edits the user's shell config or Windows user PATH | No surprise edits to the user's environment. Rejected: auto-appending to `.zshrc`/`.bashrc`/user PATH — user rejected auto-modification. |
| 8 | No sudo, no system directories, no signing/notarization in this change | Stays a small, per-user installer. macOS quarantine/signing and Windows signing are separate follow-ups. |
| 9 | Release artifacts use hyphen naming `sctl-<version>-<os>-<arch>.<ext>` everywhere: `release.yaml` `PACKAGE`, the archive's inner directory, and every URL the scripts and docs reference | One consistent pattern; the installer's download URL and `checksums.txt` lookups are trivial to construct. Rejected: keeping underscores — the user explicitly asked for `-`-separated names and to keep them from now on. |
| 10 | The published v0.1.0 release assets are replaced with hyphen-named ones: the six archives are rebuilt with the new inner directory name and `checksums.txt` is regenerated, then old assets are deleted and new ones uploaded | GitHub offers no asset rename, only delete+upload; rebuilding keeps the archive's inner directory consistent with its filename and regenerates `checksums.txt` from the new names. The user confirmed this external side effect, including that old download URLs stop working. Rejected: leaving v0.1.0 as-is — the user asked to migrate existing artifacts too. |
| 11 | scriptcat.org's `docs/use/external-access.md` install section is updated in all three locales (zh-Hans, en, ru) to lead with the one-line install commands, keeping manual download / build-from-source as fallback | The docs site is the user-facing install guide and its `check:i18n` gate requires locale parity; the user asked for the related docs to be adjusted. Rejected: sctl README only — the external guide is where real users install from. |

## Design

### POSIX installer (`scripts/install.sh`)

Precondition: run on macOS or Linux with `curl`, `tar`, `grep`, `sed` and a POSIX shell available.

Flow:

1. **Detect platform.** `uname -s` → `linux` / `darwin`; `uname -m` → `x86_64` / `aarch64` (mapped to
   `arm64`). Any other combination prints an actionable message and exits non-zero.
2. **Resolve version.** If `SCTL_VERSION` is set, strip a leading `v`. Otherwise request
   `https://api.github.com/repos/scriptscat/sctl/releases/latest`, extract `tag_name` (leading `v` stripped)
   with a minimal `grep`/`sed` parse. A non-2xx response or unparseable body exits non-zero.
3. **Download.** Into a temporary directory, fetch
   `https://github.com/scriptscat/sctl/releases/download/v<ver>/sctl-<ver>-<os>-<arch>.tar.gz` and the
   matching `checksums.txt`. `curl -fsSL` so HTTP errors fail the pipeline.
4. **Verify.** Compute the archive's sha256 and compare it to the `checksums.txt` line whose filename equals
   the downloaded archive name. On mismatch: remove the downloaded files and exit non-zero with a message
   naming the expected and actual hash.
5. **Extract.** Untar into the temporary directory; locate the `sctl` executable inside the
   `sctl-<ver>-<os>-<arch>/` directory.
6. **Install.** Create `$SCTL_INSTALL_DIR` (default `$HOME/.local/bin`) if absent; copy the binary to a
   temp file in the same directory and rename over any existing `sctl`. Preserve executable permission.
7. **Report.** Print the install path, run `sctl version` to show the installed version, and — when the
   install directory is not already in `PATH` — print the exact line to add it to the user's shell rc, with
   a note that sctl will not edit that file for them.

Failures: unsupported platform, failed download, missing `checksums.txt` line, checksum mismatch, and any
write failure all print an actionable message and exit non-zero after cleaning up the temporary directory.
Re-running after a success overwrites the binary and exits 0.

### Windows installer (`scripts/install.ps1`)

Precondition: Windows PowerShell 5.1+ or PowerShell 7; executed via
`irm https://raw.githubusercontent.com/scriptscat/sctl/main/scripts/install.ps1 | iex`.

Flow (mirrors the POSIX script):

1. **Detect platform.** `$env:PROCESSOR_ARCHITECTURE` → `x86_64` (`AMD64`) / `arm64` (`ARM64`). Unknown
   values exit non-zero.
2. **Resolve version.** `$env:SCTL_VERSION` (leading `v` stripped) or `Invoke-RestMethod` on
   `releases/latest` → `tag_name`.
3. **Download.** `Invoke-WebRequest` (with `-UseBasicParsing` when on PS 5.1) for the
   `sctl-<ver>-windows-<arch>.zip` archive and `checksums.txt` into a temp directory.
4. **Verify.** `Get-FileHash -Algorithm SHA256` on the archive; compare to the `checksums.txt` line for that
   archive filename. Mismatch → delete temp files, exit non-zero.
5. **Extract.** `Expand-Archive`; locate `sctl.exe`.
6. **Install.** Copy `sctl.exe` to `$SCTL_INSTALL_DIR` (default `$env:LOCALAPPDATA\sctl\bin`), overwriting
   any existing file.
7. **Report.** Print the install path, `& "$dir\sctl.exe" version`, and — if the install directory is not in
   the user `PATH` — the `setx` command to add it, noting the script does not modify the environment.

Failures behave like the POSIX script: non-zero exit with an actionable message and temp cleanup.

### Release naming change and the v0.1.0 migration

`release.yaml`'s build step changes `PACKAGE="sctl_${VERSION}_${GOOS}_${{ matrix.arch }}"` to
`PACKAGE="sctl-${VERSION}-${GOOS}-${{ matrix.arch }}"`, so the archive file name and its inner staging
directory both become `sctl-0.1.0-darwin-arm64`-style. `checksums.txt` is regenerated from the new names by
the existing `sha256sum *` step. From now on every release follows this pattern; no back-compat alias is
kept for the underscore names.

The already-published v0.1.0 release is migrated to the same naming: the six archives are rebuilt locally
from the tagged commit with the same flags/ldflags the workflow uses (so the binaries are byte-identical),
re-tarred with the hyphen inner directory, their sha256 recomputed into a fresh `checksums.txt`, and the
GitHub release assets are replaced (delete old, upload new). This is an external side effect the user
confirmed: existing download URLs and any saved links to `sctl_0.1.0_*` stop resolving.

### Documentation

- `README.md` "Quick start": replace the manual release download instruction with the two one-line install
  commands (POSIX + Windows), keeping a short "or download manually / build from source" fallback line.
- `docs/mcp.md` step 1: present the one-line command as the primary install path, keep the manual-extract
  instructions as the fallback, and keep the `chmod +x` note for the manual path.
- `docs/README_zh-CN.md` "快速开始": mirror the README change in Simplified Chinese.
- `docs/README.md` ownership table: no new owning doc is introduced; `scripts/` is referenced by the READMEs
  above, which already own install instructions.

### scriptcat.org documentation (separate repository)

In the scriptcat.org repository (Gitea remote `scriptscat/scriptcat.org`, Docusaurus 3 site with three
locales zh-Hans/en/ru and its own AGENTS.md + Gitea CI), the install section of
`docs/use/external-access.md` is updated in all three locales:

- Chinese: `docs/use/external-access.md` (default locale).
- English: `i18n/en/docusaurus-plugin-content-docs/current/use/external-access.md`.
- Russian: `i18n/ru/docusaurus-plugin-content-docs/current/use/external-access.md`.

Each locale's "Install sctl" section leads with the one-line commands for that platform (`curl | sh` for
macOS/Linux, `irm | iex` for Windows), then keeps the manual download/extract and build-from-source fallback
wording that the section already has. No new routes are created (the file path is unchanged), so the URL
inventory and `check:urls` baseline are unaffected. The change passes scriptcat.org's own gates
(`pnpm run typecheck && pnpm run build && pnpm run check`) per that repository's AGENTS.md before commit.

Delivery (user-confirmed): scriptcat.org is a separate repository (Gitea remote) and its change is committed
directly on its `main` branch and pushed to the Gitea remote — no separate branch or PR is opened there. The
install URLs reference `raw.githubusercontent.com/scriptscat/sctl/main/scripts/…`, so the scriptcat.org
commit lands after the sctl scripts are merged to sctl `main`; until then the advertised links are not yet
live.

## Out of scope

- macOS code-signing/notarization and Windows Authenticode signing of the scripts or binary.
- Homebrew / winget / chocolatey packages.
- An `sctl upgrade`/self-update subcommand.
- Auto-editing the user's shell rc or Windows user PATH.
- A dedicated install domain or mirror (decision B from the exploration).
- CI gates for the scripts (shellcheck or committed e2e) — deliberately not added in this change.
- Keeping underscore-named release assets or any alias/redirect for the old `sctl_<ver>_*` URLs.

## Testing decisions

| Seam | What it verifies | Prior art |
|---|---|---|
| One-shot local verification run (not committed, not in CI) | `install.sh` downloads from a locally served fake release directory (archive + `checksums.txt`), verifies checksum, extracts, installs into a temp `SCTL_INSTALL_DIR`, reports version; `SCTL_VERSION` override path; overwrite idempotency; checksum-mismatch abort leaves no binary | None in repo — first shell-script change; evidence recorded in the PR description per the user's decision |
| Source review + an optional Windows run | `install.ps1` parity with `install.sh` and PS 5.1 compatibility | None; no `pwsh` in the local environment, so this is reviewed and, if a Windows host is available, run there |
| Local rebuild of the v0.1.0 archives against the tagged commit | New hyphen-named archive + regenerated `checksums.txt` are produced; `sha256sum -c` passes; the binaries match the currently published byte hashes | Existing release workflow reproducibility (-trimpath, commit-time mtimes) |
| Post-migration release check (via `gh`) | v0.1.0 release now lists six `sctl-0.1.0-*` assets and a matching `checksums.txt`, with no underscore-named asset left | None — one-off migration; evidence recorded in the PR description |
| scriptcat.org gates (typecheck + build + check) and i18n parity | The three external-access.md locales each advertise the one-line commands and pass scriptcat.org's `pnpm run check` (incl. `check:i18n`), with no new routes | Existing scriptcat.org CI (`pnpm run check`); run locally per its AGENTS.md before committing there |

The one-shot verification is run locally by the implementer against a temporary static file server that
mimics the release layout (matching archive names and a real `checksums.txt` generated with `sha256sum`), so
the checksum path is exercised against genuine hashes without hitting the live GitHub release. Evidence
(success/failure output, `sctl version` result, temp-dir contents on abort) is captured in the PR
description. Nothing from this verification is committed.

## Open questions

<!-- Must be empty before approval. -->
