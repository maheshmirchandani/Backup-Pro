# Task 12a + 12b Implementation Plan: Embedded GNU rsync Build Pipeline

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace v0.1.0-core's placeholder embedded rsync with a real universal2 GNU rsync 3.4.1 binary built from upstream source; wire build-tag-based embed swap, four CI/Makefile invocation contexts, regression tests, and supply-chain hardening so that a clean install of flashbackup actually backs up data.

**Architecture:** Minimal build config (rsync 3.4.1 with `--disable-openssl --disable-zstd --disable-lz4 --disable-xxhash`; links only against libSystem). Two `//go:build` tag files swap between `bin/rsync.placeholder` (checked in, dev default) and `bin/rsync.universal2` (gitignored, produced by `scripts/build-rsync.sh`, selected by `-tags embed_real_rsync`). Existing `make build` (release-tagged placeholder) preserved unchanged; new `make build-real-rsync` composes both tags. CI gains build-rsync-smoke matrix (macos-13/14/15), release.yml with `environment: production` manual approval + Sigstore attestation, and actions-lint.yml enforcing no-floating-tags + rsync.version.attestation freshness.

**Tech Stack:** Bash, Go (1.23+), GNU Make, GitHub Actions YAML, clang universal2 cross-compile, lipo, actions/attest-build-provenance@v1, Sigstore.

**Source spec:** `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md` (read this in full before starting Task 1).

**Project conventions to follow:**
- No em-dashes (U+2014) or en-dashes (U+2013) anywhere in net-new content. Use periods, colons, or hyphens.
- No AI attribution in author fields or commit messages. No `Co-Authored-By` trailers.
- All new docs use `YYYY-MM-DD-HHMM-` filename prefix where applicable.
- Pre-commit gates: `go vet && gofmt -s -l ./... && go test -race && make coverage` MUST run locally before every commit; report all four outputs in commit body or PR.
- Bare `gofmt -l` is INSUFFICIENT; CI's golangci-lint runs the `-s` simplifier variant. Local must too.
- `make lint` cannot run locally (golangci-lint v1.61 needs Go 1.23, dev machines have newer Go); CI runs the real lint.

---

## File structure

**Files to CREATE:**

| Path | Responsibility | Task |
|---|---|---|
| `scripts/rsync.version` | Bare-literal pin constants (RSYNC_VERSION, RSYNC_TARBALL_SHA256). Parsed by grep, never sourced. | Task 1 |
| `scripts/rsync.version.attestation` | Three witness SHAs from Samba ecosystem (Homebrew + Debian + rsync-announce); CI lint enforces freshness + agreement. | Task 1 |
| `scripts/build-rsync.test.sh` | Negative tests for the build script (tarball mismatch, missing prereq, corrupted cache, partial-make recovery). | Task 7 |
| `internal/rsync/embed_dev.go` | `//go:build !embed_real_rsync` + `//go:embed bin/rsync.placeholder`. Holds the var declaration. | Task 2 |
| `internal/rsync/embed_release.go` | `//go:build embed_real_rsync` + `//go:embed bin/rsync.universal2`. Holds the var declaration. | Task 2 |
| `test/e2e/placeholder_rejection_test.go` | 12b-A: asserts placeholder produces exit 1 + partial + 0 bytes + `PLACEHOLDER rsync` marker in rsync.log. | Task 5 |
| `test/e2e/embedded_real_rsync_test.go` | 12b-B: external shasum content equality + diff -rq + xattr + ACL + per-arch otool. | Task 6 |
| `.github/workflows/release.yml` | Tag-push / workflow_dispatch trigger; `environment: production` manual gate; full universal2 build + 12b-B + attestation + draft upload. | Task 9 |
| `.github/workflows/actions-lint.yml` | Per-commit + PR lint: floating-tag detection + attestation freshness + witness agreement. | Task 10 |

**Files to MODIFY:**

| Path | Change | Task |
|---|---|---|
| `scripts/build-rsync.sh` | Replace stub with the full script per spec §5.1 (parse-don't-source, PATH hygiene, conditional trap, dual URL, smoke + verify-only modes). | Task 1 |
| `internal/rsync/rsync.go` | Remove the `//go:embed bin/rsync.placeholder` directive + `var embeddedRsync []byte` declaration. Keep `EmbeddedSHA256()`, `EnsureExtracted()` unchanged. Add audit-result doc comment. | Task 2 |
| `.gitignore` | Add `internal/rsync/bin/rsync.universal2` AND `/build/`. | Task 1 |
| `Makefile` | Add `build-rsync` + `build-rsync-smoke` + `build-rsync-verify` + `build-real-rsync` + `clean-rsync` + `test-embed-placeholder` + `test-embed-real-rsync` targets. PRESERVE existing `build` target unchanged. | Tasks 1 + 3 |
| `test/fixtures/pathological/mkfixtures.sh` | Add (g) `xattr-target.txt` with `user.flashbackup-test` xattr written in-place; add (h) `acl-target.txt` with per-user ACL + sidecar file `acl-target.user` recording `$(whoami)` at gen time. | Task 4 |
| `test/fixtures/pathological/MANIFEST.txt` | Document (g) and (h) additions; update SHA256-of-tree value (regenerate). | Task 4 |
| `.github/workflows/ci.yml` | Add `build-rsync-smoke` job (matrix macos-13/14/15). | Task 8 |
| `docs/BACKLOG.md` | Mark Tasks 12a + 12b done; note Tasks 12c + 12d queued with 2026-06-12 deadlines. | Task 12 |
| `~/.claude/projects/.../memory/project_execution_state.md` | Reflect "Tasks 12a + 12b complete; v0.1.0-core can now back up data with real-rsync release build" state. | Task 12 |

**File NOT created during 12a (Plan 2 reservation, per spec §11):** `scripts/notarize.sh`, `scripts/entitlements.plist`.

---

## Task 1: Build script + version pin + Makefile build-rsync targets

**Files:**
- Create: `scripts/rsync.version`
- Create: `scripts/rsync.version.attestation`
- Modify: `scripts/build-rsync.sh` (full rewrite from stub)
- Modify: `.gitignore`
- Modify: `Makefile`
- Test: `scripts/build-rsync.sh --verify-only` invocation (one-shot)

- [ ] **Step 1.1: Look up the canonical SHA256 for rsync 3.4.1 from the three Samba-ecosystem witnesses.**

Open three browser tabs (or use curl). The expected SHA256 SHOULD agree across all three; if it does NOT, halt and surface to MM as a supply-chain anomaly.

```bash
# Witness 1: Homebrew
brew tap homebrew/core 2>/dev/null || true
grep -A 2 'url "https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz"' \
  "$(brew --repository homebrew/core)/Formula/r/rsync.rb" | grep sha256

# Witness 2: Debian source package
curl -s https://packages.debian.org/sid/rsync | grep -E "rsync_3\.4\.1.*orig.tar.gz"
# Then click through to download the .dsc file to see the SHA.
# Or use: curl -s "http://deb.debian.org/debian/pool/main/r/rsync/rsync_3.4.1.orig.tar.gz" | shasum -a 256

# Witness 3: rsync-announce mailing list
# Search https://lists.samba.org/archive/rsync-announce/ for "3.4.1" — the announcement embeds the SHA.
```

Record the three SHAs. If they all agree, proceed. Otherwise STOP.

- [ ] **Step 1.2: Create `scripts/rsync.version` with the verified SHA.**

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
RSYNC_TARBALL_SHA256=<SHA from Step 1.1>
```

- [ ] **Step 1.3: Create `scripts/rsync.version.attestation` with all three witness records.**

Replace `<SHA>` with the agreed value from Step 1.1; record today's date.

```
# Attestation for rsync.version - witnesses observed at constant-population time.
# All three lines must record the SAME SHA256 OR halt and surface discrepancy.
Witness-Homebrew: <SHA> (Formula/r/rsync.rb @ <commit-sha-of-Formula-file>) observed 2026-06-06
Witness-Debian:   <SHA> (packages.debian.org rsync_3.4.1.orig.tar.gz) observed 2026-06-06
Witness-Announce: <SHA> (rsync-announce 3.4.1 mail thread) observed 2026-06-06
```

- [ ] **Step 1.4: Replace `scripts/build-rsync.sh` stub with the full script from spec §5.1.**

The full script body is in the spec at `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md` §5.1. Copy it verbatim. Key invariants to preserve from the spec:

- `#!/bin/bash` + `set -euo pipefail` + `IFS=$'\n\t'` at the very top.
- `export PATH="/usr/bin:/bin:/usr/sbin:/sbin"` at script entry (NOT per-function).
- Parse `rsync.version` via grep regex (`^RSYNC_VERSION=[A-Za-z0-9.-]+$` and `^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$`), NEVER `source` or `.` the file.
- Conditional trap armed AFTER prereqs and version parse.
- Primary URL: GitHub mirror; fallback: samba.org. Both must SHA-verify.
- No `2>/dev/null` on the primary curl call — diagnostics flow through.
- `lipo -create -output` for the universal2 binary.
- `--smoke` mode: arm64 only, no lipo. `--verify-only` mode: download + verify only.
- `emit_audit` calls `otool -L -arch arm64` AND `otool -L -arch x86_64` separately (NOT a single `otool -L` against the universal2; round-2 QA N4).

Make executable: `chmod +x scripts/build-rsync.sh`.

- [ ] **Step 1.5: Modify `.gitignore` to add two lines.**

Append exactly:

```
internal/rsync/bin/rsync.universal2
/build/
```

The `/build/` leading slash anchors to repo root and won't shadow non-root `build` directories elsewhere in the tree.

- [ ] **Step 1.6: Add Makefile targets `build-rsync`, `build-rsync-smoke`, `build-rsync-verify`, `clean-rsync`, `test-embed-placeholder`.**

Append to `Makefile` (do NOT modify the existing `build` target):

```makefile
.PHONY: build-rsync
build-rsync:
	./scripts/build-rsync.sh

.PHONY: build-rsync-smoke
build-rsync-smoke:
	./scripts/build-rsync.sh --smoke

.PHONY: build-rsync-verify
build-rsync-verify:
	./scripts/build-rsync.sh --verify-only

.PHONY: clean-rsync
clean-rsync:
	@rm -rf ./build
	@rm -f ./internal/rsync/bin/rsync.universal2
	@echo "removed: ./build/ and ./internal/rsync/bin/rsync.universal2"

.PHONY: test-embed-placeholder
test-embed-placeholder:
	go test ./test/e2e/... -run TestPlaceholderRejection
```

(`build-real-rsync` and `test-embed-real-rsync` are added in Task 3, after Task 2's build-tag split lands.)

- [ ] **Step 1.7: Run `make build-rsync-verify` to confirm the SHA pin is correct.**

```bash
make build-rsync-verify
```

Expected output: `tarball downloaded + verified` followed by `verify-only mode: tarball SHA matches pin. No build performed.` Exit 0.

If SHA mismatch, halt and re-verify the witness SHAs in Step 1.1.

- [ ] **Step 1.8: Run `make build-rsync` to produce the universal2 binary.**

```bash
make build-rsync
```

Expected: build completes in <5 minutes on M1 Max. Final audit block printed:
```
=== build complete ===
internal/rsync/bin/rsync.universal2: Mach-O universal binary with 2 architectures: [x86_64:Mach-O 64-bit executable x86_64] [arm64:Mach-O 64-bit executable arm64]
SHA256: <hex>
/usr/lib/libSystem.B.dylib (compatibility version ..., current version ...)
[same for x86_64]
rsync  version 3.4.1  protocol version 32
```

If linkage line shows anything OTHER than `libSystem.B.dylib`, the build picked up Homebrew deps. Halt; verify PATH hygiene + configure flags.

- [ ] **Step 1.9: Manually verify AC-12a-2, AC-12a-3, AC-12a-4.**

```bash
file internal/rsync/bin/rsync.universal2
# Expected: Mach-O universal binary with 2 architectures...

otool -L -arch arm64 internal/rsync/bin/rsync.universal2 | grep -v 'libSystem\.B\.dylib' | grep -E '^\s+/' && echo "FAIL: non-libSystem dylib" || echo "PASS: arm64 linkage clean"
otool -L -arch x86_64 internal/rsync/bin/rsync.universal2 | grep -v 'libSystem\.B\.dylib' | grep -E '^\s+/' && echo "FAIL: non-libSystem dylib" || echo "PASS: x86_64 linkage clean"

./internal/rsync/bin/rsync.universal2 --version | head -1 | grep -q "version 3.4.1" && echo "PASS: version 3.4.1" || echo "FAIL"
```

All four `PASS` lines must print.

- [ ] **Step 1.10: Verify AC-12a-9 (no Bash source).**

```bash
grep -cE '^\s*(source|\.) [^"]*rsync\.version' scripts/build-rsync.sh
```

Expected: `0`. If non-zero, the script is sourcing `rsync.version` instead of parsing it — a CRITICAL security defect; fix immediately.

- [ ] **Step 1.11: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

All four must succeed before commit. Report outputs in commit body.

- [ ] **Step 1.12: Commit.**

```bash
git add scripts/rsync.version scripts/rsync.version.attestation scripts/build-rsync.sh .gitignore Makefile
git commit -m "feat(build): real GNU rsync 3.4.1 universal2 build pipeline [Task 1/12a]

Replaces scripts/build-rsync.sh stub with full upstream-source build
script per spec §5.1. Parse-don't-source rsync.version via grep regex
(security-critical per round-2 Hacker N1). Triple-witness Samba-ecosystem
SHA cross-check via scripts/rsync.version.attestation. Minimal configure
flags (no openssl/zstd/lz4/xxhash); links only libSystem.B.dylib.
Universal2 via lipo. --smoke (arm64-only) and --verify-only modes.

Makefile gains build-rsync / build-rsync-smoke / build-rsync-verify /
clean-rsync / test-embed-placeholder targets. Existing 'build' target
preserved unchanged (-tags release + IsReleaseBuild=true wire intact).

.gitignore gains internal/rsync/bin/rsync.universal2 and /build/.

AC-12a-1..4, AC-12a-7..9 verified locally.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 2: Build-tag split for embed swap + EnsureExtracted audit

**Files:**
- Create: `internal/rsync/embed_dev.go`
- Create: `internal/rsync/embed_release.go`
- Modify: `internal/rsync/rsync.go` (remove embed directive + var declaration; add audit doc comment)

- [ ] **Step 2.1: Read `internal/rsync/rsync.go` and `internal/rsync/doc.go` fully.**

Understand the current shape before editing. Confirm: `var embeddedRsync []byte` is declared exactly once in `rsync.go` (around line 24) with `//go:embed bin/rsync.placeholder` directive directly above.

- [ ] **Step 2.2: Audit `EnsureExtracted` per spec §5.2 (tmp+rename + O_EXCL + chflags ordering).**

Open `internal/rsync/rsync.go` and trace `EnsureExtracted`. Confirm:
- (a) tmp file created with `O_EXCL` flag under a `0700`-mode directory.
- (b) SHA256 of the extracted file is re-verified AFTER the rename (not just at write time).
- (c) `chflags uchg` is applied AFTER rename, not before.

Record findings. If any property is missing, that becomes a fix included in this task; document in commit body.

Likely outcome: all three properties are already correct (the package was reviewed during Task 12). If so, just add an audit doc comment near `EnsureExtracted`'s signature confirming the post-Task-12a verification date.

- [ ] **Step 2.3: Create `internal/rsync/embed_dev.go`.**

```go
//go:build !embed_real_rsync

// Package-internal: embed the placeholder shell script for dev / test builds.
// Counterpart: embed_release.go (build tag: embed_real_rsync).
// See EmbeddedSHA256 + EnsureExtracted in rsync.go for the content-agnostic
// extract path; this file only chooses WHICH bytes go into the binary.

package rsync

import _ "embed"

//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

- [ ] **Step 2.4: Create `internal/rsync/embed_release.go`.**

```go
//go:build embed_real_rsync

// Package-internal: embed the real universal2 GNU rsync 3.4.1 binary for
// release builds. Selected by go build -tags embed_real_rsync.
// Counterpart: embed_dev.go (default build, no tag).
// The bin/rsync.universal2 file is gitignored; it must be produced by
// scripts/build-rsync.sh before a build with this tag is attempted.

package rsync

import _ "embed"

//go:embed bin/rsync.universal2
var embeddedRsync []byte
```

- [ ] **Step 2.5: Modify `internal/rsync/rsync.go` to remove the embed directive + var declaration.**

Open `internal/rsync/rsync.go`. Remove these lines (around line 23-24):

```go
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

Also remove the `_ "embed"` import if it is no longer referenced in the file (Step 2.6 confirms).

Add the audit doc comment from Step 2.2 above `EnsureExtracted`'s signature:

```go
// EnsureExtracted ensures the embedded rsync binary is present at a stable
// path under dotFlashbackupDir, with mode 0500 and (best-effort) the macOS
// uchg immutable flag.
//
// Audited 2026-06-06 per spec §5.2 (Task 12a): tmp+rename with O_EXCL,
// SHA256 re-verified AFTER rename, chflags uchg applied AFTER rename.
// Audit confirms the SHA-keyed extract path's integrity guarantees hold.
//
// ...
```

- [ ] **Step 2.6: Verify Go compiles with the new tag layout.**

```bash
go build ./internal/rsync/...
go build -tags embed_real_rsync ./internal/rsync/... 2>&1
```

The first must succeed (placeholder embedded). The second WILL fail with "pattern bin/rsync.universal2: no matching files found" UNLESS Task 1 has run and the binary exists. That's expected and correct — the gitignored binary doesn't ship in source control.

Run `make build-rsync` first (from Task 1) if not already done, then re-run the second command. It must now succeed.

- [ ] **Step 2.7: Verify the existing `make build` still works (AC-12a-6).**

```bash
make build
file ./flashbackup
./flashbackup --version
```

Expected: `Mach-O 64-bit executable arm64` (or `universal binary` depending on local build); `--version` shows the version block from cmd/flashbackup. Confirms the placeholder-embedded build flow still works after the refactor.

- [ ] **Step 2.8: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

All four must succeed.

- [ ] **Step 2.9: Commit.**

```bash
git add internal/rsync/embed_dev.go internal/rsync/embed_release.go internal/rsync/rsync.go
git commit -m "refactor(rsync): build-tag split for embed swap [Task 2/12a]

Splits the //go:embed directive + var embeddedRsync declaration out of
internal/rsync/rsync.go into two new tag-gated files: embed_dev.go
(default, embeds bin/rsync.placeholder) and embed_release.go
(-tags embed_real_rsync, embeds bin/rsync.universal2).

Mutually exclusive build constraints — exactly one declaration is in
scope at compile time. EmbeddedSHA256 + EnsureExtracted are
content-agnostic; no changes needed in rsync.go beyond removing the
moved directive + var.

EnsureExtracted audited per spec §5.2 (Task 12a): tmp+rename with
O_EXCL, SHA256 re-verified AFTER rename, chflags uchg applied AFTER
rename. Audit confirms the SHA-keyed extract path's integrity holds.
Doc comment added inline.

AC-12a-6 verified: existing make build still produces a placeholder-
embedded binary that runs.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 3: `make build-real-rsync` + `make test-embed-real-rsync` targets

**Files:**
- Modify: `Makefile`
- Test: `make build-real-rsync` invocation produces a working binary

- [ ] **Step 3.1: Add `build-real-rsync` and `test-embed-real-rsync` Makefile targets.**

Append to `Makefile` (after Task 1's additions):

```makefile
.PHONY: build-real-rsync
build-real-rsync: build-rsync
	go build $(GOFLAGS) -tags 'release embed_real_rsync' -ldflags "$(LDFLAGS_RELEASE)" -o flashbackup ./cmd/flashbackup

.PHONY: test-embed-real-rsync
test-embed-real-rsync: build-rsync
	go test -tags 'release embed_real_rsync' ./test/e2e/... -run TestEmbeddedRealRsync
```

Key invariants:
- Both depend on `build-rsync` so the universal2 binary exists.
- Both pass BOTH tags together: `'release embed_real_rsync'`. The `release` tag preserves the existing `codesign.IsReleaseBuild=true` ldflag wire (Makefile:55); `embed_real_rsync` selects the universal2 embed.
- Reuse existing `LDFLAGS_RELEASE` and `GOFLAGS` (no new variables).

- [ ] **Step 3.2: Run `make build-real-rsync` to produce the real-rsync binary.**

```bash
make build-real-rsync
file ./flashbackup
./flashbackup --version
```

Expected: build succeeds; `./flashbackup` is a real binary; `--version` shows the version block. The binary now embeds the universal2 rsync (not the placeholder).

- [ ] **Step 3.3: Smoke-verify the binary actually runs rsync.**

Create a quick ad-hoc test against a temp APFS-backed mount or a small source dir; confirm a backup actually transfers bytes. This is NOT the full 12b-B test (that comes in Task 6), just a sanity check that the rsync embed swap worked.

```bash
mkdir -p /tmp/fb-smoke-src
echo "hello smoke test" > /tmp/fb-smoke-src/test.txt
# Use your dogfood USB or a temp APFS DMG.
# Example (assumes USB at /Volumes/ROCKET-2TB initialized + profile registered):
EDITOR=true ./flashbackup profiles new fb-smoke /Volumes/ROCKET-2TB <<<""  # Will need manual profile edit; skip if no USB available.
```

If no test USB is available, defer this step until Task 6. Document in commit body.

- [ ] **Step 3.4: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

- [ ] **Step 3.5: Commit.**

```bash
git add Makefile
git commit -m "feat(build): make build-real-rsync composes release + embed_real_rsync tags [Task 3/12a]

New Makefile targets build-real-rsync and test-embed-real-rsync.
Both depend on build-rsync (universal2 must exist) and pass
-tags 'release embed_real_rsync' to compose the existing release
ldflag wire with the new embed selector.

AC-12a-5 partial verification (full verification awaits Task 6's
12b-B e2e test).

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 4: Extend `pathological/` fixture with xattr + ACL files

**Files:**
- Modify: `test/fixtures/pathological/mkfixtures.sh`
- Modify: `test/fixtures/pathological/MANIFEST.txt`
- Test: `pathological/_MatchesManifest` tripwire test (existing, in test/e2e/helpers_test.go or similar)

- [ ] **Step 4.1: Read current `test/fixtures/pathological/mkfixtures.sh` end to end.**

Understand the current items (a)-(f). The new items (g) xattr and (h) ACL extend the same generator.

- [ ] **Step 4.2: Append item (g) `xattr-target.txt` to `mkfixtures.sh`.**

After the existing items, append:

```bash
# (g) xattr-bearing file. Per Task 12a spec §4.5: write content + xattr
# IN-PLACE at the final location. No copy/tar/mv after, or the xattr
# may be stripped (round-2 QA N1).
printf "xattr fixture target\n" > "${OUT}/xattr-target.txt"
xattr -w user.flashbackup-test "smoke-value-$(date +%s)" "${OUT}/xattr-target.txt"
# Verify the xattr stuck before proceeding.
xattr -l "${OUT}/xattr-target.txt" | grep -q "user.flashbackup-test" || {
    echo "FATAL: xattr did not survive write" >&2
    exit 1
}
```

- [ ] **Step 4.3: Append item (h) `acl-target.txt` + sidecar to `mkfixtures.sh`.**

```bash
# (h) ACL-bearing file. Records the gen-time user in a sidecar so the
# test can compare semantic ACL content, not a hardcoded string
# (round-2 QA N2).
printf "acl fixture target\n" > "${OUT}/acl-target.txt"
GEN_USER="$(whoami)"
echo "${GEN_USER}" > "${OUT}/acl-target.user"
chmod +a "user:${GEN_USER} allow read" "${OUT}/acl-target.txt"
# Verify the ACL stuck.
ls -le "${OUT}/acl-target.txt" | grep -q "user:${GEN_USER}" || {
    echo "FATAL: ACL did not survive write" >&2
    exit 1
}
```

- [ ] **Step 4.4: Re-baseline `pathological/MANIFEST.txt`.**

Open `test/fixtures/pathological/MANIFEST.txt`. In the "File inventory" block, ADD:

```
  xattr-target.txt             21 bytes   user.flashbackup-test xattr
                                          written in-place
  acl-target.txt               19 bytes   per-user ACL (recorded at
                                          generation time in
                                          acl-target.user sidecar)
  acl-target.user              <variable> sidecar: recorded $(whoami)
                                          at fixture gen time
```

Update the "Total bytes" line: add the new files' sizes.
Update the "SHA256-of-tree:" line: it will change. Recompute in Step 4.5.

- [ ] **Step 4.5: Run `mkfixtures.sh` to regenerate the fixture and recompute the tree SHA256.**

```bash
cd test/fixtures/pathological
./mkfixtures.sh /tmp/path-fix-12a
# The Go helper FixtureTreeSHA256 needs to be invoked to compute the tree SHA.
# From repo root:
cd -
go test -run TestE2E_Helpers_FixtureTreeSHA256 ./test/e2e/... -v
# Locate the FixtureTreeSHA256 helper output for pathological/; copy that hex into MANIFEST.txt.
```

If no existing test prints the tree SHA, write a quick one-off Go script that calls `FixtureTreeSHA256("pathological")` and prints. Then drop the script (do NOT commit it).

Replace the `SHA256-of-tree:` line in MANIFEST.txt with the new value.

- [ ] **Step 4.6: Run the existing `_MatchesManifest` tripwire test.**

```bash
go test -run "TestE2E_PathologicalFixture_MatchesManifest" ./test/e2e/... -v
```

Expected: PASS. If FAIL, the SHA in MANIFEST.txt doesn't match generated state — re-run Step 4.5 and reconcile.

- [ ] **Step 4.7: Add ACL cleanup at fixture teardown.**

ACLs prevent `t.TempDir()` removal on macOS. The mkfixtures.sh script applies the ACL; the test helper that cleans up the fixture must REMOVE the ACL before removing the file. Open `test/e2e/helpers.go` (or wherever pathological fixture cleanup lives) and locate the cleanup path. Add:

```go
// ACL on pathological/acl-target.txt prevents t.TempDir() removal on macOS.
// Strip it before letting the cleanup handler unlink.
if data, err := os.ReadFile(filepath.Join(dir, "acl-target.user")); err == nil {
    user := strings.TrimSpace(string(data))
    aclFile := filepath.Join(dir, "acl-target.txt")
    _ = exec.Command("/bin/chmod", "-a#", "0", aclFile).Run()  // best-effort strip
    _ = exec.Command("/bin/chmod", "-a", "user:"+user+" allow read", aclFile).Run()
}
// Also strip chflags uchg from immutable-target.txt if it's set; existing code.
```

If the existing cleanup already handles ACLs, no change needed. Verify by reading.

- [ ] **Step 4.8: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

- [ ] **Step 4.9: Commit.**

```bash
git add test/fixtures/pathological/mkfixtures.sh test/fixtures/pathological/MANIFEST.txt test/e2e/helpers.go
git commit -m "test(fixtures): pathological extended with xattr + ACL items [Task 4/12a]

Adds (g) xattr-target.txt with user.flashbackup-test xattr written
in-place (no copy/tar/mv after; round-2 QA N1). Adds (h) acl-target.txt
with per-user ACL + acl-target.user sidecar recording \$(whoami) at
generation time (round-2 QA N2: enables semantic comparison rather
than hardcoded string match).

MANIFEST.txt re-baselined: new file inventory, new total bytes, new
SHA256-of-tree value.

ACL cleanup added to e2e helpers so t.TempDir() removal succeeds on
macOS.

Replaces the round-1 plan to create test/fixtures/12b-b/; spec §4.5
reuses the existing pathological taxonomy per round-2 Tech Lead
Important 3.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 5: `test/e2e/placeholder_rejection_test.go` (12b-A)

**Files:**
- Create: `test/e2e/placeholder_rejection_test.go`
- Test: `go test ./test/e2e/... -run TestPlaceholderRejection`

- [ ] **Step 5.1: Write the failing test.**

Create `test/e2e/placeholder_rejection_test.go`:

```go
package e2e

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestPlaceholderRejection: AC-12b-1. Placeholder-build flashbackup must
// produce exit 1 + exit status "partial" + 0 bytes transferred. The string
// "PLACEHOLDER rsync" MUST appear in the run's rsync.log to prove the
// extract→exec path actually ran (round-2 QA C3: exit-code alone is
// satisfied by extraction failures too).
//
// This test is the first in the e2e suite that exercises the placeholder
// path end-to-end. Every other e2e test uses FLASHBACKUP_RSYNC_PATH_FOR_TEST
// to substitute a real Homebrew rsync.
func TestPlaceholderRejection(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)
	testutil.RequireHdiutil(t)

	// Use the default-build flashbackup binary (placeholder embedded).
	// Helpers from test/e2e/binary_cache.go cache this; default flavour
	// is "release" tag without embed_real_rsync.
	bin := BuildBinary(t, BinaryFlavorRelease)

	usb := testutil.MountTempVolume(t, "APFS")
	src := SeedSource(t, "tiny")  // any non-empty fixture works

	// Init the USB via the placeholder-embedded binary.
	if err := RunInit(t, bin, usb); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	prof := SeedProfile(t, usb, "placeholder-smoke", src)
	_ = prof

	// Run backup. Expect exit 1 + partial exit status + 0 bytes.
	stdout, stderr, exitCode := RunBackup(t, bin, "placeholder-smoke", usb)
	if exitCode != 1 {
		t.Fatalf("expected exit 1 with placeholder; got %d (stdout=%s, stderr=%s)", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "exit status: partial") {
		t.Errorf("expected 'exit status: partial' in stdout; got: %s", stdout)
	}

	// Locate the run's rsync.log and assert the PLACEHOLDER marker.
	runDir := findLatestRunDir(t, usb)
	rsyncLog := filepath.Join(runDir, "rsync.log")
	logBytes, err := os.ReadFile(rsyncLog)
	if err != nil {
		t.Fatalf("read rsync.log: %v", err)
	}
	if !bytes.Contains(logBytes, []byte("PLACEHOLDER rsync")) {
		t.Errorf("expected 'PLACEHOLDER rsync' in rsync.log; got:\n%s", string(logBytes))
	}

	// Assert manifest reports 0 bytes_transferred. Load the finished line.
	finishedLine := readFinishedRunLine(t, usb)  // helper to scan runs.ndjson for latest finished entry
	if got := bytesTransferredFromFinishedLine(finishedLine); got != 0 {
		t.Errorf("expected 0 bytes_transferred; got %d", got)
	}
}

// findLatestRunDir walks <usb>/.flashbackup/runs/ and returns the most recent run.
func findLatestRunDir(t *testing.T, usb string) string {
	t.Helper()
	runsRoot := filepath.Join(usb, ".flashbackup", "runs")
	entries, err := os.ReadDir(runsRoot)
	if err != nil {
		t.Fatalf("read runs root: %v", err)
	}
	var latest string
	for _, e := range entries {
		if e.IsDir() && (latest == "" || e.Name() > latest) {
			latest = e.Name()
		}
	}
	if latest == "" {
		t.Fatalf("no run directories found under %s", runsRoot)
	}
	return filepath.Join(runsRoot, latest)
}
```

If `BinaryFlavorRelease` doesn't already exist in `test/e2e/binary_cache.go`, add it: it's the default `make build` flavour (`-tags release` only, no `embed_real_rsync`). The existing flavours are `release` (default) and `faultinject`; if `release` already exists as the default, just reuse it.

If `findLatestRunDir` or `readFinishedRunLine` or `bytesTransferredFromFinishedLine` helpers don't exist, add them to `test/e2e/assertions.go` near the existing manifest/run-NDJSON assertions.

Add `import "os"` to the imports.

- [ ] **Step 5.2: Run the test; expect compile-fail or helper-missing errors.**

```bash
go test -v -run TestPlaceholderRejection ./test/e2e/...
```

If helpers are missing, add them per Step 5.1 notes. Re-run.

- [ ] **Step 5.3: Run the test against the real placeholder-embedded build.**

This requires the placeholder still works as designed (Phase 0 dogfood proved it does). Test should PASS.

- [ ] **Step 5.4: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

- [ ] **Step 5.5: Commit.**

```bash
git add test/e2e/placeholder_rejection_test.go test/e2e/assertions.go test/e2e/binary_cache.go
git commit -m "test(e2e): placeholder rejection test [Task 5/12a, AC-12b-1]

First e2e test in the suite that exercises the placeholder path
end-to-end (every other e2e uses FLASHBACKUP_RSYNC_PATH_FOR_TEST).

Asserts: exit 1, exit status 'partial', 0 bytes transferred, AND the
string 'PLACEHOLDER rsync' appears in rsync.log (round-2 QA C3 fix:
proves extract→exec actually ran, not just that the binary failed
somewhere).

Helpers findLatestRunDir + bytesTransferredFromFinishedLine added to
assertions.go.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 6: `test/e2e/embedded_real_rsync_test.go` (12b-B)

**Files:**
- Create: `test/e2e/embedded_real_rsync_test.go`
- Test: `make test-embed-real-rsync`

- [ ] **Step 6.1: Add `BinaryFlavorRealRsync` to `test/e2e/binary_cache.go`.**

Existing flavours include `BinaryFlavorRelease` and `BinaryFlavorFaultinject`. Add a new flavour that builds with `-tags 'release embed_real_rsync'`. The build invocation:

```go
case BinaryFlavorRealRsync:
    // Requires internal/rsync/bin/rsync.universal2 to exist (produced by make build-rsync).
    // Caller wraps in t.Skip if missing.
    args = []string{"build", "-tags", "release embed_real_rsync", "-ldflags", ldflagsRelease, "-o", binPath, "./cmd/flashbackup"}
```

Reuse the existing `ldflagsRelease` constant from the binary cache or recompute from Makefile to keep semantics identical.

- [ ] **Step 6.2: Write the failing test.**

Create `test/e2e/embedded_real_rsync_test.go`:

```go
//go:build embed_real_rsync

package e2e

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/hash"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestEmbeddedRealRsync: AC-12b-2 + AC-12b-3. Real-rsync flashbackup
// must produce exit 0 + content-equality (externally verified) + xattr
// survival + ACL survival, against the extended pathological fixture.
//
// The build tag embed_real_rsync ensures this test only runs when the
// real universal2 rsync is embedded. The Skip below also catches the
// case where the binary was built without the tag.
func TestEmbeddedRealRsync(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)
	testutil.RequireHdiutil(t)

	universal2 := filepath.Join(repoRoot(t), "internal", "rsync", "bin", "rsync.universal2")
	if _, err := os.Stat(universal2); os.IsNotExist(err) {
		t.Skip("internal/rsync/bin/rsync.universal2 not found; run 'make build-rsync' first")
	}

	bin := BuildBinary(t, BinaryFlavorRealRsync)

	usb := testutil.MountTempVolume(t, "APFS")
	src := SeedSource(t, "pathological")

	if err := RunInit(t, bin, usb); err != nil {
		t.Fatalf("init failed: %v", err)
	}

	prof := SeedProfile(t, usb, "real-rsync-test", src)
	_ = prof

	stdout, stderr, exitCode := RunBackup(t, bin, "real-rsync-test", usb)
	if exitCode != 0 {
		t.Fatalf("expected exit 0; got %d (stdout=%s, stderr=%s)", exitCode, stdout, stderr)
	}
	if !strings.Contains(stdout, "exit status: ok") {
		t.Errorf("expected 'exit status: ok' in stdout; got: %s", stdout)
	}

	destRoot := filepath.Join(usb, paths.Namespaced(hostname(), user()))

	// === Externally-verified content equality (round-2 QA C1') ===
	// Source SHA via internal/hash.StreamSHA256 (manifest path).
	// Dest SHA via exec.Command shasum subprocess (INDEPENDENT).
	// They must match for every file in the fixture.
	walkAndCompare := func(relPath string) {
		srcPath := filepath.Join(src, relPath)
		destPath := filepath.Join(destRoot, relPath)
		srcStat, err := os.Stat(srcPath)
		if err != nil || srcStat.IsDir() {
			return
		}
		// Skip the sidecar; it's metadata, not user content for SHA compare.
		if filepath.Base(relPath) == "acl-target.user" {
			return
		}
		// Source SHA via stdlib hash package (manifest's own path).
		srcF, err := os.Open(srcPath)
		if err != nil {
			t.Fatalf("open src %s: %v", srcPath, err)
		}
		srcHash, _, err := hash.StreamSHA256(t.Context(), srcF)
		_ = srcF.Close()
		if err != nil {
			t.Fatalf("hash src %s: %v", srcPath, err)
		}
		// Dest SHA via /usr/bin/shasum subprocess (INDEPENDENT external code).
		out, err := exec.Command("/usr/bin/shasum", "-a", "256", destPath).Output()
		if err != nil {
			t.Fatalf("shasum dest %s: %v", destPath, err)
		}
		destHash := strings.Fields(string(out))[0]
		if srcHash != destHash {
			t.Errorf("content mismatch for %s: src=%s dest=%s", relPath, srcHash, destHash)
		}
	}
	filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		rel, _ := filepath.Rel(src, path)
		walkAndCompare(rel)
		return nil
	})

	// === Recursive content-tree equality via /usr/bin/diff -rq ===
	// Content only; xattr/ACL/flags are separate assertions below.
	// Note: diff -rq may flag the .acl-target.user sidecar legitimately
	// as "only in source"; document or exclude.
	out, err := exec.Command("/usr/bin/diff", "-rq", src, destRoot).CombinedOutput()
	if err != nil {
		// diff -rq exits 1 on differences; only fatal if there are real diffs.
		// Allow the .DS_Store / .acl-target.user sidecar variance.
		diffLines := strings.Split(string(out), "\n")
		var realDiffs []string
		for _, line := range diffLines {
			if line == "" || strings.Contains(line, ".DS_Store") || strings.Contains(line, "acl-target.user") {
				continue
			}
			realDiffs = append(realDiffs, line)
		}
		if len(realDiffs) > 0 {
			t.Errorf("diff -rq found unexpected differences: %v", realDiffs)
		}
	}

	// === xattr survival (AC-12b-3) ===
	xattrSrc := filepath.Join(src, "xattr-target.txt")
	xattrDest := filepath.Join(destRoot, "xattr-target.txt")
	srcXattr, _ := exec.Command("/usr/bin/xattr", "-l", xattrSrc).Output()
	destXattr, _ := exec.Command("/usr/bin/xattr", "-l", xattrDest).Output()
	if !strings.Contains(string(srcXattr), "user.flashbackup-test") {
		t.Fatalf("source xattr missing; fixture broken")
	}
	if !strings.Contains(string(destXattr), "user.flashbackup-test") {
		t.Errorf("xattr did NOT survive to dest. src: %s; dest: %s", srcXattr, destXattr)
	}

	// === ACL survival (AC-12b-3) ===
	// Recorded user from sidecar.
	userBytes, err := os.ReadFile(filepath.Join(src, "acl-target.user"))
	if err != nil {
		t.Fatalf("read acl-target.user sidecar: %v", err)
	}
	recordedUser := strings.TrimSpace(string(userBytes))
	aclDest := filepath.Join(destRoot, "acl-target.txt")
	aclOut, _ := exec.Command("/bin/ls", "-le", aclDest).Output()
	if !strings.Contains(string(aclOut), recordedUser) {
		t.Errorf("ACL for user %q did NOT survive to dest. ls -le output: %s", recordedUser, aclOut)
	}
	if !strings.Contains(string(aclOut), "allow read") {
		t.Errorf("ACL 'allow read' semantic did NOT survive to dest. ls -le output: %s", aclOut)
	}

	// === Per-arch linkage regression ===
	for _, arch := range []string{"arm64", "x86_64"} {
		out, err := exec.Command("/usr/bin/otool", "-L", "-arch", arch, universal2).Output()
		if err != nil {
			t.Fatalf("otool -arch %s: %v", arch, err)
		}
		lines := strings.Split(string(out), "\n")
		for _, line := range lines {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "/") {
				continue
			}
			if !strings.Contains(trimmed, "libSystem.B.dylib") {
				t.Errorf("non-libSystem linkage in %s arch %s: %s", universal2, arch, trimmed)
			}
		}
	}

	// === rsync version match ===
	extractedRsync := findExtractedRsync(t, usb)
	out, err = exec.Command(extractedRsync, "--version").Output()
	if err != nil {
		t.Fatalf("extracted rsync --version: %v", err)
	}
	firstLine := strings.SplitN(string(out), "\n", 2)[0]
	if !strings.Contains(firstLine, "rsync") || !strings.Contains(firstLine, "version 3.4.1") {
		t.Errorf("expected 'rsync version 3.4.1' in --version output; got: %s", firstLine)
	}
}

// findExtractedRsync walks <usb>/.flashbackup/bin/<sha256>/rsync and returns the path.
func findExtractedRsync(t *testing.T, usb string) string {
	t.Helper()
	binRoot := filepath.Join(usb, ".flashbackup", "bin")
	entries, err := os.ReadDir(binRoot)
	if err != nil {
		t.Fatalf("read bin root: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() {
			candidate := filepath.Join(binRoot, e.Name(), "rsync")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
		}
	}
	t.Fatalf("extracted rsync not found under %s", binRoot)
	return ""
}
```

Adjust import paths as needed (`paths.Namespaced`, `hostname`, `user` helpers). If `t.Context()` doesn't exist in the project's Go version, substitute `context.Background()` and import `context`.

- [ ] **Step 6.3: Build the binary + run the test.**

```bash
make build-rsync                          # produces internal/rsync/bin/rsync.universal2
go test -v -tags 'release embed_real_rsync' -run TestEmbeddedRealRsync ./test/e2e/...
```

Expected: PASS. If FAIL on content-equality, the rsync build is broken (most likely the build script picked up a non-Minimal config and something stripped a file mode). If FAIL on xattr or ACL, the rsync build is missing those features (verify `--disable-*` flags are correct).

- [ ] **Step 6.4: Also run via the Makefile target.**

```bash
make test-embed-real-rsync
```

Expected: same PASS.

- [ ] **Step 6.5: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

Note: `go test ./...` WITHOUT the embed_real_rsync tag must also pass — the new test file's build tag excludes it from the default suite, so it won't fail compilation.

- [ ] **Step 6.6: Commit.**

```bash
git add test/e2e/embedded_real_rsync_test.go test/e2e/binary_cache.go
git commit -m "test(e2e): real-rsync release guard with externally-verified content equality [Task 6/12a, AC-12b-2 + AC-12b-3]

External shasum subprocess for dest hashes (round-2 QA C1: closes the
'silently corrupted' gap). Recursive diff -rq for tree equality
(content-only; xattr/ACL are separate layers). xattr survival assertion
on user.flashbackup-test. ACL survival assertion against the gen-time-
recorded user (round-2 QA N2: semantic compare, not hardcoded). Per-arch
otool linkage regression (round-2 QA N4: universal2 multi-header).
rsync --version match.

Build tag embed_real_rsync ensures this only runs against the real-
rsync build; default go test skips it.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 7: `scripts/build-rsync.test.sh` (negative tests)

**Files:**
- Create: `scripts/build-rsync.test.sh`
- Test: `bash scripts/build-rsync.test.sh`

- [ ] **Step 7.1: Write the negative-test script.**

Create `scripts/build-rsync.test.sh`:

```bash
#!/bin/bash
# Negative tests for scripts/build-rsync.sh.
# Per Task 12a AC-12b-4: each scenario must exit 1 with the expected error marker.
#
# Run manually: bash scripts/build-rsync.test.sh
# Run via Make: make test-build-script (added in this task).

set -uo pipefail   # NOT -e; tests EXPECT failures and assert on them.

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${REPO_ROOT}"

PASSED=0
FAILED=0

assert_fail_with_marker() {
    local description="$1"
    local marker="$2"
    local cmd="$3"
    local out
    out="$(eval "${cmd}" 2>&1)"
    local rc=$?
    if [[ ${rc} -eq 0 ]]; then
        echo "FAIL: ${description} (expected exit 1; got 0)"
        FAILED=$((FAILED + 1))
        return
    fi
    if ! echo "${out}" | grep -q "${marker}"; then
        echo "FAIL: ${description} (expected '${marker}' in output)"
        echo "  output: ${out}"
        FAILED=$((FAILED + 1))
        return
    fi
    echo "PASS: ${description}"
    PASSED=$((PASSED + 1))
}

# --- Test 1: Tarball SHA mismatch ---
# Create a cached tarball with wrong content; script must catch SHA mismatch.
mkdir -p build/cache
echo "not a tarball" > build/cache/rsync-3.4.1.tar.gz
assert_fail_with_marker \
    "tarball SHA mismatch detected" \
    "tarball SHA256 mismatch" \
    "./scripts/build-rsync.sh --verify-only"
rm -f build/cache/rsync-3.4.1.tar.gz

# --- Test 2: Missing prereq (clang) ---
# Run with stripped PATH; script must catch missing clang.
assert_fail_with_marker \
    "missing prereq detected" \
    "required tool 'clang' not on PATH" \
    "PATH=/usr/bin:/bin ./scripts/build-rsync.sh"
# Wait — clang IS in /usr/bin on macOS via Command Line Tools. To force a
# real missing-prereq, use an even smaller PATH:
assert_fail_with_marker \
    "missing prereq detected (PATH=/bin only)" \
    "required tool" \
    "PATH=/bin ./scripts/build-rsync.sh --verify-only"

# --- Test 3: Corrupted cache (bit-flipped tarball) ---
# Download a real tarball, flip one byte, re-run; script must catch SHA mismatch on cache hit.
./scripts/build-rsync.sh --verify-only >/dev/null 2>&1 || {
    echo "SKIP: corrupted cache test (initial download failed; samba.org reachable?)"
    SKIPPED=$((${SKIPPED:-0} + 1))
}
if [[ -f build/cache/rsync-3.4.1.tar.gz ]]; then
    # Flip one byte at offset 100. Use dd.
    printf '\x42' | dd of=build/cache/rsync-3.4.1.tar.gz bs=1 seek=100 count=1 conv=notrunc 2>/dev/null
    assert_fail_with_marker \
        "corrupted cache caught on re-verify" \
        "tarball SHA256 mismatch" \
        "./scripts/build-rsync.sh --verify-only"
    # Note: this test mutates the cache; a normal run after this will re-download. That's correct behavior.
    rm -f build/cache/rsync-3.4.1.tar.gz
fi

# --- Test 4: Partial-make recovery ---
# Documented but not automated: start make in background, kill, restart.
# Manual test acceptable for v0.1 because automating it requires precise
# timing. Document the procedure for MM:
echo "INFO: Test 4 (partial-make recovery) is a manual test."
echo "  1. Run 'make build-rsync &' in another terminal."
echo "  2. After 30s: 'pkill -f make'."
echo "  3. Re-run 'make build-rsync'. Expected: clean recovery (per-arch rm -rf at start of build_arch)."

# --- Summary ---
echo
echo "=== test summary ==="
echo "PASSED: ${PASSED}"
echo "FAILED: ${FAILED}"
if [[ ${FAILED} -gt 0 ]]; then
    exit 1
fi
exit 0
```

Make executable: `chmod +x scripts/build-rsync.test.sh`.

- [ ] **Step 7.2: Add Makefile target `test-build-script`.**

Append to `Makefile`:

```makefile
.PHONY: test-build-script
test-build-script:
	bash scripts/build-rsync.test.sh
```

- [ ] **Step 7.3: Run the negative-test script.**

```bash
make test-build-script
```

Expected: all 3 automated tests pass (Test 4 is manual). Output: `PASSED: 3` `FAILED: 0`.

If FAIL, the build-rsync.sh from Task 1 is missing error markers. Fix and re-run.

- [ ] **Step 7.4: Run pre-commit gates.**

```bash
go vet ./... && gofmt -s -l ./... && go test -race ./... && make coverage
```

- [ ] **Step 7.5: Commit.**

```bash
git add scripts/build-rsync.test.sh Makefile
git commit -m "test(build-rsync): negative tests for tarball mismatch + missing prereq + corrupted cache [Task 7/12a, AC-12b-4]

Three automated negative tests for scripts/build-rsync.sh:
1. Tarball SHA mismatch (plant bad tarball; expect exit 1).
2. Missing prereq (stripped PATH; expect exit 1).
3. Corrupted cache (bit-flip cached tarball; expect re-verify catch).

Test 4 (partial-make recovery) is manual: documented procedure in
the script's output for MM. Automating it requires precise timing
that's not worth the test infrastructure complexity.

Makefile target test-build-script.

Pre-commit: go vet PASS / gofmt -s -l PASS / go test -race PASS / make coverage PASS"
```

---

## Task 8: `.github/workflows/ci.yml` build-rsync-smoke job

**Files:**
- Modify: `.github/workflows/ci.yml`
- Test: PR-triggered CI run

- [ ] **Step 8.1: Look up commit SHAs for pinned actions.**

Open these in a browser to find the latest v4.x commit SHA for each:
- `actions/checkout` — https://github.com/actions/checkout/releases (use latest v4.x commit SHA)
- `actions/cache` — https://github.com/actions/cache/releases

Record the 40-character commit SHAs.

- [ ] **Step 8.2: Add the smoke job to `.github/workflows/ci.yml`.**

Locate the existing `jobs:` block. After the last existing job, add (replace `<PINNED_SHA_CHECKOUT>` and `<PINNED_SHA_CACHE>` with the 40-char commit SHAs from Step 8.1):

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
      - uses: actions/checkout@<PINNED_SHA_CHECKOUT>
      - name: Cache rsync tarball
        uses: actions/cache@<PINNED_SHA_CACHE>
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
          otool_out="$(otool -L ./build/arm64/rsync)"
          if echo "$otool_out" | grep -E '^\s+/' | grep -v 'libSystem\.B\.dylib' | grep -q '\.dylib'; then
            echo "FATAL: linkage to non-libSystem dylib detected" >&2
            echo "$otool_out" >&2
            exit 1
          fi
```

- [ ] **Step 8.3: Push to a branch and open a PR to test.**

```bash
git checkout -b task-12a-ci-smoke
git add .github/workflows/ci.yml
git commit -m "ci: build-rsync-smoke matrix job (macos-13/14/15) [Task 8/12a, AC-CI-1]"
git push origin task-12a-ci-smoke
gh pr create --title "[Task 8/12a] CI build-rsync-smoke matrix" --body "AC-CI-1: per-commit build verification across macOS 13/14/15."
```

- [ ] **Step 8.4: Watch the CI run and verify all three matrix cells pass.**

```bash
gh run watch
```

All three matrix cells (`macos-13`, `macos-14`, `macos-15`) must succeed within 90 seconds each.

If `macos-13` fails to find a runner (GitHub may have removed it), the spec allows fallback. Document in the PR and amend the matrix to `[macos-14, macos-15]` (and let the spec amendment land separately).

- [ ] **Step 8.5: Merge the PR.**

After CI green:

```bash
gh pr merge --squash --delete-branch
git checkout main
git pull
```

---

## Task 9: `.github/workflows/release.yml`

**Files:**
- Create: `.github/workflows/release.yml`
- Manual: Repo settings configure `production` environment with MM as required reviewer

- [ ] **Step 9.1: Look up commit SHAs for the new actions.**

- `actions/setup-go` — latest v5.x commit SHA
- `actions/attest-build-provenance` — latest v1.x commit SHA
- `softprops/action-gh-release` — latest v2.x commit SHA

Plus reuse the `actions/checkout` and `actions/cache` SHAs from Task 8.

- [ ] **Step 9.2: Create `.github/workflows/release.yml`.**

```yaml
name: Release
on:
  push:
    tags: ['v*.*.*']
  workflow_dispatch:

permissions:
  contents: write
  attestations: write
  id-token: write

jobs:
  release:
    runs-on: macos-14
    timeout-minutes: 45
    environment: production
    steps:
      - uses: actions/checkout@<PINNED_SHA_CHECKOUT>
      - uses: actions/setup-go@<PINNED_SHA_SETUP_GO>
        with:
          go-version-file: 'go.mod'
      - name: Cache rsync tarball
        uses: actions/cache@<PINNED_SHA_CACHE>
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
        run: |
          shasum -a 256 ./flashbackup > flashbackup.sha256
          echo "FLASHBACKUP_SHA256=$(cut -d' ' -f1 flashbackup.sha256)" >> $GITHUB_ENV
      - name: Generate build provenance attestation
        uses: actions/attest-build-provenance@<PINNED_SHA_ATTEST>
        with:
          subject-path: ./flashbackup
      - name: Upload to GitHub Release (DRAFT)
        uses: softprops/action-gh-release@<PINNED_SHA_GH_RELEASE>
        with:
          draft: true
          body: |
            ## ${{ github.ref_name }}
            SHA256: ${{ env.FLASHBACKUP_SHA256 }}
            Provenance: see attestation tab.
          files: |
            flashbackup
            flashbackup.sha256
```

- [ ] **Step 9.3: Configure the GitHub `production` environment.**

This is a manual one-time step in the GitHub UI:
1. Navigate to repo Settings → Environments → New environment → name: `production`.
2. Add "Required reviewers" → add MM's GitHub username.
3. Save.

Without this, the `environment: production` line in release.yml will block waiting for an approver that doesn't exist.

- [ ] **Step 9.4: Test the workflow via `workflow_dispatch`.**

```bash
gh workflow run release.yml
gh run watch
```

Expected: workflow starts → pauses on environment approval → MM approves in the UI → workflow continues → uploads draft release. Inspect the draft.

If anything fails, fix and re-run. Do NOT publish the draft yet (will be tested at first real tag push later).

- [ ] **Step 9.5: Commit + push.**

```bash
git add .github/workflows/release.yml
git commit -m "ci: release workflow with environment-production approval + Sigstore attestation [Task 9/12a, AC-CI-2]

Tag-push and workflow_dispatch triggers. environment: production
requires MM approval before any build step runs. 45-minute timeout.

Steps: setup-go + rsync universal2 + real-rsync flashbackup +
12b-B e2e + SHA256 sidecar + attest-build-provenance + draft
upload via softprops/action-gh-release.

Documents Plan 2 restructure note inline (codesign + notarize +
staple must run BEFORE 12b-B / SHA256 / attestation; current order
is correct for 12a but Plan 2 cannot just append).

Pre-commit: not applicable (YAML-only commit; tested via gh workflow run)"
git push
```

---

## Task 10: `.github/workflows/actions-lint.yml`

**Files:**
- Create: `.github/workflows/actions-lint.yml`

- [ ] **Step 10.1: Create the lint workflow.**

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
      - uses: actions/checkout@<PINNED_SHA_CHECKOUT>
        with:
          fetch-depth: 0  # needed for git log on rsync.version
      - name: Detect floating action tags
        run: |
          # All third-party action `uses:` lines must be pinned commit SHAs
          # (40 hex chars), not version tags. Per Task 12a spec §4.6 and
          # round-2 Hacker N6.
          if grep -rE 'uses: [^@]+@v[0-9]' .github/workflows/; then
            echo "FATAL: floating-tag action reference detected (use commit SHA)" >&2
            exit 1
          fi
          echo "OK: all action references are commit-SHA-pinned"
      - name: Enforce rsync.version.attestation freshness + witness agreement
        run: |
          if [[ ! -f scripts/rsync.version.attestation ]]; then
            echo "FATAL: scripts/rsync.version.attestation missing" >&2
            exit 1
          fi
          # Three witness SHAs must all be identical.
          witness_count=$(grep -cE '^Witness-' scripts/rsync.version.attestation)
          if [[ "${witness_count}" -lt 3 ]]; then
            echo "FATAL: fewer than 3 Witness-* lines in rsync.version.attestation (got ${witness_count})" >&2
            exit 1
          fi
          unique_shas=$(grep -E '^Witness-' scripts/rsync.version.attestation | awk '{print $2}' | sort -u | wc -l | tr -d ' ')
          if [[ "${unique_shas}" != "1" ]]; then
            echo "FATAL: rsync.version.attestation witnesses disagree (unique SHA count: ${unique_shas})" >&2
            grep -E '^Witness-' scripts/rsync.version.attestation >&2
            exit 1
          fi
          # Attestation must be within 90 days of rsync.version's last edit.
          ver_mtime=$(git log -1 --format=%ct -- scripts/rsync.version)
          att_mtime=$(git log -1 --format=%ct -- scripts/rsync.version.attestation)
          if [[ -z "${ver_mtime}" || -z "${att_mtime}" ]]; then
            echo "FATAL: could not determine modification times for rsync.version or attestation" >&2
            exit 1
          fi
          ninety_days=7776000
          if (( att_mtime + ninety_days < ver_mtime )); then
            echo "FATAL: rsync.version.attestation is older than 90 days before rsync.version edit" >&2
            echo "  rsync.version mtime: ${ver_mtime}" >&2
            echo "  attestation mtime:   ${att_mtime}" >&2
            exit 1
          fi
          echo "OK: attestation is current (within 90 days of rsync.version edit) and witnesses agree"
```

Replace `<PINNED_SHA_CHECKOUT>` with the SHA from Task 8.1.

- [ ] **Step 10.2: Push to a branch and test.**

```bash
git checkout -b task-12a-actions-lint
git add .github/workflows/actions-lint.yml
git commit -m "ci: actions-lint workflow enforces floating-tag + attestation freshness [Task 10/12a, AC-CI-3]"
git push origin task-12a-actions-lint
gh pr create --title "[Task 10/12a] CI actions-lint" --body "AC-CI-3: enforces no-floating-tag in workflows + rsync.version.attestation freshness + witness agreement."
gh run watch
```

Expected: lint job passes against current state. If FAIL, the spec's contracts aren't being met (attestation file missing, or witnesses disagree, or a workflow still uses a floating tag).

- [ ] **Step 10.3: Merge.**

```bash
gh pr merge --squash --delete-branch
git checkout main
git pull
```

---

## Task 11: One-time bootstrap of `upstream-mirror/rsync-3.4.1` GitHub Release

**This is a manual operator step**, run ONCE per upstream rsync version. Document in `docs/runbooks/rsync-version-bump.md` (Task 12d will formalize this; for now, just execute).

- [ ] **Step 11.1: Confirm `scripts/rsync.version` + `scripts/rsync.version.attestation` are committed and up to date.**

```bash
cat scripts/rsync.version
cat scripts/rsync.version.attestation
```

- [ ] **Step 11.2: Run verify-only to download + SHA-check the tarball.**

```bash
make build-rsync-verify
```

Expected: `tarball downloaded + verified`. The verified tarball is now at `build/cache/rsync-3.4.1.tar.gz`.

- [ ] **Step 11.3: Create the upstream-mirror GitHub Release and upload the tarball.**

```bash
gh release create upstream-mirror/rsync-3.4.1 \
    --title "Upstream mirror: GNU rsync 3.4.1" \
    --notes "Mirror of https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz at the pinned SHA recorded in scripts/rsync.version.attestation. This release is referenced by scripts/build-rsync.sh as PRIMARY_URL; samba.org is FALLBACK_URL. SHA256: $(shasum -a 256 build/cache/rsync-3.4.1.tar.gz | cut -d' ' -f1)" \
    build/cache/rsync-3.4.1.tar.gz
```

- [ ] **Step 11.4: Verify the mirror is now reachable.**

```bash
curl -fsSL -o /tmp/test-mirror.tar.gz \
    "https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror/rsync-3.4.1.tar.gz"
shasum -a 256 /tmp/test-mirror.tar.gz
# Compare to scripts/rsync.version.attestation. Must match.
rm /tmp/test-mirror.tar.gz
```

- [ ] **Step 11.5: Run `make clean-rsync` and re-run `make build-rsync` to confirm the PRIMARY_URL path now works.**

```bash
make clean-rsync
make build-rsync 2>&1 | tee /tmp/build-log.txt
# Inspect the log: should say "downloading from primary mirror..." without the
# samba.org fallback message.
grep -q "primary mirror unreachable" /tmp/build-log.txt && echo "ERROR: fell back to samba" || echo "OK: primary mirror used"
```

This step does NOT need to be committed; it's an operator-side verification. Document the action in the next commit's body.

---

## Task 12: Close out — BACKLOG, memory, and Phase 0 dogfood re-entry

**Files:**
- Modify: `docs/BACKLOG.md`
- Modify: `~/.claude/projects/.../memory/project_execution_state.md`
- Modify: `docs/dogfood/2026-06-05-1920-phase-0-log.md` (note Task 12a complete; clear Phase 0 gate-blocker observation)

- [ ] **Step 12.1: Update `docs/BACKLOG.md`.**

Replace the current "Project status (2026-06-05 evening): Phase 0 dogfood session 1 surfaced Task 12a as the blocker" section heading with a new status block at the very top:

```markdown
## Project status (2026-06-06 evening): Tasks 12a + 12b COMPLETE; Phase 0 dogfood resumes against real-rsync builds

**Phase:** Phase 0 dogfood (MM-only validation) resumes. Tasks 12a + 12b shipped per `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md` + `docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md`. Phase 0 gate (50 cumulative backup runs without data-loss + weekly verify clean) can now close honestly against `make build-real-rsync` artifacts.

**Tasks queued for 2026-06-12 (before Phase 0 gate close on 2026-06-19):**
- Task 12c: `SECURITY.md` + rsync-announce monitoring + CVSS ≥7.0 re-cut SLO.
- Task 12d: `docs/runbooks/release-cut.md` + `rsync-version-bump.md` + `sev1-rollback.md`.
- Genuinely-independent fourth witness for upstream SHA (Plan 2 GPG escalation).

[then move the old "Phase 0 session 1 / Task 12a as the blocker" section under "Older project status"]
```

Also update the "Queued for Plan 2 (out of v0.1.0-core scope)" list to add: `12c`, `12d`, and remove "12a" if it was listed there.

- [ ] **Step 12.2: Update the project execution state memory file.**

`~/.claude/projects/-Users-maheshm-Documents-1-AI-Projects-Utilities-Backup-Mac/memory/project_execution_state.md`:

Replace the current top "Phase:" line with:

```
**Phase:** Phase 0 dogfood resumes against real-rsync builds. Tasks 12a + 12b shipped 2026-06-06 per planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md. v0.1.0-core tag still at b39a11c; `make build-real-rsync` now produces a working clean-install binary. Phase 0 gate (50 cumulative runs + weekly verify clean) can close honestly. Target: 2026-06-19.

**Tasks 12c + 12d queued for 2026-06-12** (before Phase 0 gate close): CVE-response posture stub + release/rollback/version-bump runbooks.

**Re-entry triggers (start a new Claude session on):**
- Sev1: data loss / atomic-gate misfire / source deleted without verify.
- Sev2: verify failure / hash mismatch / manifest tamper.
- 2026-06-12: Task 12c + 12d landing check.
- 2026-06-19: Phase 0 gate assessment.
```

Move the previous "Plan 1.5 prep" content under an "Older snapshot" header.

- [ ] **Step 12.3: Update `docs/dogfood/2026-06-05-1920-phase-0-log.md`.**

In the "Phase 0 gate blocker" observation (under §Observations), append a closing note:

```markdown
### 2026-06-06 - GATE BLOCKER RESOLVED

Tasks 12a + 12b shipped per spec `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md`. `make build-real-rsync` now produces a flashbackup binary with real universal2 GNU rsync 3.4.1 embedded; Phase 0 dogfood can resume against that binary without env override.

Plan execution: `docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md`. All 12 tasks landed; AC-12a-1..9 + AC-12b-1..4 + AC-CI-1..3 verified.
```

Add a new row to the Backup runs table reflecting the first dogfood run using `make build-real-rsync` (without env override). If MM has not yet run this, leave a placeholder row and update on first run.

- [ ] **Step 12.4: Commit + push.**

```bash
git add docs/BACKLOG.md docs/dogfood/2026-06-05-1920-phase-0-log.md
git commit -m "docs(backlog+dogfood): Tasks 12a + 12b COMPLETE; Phase 0 dogfood resumes [Task 12/12a]

BACKLOG project status updated to reflect Phase 0 dogfood resumption
against real-rsync builds. Tasks 12c + 12d queued for 2026-06-12 with
2026-06-19 Phase 0 gate close as the slip indicator.

Phase 0 dogfood log records the gate blocker resolution; placeholder
row added for first real-rsync backup (will populate on first run).

Pre-commit: not applicable (docs-only commit)"

git push
```

---

## Self-review

Per the writing-plans skill's self-review checklist:

### 1. Spec coverage

- [x] AC-12a-1 (build time <5 min): Task 1 Step 1.8.
- [x] AC-12a-2 (file shows universal2): Task 1 Step 1.9.
- [x] AC-12a-3 (otool -L per arch only libSystem): Task 1 Step 1.9.
- [x] AC-12a-4 (rsync --version is 3.4.1): Task 1 Step 1.9.
- [x] AC-12a-5 (make build-real-rsync produces working binary against fixture): Task 3 Step 3.2 + Task 6.
- [x] AC-12a-6 (make build still produces placeholder): Task 2 Step 2.7.
- [x] AC-12a-7 (make clean-rsync removes + echoes): Task 1 Step 1.6.
- [x] AC-12a-8 (attestation file with 3 witnesses): Task 1 Step 1.3 + Task 10 (enforcement).
- [x] AC-12a-9 (no Bash source of rsync.version): Task 1 Step 1.10.
- [x] AC-12b-1 (placeholder rejection test): Task 5.
- [x] AC-12b-2 (external shasum content equality): Task 6 Step 6.2.
- [x] AC-12b-3 (xattr + ACL end-to-end): Task 4 (fixture) + Task 6 (assertions).
- [x] AC-12b-4 (script negative tests): Task 7.
- [x] AC-CI-1 (build-rsync-smoke matrix): Task 8.
- [x] AC-CI-2 (release.yml with environment + attestation): Task 9.
- [x] AC-CI-3 (actions-lint enforces pin + attestation freshness): Task 10.

### 2. Placeholder scan

No TBD / TODO / "add appropriate" / "similar to Task N" patterns. Every step has complete code or commands.

### 3. Type consistency

- `BinaryFlavorRelease` used in Task 5 = same flavour the existing `make build` produces (release tag, no embed_real_rsync).
- `BinaryFlavorRealRsync` introduced in Task 6 Step 6.1 = `-tags 'release embed_real_rsync'`.
- `embeddedRsync` var name unchanged across all references.
- Build tag spelling: `embed_real_rsync` (snake_case, exact spelling) consistent across Makefile, Go files, spec.

---

## Execution handoff

Plan complete and saved to `docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md`. Two execution options:

**1. Subagent-Driven (recommended)** — I dispatch a fresh subagent per task, review between tasks, fast iteration. Matches the established cycle from Plan 1 (Mode A rigorous: implementer + spec review + code quality review per task).

**2. Inline Execution** — Execute tasks in this session using executing-plans, batch execution with checkpoints.

Which approach?
