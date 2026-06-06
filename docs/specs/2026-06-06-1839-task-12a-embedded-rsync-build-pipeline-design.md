---
title: FlashBackup Task 12a Design - Embedded GNU rsync 3.4.1 Build Pipeline
created: 2026-06-06
last_modified: 2026-06-06
author: Mahesh Mirchandani
status: draft
supersedes: none
---

# Task 12a: Embedded GNU rsync 3.4.1 Build Pipeline Design

## 1. Context

Phase 0 dogfood session 1 (2026-06-05) surfaced that v0.1.0-core's "embedded rsync" is a placeholder shell script at `internal/rsync/bin/rsync.placeholder` printing "PLACEHOLDER rsync; awaiting Task 12a build". On a clean install with no env override, `flashbackup backup` enumerates files, runs the placeholder for T1 transfer (exit 0, no actual transfer), T2 hash-compare tags every file `not_transferred`, and the run finishes with exit status `partial`. No data is moved. CI never caught this because every e2e test sets `FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync`; the placeholder code path is never exercised end-to-end in CI.

The v0.1.0-core tag (`b39a11c`, pushed 2026-06-05) is engine-correct but build-incomplete. Phase 0 dogfood cannot honestly close on env-override-only runs.

Task 12a delivers the missing build pipeline: a script that produces a universal2 (arm64 + x86_64) GNU rsync 3.4.1 binary from upstream source, an embed mechanism that swaps it in for release builds, and the CI plumbing that gates against shipping the placeholder by mistake. Companion Task 12b adds the regression and release tests.

## 2. Goals

1. Produce a self-contained universal2 GNU rsync 3.4.1 binary that runs on any macOS 13+ machine with zero install-time dependencies.
2. Make the dev / test build flow uninterrupted: `make build`, `go test`, and the existing e2e suite keep using the placeholder by default.
3. Make the release build flow explicit and reproducible: `make release` produces a flashbackup binary that backs up real data with no env override.
4. Gate against accidental placeholder-release at three layers: per-commit CI smoke, release-workflow CI test, and an in-repo regression test for the placeholder behavior itself.
5. Audit-clean: pinned upstream version, pinned tarball SHA256, recorded binary SHA256, single supply-chain hop.

## 3. Non-goals

1. Code signing or notarization of the embedded rsync binary or the parent flashbackup binary. That work is Plan 2.
2. Building rsync with optional features (openssl, zstd, lz4, xxhash). FlashBackup does not invoke any of these code paths. See Section 4 / Build config.
3. Cross-platform builds. macOS only. Linux / Windows are not flashbackup targets.
4. Building any rsync version other than 3.4.1. Version bumps will be a future change (single constant in the script).
5. Live integration testing against rsync release candidates or beta channels.

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

**Why Minimal:** FlashBackup uses rsync as a local-to-local file copier only. Daemon mode (openssl), network compression (zstd/lz4), and delta-rolling hash optimizations (xxhash) are never invoked. Bundling them statically would add ~5 MB to the binary and 6-12 CVEs/year to the dependency graph for code we never run. The features flashbackup actually uses (--xattrs, --acls, --inplace, --partial, --append) work fine in the Minimal build because they use libSystem syscalls on macOS, not external libraries.

**Reversibility:** if Plan N adds remote backup or `--compress` over network, the build script extends to bundle the needed dep at that point. Asymmetric reversibility favors starting Minimal.

### 4.2 Embed swap: Build-tag selection

Two Go files behind `//go:build` tags select the embed payload:

```go
// internal/rsync/embed_dev.go
//go:build !embed_real_rsync

package rsync
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

```go
// internal/rsync/embed_release.go
//go:build embed_real_rsync

package rsync
//go:embed bin/rsync.universal2
var embeddedRsync []byte
```

The current `internal/rsync/rsync.go` keeps the `embeddedRsync` variable but moves the embed directive to these tag-gated files.

**File state:**

| Path | Status | Size | Source |
|---|---|---|---|
| `internal/rsync/bin/rsync.placeholder` | Checked in | ~100 B | Existing shell script. No change. |
| `internal/rsync/bin/rsync.universal2` | **gitignored** (new pattern) | ~1-2 MB | Produced by `scripts/build-rsync.sh`. |

`.gitignore` gains `internal/rsync/bin/rsync.universal2`. Required: `bin/rsync.universal2` must NOT be checked in under any circumstance.

**Why build-tag:** the Go-side `EmbeddedSHA256()` is content-agnostic (computes from whatever the embed bytes are at compile time); the extract dir is keyed by that hash. Swapping between placeholder and real binary requires only the tag selection. Single-file distribution preserved.

### 4.3 Invocation contexts: All four

| Context | Trigger | Action | Output |
|---|---|---|---|
| Local script | `make build-rsync` | Run `scripts/build-rsync.sh` | `internal/rsync/bin/rsync.universal2` |
| Local release | `make release` | `make build-rsync` + `go build -tags embed_real_rsync` | Release flashbackup binary |
| CI smoke | Every commit on `main` | Single-arch quick build + `./rsync --version` check | Pass/fail signal only; no artifact |
| CI release | Git tag push matching `v*.*.*` OR `workflow_dispatch` | Full universal2 build + flashbackup release + Task 12b-B + artifact upload | GitHub Release artifact |

**Why all four:** each closes a distinct gap (local iteration, local release, per-commit regression, audited release). Combined cost is modest (~60 lines Makefile + ~50 lines CI YAML).

### 4.4 Upstream verification: Tarball + SHA256

Source: `https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz` (canonical upstream channel; same source Homebrew and every Linux distro use).

Verification: pinned SHA256 constant at the top of `scripts/build-rsync.sh`. After download, `sha256sum` the tarball and compare; exit 1 with clear error if mismatch.

```bash
RSYNC_VERSION="3.4.1"
RSYNC_TARBALL_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"
RSYNC_TARBALL_SHA256="<populated at implementation time from upstream announcement>"
```

The tarball is cached at `./build/cache/rsync-3.4.1.tar.gz`; re-runs of the script skip the download if the cached copy hashes correctly.

**Threat model defended:** post-pin samba.org tarball substitution (caught by SHA mismatch). Pre-pin samba.org compromise (mitigated by Wayne Davison or another community member catching it; flashbackup's threat surface is ~20 friends, not nation-state). GPG verification (option c) is not added; the ceremony / reward ratio for a low-target project is poor and CI keychain management adds operational complexity.

### 4.5 Companion Task 12b: Two regression tests

**Task 12b-A: Placeholder regression guard**

File: `test/e2e/placeholder_rejection_test.go` (new). Build tag: default (no `-tags embed_real_rsync`).

Contract: when flashbackup is built with the default (placeholder) embed, a backup of any non-empty source produces exit code 1 with exit status `partial` and zero bytes transferred. Locks down the placeholder's behavior as an explicit invariant; if someone "improves" the placeholder to exit nonzero or emit fake transfer events, this test fails.

This is the first e2e test in the suite that exercises the placeholder path end-to-end (every existing e2e overrides via `FLASHBACKUP_RSYNC_PATH_FOR_TEST`).

**Task 12b-B: Real-rsync release guard**

File: `test/e2e/embedded_real_rsync_test.go` (new). Build tag: `embed_real_rsync`. Asserts:

- `internal/rsync/bin/rsync.universal2` exists. If not, `t.Skip("requires make build-rsync first")`. Locally, devs without the binary see a clean skip; the CI release workflow has the binary and the test runs.
- After init + tiny-fixture backup with no env override: exit 0, exit status `ok`, bytes_transferred > 0, every file in manifest reports `verified`.
- The `--version` output from the embedded rsync starts with `rsync  version 3.4.1`.

**Why both:** different failure modes. 12b-A catches "placeholder behavior regressed." 12b-B catches "release pipeline shipped placeholder by accident." Either gap can land a broken release; both deserve a test.

## 5. Architecture

### 5.1 Build script structure (`scripts/build-rsync.sh`)

```
#!/bin/bash
set -euo pipefail

# --- pinned constants ---
RSYNC_VERSION="3.4.1"
RSYNC_TARBALL_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"
RSYNC_TARBALL_SHA256="<populated at implementation time>"
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

# --- download + verify ---
download_and_verify_tarball() {
    mkdir -p "${CACHE_DIR}"
    local tarball="${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz"
    if [[ -f "${tarball}" ]] && \
       [[ "$(shasum -a 256 "${tarball}" | cut -d' ' -f1)" == "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "tarball cached + verified"
        return
    fi
    curl -fsSL -o "${tarball}" "${RSYNC_TARBALL_URL}"
    local got
    got="$(shasum -a 256 "${tarball}" | cut -d' ' -f1)"
    if [[ "${got}" != "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "FATAL: tarball SHA256 mismatch" >&2
        echo "  expected: ${RSYNC_TARBALL_SHA256}" >&2
        echo "  got:      ${got}" >&2
        exit 1
    fi
    echo "tarball downloaded + verified"
}

# --- extract sources ---
extract_sources() {
    rm -rf "${SRC_DIR}"
    mkdir -p "${SRC_DIR}"
    tar -xzf "${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz" -C "${SRC_DIR}" --strip-components=1
}

# --- per-arch build ---
build_arch() {
    local arch="$1"          # arm64 or x86_64
    local build_dir="$2"     # absolute path
    rm -rf "${build_dir}"
    mkdir -p "${build_dir}"

    (cd "${build_dir}" && \
     CC="clang -arch ${arch} -mmacosx-version-min=${MIN_MACOS}" \
     "${SRC_DIR}/configure" \
        --disable-openssl \
        --disable-zstd \
        --disable-lz4 \
        --disable-xxhash \
        --build="${arch}-apple-darwin" \
        --host="${arch}-apple-darwin")

    (cd "${build_dir}" && make -j"$(sysctl -n hw.ncpu)")
}

# --- universal2 lipo (full mode only) ---
lipo_universal() {
    mkdir -p "$(dirname "${OUTPUT_PATH}")"
    lipo -create -output "${OUTPUT_PATH}" \
        "${ARM64_BUILD_DIR}/rsync" \
        "${AMD64_BUILD_DIR}/rsync"
    chmod 0755 "${OUTPUT_PATH}"
}

# --- audit ---
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

Approximate length: ~80 lines. Comments + diagnostic messages bring it to ~120-150.

### 5.2 Go-side embed selection

Today `internal/rsync/rsync.go` has:
```go
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

After Task 12a, this directive moves into two new files behind tags. The rest of `rsync.go` is untouched (`EmbeddedSHA256()`, `EnsureExtracted()`, the SHA256 verify-from-disk fast path).

`internal/rsync/embed_dev.go` (new, ~10 lines):
```go
//go:build !embed_real_rsync

package rsync

import _ "embed"

//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

`internal/rsync/embed_release.go` (new, ~10 lines):
```go
//go:build embed_real_rsync

package rsync

import _ "embed"

//go:embed bin/rsync.universal2
var embeddedRsync []byte
```

The `var embeddedRsync []byte` declaration in `rsync.go` is removed (or split out into a third file with no tag if Go complains about cross-tag visibility, which it shouldn't here).

### 5.3 Makefile additions

```makefile
.PHONY: build-rsync
build-rsync:
	./scripts/build-rsync.sh

.PHONY: release
release: build-rsync
	go build -tags embed_real_rsync -ldflags "$(RELEASE_LDFLAGS)" -o flashbackup ./cmd/flashbackup

.PHONY: test-embed-placeholder
test-embed-placeholder:
	go test ./test/e2e/... -run TestPlaceholderRejection

.PHONY: test-embed-real-rsync
test-embed-real-rsync: build-rsync
	go test -tags embed_real_rsync ./test/e2e/... -run TestEmbeddedRealRsync
```

`RELEASE_LDFLAGS` reuses the existing ldflag injection block (commit SHA, build epoch, Version, RsyncVersion). Existing `make build` is unchanged.

### 5.4 CI plumbing

Two changes to `.github/workflows/ci.yml`:

**Per-commit smoke** (new job, ~15 lines):
```yaml
build-rsync-smoke:
  runs-on: macos-14
  steps:
    - uses: actions/checkout@v4
    - name: Cache rsync tarball
      uses: actions/cache@v4
      with:
        path: build/cache
        key: rsync-tarball-${{ hashFiles('scripts/build-rsync.sh') }}
    - name: Smoke-build rsync (single arch)
      run: |
        ./scripts/build-rsync.sh --smoke
        ./build/arm64/rsync --version | head -1
        ./build/arm64/rsync --version | grep -q "version 3.4.1"
```

(The `--smoke` flag is a script feature: skip x86_64 + lipo, just build arm64 quickly.)

**New CI release workflow** at `.github/workflows/release.yml`:
```yaml
name: Release
on:
  push:
    tags: ['v*.*.*']
  workflow_dispatch:

jobs:
  release:
    runs-on: macos-14
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
      - name: Cache rsync tarball
        uses: actions/cache@v4
        with:
          path: build/cache
          key: rsync-tarball-${{ hashFiles('scripts/build-rsync.sh') }}
      - name: Build rsync universal2
        run: make build-rsync
      - name: Build flashbackup release binary
        run: make release
      - name: Task 12b-B real-rsync e2e
        run: make test-embed-real-rsync
      - name: Compute artifact SHA256
        run: shasum -a 256 ./flashbackup > flashbackup.sha256
      - name: Upload to GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          files: |
            flashbackup
            flashbackup.sha256
```

(Plan 2 adds codesign + notarize steps between "Build flashbackup release binary" and "Upload.")

## 6. Test strategy

| Layer | Test | Location | Build mode | Purpose |
|---|---|---|---|---|
| Unit | `internal/rsync` package | `internal/rsync/*_test.go` | Default (placeholder) | Existing: extract, SHA256 verify, chmod, chflags |
| E2E | Placeholder rejection (12b-A) | `test/e2e/placeholder_rejection_test.go` | Default | Lock placeholder behavior as contract |
| E2E | Real-rsync release (12b-B) | `test/e2e/embedded_real_rsync_test.go` | `-tags embed_real_rsync` | Gate release against shipping placeholder |
| CI | Per-commit script smoke | GitHub Actions `build-rsync-smoke` job | Run script with `--smoke` | Catch script regressions early |
| Release | Full release workflow | GitHub Actions `release.yml` | `-tags embed_real_rsync` | Audited release artifact |

Coverage gates: 12b-A and 12b-B follow the existing e2e patterns (no coverage minimum at the e2e layer; pass/fail only).

## 7. Acceptance criteria

Task 12a is complete when:

1. **AC-12a-1**: `scripts/build-rsync.sh` produces `internal/rsync/bin/rsync.universal2` from a clean checkout in <5 minutes on M1 Max.
2. **AC-12a-2**: `file internal/rsync/bin/rsync.universal2` reports `Mach-O universal binary with 2 architectures: [x86_64] [arm64]`.
3. **AC-12a-3**: `otool -L internal/rsync/bin/rsync.universal2` reports only `/usr/lib/libSystem.B.dylib`.
4. **AC-12a-4**: `./internal/rsync/bin/rsync.universal2 --version` reports `rsync  version 3.4.1` on both arm64 and x86_64 Macs.
5. **AC-12a-5**: `make release` produces a flashbackup binary that, when run against a tiny fixture with no env override, completes a backup with exit 0 and bytes_transferred > 0.
6. **AC-12a-6**: `make build` (no tag) still produces a flashbackup binary that embeds the placeholder.

Task 12b is complete when:

7. **AC-12b-1**: `test/e2e/placeholder_rejection_test.go` passes under default build, asserts the placeholder produces exit 1 + exit status `partial` + 0 bytes transferred.
8. **AC-12b-2**: `test/e2e/embedded_real_rsync_test.go` skips cleanly when `bin/rsync.universal2` is absent; when present, passes under `-tags embed_real_rsync` with exit 0 + bytes > 0 + all files verified.

CI plumbing is complete when:

9. **AC-CI-1**: The `build-rsync-smoke` job runs on every commit to `main`, completes in <90 seconds, and fails loudly if the script does not produce a working single-arch rsync.
10. **AC-CI-2**: The `release.yml` workflow runs on tag push, executes 12b-B, and uploads the flashbackup binary + SHA256 file to GitHub Releases. Fails the workflow if 12b-B fails.

## 8. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| rsync 3.4.1 has a build quirk on macOS 13/14/15/16 that the configure flags don't handle | Low | Medium | CI smoke catches per-commit; CI matrix already tests multiple macOS versions; rsync 3.4.x is stable upstream |
| samba.org tarball SHA256 changes (re-release without version bump) | Very low | High (build fails) | Pinned SHA256 catches it; MM updates constant after manual upstream confirmation |
| Apple deprecates `lipo` or universal2 binaries | Very low | High (release blocked) | macOS 13-16+ all support universal2; if Apple changes course, we revisit |
| Embedded rsync extracted on a USB triggers Gatekeeper quarantine | Medium | Low | EnsureExtracted writes via tmp+rename; macOS does not quarantine files written via filesystem APIs from a vouched parent process; Plan 2 notarization fully resolves |
| The `--disable-xxhash` flag actually removes a feature flashbackup uses | Very low | High | Audit pre-implementation by grepping `internal/rsync/wrapper.go` for any rsync flag; current flashbackup uses `-a -c --xattrs --acls --from0 --info=progress2 --partial`; none touch xxhash |

## 9. Out of scope (queued for later)

- **Code signing of flashbackup or embedded rsync.** Plan 2.
- **Notarization.** Plan 2.
- **Reproducible-build attestation.** Plan 2+ (SLSA-style).
- **GPG signature verification of upstream rsync.** Reconsider in Plan 2 if threat model changes.
- **Building rsync versions other than 3.4.1.** Future single-constant-bump change.
- **Adding back any of the disabled features.** Triggered only when a real flashbackup feature requires it.

## 10. Spec self-review log

Inline review after first draft. Items addressed:

- [x] **Placeholder scan.** Only deferred fill is `RSYNC_TARBALL_SHA256`, marked explicitly; implementer populates from upstream announcement at task start. No other TBDs.
- [x] **Internal consistency.** `embed_real_rsync` tag name used consistently across Makefile, Go files, CI YAML, and ACs.
- [x] **Scope check.** Single task family (12a engine + 12b tests); does not trigger spec-development-discipline thresholds (not multi-week, not multi-user, not high-stakes domain).
- [x] **Ambiguity check.** File paths, tag name, configure flags, AC numbering all explicit. macOS minimum (13.0) called out in script body.
- [x] **Bash correctness.** Initial draft had `command -v` over multiple args (only checks first) and `${PWD}/../src/configure` (PWD inside subshell is post-cd, ambiguous). Rewrote prereq check as a loop over individual tools; switched to absolute paths via `PROJECT_ROOT="$(pwd)"` captured once at script start.
- [x] **Smoke mode.** Added `--smoke` flag to the script body in Section 5.1 (was only mentioned in Section 5.4 CI YAML). Smoke mode builds arm64 only and skips lipo; audit path is the per-arch binary not the lipo'd output.

## 11. References

- Phase 0 dogfood log: `docs/dogfood/2026-06-05-1920-phase-0-log.md`
- Existing rsync wrapper: `internal/rsync/wrapper.go`
- Existing rsync extraction: `internal/rsync/rsync.go`
- Existing build script stub: `scripts/build-rsync.sh`
- Project design spec (Phase rollout, supported macOS): `docs/specs/2026-06-03-1532-flashbackup-design.md`
- Upstream rsync project: https://github.com/RsyncProject/rsync
- Upstream rsync downloads: https://download.samba.org/pub/rsync/src/
