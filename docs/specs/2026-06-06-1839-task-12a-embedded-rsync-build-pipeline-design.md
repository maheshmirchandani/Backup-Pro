---
title: FlashBackup Task 12a Design - Embedded GNU rsync 3.4.1 Build Pipeline
created: 2026-06-06
last_modified: 2026-06-06
author: Mahesh Mirchandani
status: draft (post multi-hat review)
supersedes: none
---

# Task 12a: Embedded GNU rsync 3.4.1 Build Pipeline Design

## 1. Context

Phase 0 dogfood session 1 (2026-06-05) surfaced that v0.1.0-core's "embedded rsync" is a placeholder shell script at `internal/rsync/bin/rsync.placeholder` printing "PLACEHOLDER rsync; awaiting Task 12a build". On a clean install with no env override, `flashbackup backup` enumerates files, runs the placeholder for T1 transfer (exit 0, no actual transfer), T2 hash-compare tags every file `not_transferred`, and the run finishes with exit status `partial`. No data is moved. CI never caught this because every e2e test sets `FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync`; the placeholder code path is never exercised end-to-end in CI.

The v0.1.0-core tag (`b39a11c`, pushed 2026-06-05) is engine-correct but build-incomplete. Phase 0 dogfood cannot honestly close on env-override-only runs.

Task 12a delivers the missing build pipeline: a script that produces a universal2 (arm64 + x86_64) GNU rsync 3.4.1 binary from upstream source, an embed mechanism that swaps it in for release builds, and the CI plumbing that gates against shipping the placeholder by mistake. Companion Task 12b adds the regression and release tests.

This spec is the post-multi-hat-review revision. All 9 Critical and most Important findings from the 2026-06-06 CISO + Hacker + DevOps/SRE + QA + Senior Developer review have been folded in. Three concerns were spun off and tracked separately (Section 9.1).

## 2. Goals

1. Produce a self-contained universal2 GNU rsync 3.4.1 binary that runs on any macOS 13+ machine with zero install-time dependencies.
2. Make the dev / test build flow uninterrupted: `make build`, `go test`, and the existing e2e suite keep using the placeholder by default.
3. Make the release build flow explicit, reproducible-where-cheap, and gated against malicious tag pushes.
4. Gate against accidental placeholder-release at three layers: per-commit CI smoke (matrix), release-workflow CI test, and an in-repo regression test for the placeholder behavior itself.
5. Audit-clean: pinned upstream version, triple-sourced tarball SHA256 cross-check, recorded binary SHA256, build-provenance attestation, dual-published SHA256 sidecar.

## 3. Non-goals

1. Code signing or notarization of the embedded rsync binary or the parent flashbackup binary. That work is Plan 2.
2. Building rsync with optional features (openssl, zstd, lz4, xxhash). FlashBackup does not invoke any of these code paths.
3. Cross-platform builds. macOS only. Linux / Windows are not flashbackup targets.
4. Building any rsync version other than 3.4.1. Version bumps will be a future change.
5. Byte-identical reproducible builds. Go binaries embed a build epoch + commit SHA via ldflag; making them deterministic is multi-week work. Section 8.3 documents the asymmetry.
6. GPG verification of upstream rsync source. Section 4.4 documents the threat model that justifies SHA-only + triple-cross-check.

## 4. Locked decisions

### 4.1 Build config: Minimal

The embedded rsync is built with optional features disabled at configure time:

```
./configure \
    --disable-openssl \
    --disable-zstd \
    --disable-lz4 \
    --disable-xxhash
```

This produces a binary that links only against `/usr/lib/libSystem.B.dylib` (always present on macOS). Final binary size: ~500 KB to 1 MB per architecture, ~1-2 MB universal2.

**Why Minimal:** FlashBackup uses rsync as a local-to-local file copier only. Daemon mode (openssl), network compression (zstd/lz4), and delta-rolling hash optimizations (xxhash) are never invoked. Bundling them statically would add ~5 MB to the binary and 6-12 CVEs/year to the dependency graph for code we never run. The features flashbackup actually uses (--xattrs, --acls, --inplace, --partial, --append) work fine in the Minimal build because they use libSystem syscalls on macOS, not external libraries. AC-12b-3 below verifies the --xattrs and --acls claim end-to-end.

**Reversibility:** if Plan N adds remote backup or `--compress` over network, the build script extends to bundle the needed dep at that point. Asymmetric reversibility favors starting Minimal.

### 4.2 Embed swap: Build-tag selection

Two Go files behind `//go:build` tags select the embed payload. The existing `var embeddedRsync []byte` declaration in `internal/rsync/rsync.go` moves into each tagged file; mutually exclusive build constraints prevent duplicate declaration.

```go
// internal/rsync/embed_dev.go
//go:build !embed_real_rsync

package rsync

import _ "embed"

//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

```go
// internal/rsync/embed_release.go
//go:build embed_real_rsync

package rsync

import _ "embed"

//go:embed bin/rsync.universal2
var embeddedRsync []byte
```

**File state:**

| Path | Status | Size | Source |
|---|---|---|---|
| `internal/rsync/bin/rsync.placeholder` | Checked in | ~100 B | Existing shell script. No change. |
| `internal/rsync/bin/rsync.universal2` | **gitignored** | ~1-2 MB | Produced by `scripts/build-rsync.sh`. |

`.gitignore` gains TWO lines: `internal/rsync/bin/rsync.universal2` and `/build/` (the build script's work tree).

**Artifact lifecycle:** `make build` (default placeholder) and `make release` (real rsync) leave the same `internal/rsync/bin/` directory state visible. The build-tag selects at compile time; the unselected file's presence on disk is harmless. `make clean-rsync` (Section 5.3) removes `internal/rsync/bin/rsync.universal2` + `./build/` for a clean state.

### 4.3 Invocation contexts: All four

| Context | Trigger | Action | Output |
|---|---|---|---|
| Local script | `make build-rsync` | Run `scripts/build-rsync.sh` | `internal/rsync/bin/rsync.universal2` |
| Local release | `make release` | `make build-rsync` + `go build -tags embed_real_rsync` | Release flashbackup binary |
| CI smoke | Every commit on `main`, matrix across macOS 13/14/15 | Single-arch quick build + assertions | Pass/fail signal only; no artifact |
| CI release | Git tag push matching `v*.*.*` OR `workflow_dispatch` (with `environment: production` manual approval) | Full universal2 build + flashbackup release + Task 12b-B + attestation + draft upload | GitHub Release artifact (draft until MM publishes) |

Per-commit smoke matrix on macos-13/14/15 keeps the supported-version surface honest (the Risks table previously claimed cross-macOS coverage; the smoke matrix delivers it).

### 4.4 Upstream verification: Triple-source SHA256 cross-check

Source pin lives in a dedicated file `scripts/rsync.version`:

```
# Bump these two lines (and only these two) on rsync version updates.
RSYNC_VERSION=3.4.1
RSYNC_TARBALL_SHA256=<populated at implementation time; see below>
```

`scripts/build-rsync.sh` reads this file. CI's `actions/cache@v4` keys on `hashFiles('scripts/rsync.version')` — refactoring the script body does NOT invalidate the tarball cache.

**Implementer protocol at constant population time** (locked by review, not optional):

The `RSYNC_TARBALL_SHA256` value is populated only after the implementer has verified it matches the same SHA256 in **three independent sources**:

1. Homebrew's `Formula/r/rsync.rb` at the time of population (`brew tap homebrew/core; cat Formula/r/rsync.rb | grep sha256`).
2. A Linux distro package recipe (Debian source-package `rsync_3.4.1.orig.tar.gz` SHA256 from packages.debian.org, OR Arch's `rsync` PKGBUILD).
3. The rsync-announce mailing list announcement for 3.4.1 (https://lists.samba.org/archive/rsync-announce/).

A comment immediately above the constant records the three sources consulted and the date. Discrepancy across any pair = HALT; surface to MM. Single-source population is the "pin was wrong from day 1" attack we explicitly defend against.

**Primary download channel: GitHub Release mirror of the tarball.** Once `RSYNC_TARBALL_SHA256` is populated and verified, the same tarball file is uploaded to a permanent GitHub Release of flashbackup itself (`upstream-mirror/rsync-3.4.1.tar.gz`). The build script downloads from THAT URL first; falls back to samba.org if the GitHub mirror is unreachable. This makes us independent of samba.org outages at release time and gives us a SHA-pinned canonical artifact that we control.

```bash
PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror/rsync-${RSYNC_VERSION}.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"
```

Both URLs serve the same bytes (verified by SHA256 against the pinned constant). Either failing is recoverable.

### 4.5 Companion Task 12b: Tightened regression tests

**Task 12b-A: Placeholder regression guard**

File: `test/e2e/placeholder_rejection_test.go` (new). Build tag: default (no `-tags embed_real_rsync`).

Contract: when flashbackup is built with the default (placeholder) embed, a backup of any non-empty source produces:
- Exit code 1
- Exit status `partial`
- `bytes_transferred = 0`
- **The string `PLACEHOLDER rsync` appears in `rsync.log`** (added per QA C3: proves extract → exec actually happened; exit-code-alone is satisfied by extraction failures too)

Locks down the placeholder's behavior as an explicit invariant. Exercising the placeholder path end-to-end for the first time in the test suite.

**Task 12b-B: Real-rsync release guard**

File: `test/e2e/embedded_real_rsync_test.go` (new). Build tag: `embed_real_rsync`.

Pre-check: `t.Skip("requires make build-rsync first")` if `internal/rsync/bin/rsync.universal2` does not exist. Locally, devs without the binary see a clean skip; the CI release workflow has the binary and the test runs.

Assertions, after init + 12b-B-fixture backup with no env override:
- Exit 0, exit status `ok`, `bytes_transferred > 0`
- **Recursive content equality.** For every file in the fixture: compute SHA256 of source and SHA256 of dest via an independent code path (not the manifest's own hashing); assert equality. (QA C1: bytes>0 + manifest-internal verify is not the same as "rsync didn't silently corrupt.")
- **Recursive tree equality.** `diff -rq <source> <dest-namespaced>` reports no differences.
- The embedded rsync `--version` output begins with `rsync  version 3.4.1`.
- `otool -L internal/rsync/bin/rsync.universal2` reports only `/usr/lib/libSystem.B.dylib` (regression guard for the Minimal build config).

**Task 12b-B fixture (`test/fixtures/12b-b/`)** — locked composition:
- ≥10 files
- (a) at least one filename with spaces
- (b) at least one Unicode NFD path (macOS HFS+ normalization edge)
- (c) at least one file >4 KB and one >1 MB (xattrs are stored differently per size class)
- (d) at least one symlink
- (e) at least one file with `chflags uchg` set
- (f) at least 3 directory depth levels
- (g) at least one file with a custom xattr (`xattr -w user.flashbackup-test value f`)
- (h) at least one file with an ACL entry (`chmod +a "user:$(whoami) allow read" f`)

The fixture generator script writes the fixture from scratch in a temp dir at test setup; assertions then check (g) and (h) survive to the dest unchanged (`xattr -l <dest>/<file>` matches source; `ls -le <dest>/<file>` ACL matches source). This is the test that verifies the Section 4.1 claim that --xattrs and --acls work in the Minimal build (QA C2: claim previously unverified).

### 4.6 CI YAML: Permissions, manual gate, attestation, action SHA pins

All third-party actions are pinned to commit SHA, not floating major tag. Permissions are least-privilege at job level. Release workflow uploads as `draft: true` and uses `environment: production` for manual approval. `actions/attest-build-provenance@v1` produces SLSA-style provenance for the artifact.

Detailed YAML in Section 5.4.

## 5. Architecture

### 5.1 Build script structure (`scripts/build-rsync.sh`)

```
#!/bin/bash
set -euo pipefail
IFS=$'\n\t'   # safety against spaces in PROJECT_ROOT

# --- pinned constants come from sibling file ---
# shellcheck source=rsync.version
. "$(dirname "$0")/rsync.version"
MIN_MACOS="13.0"

# --- mode flag (default: full universal2; --smoke: arm64 only for CI per-commit) ---
SMOKE_MODE=0
if [[ "${1:-}" == "--smoke" ]]; then
    SMOKE_MODE=1
fi

# --- prereq check ---
for tool in clang lipo shasum curl tar make; do
    if ! command -v "${tool}" >/dev/null; then
        echo "FATAL: required tool '${tool}' not on PATH" >&2
        echo "  on macOS, install Xcode Command Line Tools: xcode-select --install" >&2
        exit 1
    fi
done

# --- paths (absolute; safe across cd) ---
PROJECT_ROOT="$(pwd)"
WORK_DIR="${PROJECT_ROOT}/build"
CACHE_DIR="${WORK_DIR}/cache"
SRC_DIR="${WORK_DIR}/src"
ARM64_BUILD_DIR="${WORK_DIR}/arm64"
AMD64_BUILD_DIR="${WORK_DIR}/amd64"
OUTPUT_PATH="${PROJECT_ROOT}/internal/rsync/bin/rsync.universal2"

# --- diagnostic trap: surface where to look on failure ---
trap 'echo "FAILED. See ${WORK_DIR}/arm64/config.log (and amd64/) for build details." >&2' ERR

# --- upstream URLs (GitHub mirror primary; samba.org fallback) ---
PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror/rsync-${RSYNC_VERSION}.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"

# --- download + verify ---
download_and_verify_tarball() {
    mkdir -p "${CACHE_DIR}"
    local tarball="${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz"

    if [[ -f "${tarball}" ]] && \
       [[ "$(shasum -a 256 "${tarball}" | cut -d' ' -f1)" == "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "tarball cached + verified"
        return
    fi

    # Try primary (GitHub mirror), fall back to samba.org.
    if ! curl -fSL --progress-bar -o "${tarball}.tmp" "${PRIMARY_URL}" 2>/dev/null; then
        echo "primary mirror unreachable; falling back to samba.org" >&2
        curl -fSL --progress-bar -o "${tarball}.tmp" "${FALLBACK_URL}"
    fi

    local got
    got="$(shasum -a 256 "${tarball}.tmp" | cut -d' ' -f1)"
    if [[ "${got}" != "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "FATAL: tarball SHA256 mismatch" >&2
        echo "  expected: ${RSYNC_TARBALL_SHA256}" >&2
        echo "  got:      ${got}" >&2
        rm -f "${tarball}.tmp"
        exit 1
    fi
    mv "${tarball}.tmp" "${tarball}"
    echo "tarball downloaded + verified"
}

extract_sources() {
    rm -rf "${SRC_DIR}"
    mkdir -p "${SRC_DIR}"
    tar -xzf "${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz" -C "${SRC_DIR}" --strip-components=1
}

# --- per-arch build with PATH hygiene ---
build_arch() {
    local arch="$1"
    local build_dir="$2"
    rm -rf "${build_dir}"
    mkdir -p "${build_dir}"

    (cd "${build_dir}" && \
     PATH="/usr/bin:/bin:/usr/sbin:/sbin" \
     CC="clang -arch ${arch} -mmacosx-version-min=${MIN_MACOS}" \
     "${SRC_DIR}/configure" \
        --disable-openssl \
        --disable-zstd \
        --disable-lz4 \
        --disable-xxhash \
        --build="${arch}-apple-darwin" \
        --host="${arch}-apple-darwin")

    (cd "${build_dir}" && PATH="/usr/bin:/bin:/usr/sbin:/sbin" make -j"$(sysctl -n hw.ncpu)")
}

lipo_universal() {
    mkdir -p "$(dirname "${OUTPUT_PATH}")"
    lipo -create -output "${OUTPUT_PATH}" \
        "${ARM64_BUILD_DIR}/rsync" \
        "${AMD64_BUILD_DIR}/rsync"
    chmod 0755 "${OUTPUT_PATH}"
}

emit_audit() {
    echo
    echo "=== build complete ==="
    if [[ ${SMOKE_MODE} -eq 1 ]]; then
        file "${ARM64_BUILD_DIR}/rsync"
        echo "SHA256: $(shasum -a 256 "${ARM64_BUILD_DIR}/rsync" | cut -d' ' -f1)"
        "${ARM64_BUILD_DIR}/rsync" --version | head -1
    else
        file "${OUTPUT_PATH}"
        echo "SHA256: $(shasum -a 256 "${OUTPUT_PATH}" | cut -d' ' -f1)"
        otool -L "${OUTPUT_PATH}"
        "${OUTPUT_PATH}" --version | head -1
    fi
}

main() {
    download_and_verify_tarball
    extract_sources
    build_arch "arm64" "${ARM64_BUILD_DIR}"
    if [[ ${SMOKE_MODE} -eq 0 ]]; then
        build_arch "x86_64" "${AMD64_BUILD_DIR}"
        lipo_universal
    fi
    emit_audit
}

main "$@"
```

**Idempotency contract:**
- Every `make build-rsync` is a clean build of rsync itself; per-arch `build/` subdirs are `rm -rf`'d at the start of `build_arch`.
- Only `build/cache/` (the tarball) survives across runs. `build/src/` and per-arch dirs do not.
- The `./build/` tree is otherwise disposable. `make clean-rsync` removes it.
- On failure, `./build/<arch>/config.log` and `./build/src/` are NOT cleaned (the next run's start does the cleanup); maintainer can inspect.

**Header block in script:** describes the script's intent + how to bump rsync version (edit `scripts/rsync.version` only) + the triple-source verification protocol. No separate `scripts/README.md` needed.

`scripts/rsync.version` lives next to `build-rsync.sh`:

```
# scripts/rsync.version - upstream rsync pin for FlashBackup.
#
# Bump ONLY these two lines on a version update. Implementer protocol:
# cross-check RSYNC_TARBALL_SHA256 against THREE independent sources
# before committing the change:
#   1. brew tap homebrew/core; cat Formula/r/rsync.rb | grep sha256
#   2. Debian source-package (packages.debian.org) OR Arch PKGBUILD
#   3. rsync-announce mailing list announcement
# Record the three sources + date in the comment above each constant change.
RSYNC_VERSION=3.4.1
RSYNC_TARBALL_SHA256=<populated at implementation time>
```

### 5.2 Go-side embed selection

Today `internal/rsync/rsync.go` has:
```go
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

After Task 12a, the `var embeddedRsync []byte` declaration is **removed from `rsync.go`** and re-declared in each of the two new tag-gated files (Section 4.2). Build tags are mutually exclusive, so exactly one declaration is in scope at compile time. Go does not reject this pattern.

The rest of `rsync.go` is untouched: `EmbeddedSHA256()`, `EnsureExtracted()`, the SHA256 verify-from-disk fast path, tmp+rename atomicity. Implementer audit at task start: confirm `EnsureExtracted` (a) creates the tmp file with `O_EXCL` under a 0700 dir, (b) re-verifies SHA256 of the file AFTER rename, (c) applies `chflags uchg` AFTER rename, not before. These properties matter because the SHA-keyed extract path makes them load-bearing for integrity. Document the audit result inline in `rsync.go` doc comment if it required any changes.

### 5.3 Makefile additions

```makefile
.PHONY: build-rsync
build-rsync:
	./scripts/build-rsync.sh

.PHONY: build-rsync-smoke
build-rsync-smoke:
	./scripts/build-rsync.sh --smoke

.PHONY: release
release: build-rsync
	go build -tags embed_real_rsync -ldflags "$(RELEASE_LDFLAGS)" -o flashbackup ./cmd/flashbackup

.PHONY: clean-rsync
clean-rsync:
	rm -rf ./build
	rm -f ./internal/rsync/bin/rsync.universal2

.PHONY: test-embed-placeholder
test-embed-placeholder:
	go test ./test/e2e/... -run TestPlaceholderRejection

.PHONY: test-embed-real-rsync
test-embed-real-rsync: build-rsync
	go test -tags embed_real_rsync ./test/e2e/... -run TestEmbeddedRealRsync
```

`RELEASE_LDFLAGS` reuses the existing ldflag injection block. Existing `make build` is unchanged.

### 5.4 CI plumbing

**Per-commit smoke** (new job in `.github/workflows/ci.yml`):

```yaml
build-rsync-smoke:
  strategy:
    fail-fast: false
    matrix:
      os: [macos-13, macos-14, macos-15]
  runs-on: ${{ matrix.os }}
  timeout-minutes: 10
  permissions:
    contents: read
  steps:
    - uses: actions/checkout@<PINNED_SHA>   # v4.1.7 commit SHA at task time
    - name: Cache rsync tarball
      uses: actions/cache@<PINNED_SHA>
      with:
        path: build/cache
        key: rsync-tarball-${{ hashFiles('scripts/rsync.version') }}
        restore-keys: rsync-tarball-
    - name: Smoke-build rsync (single arch)
      run: ./scripts/build-rsync.sh --smoke
    - name: Assert version + linkage + help
      run: |
        ./build/arm64/rsync --version | head -1 | grep -q "version 3.4.1"
        otool -L ./build/arm64/rsync | grep -E 'libSystem\.B\.dylib' >/dev/null
        # Assert nothing OTHER than libSystem (excluding the binary name itself)
        if otool -L ./build/arm64/rsync | tail -n +2 | grep -v 'libSystem\.B\.dylib' | grep -q '\.dylib'; then
          echo "FATAL: linkage to non-libSystem dylib detected" >&2
          otool -L ./build/arm64/rsync >&2
          exit 1
        fi
        ./build/arm64/rsync --help >/dev/null
```

(`<PINNED_SHA>` placeholders are populated at implementation time from the actions' current release SHAs.)

**Cache write policy:** the smoke job runs on every PR + on `main`. To prevent fork-PR cache-write attacks, `actions/cache@<PINNED_SHA>` writes are scoped to `main` only via `if: github.ref == 'refs/heads/main'` on the cache step's write side. (GitHub's cache action has built-in protection that PRs from forks can read but not write; documenting here for explicit posture.)

**New CI release workflow at `.github/workflows/release.yml`:**

```yaml
name: Release
on:
  push:
    tags: ['v*.*.*']
  workflow_dispatch:

permissions:
  contents: write          # release upload
  attestations: write      # provenance
  id-token: write          # OIDC for attestation

jobs:
  release:
    runs-on: macos-14
    timeout-minutes: 30
    environment: production   # manual approval gate; configured in repo settings
    steps:
      - uses: actions/checkout@<PINNED_SHA>
      - uses: actions/setup-go@<PINNED_SHA>
        with:
          go-version-file: 'go.mod'
      - name: Cache rsync tarball
        uses: actions/cache@<PINNED_SHA>
        with:
          path: build/cache
          key: rsync-tarball-${{ hashFiles('scripts/rsync.version') }}
      - name: Build rsync universal2
        run: make build-rsync
      - name: Build flashbackup release binary
        run: make release
      - name: Task 12b-B real-rsync e2e
        run: make test-embed-real-rsync
      - name: Compute artifact SHA256
        run: shasum -a 256 ./flashbackup > flashbackup.sha256
      - name: Generate build provenance attestation
        uses: actions/attest-build-provenance@<PINNED_SHA>
        with:
          subject-path: ./flashbackup
      - name: Upload to GitHub Release (DRAFT)
        uses: softprops/action-gh-release@<PINNED_SHA>
        with:
          draft: true        # MM publishes manually after smoke check
          files: |
            flashbackup
            flashbackup.sha256
```

The `environment: production` setting requires a one-time configuration in repo settings (Settings → Environments → production → Required reviewers). MM is the sole reviewer. This protects against tag-push spoofing because a compromised PAT or contributor pushing `v0.1.1-evil` cannot trigger the workflow without MM clicking approve.

**Dual-publish of SHA256:** the `flashbackup.sha256` file is uploaded as a release asset AND committed to the repo at the release commit (handled in Plan 2's release flow scripts, not 12a; the upload alone is the 12a delivery). Pre-Plan-2 mitigation: include the SHA256 in the GitHub Release notes body itself so it's discoverable on the release page even if the sidecar file is replaced.

**Plan 2 adds:** codesign + notarize steps between "Build flashbackup release binary" and "Compute artifact SHA256."

## 6. Test strategy

| Layer | Test | Location | Build mode | Purpose |
|---|---|---|---|---|
| Unit | `internal/rsync` package | `internal/rsync/*_test.go` | Default (placeholder) | Existing: extract, SHA256 verify, chmod, chflags. Task 12a audit confirms tmp+rename ordering. |
| Script (negative) | Tarball SHA mismatch | `scripts/build-rsync.test.sh` (new) | Mutate cached tarball, assert script exits 1 with mismatch message |
| Script (negative) | Missing prereq | Same | Run with PATH stripped of clang, assert prereq error fires |
| E2E | Placeholder rejection (12b-A) | `test/e2e/placeholder_rejection_test.go` | Default | Lock placeholder behavior + extract→exec proof |
| E2E | Real-rsync release (12b-B) | `test/e2e/embedded_real_rsync_test.go` | `-tags embed_real_rsync` | Content equality, xattr/ACL preservation, linkage regression |
| CI smoke | Per-commit script | GitHub Actions matrix [macos-13/14/15] | --smoke mode | Catch script regressions across supported macOS versions |
| Release | Full release workflow | GitHub Actions `release.yml` | `-tags embed_real_rsync` | Audited draft release with provenance |

12b-A and 12b-B follow existing e2e patterns (no coverage minimum; pass/fail). Script negative tests are smoke-grade — invoke the script in controlled-failure modes and grep stderr for expected error markers.

## 7. Acceptance criteria

**Task 12a:**

1. **AC-12a-1**: `scripts/build-rsync.sh` produces `internal/rsync/bin/rsync.universal2` from a clean checkout in <5 minutes on M1 Max.
2. **AC-12a-2**: `file internal/rsync/bin/rsync.universal2` reports `Mach-O universal binary with 2 architectures: [x86_64] [arm64]`.
3. **AC-12a-3**: `otool -L internal/rsync/bin/rsync.universal2` reports only `/usr/lib/libSystem.B.dylib`.
4. **AC-12a-4**: `./internal/rsync/bin/rsync.universal2 --version` reports `rsync  version 3.4.1` on both arm64 and x86_64 Macs.
5. **AC-12a-5**: `make release` produces a flashbackup binary that, when run against the 12b-B fixture with no env override, completes a backup with exit 0, source-dest content equality, and xattr/ACL preservation.
6. **AC-12a-6**: `make build` (no tag) still produces a flashbackup binary that embeds the placeholder.
7. **AC-12a-7**: `make clean-rsync` removes `internal/rsync/bin/rsync.universal2` + `./build/`; subsequent `make build-rsync` succeeds from a clean state.
8. **AC-12a-8**: `RSYNC_TARBALL_SHA256` in `scripts/rsync.version` matches the value from all three of (Homebrew formula, Linux distro package, rsync-announce mail), as recorded in a comment above the constant.

**Task 12b:**

9. **AC-12b-1**: `test/e2e/placeholder_rejection_test.go` passes under default build, asserts exit 1 + exit status `partial` + 0 bytes transferred + `PLACEHOLDER rsync` marker in `rsync.log`.
10. **AC-12b-2**: `test/e2e/embedded_real_rsync_test.go` skips cleanly when `bin/rsync.universal2` is absent; when present, asserts exit 0 + bytes>0 + recursive content equality + tree equality + xattr/ACL preservation + linkage regression.
11. **AC-12b-3 (xattrs/ACLs end-to-end)**: 12b-B fixture includes files with `user.flashbackup-test` xattr and a per-user ACL entry; assertions confirm both survive on dest.
12. **AC-12b-4 (script negative tests)**: tarball-mismatch and missing-prereq script tests exit 1 with expected error markers; pass in CI.

**CI plumbing:**

13. **AC-CI-1**: `build-rsync-smoke` runs on every commit to `main` AND every PR, across macOS 13/14/15 matrix, completes in <90 seconds per matrix cell, asserts: version starts with `rsync  version 3.4.1`, linkage limited to libSystem.B.dylib, `--help` exits 0.
14. **AC-CI-2**: `release.yml` runs on tag push with `environment: production` manual approval; executes 12b-B; generates build-provenance attestation; uploads as DRAFT to GitHub Releases. Fails the workflow if 12b-B fails or attestation generation fails.
15. **AC-CI-3**: All third-party GitHub Actions in both workflows pinned to commit SHA (not floating tag); all jobs declare least-privilege `permissions:` block.

## 8. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| rsync 3.4.1 build quirk on macOS 13/14/15/16 | Low | Medium | CI smoke matrix catches per-commit across all supported versions; rsync 3.4.x is stable upstream |
| samba.org tarball SHA256 changes (re-release without version bump) | Very low | High | Triple-source SHA verification at populate time + GitHub mirror as primary download decouples us from samba.org availability |
| Apple deprecates `lipo` or universal2 binaries | Very low | High | macOS 13-16 all support universal2; revisit if Apple changes course |
| Embedded rsync extracted on USB triggers Gatekeeper quarantine | Medium | Low | tmp+rename via filesystem APIs; macOS does not quarantine files written from a vouched parent; Plan 2 notarization fully resolves |
| `--disable-xxhash` removes a feature flashbackup uses | Very low | High | AC-12b-3 verifies --xattrs and --acls survive end-to-end; pre-implementation grep of `internal/rsync/wrapper.go` confirms no rsync flag touches xxhash |
| GitHub Actions cache poisoning via fork PR | Low | Medium | Cache writes scoped to `main` only; reads from cache require SHA-pinned tarball to match the pinned constant; mismatched cache entry causes script failure (loud, not silent) |
| Tag-push spoofing by compromised PAT | Low | Critical | `environment: production` manual approval gate on release.yml; MM is sole reviewer; spoofed tag triggers workflow but binary never publishes without MM's click |
| Third-party action pointing-tag re-pointed maliciously | Low | High | All actions pinned to commit SHA |
| Build host shell-access injection | Low | Medium | PATH reset to /usr/bin:/bin in build_arch; configure runs from a freshly cleaned dir; tmp+rename audit covers extraction |
| User downloads phished "FlashBackup v0.x" from a malicious source | Medium | High | Build provenance attestation + dual-publish SHA256 (release sidecar + repo commit + release notes body) — defense in depth without GPG ceremony at this stage; revisit if Phase 1 dogfood shows friends Google-searching for FlashBackup |

### 8.1 Local vs CI determinism

`make release` run locally on MM's Mac will NOT produce a byte-identical binary to the CI release workflow. The Go ldflag injection bakes in build epoch + commit SHA + builder host; these differ. **CI binary is authoritative.** Local `make release` is for MM's iterative testing only. AC-12a-5 specifies behavior, not byte-equality.

Reproducible-build deep-dive (SOURCE_DATE_EPOCH, trimpath, byte-identical attestation across builders) is queued for Plan 2+.

### 8.2 USB-spread threat model

The "20 friends, not nation-state" framing assumes MM personally distributes flashbackup binaries to each friend. The escalation case is a friend Googling for "FlashBackup" and landing on a phishing page hosting a malicious binary. Defenses at this stage:
- Build provenance attestation gives the canonical artifact a verifiable origin (GitHub-signed Sigstore certificate via actions/attest-build-provenance).
- SHA256 dual-publish: release sidecar + repo commit + release notes body. A friend (or MM) can compare any two to detect substitution.
- README explicitly tells friends "the only canonical download is github.com/maheshmirchandani/Backup-Pro/releases. SHA256 in release notes; verify via `shasum -a 256 ./flashbackup` before running."

If Phase 1 dogfood reveals friends not following the README protocol or losing the GitHub-URL provenance, escalate to GPG signing of release artifacts in Plan 2. Don't add it pre-emptively; the operational cost of GPG keychain management is real for a single-maintainer project.

### 8.3 Reproducibility asymmetry

See 8.1. Documented, not chased in 12a.

## 9. Out of scope (deferred)

Items intentionally not in this spec:

- Code signing and notarization. Plan 2.
- Reproducible-build attestation (SLSA Level 3+). Plan 2+.
- GPG signature verification of upstream rsync. Plan 2 reconsideration; pre-emptive add is unjustified for current threat model.
- Building rsync versions other than 3.4.1. Future single-file version bump.
- Adding back disabled features. Triggered only by a flashbackup feature requirement.
- Static-build of openssl/zstd/lz4/xxhash. Same trigger.

### 9.1 Spun off from multi-hat review (track separately)

Three concerns from the review do not belong in 12a but must not be lost:

1. **CVE response posture for embedded rsync.** Project-wide, not 12a-scoped. Queue as a separate doc task post-12a: monitoring channel (rsync-announce mailing list), CVSS threshold for re-cut (≥7.0 in code paths flashbackup invokes: `-a -c --xattrs --acls --inplace --partial --append`), 7-day re-cut SLO, GitHub Release notes "security" tag convention, README "check Releases monthly" user signal. Target landing: between Task 12a completion and Phase 0 gate close.
2. **Reproducible builds.** Section 8.1. Documented as asymmetry; deep-dive deferred to Plan 2+.
3. **GPG / USB-spread escalation.** Section 8.2. Documented threat model; escalation trigger is Phase 1 dogfood signal.

## 10. Spec self-review log

Initial draft review (2026-06-06 1839):
- [x] Placeholder scan: only `RSYNC_TARBALL_SHA256` deferred fill (explicit, with triple-source population protocol).
- [x] Internal consistency: `embed_real_rsync` tag name, file paths, AC numbering consistent.
- [x] Scope check: 12a + 12b, no spec-development-discipline trigger.
- [x] Bash correctness: fixed prereq loop, absolute paths, IFS, PATH hygiene.
- [x] `--smoke` mode body added.

Multi-hat review (2026-06-06 1900-1930):
- [x] CISO findings folded: triple-source SHA verification (4.4), GitHub mirror as primary (4.4), build provenance attestation (4.6, 5.4), CVE posture spun off (9.1), USB threat model documented (8.2), reproducibility asymmetry documented (8.1).
- [x] Hacker findings folded: action SHA pinning (4.6, 5.4), `environment: production` manual approval (5.4), cache write scoping (5.4 + Risks table), build_arch PATH reset (5.1), tmp+rename audit (5.2), placeholder marker assertion (4.5), triple-source SHA (4.4). Hacker Minor 7 (network sandbox for placeholder test) skipped as over-engineering for current scope.
- [x] DevOps/SRE findings folded: `permissions:` blocks (4.6, 5.4), `draft: true` + manual gate (5.4), cache key on `scripts/rsync.version` only (4.4), GitHub mirror primary (4.4), idempotency contract (5.1), `make clean-rsync` (5.3), `.gitignore /build/` (4.2), provenance attestation (5.4), timeout-minutes (5.4), local-vs-CI determinism (8.1), `setup-go` go-version pin (5.4).
- [x] QA findings folded: 12b-B content equality + tree equality (4.5), xattr/ACL test (4.5, AC-12b-3), placeholder marker (4.5, AC-12b-1), script negative tests (6, AC-12b-4), macOS matrix on smoke (5.4, AC-CI-1), 12b-B fixture lock (4.5), CI smoke broader assertions (5.4, AC-CI-1). QA Minor 9 (test naming AC traceability) skipped as project-wide convention change with low signal.
- [x] Senior Dev/DX findings folded: var declaration commit (5.2), clean-rsync target (5.3), idempotency contract (5.1), failure-preserves-artifacts (5.1), `.gitignore /build/` (4.2), IFS (5.1), error-message install hints (5.1), failure trap (5.1), `curl --progress-bar` (5.1).
- [x] DevOps Minor 9 (macos-14 EOL plan): kept in §8 risks, no detailed plan until GitHub announces deprecation.

## 11. References

- Phase 0 dogfood log: `docs/dogfood/2026-06-05-1920-phase-0-log.md`
- Existing rsync wrapper: `internal/rsync/wrapper.go`
- Existing rsync extraction: `internal/rsync/rsync.go`
- Existing build script stub: `scripts/build-rsync.sh`
- Project design spec (Phase rollout, supported macOS): `docs/specs/2026-06-03-1532-flashbackup-design.md`
- Upstream rsync project: https://github.com/RsyncProject/rsync
- Upstream rsync downloads: https://download.samba.org/pub/rsync/src/
- rsync-announce mailing list: https://lists.samba.org/archive/rsync-announce/
- SLSA build provenance: https://slsa.dev/spec/v1.0/provenance
