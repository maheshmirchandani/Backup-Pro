# FlashBackup Backlog

> Rolling log of design decisions, open items, and historical context for the FlashBackup project. Updated as the project evolves. Lives at `docs/BACKLOG.md`.

## Project status (2026-06-04 night session, after Task 29 + Task 28 review fixes)

**Phase:** Plan 1 execution. Tasks 1-29 complete. CI green at `8461b0f`. RUNNER PACKAGE COMPLETE end-to-end: T0 through T4 phases, fault-injection harness, top-level `runner.Run` orchestrator with signal handling and atomic gate decision. Next: Task 30 (`internal/verify/load` manifest reader).

**Repo:** `https://github.com/maheshmirchandani/Backup-Pro`.

**Next session begin protocol:**

1. Pull latest; confirm CI green via `gh run list --limit 1`.
2. **Begin Task 22** (`internal/runner/t0_preflight.go`): preflight orchestration, emit `state.Event{Kind:"phase_started",Phase:"T0"}` via `EventStore.Append`, "started" line via `RunLogStore.AppendStarted`, `Checkpoint()` at phase end. Per the plan API Contracts (now reconciled with Task 20's actual `PreflightContext` shape) and Task 21's runner types.
3. Continue overlap-CI cycle: Task N+1 implementer dispatched in parallel with Task N review subagent.

**Critical: dispatch protocol amendment (2026-06-04, post-CI-rescue).** Every implementer subagent MUST run `make lint && make coverage` locally before committing. The previous foundation phase's implementers shipped without these, accumulating 10 consecutive red CI runs (Tasks 12-20) that the BACKLOG falsely recorded as green. The coverage gate also had a latent bug where it took only the FIRST subpackage's percentage (alphabetical) and masked under-80% subpackages; this is now fixed in `b17dc22`. Statement-weighted tree totals are the gate going forward.

**Coverage at session end (true tree-weighted, per new gate in `b17dc22`):**
- runner: vacuously covered (types only)
- hash: 81.8%
- state: 83.0%
- preflight: 83.0%

Old gate falsely reported `preflight` based on `codesign` alone (92%); the lock subpackage at 75.4% was masked. Aggregate tree number is honest at 83.0%.

**Repo:** `https://github.com/maheshmirchandani/Backup-Pro` (private, GPLv3). Local: `/Users/maheshm/Documents/1-AI-Projects/Utilities/Backup-Mac/`.

**Latest CI green:** confirmed after fix for gosec G306 (commit `f0cf05c`). Per-package coverage real: hash 84.6%, state 83.0%, profiles 81.9%, drives 85.3% (all above 80% gate).

**Tasks complete (29/58):**
1. Bootstrap (manual): git init, GPLv3, conventions, GitHub Releases-ready
2. Makefile + golangci-lint + CI workflow (+ 4 code-review fixes + 4 Makefile-guard fixes + coverage-gate correctness fix + gosec G306 test-fixture fix)
3. `internal/paths` namespace prefix (3 tests)
4. `internal/hash` streaming SHA256 with sync.Pool + ctx (4 tests + property test + benchmark 2.35 GB/s)
5. `internal/state` event store (NDJSON + Checkpoint API, 9 tests + 21 subtests)
6. `internal/state` manifest store + length-prefixed HMAC + stream-gzip (7 tests + property + pipe-separator forgery test)
7. `internal/state` run log store (two-line model + torn-write recovery, 13 tests + 6 subtests)
8. `internal/state` version.json (FAIL-CLOSED on corruption, 12 tests)
9. `internal/profiles` Store + strict glob allowlist (14 tests + 9 subtests)
10. `internal/drives` macOS volume enumeration via diskutil (5 tests)

**Tasks remaining (48):** 11 (selection), 12 (rsync extract + hardening), 12a (build-rsync.sh), 13 (rsync wrapper), 14 (rsync progress parser), 15 (preflight/lock), 16 (preflight/filesystem), 17 (preflight/symlink), 18 (preflight/codesign), 19 (preflight/volume_uuid), 20 (preflight integrate), 21-29 runner state machine, 30-32 verify, 33 plain renderer, 34-41 CLI subcommands, 42 e2e helpers, 42a fixtures, 43-51 e2e tests, 51a AC-19 tamper test, 51b missing fault hooks, 52 delete confirm, 53 ERROR_CATALOG, 54 README, 55 dogfood tag.

**Plans:**
- `docs/planning/2026-06-03-flashbackup-core-engine.md` (Plan 1, ~2500 lines, ~58 tasks)
- Plan 2 (TBD; covers TUI + signed/notarized release pipeline + full friend-facing docs)

**Source PRD:** `docs/specs/2026-06-03-1338-flashbackup-prd.md`
**Design spec:** `docs/specs/2026-06-03-1532-flashbackup-design.md` (status: draft-complete, 472 lines)

**All 9 design sections locked:**
1. Architecture (Go + Bubble Tea + embedded GNU rsync 3.x)
2. On-disk layout (`<USB>/.flashbackup/`, mirror at root, namespaced)
3. Run flow (T0 to T4 state machine)
4. Move semantics (atomic gate, mutation re-stat, type DELETE prompt)
5. Verify command (per-manifest, no quick mode)
6. Error handling (tabulated by phase, signal contract, sleep prevention)
7. TUI shape (two-pane, linear wizard, compact progress, minimal modal, detailed summary, secondary screens)
8. Testing strategy (test pyramid, fault injection, TUI snapshots, cross-version CI, benchmarks)
9. Packaging (universal2, codesign + notarize + staple, GitHub Releases, GPLv3 compliance)

**Locked invariants count:** 28. See the master table in the design spec.

**Three adversarial review rounds completed during brainstorming:**
1. Hacker + Pre-mortem after Section 1: 3 showstoppers caught, locked as invariants #1-3.
2. Five-hat (CTO, Enterprise Architect, DevOps/SRE, QA, End User) after Section 3: 17 findings, 11 locked.
3. Five-hat (UX, End User, Hacker, QA, DevOps) after Section 7: 5 majors locked as invariants #24-28, ~10 smaller folds into spec.

## Open items (ordered by priority)

1. **Spec-development-discipline retrofit.** Apply 12-section structure per global CLAUDE.md (hypotheses, wedge analysis, locked decisions, behavioral principles, trust signals, cheap-now-expensive-later, audit abstraction, test pyramid, SLOs, acceptance criteria, etc.). Non-negotiable for substantial specs.
2. **Spec self-review.** Placeholder scan, internal consistency, scope check, ambiguity check.
3. **User review of finalized spec.** MM reads and approves or requests changes.
4. **Full multi-hat review.** Per global CLAUDE.md menu (CTO, Enterprise Architect, BA, CIO, CISO, Hacker, UX, DevOps/SRE, DPO, QA, End User). Subagent-driven, parallel. Apply approved amendments to spec.
5. **Transition to writing-plans skill.** Generate implementation plan from approved spec.
6. **Multi-hat review of implementation plan** (CISO, Hacker, DevOps/SRE, QA, Senior Developer, DX). Apply amendments.
7. **Subagent-driven implementation** via `superpowers:subagent-driven-development`.

## Cleanup / housekeeping

- Original `Portable macOS Backup Utility.txt` archived to `docs/archive/2026-06-03-portable-macos-backup-utility-original.txt` for historical record. Canonical PRD is now `docs/specs/2026-06-03-1338-flashbackup-prd.md`.
- `.superpowers/` directory in repo root holds visual companion mockups. Add to `.gitignore` once the repo is git-initialized.
- Project not yet under version control. Recommend `git init` before any implementation work begins.

## History (newest first)

### 2026-06-04 (later night): Task 28 review fixes + Task 29 (runner.Run state machine)

Task 28 combined review (commit `6e958fe`) returned a single important finding plus minor follow-ups. The important one was structurally significant: `LDFLAGS_RELEASE` and `LDFLAGS_FAULTINJECT` both passed `-s -w`, which strips the Go symbol table. With `-s` applied, `go tool nm flashbackup` returns only ~58 dynamic libc symbols, so the invariant #35 release-gate grep `(^|[._/])faultinject` was structurally a no-op against the actually-built binary. The implementer's claim that `faultinjectBuildTagPresent` is the gate's grep target was correct in principle but unverifiable against the stripped binary.

Fix landed in `8461b0f`: dropped `-s` from both LDFLAGS (kept `-w` for DWARF strip). Costs ~1MB in binary size; pays for itself by making the release gate actually have signal to scan. Spec amendments: build pipeline table row updated to `-w -buildid=` (not `-s -w -buildid=`), rationale row updated to explain the deliberate `-s` omission, invariant #35 row expanded to spell out the gate implementation, the build-flag requirement, and the `ErrFaultinjectStripped` mixed-case safety note. Plan amendment under DSL grammar block: canonical Point wire-string set documented (`T1-pre`, `T1`, `T1-post`, `T2-pre`, `T2`, `T3-pre`, `T3`) so future hook sites cannot drift from the constant set in code. Minor: `parseOne` now has a comment documenting why colon-split is safe on macOS APFS/HFS+ paths.

Task 29 (`internal/runner/runner.go`) dispatched in parallel with the Task 28 review per the overlap-CI protocol. The top-level orchestrator: generates RunID in canonical format via `crypto/rand`; opens EventStore + RunLogStore before T0 (T0 takes them as inputs); opens ManifestStore after T0 (needs `pc.VersionFile.HMACKey`); threads `PreflightContext.VerifyVolumeUnchanged` at every phase boundary; computes the namespaced destination `<DestRoot>/<hostname>-<username>/` per invariant #14; enforces the atomic gate (move mode + non-100% verified = skip T4 unlink, set `ExitStatus = "copy_only_aborted_delete"`); always runs T5 finalize so the manifest and runs.ndjson finished line land regardless. Instruments T1/T2/T3 phase files with `faultinject.Hook` calls at the seven canonical Point locations; hook firing in T1 cancels the rsync child context and prefers the hook error over the resulting "killed" error. Precondition assertions code as panics (signature-cover-candidates at T2 entry; verified-subset coverage at T3 entry) since a violation indicates an upstream T0+ bug, not user input.

Implementer commit `da24cd1`; 17 new tests (14 in `runner_test.go` + 3 in `runner_faultinject_test.go`); runner package coverage 83.5% (below the 85% sub-target; gate is 80%). CI green first try across all four jobs (test, bench, e2e-fast, e2e-safety). Coverage shortfall is in the `Run` function itself (66.9% per-function): the various intermediate phase-error returns and VerifyVolumeUnchanged failure branches need either an interface seam in PreflightContext or a gocovmerge across faultinject + release runs. Driving them without invasive seams is impractical in this task.

Design decisions worth recording:
- `newRunID(startedAt time.Time)` takes startedAt as a parameter so the RunID's timestamp portion and the reported `RunResult.StartedAt` cannot drift.
- Store-open order: EventStore + RunLogStore BEFORE T0 (T0 needs them as inputs); ManifestStore AFTER T0 (needs HMAC key from preflight). Three deferred Close paths.
- Pre-T0 minimum dir prep: runner does `os.MkdirAll(<dest>/.flashbackup/runs/<runID>)` before opening stores. T0's preflight still fully validates filesystem, lock, volume UUID.
- Pre-T0 failure path: `emitPreflightFailedSummary` returns `(result, err)` (not `(nil, err)`) so the caller sees `RunResult{ExitStatus:"preflight_failed", RunID, FinishedAt}` even on pre-T0 errors.
- Test seam for rsync override: `rsyncPathOverrideForTest` package-private var (matches the `deletionLogTestHook` pattern in t4) because the embedded rsync is a non-copying placeholder; e2e tests need a real GNU rsync (`brew install rsync` 3.4.3 installed on the dev machine to exercise this).
- Signal handling: `signal.NotifyContext(ctx, SIGINT, SIGTERM)` at runner entry. Second-signal-within-5s escalation deferred to cmd/main (Task 34).

Punted / surfaced for follow-up:
- TestRun_VolumeUUIDChangedMidRun skipped because PreflightContext does not expose an injectable VerifyVolumeUnchanged. Recommend a small Task 29a to add an injectable `verifyHookForTest func(ctx) error` to PreflightContext (logged as latent gap; not blocking Task 30+).
- Coverage 83.5% below 85% sub-target but above 80% gate. Accepted for v0.1; revisit if tests pile up.
- Faultinject tests don't contribute to standard coverage profile (Makefile coverage target runs without `-tags faultinject`). Acceptable for v0.1.

Task 28-29 commits this segment: `6e958fe` (Task 28 impl), `0bd583d` (Task 28 BACKLOG), `da24cd1` (Task 29 impl), `8461b0f` (Task 28 review fixes).

### 2026-06-04 (later night): Task 28 (faultinject DSL + release stub)

Task 28 (`internal/runner/faultinject.go` build tag `faultinject` + `internal/runner/faultinject_release.go` build tag `!faultinject`) dispatched per the established protocol. **First task to need a release-vs-faultinject build-tag pair**: same public API in both files so the runner phase code at instrumented sites compiles under either tag; the faultinject build executes the DSL grammar, the release stub returns `ErrFaultinjectStripped` on any non-empty Parse and no-ops Hook.

Implementer commit `6e958fe`; 30 new tests (24 faultinject + 6 release-stub); runner package coverage holds at 90.0%. CI green first try across all four jobs (test, bench, e2e-fast, e2e-safety). All pre-commit gates passed (vet, gofmt -s -l, race, coverage, both build-tag compilations).

Design decisions worth recording (subject to reviewer scrutiny):
- **Path discovery**: HookArgs carries `DestRoot` + `SourceRoot`; action helpers build absolute paths via `filepath.Join(<root>, CurrentFile)`. Keeps `Fault` parse-time pure; runner threads roots through per-phase.
- **Kill semantics**: returns sentinel `ErrFaultKill` instead of `os.Exit` so tests can assert the would-have-killed path without crashing the test process. `SetKillActionForTest(fn)` lets tests swap the helper.
- **One-shot triggers**: package-private `armedFault` wrapping each active `Fault` with an `armed bool`; first match flips the bit and stops further firings (locks the "after_pct/after_count fire once per fault" contract).
- **Release-gate sentinel**: package var `faultinjectBuildTagPresent` deliberately includes lowercase "faultinject" substring as the `nm | grep` target. Reassigned in `init()` to defeat dead-code elimination.
- **Release stub import surface**: only `context` and `errors` to prevent the release binary from dragging code containing the substring "faultinject" through indirect references.

Follow-ups surfaced for downstream consideration (not blocking Task 29):
- `make verify-release` gate is structurally limited against `-s -w` stripped binaries (no symbol table). Could swap to a DWARF-string scan or `strings | grep ErrFaultinjectStripped` for Phase 0 dogfood. Logged for post-Task-34 review.
- `Point` constants exist for documentation but Hook matching is `args.Phase`-string-only; extend `hookMatches` to consider Point if a future fault needs callsite-specific gating (e.g., distinguish T1-pre from T1-post within a single phase). Today's grammar doesn't expose Point to the DSL.
- `make verify-release` cannot fully run until `cmd/flashbackup/` lands (Task 34); Makefile guards skip cleanly today. The implementer demonstrated the gate works by building a temp `cmd/_fbverifytest` stub: unstripped release binary `nm` returns no faultinject hits; unstripped faultinject binary returns `faultinjectBuildTagPresent` hit (gate would fail the release).

Task 28 commit: `6e958fe` (implementer; combined review dispatching with Task 29 implementer).

### 2026-06-04 (latest): Task 27 (T4 finalize) - runner state machine COMPLETE

Task 27 (`internal/runner/t5_finalize.go`, phase T4) dispatched per the revised protocol. The LAST runner phase: gzip-finalizes the manifest (rename .tmp.gz → .gz + fsync parent dir via existing `ManifestStore.Gzip`), writes the runs.ndjson "finished" line via `RunLogStore.AppendFinished` (invariant #10 two-line model), prunes old run dirs beyond the 10-default retention limit, and emits `manifest_finalized` + `run_finished` audit events.

Implementer commit `0d4572a`; 19 test functions / 24 subtests; runner package coverage 89.9%. **First task this session to clear all 6 pre-commit checks on the first push** (the implementer ran `gofmt -s -l` AND used 0o600 fixtures correctly — the cumulative lessons from Tasks 24 + 26 CI rescues paid off).

Review verdict: **approve**. Two minor optional cleanups applied as a style-only follow-up (`2f35c7b`):
- Renamed `t5Abort` / `t5AbortOnAuditFail` to `runT5Abort` / `runT5AbortOnAuditFail` to match the sibling `runT1Abort` / `runT3Abort` / `runT4Abort` naming convention across the package.
- Dropped the dead `phaseWire` parameter from `runT5AbortOnAuditFail` (it was hardcoding `types.PhaseFinalize` anyway).

Implementer decisions (all reviewed and approved as documented):
- Prune is audit-silent on RemoveAll failures (no new `run_pruned` event Kind invented; stale dir remains for next-run retry).
- `RetentionLimit ≤ 0` defaults to 10.
- Order: phase_started → ManifestStore.Gzip → manifest_finalized → AppendFinished → RunLogStore.Checkpoint → prune → phase_completed → run_finished → EventStore.Checkpoint. Manifest durable before "finished" line (verify reads manifest); finished line durable before `run_finished` audit (compound-failure guard).

Task 27 commits: `0d4572a` (impl), `2f35c7b` (style polish per review).

**Status at session pause:** all six runner phase functions exist as siblings in `internal/runner/`. Runner package tree-weighted coverage 89.9%; preflight 84.9%; state 83.0%; hash 81.8%. CI green at `2f35c7b`. Memory file updated. Next session begins with Task 28.

### 2026-06-04 (later): Task 26 (T3 move-mode delete-source + atomic gate)

Task 26 (`internal/runner/t4_delete_source.go`, phase T3) dispatched per the revised protocol. **The most data-loss-sensitive phase in the whole runner**: implements the atomic gate (invariant #1: any T2 non-verified file blocks ALL deletions), per-file mutation re-stat (invariant #8 defense in depth on top of T2's re-stat), permanent `os.Remove` unlink (not Trash), and `deletion-log.ndjson` for crash recovery with fsync per line.

Implementer commit `e14181b`; 20 tests, runner package coverage 88.0%. **CI failed once** on a gosec G306 (test fixture used WriteFile mode 0o644 instead of 0o600). Pattern was already documented in memory but the implementer missed it. Fixed in `11aab3f`.

Review verdict: **minor-fixes-needed**. One important contract drift: `delete_failed.Details.errno` was required by the canonical Event Kinds table but the producer never populated it (the `deletionLogLine` struct even declared `ErrnoString` with json tag `errno` but it was never assigned). Fixed in `11aab3f` by adding an `errnoString(err error) string` helper that uses `errors.As` against `syscall.Errno` and maps the well-known POSIX names (EACCES, EPERM, ENOENT, EBUSY, EROFS, ENOTEMPTY, EIO). Test assertion added to `TestRunT4DeleteSource_PermissionDenied` for both the audit event AND the deletion-log line.

Doc amendments (`f95a624`):
- Plan Event Kinds table: paragraph after the table authorizes optional `phase_completed.Details` extensions (`skipped:true` for no-op phases, `gate_blocked:true` + `failed_count:int` for T3 gate, phase-specific counters).
- Spec section 4: deletion-log.ndjson line schema documented with the full field list (v, path, status, attempted_at, optional errno + error).
- Plan Task 29 entry: per-phase preconditions spelled out (one Signature per Candidate at T2/T3; Signatures must contain every RelativePath in the verified subset of Candidates at T4); also consumes T2Result.RsyncLogPath + T4Result.DeletionLogPath for the support-bundle path list.

Implementer decisions worth noting (all reviewed and approved):
- Lstat failure (ENOENT or EACCES) classified as `failed_permission` — fail-safe to denied rather than continuing. Reviewer flagged the ENOENT-as-permission conflation as a minor; not a data-loss risk, just wire-string clarity.
- Missing baseline signature classified as `skipped_mutated` — fail-closed; matches the sibling pattern in T2.
- Gate fire emits `phase_completed{gate_blocked:true}` not `phase_aborted` — gate is a protective outcome, not a phase failure. Locked by the new plan amendment.

Task 26 commits: `e14181b` (impl), `11aab3f` (review fixes + gosec G306 rescue), `f95a624` (doc amendments).

### 2026-06-04 (later): Task 25 (T2 hash+compare + manifest write)

Task 25 (`internal/runner/t3_hash_compare.go`, phase T2) dispatched per the revised protocol. Implementer commit `a9a3ec5`; 18 tests, runner package coverage 89.6%. Review verdict: **approve** with minor cleanups only. CI green first try (the `gofmt -s` lesson from Task 24 was applied at dispatch time).

Per-file inner loop locked: `os.Lstat` source for mutation gate (size + mtime_ns from T0+ Signature; if changed → status `source_mutated`, skip both hashes); open + StreamSHA256 source; open + StreamSHA256 dest; classify into one of {verified, hash_mismatch, source_mutated, not_transferred, source_unreadable, dest_unreadable}; emit per-file event (file_completed | hash_mismatch | source_mutated); append manifest entry with HMAC via length-prefixed canonical encoding (invariant #33); emit UIEvtFileCompleted. Per-file errors are NOT fatal; they are recorded as FileStatus and the loop continues. Audit AND manifest Append failures ARE fatal.

`T3Result.PerFileStatus` (`map[string]state.FileStatus` keyed by RelativePath) is consumable by Task 26 (atomic gate decision: `FilesVerified == FilesTotal`).

Review-polish commit `6815725` applied:
- CancelledMidLoop now asserts at least one per-file event landed (locks the "partial files processed" claim).
- RendererErrorIsNonFatal now asserts the broken renderer was called 3 times (locks renderer is exercised, not bypassed on first error).
- SourceMutated also asserts the sibling stable.txt classifies as verified (locks: mutation on one file doesn't poison classification of others).
- New EmptyCandidates test locks the zero-file contract (phase_started + phase_completed, no per-file events).

Deferred (not blocking Task 26): runT3Abort at 0% coverage (cadenced-cancel dead code), asymmetric phase_aborted policy on manifest-Append failure (audit store is healthy in that branch so phase_aborted COULD be emitted), T3Input.Mode declared but unread (forward-compat).

Task 25 commits: `a9a3ec5` (impl), `6815725` (review polish).

### 2026-06-04 (later): Task 24 + Task 24 review fixes

Task 24 (`internal/runner/t2_transfer.go`, phase T1) dispatched per the revised protocol. Implementer commit `7dcce88` shipped but CI failed at the `Lint` step on `gofmt -s` drift. The bare `gofmt -l` check the implementer ran did not catch doc-comment list-indent shapes that `gofmt -s` (the simplifier; what golangci-lint enforces) rewrites. Caught and fixed in `b7026eb`. Lesson added to memory: local pre-commit must run `gofmt -s -l`.

Combined review (`b7026eb` for fixes):
- gofmt -s drift on both files (the CI blocker).
- UI stranded on `transfer_started.Append` failure: the audit-fail path returned without emitting `UIEvtPhaseCompleted(aborted)`, leaving a TUI renderer stuck on the "T1 started" frame. Now emits the abort UIEvent before returning, matching the t1_enumerate.go mid-stream pattern. Memory updated with the principle.
- `command_line` drift trap: `rsyncCommandLine` duplicated the argv-construction logic from internal/rsync's unexported `buildArgs`. Fixed by exporting `rsync.BuildArgs` (renamed from `buildArgs`) and rewriting `rsyncCommandLine` to call it. Single source of truth. Plus a lockstep test in `TestRunT2Transfer_HappyPath` asserting the audit's command_line is byte-equal to a fresh `rsyncCommandLine(expectedOpts)`.

Two minor follow-ups from the review queued as latent (not blocking):
- Empty-Candidates audit semantics: code currently emits `transfer_started`/`transfer_completed` even when rsync is not invoked. Either tighten the canonical Event Kinds table to allow this OR change the code to skip the transfer_* pair. Pick one in a future amendment.
- UIEvtProgress throttling: `internal/runner/types/types.go` doc-comment promises "one progress tick per 200ms" but the current T1 emits one per ProgressTransferring event. Add a throttle for v0.2; not blocking v0.1.

Task 24 commits: `7dcce88` (impl), `b7026eb` (review fixes + gofmt rescue + BuildArgs export).

### 2026-06-04 (later): Task 23 + spec/plan amendments

Task 23 (`internal/runner/t1_enumerate.go`, phase T0+) dispatched per the revised protocol. Implementer reported runner package coverage at 92.9% with 13 tests. Commit `de5435b`.

Combined review (`a1a75fd` for fixes; `af39928` for doc amendments):
- Two em-dashes (U+2014) in `t1_enumerate.go` violating global em-dash discipline; replaced with sentence breaks.
- `Exported via the const` comment was misleading (lowercase = unexported); reworded.
- `TestRunT1Enumerate_CancelledMidEnumeration` had a comment promising assertions that weren't there. While adding them, discovered a subtle design point: cancellation in this test actually exercises the mid-stream audit-failure branch (NOT the cadenced-cancel branch), because the next EventStore.Append after cancellation returns ctx.Err from NDJSON's entry guard BEFORE the cadenced check at i%256==0 fires. Mid-stream audit-failure branch deliberately skips emitting `phase_aborted` (re-Appending to a just-failed store could compound the error). The test now asserts the only true invariant: `phase_completed` is absent. Updated the test comment to document the two abort paths.
- Side observation: the cadenced ctx-check is largely dead code under the current NDJSON EventStore. Filed as a low-priority cleanup item in memory.

Doc amendments (`af39928`):
- Plan Event Kinds table: explicit paragraph that `phase_aborted` is best-effort, skipped when the audit store is the failure mode. Recovery rule documented.
- Spec section 3 row T0+: footnote that audit-store failure mid-phase may terminate events.ndjson without a closing event; missing closing event is the crashed signal per invariant #10.

Task 23 commits: `de5435b` (impl), `a1a75fd` (review fixes), `af39928` (doc amendments).

### 2026-06-04 (later): Task 22 + cleanup of two deferred follow-ups

After the CI-rescue + Task 21 work, dispatched Task 22 (`internal/runner/t0_preflight.go`) per the revised dispatch protocol (implementer runs `go vet && make lint && go test -race && make coverage` locally before commit). Implementer reported runner package at 89.8% coverage with 12 tests; review classified as minor-fixes-needed with no critical or important code-level issues; two important plan-level drifts surfaced.

Cleaned up both deferred follow-ups MM had asked when they'd be done:

1. **Spec invariant #11** (commit `bde644f`): patched from pre-refinement "assume current, warn, rewrite" to the locked FAIL-CLOSED behavior matching `internal/state/version.go`. Cited the Plan 1 multi-hat security review as the source of the refinement.
2. **Spec section 3 row T0 / T0+** (commit `76bb66f`): reconciled the "started line ownership" drift in favor of T0 (matches plan + code). Updated both T0 row (now lists all preflight gates by invariant + explicit "started line at end of T0 success") and T0+ row (started-line moved out; on-crash text references `crashed_resumed` orphan finalization).
3. **Lock subpackage coverage** (commit `4403db8`): added 4 targeted tests (`TestRelease_AfterExternalUnlink`, `TestFallbackHostname`, `TestProcessStartTimeUnix_DeadPID`, `TestAcquire_ParentDirMissing`). 75.4% → 80.5%; preflight tree 83.0% → 84.6%. Remaining sub-80% spots (`finishAcquire`, `Release` switch branches) need DI for failure injection; deferred to a future testutil pass.

Task 22 review also surfaced 5 unowned T0-domain event Kinds in the canonical Event Kinds table (`lock_acquired`, `lock_stale_detected`, `lock_contention`, `filesystem_refused`, `volume_uuid_changed`). Queued as Task 22a (commit `edaf193`); design option (b) selected: keep preflight gates pure, extend `PreflightContext` with snapshots + typed errors, runner translates to events post-hoc.

Commits this segment (in order): `b31e271` (Task 22 impl), `bde644f` (spec #11), `1b10454` (Task 22 review fixes), `76bb66f` (spec section 3), `edaf193` (plan: Task 22a), `4403db8` (lock tests).

### 2026-06-04: Resume session - Tasks 19, 20 reviewed + Task 21 implemented + CI rescue

Context: previous session ended having pushed Tasks 19 (volume_uuid + drives.Query promotion) and 20 (preflight integrate) implementer-only, deferring combined reviews. The BACKLOG claimed CI was green through Task 18.

Reality on resume diverged:
- CI was actually RED on the last 10 commits (Tasks 12 through 20 plus the session-end docs commit). The screenshot MM shared mid-session showed "test Failed" on every run.
- Root causes (compounding):
  1. golangci-lint errcheck on `defer h.Release()` and `defer pc.Release()` (5 sites flagged, 8 sites real after fix; max-same-issues default truncated the report).
  2. gosec G115 on `uint64(stat.Dev)` in symlink.go (false-positive on bounded device-id ABI).
  3. gosec G204 on `/bin/ps` exec.Command and `rsync` exec.CommandContext (false-positives on absolute path + SHA256-verified path).
  4. Coverage gate failing on preflight at 79.7%: `VerifyVolumeUnchanged` at 55.6%, four uncovered error branches.
  5. After fix #4, coverage gate itself had a latent bug: it took only the first subpackage's percentage (alphabetical), masking lock at 75.4% under codesign at 92%.
- Each fix exposed the next; six commits to reach honest green CI.

Commits this session (in order):
- `779a43e fix(lint): clear errcheck on defer Release; gosec G115/G204 on bounded inputs` — Action 0
- `1c4085e fix(preflight/volume_uuid): Task 19 review fixes (doc typo, LC_ALL=C)` — Action 1 (Task 19 review applied)
- `f552cbf fix(preflight): Task 20 review fixes + coverage rescue to 84.8%` — Action 2 (Task 20 review applied + 4 new VerifyVolumeUnchanged tests)
- `2cf71c1 docs(plan): reconcile PreflightContext API Contracts with Task 20 code` — Action 3
- `7fda0e1 feat(runner): add runner types (Phase, Mode, UIEvent, RunOptions, RunResult) [Task 21]` — Action 4 implementer
- `d941105 fix(runner/types): Task 21 review fixes` (added 5 ExitStatus constants, state diagram T1-fail-through-T2 correction, signal-handler 5s clarification, 2 new tests)
- `e0f003d docs(plan): relocate Renderer interface to runner/types; expand ExitStatus list` (plan amendment per Task 21 review finding)
- `b17dc22 fix(coverage): tree-weighted gate; handle no-statement packages` (Makefile honesty fix; new gate aggregates via -coverprofile + go tool cover -func)

Key design corrections that landed in this session:
- `PreflightContext` reconciled in the plan with the actual code shape (`VolumeUUID *volume_uuid.Captured` not `string`; `RsyncPath` not `EmbeddedRsyncPath`; `SymlinkBaseline`, `Filesystem`, `DotDir` added; `Options` struct with `SkipCodesign` test escape; `Release()` method).
- `Renderer` interface relocated from `internal/plain` to `internal/runner/types` — the import-cycle fix. Plan's API Contracts updated; internal/plain (Task 33) now implements `runner.Renderer`.
- ExitStatus constants now exported from `runner/types` (5 values matching spec section 5; `crashed_resumed` was missing from the plan but present in the spec).
- Coverage gate now reports honest statement-weighted totals across the whole package tree; old gate had been silently masking under-80% subpackages.

Follow-ups queued (not blocking):
- Spec invariant #11 still uses pre-refinement "assume current, warn, rewrite" wording for version.json corruption; refined intent (FAIL-CLOSED, never silently re-init) lives in `internal/state/version.go:44-46`. Spec-doc patch needed.
- T1-T2 fail-flow narrative in spec section 3 / Task 20 review finding 1.2: spec table predates codesign + volume_uuid invariants; could be tightened.
- Lock subpackage at 75.4% is below 80% (was masked by old gate). Tree-weighted preflight is 83% so the gate passes, but lock specifically would benefit from more error-path tests (T15 review noted some already; a few more would help).

### 2026-06-04: Tasks 11-20 executed (integration phase complete)
- Switched from sequential to overlap-CI mode mid-session: dispatch task N+1 implementer in parallel with task N review subagent. CI runs in background. Saved 1-2 min per task.
- Task 11 (selection): NFC canonicalization + symlink-not-followed + duplicate-NFC rejection. 84.6% coverage. One em-dash review fix.
- Task 12 (rsync embed): placeholder shell script + SHA256-keyed extract path + chflags uchg. Task 12a (build-rsync.sh real implementation) still stubbed.
- Task 13 (rsync wrapper): --from0 --files-from=- stdin pipe; review caught missing spec-mandated --prefix filename test (now added in TestFileListBytes_DoubleDashPrefixSurvives).
- Task 14 (rsync progress parser): hand-authored golden file at `testdata/rsync-3.4.1-progress.golden`; contract test pins event counts per invariant #43. Will need regen once Task 12a embeds real rsync.
- Task 15 (preflight/lock): strong stale detection. Review caught 4 issues: missing age in HeldLockError, LC_ALL=C on ps invocation, missing recycled-PID test, doc.go call-order. All applied. Plan API Contracts updated: `Acquire(ctx, path, volumeUUID)` instead of `AcquireLock(ctx, path)`.
- Task 16 (preflight/filesystem): syscall.Statfs + exFAT/msdos/unknown rejection + noexec rejection + reformat recipe. MNT_* constants inlined (4 values, BSD ABI stable).
- Task 17 (preflight/symlink): (dev,ino) baseline per path component + Verify for phase-boundary cross-checks. Caught my prompt bug (`%w` in `fmt.Sprintf` is invalid). `realTempDir` helper resolves macOS /var symlink.
- Task 18 (preflight/codesign): `codesign --verify --strict` self-check; ldflags `IsReleaseBuild=true` for release switch. Makefile updated with LDFLAGS_RELEASE / LDFLAGS_FAULTINJECT.
- Task 19 (preflight/volume_uuid): promoted `drives.queryVolume` to public `drives.Query`. Capture + Verify on VolumeUUID, complementary to symlink's dev/ino check.
- Task 20 (preflight integrate): 9-gate composition with rollback on partial failure. PreflightContext stored by runner. Tests use hdiutil-mounted APFS DMG since `t.TempDir()` under /var/folders can't return a VolumeUUID via diskutil.
- CI fixes during phase: Makefile guards for not-yet-existing dirs (cmd/, test/e2e/), coverage gate switched to real per-package `go test -cover` (was buggy per-function average), gosec G306 on test fixtures dropped to 0600, errcheck on defer chains migrated to t.Cleanup.

### 2026-06-04: Tasks 1-10 executed via subagent-driven development
- Mode A locked (rigorous: implementer + spec review + code quality review per task). MM preference saved to memory `feedback_quality_over_speed.md`.
- Task 1 (bootstrap) done manually (pure boilerplate).
- Tasks 2-10 each: implementer subagent + combined review subagent + targeted fix patches.
- Multiple CI fixes landed during foundation execution: 4 code-review findings on Task 2 tooling config, gosec G115 nolint comments, errcheck `defer` cleanup, Makefile guards for not-yet-existing directories (cmd/, test/e2e/), coverage-gate correctness fix (statement-weighted via `go test -cover` instead of buggy per-function average), gosec G306 on test-fixture file modes, go.mod bump from 1.22 to 1.23 forced by `rapid v1.3.0`.
- Tools installed: Go 1.26.4 via Homebrew, GitHub CLI auth confirmed for maheshmirchandani.
- Repo conventions: GPLv3 LICENSE, Conventional Commits, error-wrap `<verb> <noun>: %w`, file mode 0600 for HMAC key + 0700 for `.flashbackup/` dir, atomic write-then-rename helper at `state.WriteTmpThenRename`.
- Coverage discipline: real statement-weighted gate per package; failing if any safety-critical package drops below 80% line.

### 2026-06-03: Plan 1 multi-hat review + amendments applied
- Spawned 9 hats in parallel: Senior Go Developer, Hacker / Pen Tester, CISO, DevOps/SRE, Senior QA, DX (Developer Experience), Code Maintainability + Archaeologist, Performance Engineer, Subagent-Execution Reviewer (meta).
- ~90 findings: 15 critical, ~50 important, ~25 deferable.
- 3 strategic decisions surfaced + 1 hidden 4th from skeptical Tech Lead pass: PS1 (HMAC = integrity checksum, length-prefixed, honest scope), PS2 (e2e split into fast/safety), PS3 (Renderer.OnEvent event-bus shape), PS4 (runner.UIEvent distinct from state.Event).
- MM approved all 4 strategic decisions with refinements.
- 12 critical mechanical fixes + 28 important fixes applied; 25 items deferred to Plan 2 / v0.2 with explicit recording.
- Spec amendments: invariant #33 reworded (HMAC = integrity checksum, not authentication), AC-19 added (manifest tamper-rejection in verify), AC-to-code traceability table updated.
- Plan amendments: new 200-line "API Contracts, Conventions, and Cross-Task Anchors" section pins all cross-task types/interfaces + canonical Event Kinds + fault-injection DSL + status --json schema; Tasks 4-9 annotated with critical fixes (length-prefixed HMAC canonical, fail-closed version.json, strict glob allowlist, sync.Pool buffer, Checkpoint API, context.Context plumbing); Task 2 Makefile + CI workflow rewritten with e2e split + symbol-scan + coverage gates + SOURCE_DATE_EPOCH; Tasks 10-55 expanded with concrete implementations + new Tasks 12a (rsync build script), 42a (fixture creation), 51a (AC-19 test), 51b (missing fault hooks).
- Plan grew from 1887 to 2477 lines; spec grew from 979 to 983 lines.
- Em-dash discipline maintained.

### 2026-06-03: Plan 1 (core engine + minimal CLI) saved
- Decomposition picked: 2 plans (Plan 1 = core engine + CLI; Plan 2 = TUI + packaging + docs).
- Plan 1 saved to `docs/planning/2026-06-03-flashbackup-core-engine.md`, 1887 lines.
- Tasks 1-9 fully expanded with TDD steps + complete code (~700 lines of plan content + code blocks).
- Tasks 10-55 named and scope-bounded with referenced invariants/ACs (~250 lines).
- Self-review notes captured at end: spec coverage all 18 ACs + 58 invariants except TUI-specific and full-release-pipeline items.
- Pending: execution choice (subagent-driven vs inline). Tasks 10-55 expanded at execution time.

### 2026-06-03: Full 11-hat multi-hat review + amendments applied
- Spawned 11 subagent hats in parallel: Senior Product Owner / BA, Multi-Hat Critique (CTO+CIO+CFO+Founder+Investor bundled), End User, Senior Security Engineer, SRE / DevOps Engineer, Senior QA / Test Engineer, Performance Engineer, OSS Maintainer + License Compliance + Sustainability, Technical Writer, Code Maintainability + Contributor DX, Accessibility Specialist (TUI-scope).
- 11 reviews returned ~80 findings total (49 critical, ~30 important).
- Consolidated into 7 strategic decisions + 30 mechanical additions + 12 deferred items.
- MM approved all amendments in one go (option A: accept all leans).
- Strategic decisions locked: encryption-at-rest documented-not-required; notarization kept; manual Finder restore; hypotheses downgraded to qualitative signals (no-telemetry constraint makes statistical measurement impossible); phase rollout plan (dogfood 2wk, trusted 1mo, broader 6mo); docs as v0.1 done-criteria; timeline re-baselined 4-6 weeks to 3-4 months.
- 30 new invariants #29-#58 added across 6 themes: security hardening (5), CI/build/OSS compliance (6), test strategy expansion (5), repo structure & DX (4), accessibility (3), OSS governance (3), performance reality (4).
- 12 deferred items recorded for v0.2+: audio bell, mouse support, increase-contrast, i18n, parallel T2 hashing, recent-files render cap, glossary subcommand, .superpowers gitattribute, generic Appender, dev-beta soak, --license subcommand, runs.ndjson rotation.
- New spec sections added: Personas, Distribution and enrollment model, Stakeholder map, Phase rollout plan, Repository layout, Build commands (Makefile), Documentation deliverables, Encryption at rest, Restore (manual), Operational runbook, Bus factor and succession, Deferred items, AC-to-code traceability table.
- Spec grew from 713 to 979 lines (34 H2 sections, 58 invariants, 18 ACs, em-dash-free).

### 2026-06-03: Spec-discipline retrofit + self-review complete
- Retrofitted design spec with the 9 missing sections from the 12-section spec-discipline framework: Hypotheses, Build-vs-buy expansion, Cost projections, Hard ethical rules, Behavioral design principles, Trust signals, Cheap-now-expensive-later, Audit storage abstraction, SLOs/incident classification, Acceptance criteria.
- 18 acceptance criteria in Given/When/Then form added (AC-1 through AC-18).
- Spec grew from 472 to 713 lines; all 12 spec-discipline sections now present.
- Self-review pass found 3 minor ambiguities; fixed inline: phantom "mirror-layout invariant" reference, unclear "user pool" scope, missing mode/exit_status enum enumeration in schema.
- Em-dash discipline maintained throughout.

### 2026-06-03: Section 9 (packaging and distribution) locked
- Build pipeline: GitHub Actions, universal2 lipo, embedded GNU rsync 3.x built from pinned source in CI.
- Code signing: Developer ID Application cert + `--options runtime` + `--timestamp`.
- Notarization: `xcrun notarytool submit --wait` + `xcrun stapler staple`.
- Distribution: GitHub Releases with binary + zip + SHA256SUMS.
- Versioning: semver, pre-1.0 allows breaking changes between minor versions.
- GPLv3 compliance: written-offer route, `THIRD_PARTY_LICENSES.md`, `scripts/build-rsync.sh`.
- No auto-update mechanism in v0.1.
- `init` does NOT reformat USB; refuses with printed `diskutil eraseDisk` recipe.

### 2026-06-03: Section 8 (testing strategy) locked
- Test pyramid: unit / integration / e2e / fault-injection / TUI snapshot / cross-version.
- Fault injection via `//go:build faultinject` (release-stripped).
- 8 fault-injection hook contracts: kill/corrupt/mutate-source/unmount/disk-full at named phases.
- Cross-macOS-version CI matrix: 13, 14, 15, 16/anticipated.
- Benchmark scaffolding with 15% regression guard.
- Filesystem fixtures: tiny/realistic/pathological/huge.
- TUI-specific tests added per UX hat review: snapshot (`teatest`), resize, path sanitization, SSH+tmux, status --json.
- Coverage targets: 80% line minimum, 90% for runner/state/hash/preflight.

### 2026-06-03: Hat review round 3 (UX-focused, after Section 7)
- Five hats: UX, End User, Hacker, QA, DevOps.
- 5 majors locked as invariants #24-28: color+icon pairing, 80x24 minimum size refusal, empty states designed, path sanitization, non-TTY fallback.
- Smaller items folded into spec: mode-toggle keyboard mapping, dry-run preview design, profile editor design, headroom warning, TUI snapshot tests, resize tests, path-sanitization tests, SSH+tmux matrix, status --json.

### 2026-06-03: Section 7 (TUI shape) locked
- Main screen: C (two-pane: persistent left menu + right context).
- Backup wizard: A (linear 4-step: profile → confirm paths → mode + options → review).
- Progress screen: A (compact metrics + recent files; phase indicator in header).
- Move-mode confirmation modal: A (minimal centered modal; type DELETE).
- Run summary: B (detailed inline; status breakdown + failed-files list).
- History: list view with hotkeys (Enter open, V verify, F filter, / search).
- Verify wizard: two-step (pick scope → live progress).
- Profiles: list + inline editor (N new, Enter edit, D delete, V validate).

### 2026-06-03: File management housekeeping
- Created `docs/specs/` and `docs/archive/` directories.
- Relocated PRD content to `docs/specs/2026-06-03-1338-flashbackup-prd.md` with frontmatter.
- Archived the original `Portable macOS Backup Utility.txt` to `docs/archive/2026-06-03-portable-macos-backup-utility-original.txt`.
- Created working design spec at `docs/specs/2026-06-03-1532-flashbackup-design.md`.
- Created this `BACKLOG.md`.

### 2026-06-03: Section 7 (TUI shape) start
- Visual companion server launched at `http://localhost:55174`.
- Main screen layout question: 3 options (wizard-first, dashboard-first, two-pane). MM picked **C** (two-pane).
- Backup wizard flow question: 2 options (linear 4-step, single-form). MM picked **A** (linear).
- Progress screen question: 3 options (compact metrics, multi-phase stack, logs-first). **Pending pick.**
- Global preference added to `~/.claude/CLAUDE.md`: auto-activate visual companion in brainstorming sessions, don't ask.

### 2026-06-03: Section 6 (error handling and failure modes) locked
- Reference tables for each phase: trigger / message / state / recovery.
- Error message principles: three-part structure (what / where / next step), full paths, errno-mapped messages, raw error in events.ndjson.
- Signal handler contract: SIGINT/SIGTERM with phase-specific cancellation; second signal forces exit.
- Sleep prevention: spawn under `caffeinate -i`.
- Explicit non-goals listed (cosmic-ray, TOCTOU-after-verify, network FS as source).

### 2026-06-03: Section 5 (verify command) locked
- State machine: PREFLIGHT → LOAD MANIFEST → RE-HASH DEST → SUMMARIZE → WRITE RECORD.
- CLI: `flashbackup verify [<run-id> | --all | --check-extras]`.
- No `--quick` mode in v0.1 (non-goal).
- Extra dest files counted silently, listed with `--check-extras`.
- Exit codes 0 / 1 / 2.
- Rejects manifest lines with `v != 1`.

### 2026-06-03: Section 4 (move semantics deep dive) locked
- Atomic gate semantics: any T2 non-verified file → zero deletions.
- Per-file mutation re-stat at T3.
- Unlink (permanent), not Trash.
- Empty source directories left in place.
- Upfront `Type DELETE` confirmation prompt. No CLI flag to skip.
- Move-mode manifest fields: `deletion_status` field added per line.
- Crash recovery: `deletion-log.ndjson` appended per unlink with fsync; orphaned-run detection on next preflight.

### 2026-06-03: Hat review round 2 (after Sections 1-3)
- Five hats: CTO, Enterprise Architect, DevOps/SRE, QA Engineer, End User.
- 17 findings; 2 strategic (Apple Developer Program membership, ship-v0.1-copy-only); 15 mechanical-or-lockable.
- Item 2 (notarization): **locked YES.** Apple Developer Program account in scope.
- Item 3 (copy-only v0.1): **locked NO.** Ship full copy + move in v0.1 per MM's preference for completeness over staged rollout.
- Items 1, 4-9, 11, 15 locked as-recommended: drop in-binary migration, split runner/preflight, shared paths package, collapse manifest+runlog into state, events.ndjson, stronger lock semantics, status subcommand, fault-injection hooks, init subcommand.

### 2026-06-03: Section 3 (backup-run data flow) locked
- State machine: T0 (preflight) → T0+ (enumerate) → T1 (transfer) → T2 (hash+compare) → T3 (delete-source, move only) → T4 (finalize).
- rsync invoked once over full file list, not per file.
- T2 hashes source AND dest per file, classifies status (verified / hash_mismatch / source_mutated / not_transferred).
- T3 atomic gate: any non-verified file blocks all deletion.
- Two-line `runs.ndjson` model: "started" at T0+, "finished" at T4.
- Per-phase crash recovery semantics tabulated.

### 2026-06-03: Section 2 drawback analysis ("think deep")
- 12 drawbacks raised across major / moderate / minor.
- 4 major drawbacks locked as recommended: APFS/HFS+ required (refuse exFAT), auto-namespace by hostname-username, track FB-written paths for `--delete` protection, concurrency lock with stale-PID detection.
- 5 moderate drawbacks folded into spec: gzip manifests, UTC run IDs, runs.ndjson torn-write recovery, version.json corruption recovery, chmod 555 on binary.
- 3 minor drawbacks noted but not acted on (sparse file preflight inaccuracy, birthday paradox on hex IDs, third-party cleaners eating hidden files).

### 2026-06-03: Section 2 (on-disk layout) initial proposal
- Layout: binary at root, `.flashbackup/` for state, mirrored user files.
- `.metadata_never_index` to suppress Spotlight indexing.
- Manifest line schema with `sha256_source` field.
- Run summary line schema.
- Retention default: 10 runs.

### 2026-06-03: Hat review round 1 (after Section 1)
- Two hats: Hacker, Pre-mortem.
- 3 showstoppers identified:
  - Hash dest-only doesn't detect transfer corruption. Fix: hash source + hash dest + compare; manifest stores source hash.
  - Move-mode race deletes user's mid-run edits. Fix: re-stat source at delete-time.
  - Q1 underestimated notarization friction (Gatekeeper warnings on signed-but-not-notarized binaries downloaded from internet). Walked back; notarization in scope.
- Lower-severity flags: manifest accumulation (need retention), schema versioning, verify-is-slow disclaimer, embedded-rsync TOCTOU (out of threat model).

### 2026-06-03: Section 1 (architecture overview) initial proposal
- Single Go binary, universal2 lipo, statically linked.
- Embedded GNU rsync 3.x extracted to `.flashbackup/bin/rsync`.
- 10 internal packages enumerated.
- "First run extracts rsync" mechanism.

### 2026-06-03: Architecture options proposed and Option A locked
- 3 options: Go+Bubble Tea+embedded rsync (A), Rust+Ratatui+native copy (B), Go+Bubble Tea+native copy (C).
- Recommended A: rsync's 20-year accumulated bug-fix history on macOS edge cases (HFS+ Unicode, AppleDouble, sparse files, symlinks, hard links, ACLs, APFS clones) is too valuable to give up.
- Walked back openrsync recommendation: openrsync lacks `--partial` (resume), `--xattrs` (macOS metadata), `--progress` (TUI integration), `--sparse`, `--hard-links`. Embedded GNU rsync 3.x chosen instead. GPLv3 licensing is not an obstacle for the open-source wedge.
- MM confirmed Option A with corrected rsync choice.

### 2026-06-03: Clarifying questions Q1 to Q5 answered
1. **Wedge:** B (inner circle, 5 to 20 known users, signed binary, no notarization burden initially → revised to include notarization in round 2).
2. **UX shape:** B (TUI, Bubble Tea or Ratatui).
3. **Validation contract:** D (strict + atomic + manifest + separate `verify` subcommand).
4. **Run-to-run semantics:** C (mirror at destination + append-only manifest log).
5. **Filter UX:** C (presets + include/exclude + saved profiles on USB).

### 2026-06-03: Project initialized
- Repo discovered: only artifact is `Portable macOS Backup Utility.txt` (PRD).
- `CLAUDE.md` created at repo root via `/init`. Establishes project status (pre-implementation), source-of-truth pointer to PRD, non-negotiable constraints from PRD §4-§11, Superpowers workflow expectation.
- Brainstorming session started.
- Visual companion offered initially; later set to auto-activate per global preference.
