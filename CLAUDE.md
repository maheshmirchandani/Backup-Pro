# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

**Last updated: Sun, 24 Aug 2026.** Re-read the "Project status" section before assuming anything about
where the work stands; this file went badly stale once already.

## Project status

**Plan 1 is complete and tagged. The product does not yet work on a clean install.**

`v0.1.0-core` is tagged at commit `b39a11c` and pushed. The engine is finished: runner state machine
(T0 preflight through T4 finalize), verify subsystem, plain renderer, and a `cmd/flashbackup` CLI with
`init`, `backup`, `verify`, `status`, `profiles` and `help`. Roughly 37,000 lines of Go across 22
packages, with unit, property, e2e and fault-injection tests.

**The blocker is Task 12a.** `internal/rsync/bin/rsync.placeholder` is a 342-byte shell script that
prints `PLACEHOLDER rsync; awaiting Task 12a build` and exits 0. On a clean install, `flashbackup
backup` enumerates files, runs the placeholder for the T1 transfer (exit 0, nothing transferred), T2
hash-compare tags every file `not_transferred`, and the run ends `partial`. Zero bytes are moved.
There is no data-loss risk (copy mode preserves the source, and the atomic gate would block move
mode), but the binary cannot back up data without the `FLASHBACKUP_RSYNC_PATH_FOR_TEST` override.

State this plainly rather than describing v0.1.0-core as a shipped v1: the engine is correct, the
build is incomplete.

**Current stage:** the Task 12a design spec is locked; plan v2 is being written. See "Task 12a" below.

**Phase 0 dogfood is paused** until Task 12a lands. Its gate (50 backup runs, clean weekly verify,
over a two-week window) targeted Fri, 19 Jun 2026 and has not been met; one session and two backups
are logged. Do not close that gate on the strength of env-override runs.

## Source-of-truth documents

| File | Purpose |
|---|---|
| `docs/BACKLOG.md` | Rolling project status, decision history, queued tasks. **Read the top section first**; it carries the current status. Sections below "History" are historical and are not maintained. |
| `docs/specs/2026-06-03-1532-flashbackup-design.md` | Canonical design spec. 58 locked invariants, architecture, on-disk layout, run flow, move semantics, verify, error handling, acceptance criteria. |
| `docs/specs/2026-06-03-1338-flashbackup-prd.md` | Canonical PRD. Fall back to it for context the design spec does not carry forward. |
| `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md` | Task 12a design spec. **LOCKED** after two multi-hat review rounds. |
| `docs/planning/2026-06-03-flashbackup-core-engine.md` | Plan 1 (core engine). Executed to completion. |
| `docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md` | Task 12a plan **v1. Superseded and not executable**: a four-hat review found 14 Critical findings, chiefly fabricated helper signatures. Kept for history. |
| `docs/ERROR_CATALOG.md` | Single source of truth for user-visible error strings and canonical event Kinds. No inline `fmt.Errorf` user-facing strings. |
| `docs/DOGFOOD.md` | Phase 0 dogfood checklist and gate definition. |
| `docs/dogfood/2026-06-05-1920-phase-0-log.md` | Dogfood run log. |
| `docs/archive/` | Original PRD as received, for historical record. |

## Non-negotiable invariants (summary)

The design spec carries all 58 locked invariants in its "Locked invariants (master list)" section.
Read them there. The ones most likely to be violated by a careless change:

- **Move semantics (#1 family):** copy, then validate, then delete, behind an atomic gate. Any
  non-verified file blocks all source deletion.
- **Validation:** SHA256 captured at source-read time and at dest-read time, then compared. The
  manifest stores the source-side hash, protected by a keyed checksum (#33).
- **Source mutation gate:** re-stat the source at T3 before unlinking; skip the unlink if
  `(size, mtime_ns)` changed since T0.
- **User files at the destination are never touched (#6):** only paths FlashBackup itself wrote are
  eligible for mirror-delete.
- **Portability:** runs from a USB drive, no install, no admin rights, all state under
  `<USB>/.flashbackup/`.
- **Filesystem:** APFS or HFS+ only. exFAT is refused with a reformat recipe.
- **Namespacing (#5, #15):** destination paths are prefixed `<hostname>-<username>`, computed only
  via the shared `internal/paths` package.
- **Release build hygiene (#35):** no fault-injection symbols in a release binary; enforced by the
  `verify-release` symbol scan.
- **Coverage (#42):** `runner`, `hash`, `state` and `preflight` each hold at or above 80% statements.

If a proposed approach violates any locked invariant, stop and surface the conflict.

## Build, test and run

Go 1.23 is the `go.mod` floor. The tree builds and tests clean on Go 1.26.5 as of Sun, 24 Aug 2026.

```bash
make build              # release binary -> ./flashbackup (tags: release)
make build-faultinject  # fault-injection binary -> ./flashbackup-faultinject
make test               # unit tests, whole tree
make test-pkg PKG=./internal/state   # single package, fast TDD loop
make e2e-fast           # PR-gating e2e (happy paths); needs macOS + hdiutil
make e2e-safety         # faultinject + hdiutil-heavy e2e; gates main
make coverage           # per-package 80% gate on the safety-critical four
make verify-release     # symbol scan: no faultinject leakage into release
make lint               # gofmt -s, go vet, golangci-lint
make ci-local           # everything above, in CI order
make clean
```

### Test prerequisites

- **macOS.** The e2e suite and the `preflight`, `drives`, `rsync` and `runner` packages need macOS.
  Portable packages (`hash`, `state`, `profiles`, `paths`, `selection`, `runner/types`, `verify/*`)
  run on Linux; CI splits on exactly that line.
- **`FLASHBACKUP_E2E=1`.** Every e2e test self-gates on it. The Makefile targets set it for you.
- **`hdiutil`.** e2e tests mount a real APFS disk image per test.
- **GNU rsync 3.x** at `/opt/homebrew/bin/rsync` (`brew install rsync`). Tests inject it via
  `FLASHBACKUP_RSYNC_PATH_FOR_TEST` because the embedded rsync is still the Task 12a placeholder.
- **`golangci-lint`** is pinned to `1.61.0` in `scripts/golangci-version.txt`. It needs Go 1.23 and
  will not run against a newer local toolchain, so `make lint` is a CI-only gate in practice. Run
  `gofmt -s -l . && go vet ./...` locally instead.

### Silent-failure modes (read this before trusting a green run)

Several gates in this repo return success when they have done nothing. A passing run is not by
itself evidence.

1. **Bare `go test ./test/e2e/...` proves almost nothing.** Without `FLASHBACKUP_E2E=1` every e2e
   test calls `t.Skip` and the package reports `ok` in under a second. Verified Sun, 24 Aug 2026:
   `TestE2E_BackupHappy_CopyMode` skips bare and passes in 5.7s with the gate set. Always use the
   `make e2e-*` targets.
2. **The Makefile skips missing directories on purpose.** `test-faultinject`, `e2e-fast`,
   `e2e-safety`, `bench` and `verify-release` echo `skip: ...` and exit 0 when their inputs are
   absent. This dates from the early plan tasks; it is still live and will mask a deleted directory.
3. **`make coverage` swallows test failures.** Each package runs under `|| true`, so a compile error
   or panic shows up only as a missing or empty profile, reported as `(no statements; vacuously
   covered)`. Read the output; do not just check the exit code.
4. **`gofmt -l` is not enough.** CI runs the `-s` simplifier variant, which rewrites list-indent
   shapes bare `gofmt` accepts. Always `gofmt -s -l`.
5. **Three e2e tests currently `t.Skip` by design**, with their future-state assertions already
   written below the skip: `crash_resume_test.go` (waits on Task 50a), `delete_flag_test.go` (waits
   on Task 51c), and the AC-13b case (waits on Task 50b). They flip on automatically when those
   tasks land.
6. **The placeholder rsync path is not exercised end-to-end anywhere.** That gap is precisely how the
   Task 12a blocker reached a tagged release. Task 12b closes it.

### Running against a real USB drive

```bash
./flashbackup init /Volumes/<USB>
./flashbackup profiles add        # opens $EDITOR
./flashbackup backup <profile> /Volumes/<USB>            # copy mode
./flashbackup backup <profile> /Volumes/<USB> --move     # move mode, confirmation required
./flashbackup verify /Volumes/<USB>
./flashbackup status /Volumes/<USB>
```

Until Task 12a lands, prefix backup and verify commands with
`FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync` or nothing will transfer.

### Platform notes

- **Apple Silicon and Intel.** All development so far has been arm64 (M1 Max). The universal2
  binary is a Task 12a deliverable; no x86_64 path has been exercised yet. CI covers macos-14 only,
  which is arm64.
- **Release binaries are unsigned and not notarized.** Gatekeeper refuses them; the README documents
  the `xattr` bypass. Signing and notarization are Plan 2.
- **`chflags uchg`** is set on the extracted rsync. Test helpers must clear it during cleanup, or a
  temp directory removal fails. `SetupUSB` already handles this.

## Task 12a: the current work

Replace the placeholder with a real universal2 GNU rsync 3.4.1 built from upstream source, so a clean
install actually transfers data.

- **Spec: locked.** `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md`.
  Two multi-hat rounds (five hats, then six). Do not reopen it without cause.
- **Plan v1: superseded.** Its Tasks 5 and 6 invented helper signatures instead of reading
  `test/e2e/helpers.go`. Treat every code reference in it as suspect.
- **Plan v2** supersedes it. Verified helper signatures, corrected workflow ordering, hardened supply
  chain gates, deterministic fixtures.
- **`scripts/build-rsync.sh` is a comment-only stub.** It documents the intended steps and builds
  nothing.
- Companion **Task 12b** adds the e2e test that runs without the env override.

Apple notarization is out of scope for 12a. It belongs to Plan 2.

## Queued work (not blocking, tracked in BACKLOG.md)

`12b` (e2e without rsync override) · `12c` (CVE posture) · `12d` (release, rollback and version-bump
runbooks) · `22a` (unowned T0 event Kinds) · `29a` (PreflightContext test injection) · `50a`
(preflight orphan-recovery gate) · `50b` (AC-13b `--partial` resume e2e) · `51c` (`--delete` flag and
mirror-delete-DEST phase) · `PA-1`, `PA-2` (minor plan amendments).

Three cosmetic defects from dogfood session 1, unfixed: free space reports 0 B on APFS (likely
`statfs.f_bfree` where it should be `f_bavail`); the verify renderer's summary `details:` falls back
to a generic path instead of the specific `summary.json`; `duration_seconds` truncates sub-second
verifies to 0.

## Workflow

The Superpowers flow governs all substantial work here and has already produced Plan 1 and the
Task 12a spec.

1. **Brainstorm** with `superpowers:brainstorming` before any new feature or change.
2. **Apply `spec-development-discipline`** when finalising a substantial spec.
3. **Multi-hat review** the spec per the global CLAUDE.md menu. Subagent-driven, parallel.
4. **Write the implementation plan** with `superpowers:writing-plans`.
5. **Multi-hat review** the plan (CISO, Hacker, DevOps/SRE, QA, Senior Developer, DX).
6. **Execute** with `superpowers:subagent-driven-development`.
7. **Debug** with `superpowers:systematic-debugging` for any test failure or unexpected behaviour.

**Dispatch protocol (in force since Thu, 04 Jun 2026).** Every implementer subagent runs
`go vet ./... && gofmt -s -l . && go test -race ./... && make coverage` locally before committing,
and reports all four outputs. A combined spec-plus-quality review is dispatched after each push.

**Plans are grounded in code, never in recall.** Plan v1's failure mode was generating pseudo-code
for helpers that were never opened. Any plan referencing a function signature must quote it from the
file it lives in.

**Hard rule from PRD §11:** do not start coding without explicit approval. No exploratory prototypes.

## Repository layout

```
cmd/flashbackup/     CLI entry point and subcommand handlers
internal/
  drives/            USB volume discovery
  hash/              SHA256 helpers
  paths/             namespaced destination paths (single source of truth)
  plain/             non-TTY plain-text renderer
  preflight/         T0 gates: lock, filesystem, symlink, codesign, volume_uuid
  profiles/          profile store (<USB>/.flashbackup/profiles.json)
  rsync/             embedded rsync, extraction, wrapper, progress parser
  runner/            T0-T4 state machine and phase functions
  selection/         include/exclude evaluation
  state/             events, manifest, runs.ndjson, version.json
  testutil/          hdiutil mount helpers shared across packages
  verify/            verify state machine, manifest load, rehash
test/
  e2e/               end-to-end tests (helpers.go carries the shared API)
  fixtures/          tiny, realistic and pathological trees with MANIFEST.txt tripwires
scripts/             build-rsync.sh (stub), golangci-version.txt
```

`test/fixtures/*/MANIFEST.txt` holds a SHA256-of-tree tripwire enforced by `FixtureTreeSHA256`.
Changing a fixture without regenerating the manifest via `test/fixtures/regen-manifest.sh` fails the
tripwire test.

## Housekeeping

- The repository is public at `https://github.com/maheshmirchandani/Backup-Pro` (GPLv3), and was made
  public to resolve a GitHub Actions billing block. The local working directory keeps the older name
  `Backup-Mac`.
- `.superpowers/` holds brainstorm and visual-companion artefacts and is already in `.gitignore`.
- CI (`.github/workflows/ci.yml`) runs five jobs: `test-linux` (ubuntu, portable packages plus
  `GOOS=darwin` cross-vet), `test` (macos-14, macOS-only packages plus lint, coverage and
  verify-release), `e2e-fast` and `e2e-safety` (macos-14, hdiutil), and `bench` (ubuntu). Docs-only
  commits are skipped via `paths-ignore`, so a run of documentation commits leaves CI unexercised.
