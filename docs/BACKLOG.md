# FlashBackup Backlog

> Rolling log of design decisions, open items, and historical context for the FlashBackup project. Updated as the project evolves. Lives at `docs/BACKLOG.md`.

## Project status (2026-06-06 evening): Task 12a spec FINAL; plan v1 reviewed; plan v2 rewrite pending

**Phase:** Plan 1.5 prep. Task 12a brainstorm + 3-round spec review + plan v1 + plan-review all shipped this session. **Plan v2 NOT YET WRITTEN.** That's the next-session pickup.

**Spec status (LOCKED):** `docs/specs/2026-06-06-1839-task-12a-embedded-rsync-build-pipeline-design.md`. Commits `f35a7ef` (v3 post-round-2-review) + `418d8e2` (GitHub release URL format fix). Two multi-hat review rounds completed (5 hats round 1; 6 hats round 2 including fresh Tech Lead).

**Plan v1 status (HAS KNOWN BUGS):** `docs/planning/2026-06-06-1917-task-12a-embedded-rsync-build-pipeline.md`. Commit `a77c95b`. 12 tasks, 1660 lines. Plan-review (4 hats: Senior Dev + DevOps + QA + CISO/Hacker re-check) found 14 Critical + 20 Important + 15 Minor. Cross-cutting Critical: helper signature mismatches with actual `test/e2e/helpers.go` API (BuildBinary, RunBackup, RunInit, SeedProfile, paths.Namespaced, repoRoot, t.Context) — plan v1 generated pseudo-code without reading the actual files.

**Plan v2 scope (next-session pickup):** rewrite Tasks 5+6 using actual helper signatures (verified by reading code); fix workflow ordering (Task 9 PR-gate + dispatch-before-merge); harden security gates (parse-don't-source regex strengthening, pre-commit grep for `<PINNED_SHA` literals, `gh api` environment verification); fix mkfixtures.sh nondeterminism (no `$(date +%s)`, no `$(whoami)` baked into fixture content); standardize pre-commit gate language (run gates for ALL commits); split Task 1 Step 1.4 into 5 sub-steps; misc Important findings. Memory file has the full list at `project_execution_state.md`.

**Spec change folded in via 418d8e2:** GitHub release tag renamed from `upstream-mirror/rsync-3.4.1` (slash, ambiguous URL parsing) to `upstream-mirror-rsync-3.4.1` (single-segment hyphen form). PRIMARY_URL now correctly composes `.../releases/download/<tag>/<filename>`. Bootstrap procedure tightened with second-network re-verify + post-upload SHA cross-check.

**Tasks queued for 2026-06-12 (before Phase 0 gate close on 2026-06-19):** Task 12c (CVE posture stub), Task 12d (release + rollback + rsync-version-bump runbooks). Tracker dates land in spec §9.1 + slip-indicators surface to MM if missed.

**Phase 0 dogfood remains paused** until plan v2 lands and Task 12a actually ships a working `make build-real-rsync`. v0.1.0-core binary still cannot back up data on a clean install; env override via `FLASHBACKUP_RSYNC_PATH_FOR_TEST` continues to be the dogfood workaround for engine validation.

## Older project status (2026-06-05 evening): Phase 0 dogfood session 1 surfaced Task 12a as the blocker

**Phase:** Plan 1.5 prep. Phase 0 dogfood session 1 (1920 to 2030 local) ran on MM's M1 Max + ROCKET-2TB USB. **First real backup revealed the embedded rsync is the Task 12a placeholder stub:** the binary at `internal/rsync/bin/rsync.placeholder` prints "PLACEHOLDER rsync; awaiting Task 12a build" and exits 0. Engine reports T1 OK because rsync exit-code-only check passes; T2 hash-compare tags all files `not_transferred`; final exit `partial`. **No data loss** (copy mode; would have been blocked by atomic gate in move mode anyway). CI never caught this because every e2e test sets `FLASHBACKUP_RSYNC_PATH_FOR_TEST=/opt/homebrew/bin/rsync`; the placeholder code path is not exercised end-to-end.

**Plain statement (per global "outcome over architecture"):** v0.1.0-core ships a binary that cannot back up data on a clean install. Engine-correct, build-incomplete. Phase 0 gate cannot honestly close without Task 12a.

**Session 1 results (commit `54a1bb0`, dogfood log `docs/dogfood/2026-06-05-1920-phase-0-log.md`):**
- Backup #1 (placeholder rsync): 3 s, 0 bytes, exit `partial`. Surfaced the blocker.
- Backup #2 (env override): 6.6 s, 1.4 GB, 210/210 files, exit `ok`. First real working backup; MM's important docs are now backed up.
- Verify #1 (env override): 0.9 s, 210 files, 1.42 GB SHA256 rehashed, exit `ok`.
- Status: clean. Nit: free-space reports 0 B on APFS (likely `statfs.f_bfree` vs `f_bavail`).

**Other nits logged:**
- Verify renderer summary `details:` falls back to generic path instead of specific `summary.json`.
- Verify `summary.json` `duration_seconds` integer-truncates sub-second runs to 0.
- Test-pyramid gap: no e2e exercises the placeholder end-to-end; Task 12b candidate.

**Hybrid path agreed with MM:** dogfood continues only as engine validation via env override; real gate close requires Task 12a. **Next session: Task 12a brainstorm + spec + plan + implement.**

**Task 12a scope (next session):**
- Build GNU rsync 3.4.1 universal2 (arm64 + x86_64) from upstream source on macOS.
- `scripts/build-rsync.sh` is currently a stub; needs to: download release tarball, verify upstream signature, configure twice (arch-specific), make twice, `lipo -create`, verify with `file`, SHA256.
- Embedded SHA256 constant location: TBD (check `internal/rsync/`).
- Replace placeholder with the real binary at build time (or keep placeholder for dev builds and use a build tag for release).
- Apple notarization is Plan 2, not Task 12a.
- Companion Task 12b: e2e test that runs without the env override and either asserts the placeholder rejection path or (once 12a lands) the real-bytes-transferred path.

**Out of scope until Task 12a lands:** Plan 2 brainstorm; 50a + 51c + 22a + 29a + 50b + PA-1 + PA-2; retag of v0.1.0-core. v0.1.0-core tag stays as historical record of engine completeness.

## Older project status (2026-06-05): PLAN 1 COMPLETE; v0.1.0-core tagged at `b39a11c`; CI green at `27558c9`

**Phase:** Plan 1 DONE. Tasks 1-55 shipped; v0.1.0-core tag at commit `b39a11c` (tag object SHA `466dc65`), pushed to origin. Repo public. CI green at HEAD `27558c9` (post-tag gosec G204 nolint commit; test-file lint comments only; binary at `b39a11c` is functionally identical to `27558c9` since nolint comments don't affect compiled output).

**Known post-tag CI fix:** `27558c9 fix(e2e+lint): gosec G204 nolint comments for bounded test exec.Command sites` resolves CI without changing runtime behavior. Optional next-session action: retag v0.1.0-core to `27558c9` (requires `git push --force origin v0.1.0-core` — gated by auto-mode classifier; needs explicit user authorization). For Phase 0 dogfood with no external adopters yet, leaving the tag at `b39a11c` is the lower-risk default.

Runner + verify + plain renderer + cmd/flashbackup (init/backup/verify/status/profiles/help) + 12 e2e tests + ERROR_CATALOG + README + DOGFOOD all done. Move-mode atomic gate working end-to-end. faultinject DSL + release stub clean of symbols.

**Queued for Plan 2 (out of v0.1.0-core scope):**
- 22a: unowned T0 event Kinds wiring (lock_acquired, lock_stale_detected, lock_contention, filesystem_refused, volume_uuid_changed).
- 29a: PreflightContext test injection for TestRun_VolumeUUIDChangedMidRun.
- 50a: preflight orphan-recovery gate (synthesize crashed_resumed finished line for started-without-finished orphans).
- 50b: AC-13b rsync --partial resume e2e with >tiny fixture.
- 51c: --delete CLI flag + mirror-delete-DEST runner phase + FB-paths reconstruction.
- PA-1, PA-2: minor plan amendments from Task 53 review (delete_failed.errno optional; file_enumerated path Details placement).
- Plan 2 itself: TUI (Bubble Tea), signed + notarized release pipeline, full friend-facing docs.

## Older project status (2026-06-05, after Tasks 53 + 54: ONE TASK LEFT)

**Phase:** Plan 1 execution near end. Tasks 1-54 complete (56/58). Repo public. CI green. e2e + ERROR_CATALOG + README done. **Final task: 55 (v0.1.0-core tag completes Plan 1).**

**Queued architectural follow-ups (out of v0.1 scope):** 22a (unowned T0 event Kinds wiring) · 29a (PreflightContext test injection) · 50a (preflight orphan-recovery gate) · 50b (rsync --partial resume e2e with >tiny fixture) · 51c (--delete CLI flag + mirror-delete-DEST runner phase). Plus 2 minor plan amendments queued from Task 53 review: PA-1 (delete_failed.errno required→optional) and PA-2 (file_enumerated path Details vs top-level).

## Older status (2026-06-05, after Tasks 50-51 + Task 50 review I1/A1 + new Tasks 50a/50b/51c queued)

**Phase:** Plan 1 execution. Tasks 1-51 complete. Repo public. CI green. e2e tests: init + backup-happy + verify-intact + lock + non-tty + atomic-gate + mutation + crash-resume + delete-flag (AC-13a + AC-13b + AC-14 tests skip with documented architectural gaps; future-state assertions sit below the skip).

**Spec AC-13 split per Task 50 review A1:**
- AC-13a (orphan finalization → crashed_resumed): test exists, skipped pending Task 50a.
- AC-13b (rsync --partial resume): no test yet; queued as Task 50b (needs >tiny fixture).

**Three new tasks queued:**
- Task 50a (preflight orphan-recovery gate that finalizes started-without-finished as crashed_resumed).
- Task 50b (AC-13b rsync --partial resume e2e test with a larger fixture).
- Task 51c (--delete CLI flag + mirror-delete-DEST runner phase + FB-paths reconstruction).

Next: Task 51 review + Task 51a (e2e tampered manifest AC-19).

**Latent infrastructure debt** (tracked, not blocking):
- A1: hdiutil + APFS test helpers duplicated across 6 test files (preflight, runner×3, verify, cmd/flashbackup). Extract to `internal/testutil` before Task 38 (verify subcommand) makes copy #7.
- A2: `cmd/flashbackup/main.go` dispatcher now has 2 real-handler arms (init, backup) + 4 stub arms. Refactor `subcommandList` to carry a `handler` field before Task 38 adds the third real handler.

**CI architecture (commits `c3f2ca0`, `b297cc4`):** `.github/workflows/ci.yml` has `test-linux` on ubuntu-latest for portable packages (hash, state, profiles, paths, selection, runner/types, verify/*) plus lint + vet. macOS `test` job narrowed to macOS-only packages (drives, preflight subtree, rsync, runner). `bench` on ubuntu. `e2e-fast` + `e2e-safety` stay on macos-14 (need hdiutil + APFS mounts). Docs-only commits skip CI via `paths-ignore`. Public repo → unlimited Actions minutes across all runner OSes.

**Dispatch protocol (still in force):** Every implementer subagent runs `go vet && gofmt -s -l && go test -race && make coverage` locally before commit. Combined spec+quality review dispatched after each push; minor findings applied inline. Subagent-driven, overlap-CI cycle.

**Dispatch protocol amendment (2026-06-04, post-CI-rescue, still in force).** Every implementer subagent MUST run `go vet && gofmt -s -l && go test -race && make coverage` locally before committing. Bare `gofmt -l` accepts list-indent shapes the simplifier (`-s`) rewrites; CI's golangci-lint runs the -s variant, so local must too. Statement-weighted tree totals are the coverage gate.

**Local test sweep at halt time (verified 2026-06-05):**
`go test -race -count=1 ./...` and `go test -race -count=1 -tags faultinject ./...` both pass across all 17 packages. Coverage holds: runner 83.4%, hash 81.8%, state 83.0%, preflight 84.9%, verify/load 87.7%, verify/rehash 95.9%. All above the 80% gate.

**Tasks complete (50/58):**
1-10. Foundation (bootstrap, Makefile, paths, hash, state event/manifest/runlog/version, profiles, drives)
11-20. Integration (selection, rsync embed/wrapper/parser, preflight lock/filesystem/symlink/codesign/volume_uuid, preflight integrate)
21-22a. Runner types + T0 preflight + Task 22a queued for T0 unowned event Kinds
23-27. Runner phase functions: T0+ enumerate, T1 transfer, T2 hash+compare, T3 delete-source, T4 finalize
28. Fault-injection DSL + release stub (commit `6e958fe`; review fixes in `8461b0f`)
29. Top-level runner.Run state machine + faultinject hooks in t2/t3/t4 (commit `da24cd1`; review fixes in `06a4255`)
30. internal/verify/load manifest reader with inline HMAC verification (commit `14f73e0`; review minors in `12204ae`)
31. internal/verify/rehash per-file rehash + classify (commit `838faee`; review verdict approve, minor #1 wording polish applied inline)
32. internal/verify top-level Verify state machine (commit `8a5047b`; integrates load + rehash; writes per-verify summary.json; review fixes added results.ndjson + types.ExitStatus reuse in `09b6943`)
33. internal/plain renderer (commit `988ba49`; TTY + non-TTY modes; throttled progress; concurrency-safe; review fix M1 substituted real run-dir in summary block + plan A1 clarified types.Renderer in API Contracts)
34. cmd/flashbackup CLI entry point (commit `19a8573`; subcommand dispatch stubs; --version with ldflag-injected Version/RsyncVersion/CommitSHA/BuildEpoch + GPLv3 warranty disclaimer; second-signal-within-5s force-exit; 90.9% coverage; review fixes in `477e24a` for subcommand label off-by-one + spec amendment to "any second SIGINT or SIGTERM")
35. cmd/flashbackup init subcommand (commit `3644204`; AC-1 + AC-2; refuses exFAT with reformat recipe; refuses overwrite without `--reset-keys`; rsync.EnsureExtracted wired; 83.1% coverage; review approve with cosmetic doc-step renumbering applied)
36. cmd/flashbackup backup subcommand (commit `bf99233`; runs runner.Run end-to-end with plain renderer; `--move` gate refused with Task 37 redirect; ExitStatus → process exit code mapping; 80.8% coverage; first commit where verify-release gate has a real positive control; review verdict approve)
37. cmd/flashbackup backup move-mode DELETE confirmation modal (commit `7123a81`; replaces --move refusal with promptDeleteConfirm; renderer-driven UIEvtPrompt with cmd-composed warning text in ev.Status; case-sensitive exact match against "DELETE"; aborts on lowercase, typo, empty, trailing whitespace, EOF; 78.3% coverage; AC-7 + AC-8; review fixes amended spec section 4 + AC-7 + AC-8 + ExitStatusCopyOnlyAbortedDelete to reflect the pre-T0 gate architecture instead of the originally-specced post-T2 modal; M2 SIGINT comment tightened; M4 `deleteToken` const replaces dead `ev.Path` marker)
38. cmd/flashbackup verify subcommand + A1 testutil extraction + A2 dispatcher handler-field refactor (3 commits: `db6972a` testutil, `8dc7de3` handler-field, `4dfe3ca` verify; verify wires internal/verify.Verify with --all / --check-extras / explicit run-id; AC-9 + AC-10 + AC-19; 14 tests; cmd/flashbackup coverage 87.1%; review verdict approve with one important I1: renderer summary "details: see" line was wrong for verify — fixed by switching UIEvent.Path semantics from "run dir" to "exact artifact file path" so each producer names its own artifact)
39. cmd/flashbackup status subcommand with --json (commit `4f69943`; locked JSON schema per API Contracts; tabular plain text default; last_run from runs.ndjson scan; last_verify from scan-and-pick-newest by VerifiedAt; lock status via stat; 26 tests; cmd/flashbackup coverage 73.6%; review fixes: scrubbed 4 em-dash violations in status_helpers.go + status_test.go; plan amendment locks `last_run.profile` as omitempty)
40. cmd/flashbackup profiles subcommand (commit `9773705`; CRUD wrapper around internal/profiles.Store: list / new / edit / delete / validate; new + edit open $EDITOR or vim fallback on a temp JSON file; editorRunOverrideForTest seam for callback-based tests; 24 test functions / ~37 subtests; cmd/flashbackup coverage 80.2%; review verdict approve; CI rescued by gosec G204 nolint on exec.CommandContext for the operator-controlled EDITOR)
41. cmd/flashbackup help subcommand (commit `187ab5f`; constants-table `subcommandHelpTexts` maps subcommand name to detailed help text; empty-string key holds top-level usage; `printUsage` in main.go now pulls from the same table; 10 new tests including `TestHelp_AllSubcommandsHaveText` drift guard + `TestHelp_HelpTextHasNoEmDashes` content-level discipline check; 73.7% coverage; review verdict approve with cosmetic minors only; plan amendment A1 clarifies Task 41 ships verb-form `help <subcommand>` while existing handlers' fs.Usage stays untouched for v0.1)
42. test/e2e/ helpers package + Task 42a fixture trees (commit `9d7ee87`; SetupUSB / SeedSource / SeedProfile / RunBackup / RunVerify / RunStatus / RunProfiles / RunInit + Assert helpers; binary-build cache via sync.Once per flavour; `test/fixtures/{tiny,realistic,pathological}` with MANIFEST.txt per dir; review verdict approve with 3 deferrable minors)
43. test/e2e/init_test.go (commit `ecf5bc3`; 3 tests: HappyPath_APFS for AC-1, RefusesExFAT for AC-2, AlreadyInitialized_RefusesWithoutResetKeys; review approve with spec traceability table fix for AC-2 file path)
44. test/e2e/backup_happy_test.go (commit `eaeb49f`; AC-3; TestE2E_BackupHappy_CopyMode; discovers GNU rsync at /opt/homebrew/bin/rsync or /usr/local/bin/rsync via --version check rejecting openrsync; line-by-line JSON parsing for events.ndjson phase set assertions; namespaced-dest assertion via paths.Prefix; review approve with spec traceability fix)
45. test/e2e/verify_test.go (commit `9f97dd3`; AC-9 + AC-10; 4 tests: HappyPath, MissingFile, HashMismatch, LatestRun; review approve)
46. test/e2e/lock_test.go (commit `781c92f`; AC-11 + AC-12; HeldBlocksConcurrentBackup uses test's own PID with real start_time_unix + IOPlatformUUID for full liveness check; StaleLockBypassed uses reaped /usr/bin/true PID; lock_stale_detected event Kind assertion punted because the lock package silently recovers without emitting a phase event — maps to Task 22a)
47. test/e2e/non_tty_test.go (commit `230293b`; AC-15; review approve clean)
48. test/e2e/atomic_gate_test.go + cmd/flashbackup/inject_{faultinject,release}.go (commit `0193291`; AC-4; --inject CLI flag wired; phase=T2-pre; review I1 fixed: spec AC-4 narrative now says copy_only_aborted_delete + atomic_gate_blocked event + no deletion-log)
49. test/e2e/mutation_test.go (commit `b8ba6fc`; AC-5 + AC-6; review approve)
50. test/e2e/crash_resume_test.go (commit `18d8fe3`; AC-13 architectural gap documented; kill:phase=T1:after_pct=50 propagates ErrFaultKill; orphan started line confirmed in runs.ndjson; second backup detects but does NOT finalize the orphan as crashed_resumed; test skips with clear "AC-13 orphan finalization gap" message; future-state assertions ready below the skip so wiring the recovery gate switches them on with zero test changes)

**Tasks remaining (7):** 22a + 29a + 50a (preflight orphan-recovery gate) + 51c (NEW: `--delete` CLI flag + mirror-delete runner phase), 51a (e2e tampered manifest AC-19), 51b (e2e missing fault hooks per QA hat), 52 (e2e delete-confirm AC-7 + AC-8), 53 (ERROR_CATALOG.md), 54 (README polish), 55 (v0.1.0-core tag).

**Task 50a (queued):** wire a preflight gate that scans `<DotDir>/runs.ndjson` for `event=started` entries lacking a matching `event=finished` for the same run_id; finalize each orphan with a synthetic FinishedRun (`ExitStatus=crashed_resumed`, preserved StartedAt, `FinishedAt=time.Now().UTC()`, metadata from the orphan started line). Emit an audit event for the recovery. Once shipped, the `t.Skip` in `crash_resume_test.go` flips to assertion automatically (the future-state checks already exist below the skip). Out of scope for v0.1 core engine per Task 29's documented punt at `runner.go:78-79`; consider for Plan 2 or a late-Plan-1 amendment.

**Task 51c (new, queued):** wire the `--delete` CLI flag at `cmd/flashbackup/backup.go` (currently the flagset only registers `--move` and the faultinject-only `--inject`; passing `--delete` today exits 2 with the standard "flag provided but not defined" rejection) AND add a mirror-delete phase function in the runner that consults `opts.Delete`. The runner side requires three pieces: (a) an FB-written-paths reconstruction step at preflight (T0) that scans prior manifests under `<DotDir>/runs/*/manifest.ndjson.gz` and unions every recorded NFC-canonical relative path into a set; (b) a new mirror-delete phase (suggested placement: between T3 hash-compare and T4 finalize, i.e., a new T3.5 "MirrorDeleteDest", or fold into T4 finalize as a pre-finalize step) that computes the diff `prior_FB_paths - current_manifest_paths`, walks the namespaced dest root, and unlinks each diff entry; (c) explicit exclusion of any dest-root file whose relative path is NOT in the union of prior FB-written paths, which enforces invariant #6 (user-added files at the namespaced dest are NEVER touched). Note the existing `internal/runner/t2_transfer.go:224` comment `Delete:false, // invariant #6: mirror-delete is T3, not T1` predicts T3 placement; the running implementation has T3 as delete-SOURCE (move-mode), so the new mirror-delete-DEST phase needs a fresh phase ID (T3.5 or T4-pre). Once shipped, the `t.Skip` in `test/e2e/delete_flag_test.go` flips to assertion automatically (the future-state checks already exist below the skip). Out of scope for v0.1 core engine per the existing T2 comment that pre-supposed a T3-side wiring that never materialized; consider for Plan 2 or a late-Plan-1 amendment.

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

### 2026-06-05 (latest): Task 41 review approve + Tasks 42+42a (e2e infra) + 1 more CI rescue

Task 41 review verdict: approve, 3 cosmetic minors (M1 + M2 line/test count drift in brief, M3 helptext.go file-header convention note). Plan amendment A1 applied: master plan Task 41 entry clarifies that v0.1 ships the verb-form `help <subcommand>` reading from `subcommandHelpTexts`, while existing handlers' `fs.Usage` blocks keep providing the `flashbackup <subcommand> --help` path (future refactor may unify). helptext.go file-header now documents the "Use Flags / Actions / Subcommands as appropriate for surface shape" convention (M3).

Task 42 + Task 42a (bundled) shipped commit `9d7ee87`:
- `test/e2e/` package: doc.go + helpers.go + binary_cache.go + assertions.go + chflags_{darwin,other}.go + helpers_test.go. SetupUSB / SeedSource / SeedProfile / RunBackup / RunVerify / RunStatus / RunProfiles / RunInit / AssertManifestExists / AssertRunsNDJSONHasFinishedLine / AssertVerifySummaryExists exports. Binary-build cache via `sync.Once` per binary flavour (release + faultinject); built tempdir intentionally NOT registered with t.Cleanup so the cached path outlives the originating test (OS reaps on process exit). Repo-root discovery walks up from os.Getwd() looking for go.mod, fallback to $GOMOD env.
- `test/fixtures/{tiny,realistic,pathological}`: MANIFEST.txt per dir documents file count, sizes, SHA256-of-tree, special-char and edge cases, and which ACs consume the fixture. SHA256-of-tree recipe: concat sorted (path, file bytes) pairs with newline framing between chunks; canonical impl in `FixtureTreeSHA256`, mirrored in `regen-manifest.sh`. Tripwire test `*_MatchesManifest` fires if either side drifts.
- tiny + realistic are static (checked in); pathological is dynamic via `mkfixtures.sh` because NFC/NFD twins, \x07/\x1b control bytes in filenames, sparse files, and chflags state don't survive a generic git checkout. `SeedSource` dispatches on fixture name and execs the script for pathological.
- realistic/code/*.txt files contain Go-shaped placeholder source but use `.txt` extension so `go test ./...` and `go vet ./...` skip them.
- 7 hermetic sanity tests pass; FixtureTreeSHA256 tripwire holds.

CI rescue #3 this session: SA9009 staticcheck (commit `fde74c7`) tripped on a doc comment containing `go:embed)` which staticcheck interpreted as a malformed compiler directive. Rephrased to "a `go embed` directive" (backticked words + space) so the lint sees prose. First exec.Command in cmd/flashbackup (Task 40's profiles) and first prose mention of compiler-directive-shaped strings (Task 41's helptext.go); both are minor pain points of growing a package by accretion.

Commits this segment: `6ebac26` (gosec G204 fix), `3c95586` (BACKLOG through Task 41), `fde74c7` (SA9009 fix), `9d7ee87` (Task 42 + 42a), this commit (Task 41 review A1 + M3 + BACKLOG through Task 42).

### 2026-06-05 (latest): Tasks 40 review approve + Task 41 (help) + 2 CI rescues

Task 40 review verdict: approve, 6 minors (all cosmetic/deferrable):
- M1 (`runEditorSubprocess` 0% test coverage): TTY-dependent code is hard to unit test; deferred.
- M2 (stdin/stdout to os.Stdin/Stdout asymmetry vs stderr to passed io.Writer): mixed signature is correct (vim needs the real TTY); doc'd inline.
- M3 (`*` + `.DS_Store` skeleton choice undocumented): correct decision (allowlist forbids `**`); inline comment missing.
- M4 (coverage 73.9% measured by reviewer vs 80.2% reported by implementer): file-scoped vs package-aggregate discrepancy; just noting.
- M5 (exit-code-1 brief claim wrong; impl is correct).
- M6 (profiles.go 516 lines exceeds 200-line guideline): convention is "suggest splitting"; defer with possibly a `profiles_editor.go` split later.

Task 41 (`cmd/flashbackup/help.go` + `helptext.go`) shipped commit `187ab5f`. Constants table `subcommandHelpTexts` maps subcommand name to detailed help text. Empty-string key holds top-level usage; `printUsage` in main.go now pulls from the same table, removing duplicated text. Help text follows Tech Writer hat convention: Usage / Description / Flags / Examples / See also. Each subcommand has at least one concrete invocation example.

Tests: `TestHelp_AllSubcommandsHaveText` iterates over `subcommandList` and asserts every name has an entry in the table (prevents drift when a new subcommand lands without help text). `TestHelp_HelpTextHasNoEmDashes` scans every entry for U+2014 / U+2013 and fails if either is present (extends CLAUDE.md em-dash discipline from code to user-visible help strings).

Design decisions:
- Did NOT refactor existing init/backup/verify/status/profiles fs.Usage funcs to pull from the table; runHelp reads directly from the table while existing handlers keep their own local Usage blocks as a fallback so `flashbackup <subcommand> --help` continues working even if a future helptext edit shadows or empties an entry.
- `help help` self-reference: just a "help" entry in the table; `flashbackup help help` prints the help-subcommand's own body.
- `flashbackup help --help` lands in the "unknown" arm: `--help` is a binary-level flag, not a subcommand of `help`.

CI rescues:
- `b43b2f2`: removed redundant actions/cache@v4 step from .github/workflows/ci.yml. `setup-go@v5` already does module caching with the same go.sum-based key; the redundant step caused tar collisions on `~/go/pkg/mod` ("Cannot open: File exists" for ~20 pgregory.net/rapid files).
- `6ebac26`: added `//nolint:gosec G204` on `exec.CommandContext(parts[0], args...)` in profiles.go runEditorSubprocess. EDITOR env var IS operator-controlled by design (POSIX convention); refusing would defeat the feature. First exec.Command in cmd/flashbackup, so the lint surfaced only now.

Commits this segment: `9773705` (Task 40 profiles; earlier), `292fbb7` (Task 39 review I1+I2; earlier), `d5b5d47` (BACKLOG through Task 40; earlier), `b43b2f2` (CI cache fix), `187ab5f` (Task 41 help), `6ebac26` (CI gosec fix), this commit (Task 40 review approve + Task 41 + BACKLOG through Task 41).

### 2026-06-05 (latest): Tasks 39 review fixes + Task 40 (profiles) + transient CI

Task 39 review verdict: minor-fixes-needed. Two important findings:
- I1: four em-dash (U+2014) violations in `status_helpers.go` (lines 80, 128, 131) and `status_test.go` (line 228). CLAUDE.md em-dash discipline is hard; replaced each with semicolon + continuation per the project substitution table.
- I2: API Contracts schema example showed `last_run.profile` as populated without an "optional" annotation, but `state.FinishedRun.Profile` already uses `omitempty` so a backup without a named profile drops the field. Plan amendment locks the optionality (drops `last_run`/`last_verify` entirely when absent; drops `last_run.profile` when no named profile).

Other Task 39 minors deferred: M1 (status.go 321 lines and status_helpers.go 374 lines; ~50% is doc-comment); M3+M4 (runIDPattern + retention_limit triplication; tracked as backlog amendments A2+A3); M5 (verify summaryRecord shape duplication intentional one-way contract); M6 (lock_status semantic for stale locks out of locked-schema scope).

Task 40 (`cmd/flashbackup/profiles.go` + `profiles_helpers.go`) shipped commit `9773705`. CRUD wrapper around `internal/profiles.Store`:
- `list <USB> [--json]`: tabular plain text or JSON array.
- `new <name> <USB>`: opens $EDITOR (vim fallback) on a temp JSON skeleton; on save, parse + validate + Upsert. Refuses to overwrite existing profile (exit 2).
- `edit <name> <USB>`: opens $EDITOR on the existing profile JSON; rejects rename attempts.
- `delete <name> <USB>`: Store.Delete; clear "not found" if name unknown.
- `validate <name> <USB>`: profile.Validate(); OK or per-line errors + exit 1.

Editor seam: `editorRunOverrideForTest var func(path string) error` lets tests inject a Go callback that mutates the temp file in-process; tests register `t.Cleanup` to reset. The real `runEditorSubprocess` execs via `exec.CommandContext` wired to `os.Stdin/Stdout/Stderr`, supports space-separated EDITOR values like `code --wait`.

Design decisions worth recording:
- `list` uses a hand-rolled pre-scan for `--json` instead of `flag.FlagSet` so the documented `list <USB-path> [--json]` shape works with the flag in either position (stdlib flag stops at first positional).
- `validate` is the only path that surfaces exit 1 (on-disk tampering after Upsert validated cleanly); name collisions / rename attempts / post-editor validation failures all exit 2 (operator-fixable).
- `delete`-not-found is exit 2 (likely a name typo).
- Unit tests use `t.TempDir()` as a stand-in for a USB mountpoint (profiles operations don't touch rsync or immutable bits, so the DMG dance is unnecessary). Only one e2e test mounts a real APFS DMG.
- The brief's `**/*` skeleton was changed to `*` + `.DS_Store` because Store's allowlist forbids `**` (would behave differently between Go's walker and rsync). Skeleton is a starting point; doc'd inline.

Transient CI failure on Task 40 push (`27001541408`): Go module cache restore `tar` collision (`rapid@v1.3.0/...: Cannot open: File exists` on multiple files). Not a code issue. Rerunning after the em-dash fix push.

24 test functions / ~37 subtests; cmd/flashbackup coverage 80.2%. Files: profiles.go (516 lines), profiles_helpers.go (59 lines), profiles_test.go (670 lines). All pre-commit gates green locally.

Punted: no `rename` action (out of scope; would need delete+recreate semantics).

Commits this segment: `9773705` (Task 40 profiles), `292fbb7` (Task 39 review I1+I2 + BACKLOG).

### 2026-06-05 (latest): Tasks 38 review + Task 39 (status) + UIEvent.Path artifact-path refinement

Task 38 review verdict: approve with one important I1 — the plain renderer's summary block emitted `details: see <RunDir>/events.ndjson` (Task 33 review M1 had set up Path = run dir + renderer appends "/events.ndjson"); verify writes `summary.json` under `<verifyDir>/`, NOT `events.ndjson` at the run root. So `flashbackup verify <USB>` ended its plain-text output with a "details: see..." line pointing at a file that didn't exist.

Fix applied inline: switched `UIEvent.Path` semantics from "run dir" to "exact artifact file path." Each producer now names its own artifact: `runner.Run` sets `<runDir>/events.ndjson`, `verify.Verify` sets `<verifyDir>/summary.json` (new `VerifyResult.SummaryPath` field captures it from `verifyOneRun` after the successful summary write). Renderer prints `details: see <ev.Path>` verbatim with no suffix append. Empty `Path` falls back to a generic `details: see the <USB>/.flashbackup/ directory` placeholder for All-mode aggregates and pre-pipeline error paths.

Two new renderer tests lock both shapes (backup artifact `.../events.ndjson` + verify artifact `.../summary.json`). Plan amendment to Task 33 entry + types.UIEvtSummary doc both updated to reflect the artifact-path contract.

Other Task 38 review findings:
- M1 (test count drift in commit message): cosmetic, skipped.
- M2 (A1 commit message says 6 files; actually 7): cosmetic, skipped.
- M3 (2 non-mount test files still have local requireMacOS + requireDiskutil): out of A1 scope; can adopt testutil exports in a future cleanup pass.

Task 39 (`cmd/flashbackup/status.go` + `status_helpers.go`) shipped commit `4f69943`: surfaces USB state per the locked API Contracts JSON schema (`--json`) and a 2-column tabular plain-text default. last_run sourced from the latest `finished` line in `runs.ndjson`; last_verify sourced from scan-and-pick-newest by VerifiedAt across all `<DotDir>/runs/*/verifications/*/summary.json` files (cost bounded by retention limit; documented as the future trigger for a maintained index if perf becomes a problem). Lock detection is stat-only (not Acquire) since status is a read-only view; presence of `<DotDir>/lock` means held. Byte formatting via explicit decimal SI helper (`humanizeBytes`); JSON keeps raw bytes. Empty-state handling: `LastRun` and `LastVerify` are pointer + omitempty so a fresh USB drops both keys; plain renderer prints `(none yet)`. 26 tests (21 unit + 5 e2e); cmd/flashbackup coverage 73.6% (above 70% bar).

Commits this segment: `4f69943` (Task 39 status), this commit (Task 38 review I1 + plan/spec amendments + BACKLOG/memory through Task 39).

### 2026-06-05 (latest): Task 37 review + Task 38 + A1 + A2

Task 37 review verdict: minor-fixes-needed with one important (I1: spec-vs-impl semantic drift on WHEN the DELETE prompt fires — spec said "post-T2 modal" with exit 0 on decline; impl does "pre-T0 cmd-side gate" with exit 2 on decline) + 4 minors + 2 plan/spec amendments. I1 is "spec catches up to code" rather than a code change: the runner has no callback hook for cmd-side post-T2 confirmation and adding one would be invasive; the pre-T0 placement is architecturally simpler and arguably safer (no copies made if declined).

Applied inline:
- Spec section 4 (Upfront confirmation prompt): rewritten to describe the pre-T0 cmd-level prompt with stdin read + exit 2 on decline; architecture note explains why post-T2 modal was abandoned (no cmd↔runner callback contract).
- Spec line 207 (exit_status enum): `copy_only_aborted_delete` narrowed to "T1+T2 completed but atomic gate fired"; added note that operator-declined-DELETE never reaches the runner so produces no runs.ndjson record.
- Spec AC-7 + AC-8: GIVEN/WHEN/THEN rewritten to match pre-T0 cmd flow.
- Master plan line 2489 (Task 37 entry): added temporal placement clarification.
- Minor M2 in backup_prompt.go: tightened the SIGINT-during-prompt comment (first SIGINT does NOT interrupt the read syscall on macOS TTYs; the safety net is the installSignalHandlers second-signal-within-5s force-exit, not the prompt itself).
- Minor M4 in backup_prompt.go: promoted the expected token to a `deleteToken` constant. Previously `ev.Path = "DELETE"` was the live comparison source despite the doc claiming it was "documentation only"; reviewer was right to flag the drift trap. The single source of truth is now the const, not the UIEvent.

Deferred: minor M1 (cosmetic test name), minor M3 (CRLF-acceptance docs).

Task 38 (`cmd/flashbackup/verify.go`) shipped in 3 commits per the bundled scope:
- `db6972a` — A1 testutil extraction. Created `internal/testutil/{doc.go, hdiutil_darwin.go, hdiutil_other.go}`. Exports: `RequireE2E`, `RequireMacOS`, `RequireDiskutil`, `RequireHdiutil`, `MountTempVolume(t, fsType)`. The two prior mount variants (`mountTempVolume(t)` + `mountTempVolumeFS(t, fsType)`) collapsed into one parametric entrypoint. 7 test files updated to import testutil and call the shared exports. Non-darwin file panics on `MountTempVolume` if reached past the `RequireMacOS` guard.
- `8dc7de3` — A2 dispatcher handler-field refactor. `subcommandList` entries now have `handler subcommandHandler` (`func(ctx, argv, stdin, stdout, stderr) int`); nil means dispatchStub. `runInit` + `runBackup` signatures normalized to accept stdin even when unused (init explicitly discards with `_ = stdin`). The run() dispatcher loop becomes a 4-line if-handler-then-call. No behavior change.
- `4dfe3ca` — verify subcommand. `flashbackup verify [--all | <run-id>] [--check-extras] <USB-path>`. Mutually exclusive --all and run-id rejected at cmd layer for a better error message. ExitStatus mapping: ok=0, integrity_failed=1, preflight_failed=2, default=1. 14 new tests (8 unit + 6 e2e). cmd/flashbackup coverage 79.0% (above 70% bar). Files: verify.go (187 lines), verify_helpers.go (46 lines), verify_test.go (571 lines).

Implementer hit one fixture bug: first E2E test concatenated `host+"-"+user` literally for the namespaced dest, but `paths.Prefix` swaps dots for hyphens (`macbook.local` → `macbook-local`). Switched to `paths.Namespaced(...)` so the rehash loop finds the planted files. Caught + fixed in `4dfe3ca`.

All 7 pre-commit gates green after the third commit.

Commits this segment: `7123a81` (Task 37 impl), `8ec2063` (BACKLOG through Task 37), `db6972a` (A1 testutil), `8dc7de3` (A2 handler-field), `4dfe3ca` (Task 38 verify), this commit (Task 37 review I1+A1+A2+M2+M4 + BACKLOG through Task 38).

### 2026-06-05 (latest): Tasks 36 review approve + Task 37 (DELETE confirm modal)

Task 36 review verdict: **approve**. Two non-actionable minor flags (M1: typed-nil renderer pitfall for future refactor; M2: profile file-mode mismatch between seedProfile test fixture and runtime Upsert — both immaterial). No fixes applied.

Task 37 (`cmd/flashbackup/backup.go` + new `backup_prompt.go`) shipped commit `7123a81`: replaces the `--move` refusal gate with `promptDeleteConfirm`. Design picked **Option A** (renderer-driven prompt) per the brief: cmd composes the multi-line warning (PERMANENT DELETION + atomic-gate protection + "Type DELETE (exact case) to proceed") into a single `UIEvent{Kind:UIEvtPrompt, Path:"DELETE", Status:warning}`; the existing renderer.writePrompt handler writes `ev.Status + " "` with no trailing newline so the operator types immediately after the prompt. Status field carrying warning text is a slight semantic stretch (Status usually holds ExitStatus-style values) but pragmatic; reviewer let it stand.

Stdin reading via `bufio.Scanner` (default ScanLines + 64KiB buffer cap). Exact case-sensitive byte-equality against "DELETE"; `delete`, `DELETE ` (trailing space), ` DELETE` (leading), `DELETE\t`, `Delete`, empty all decline. EOF before line → `io.ErrUnexpectedEOF` → exit 1 (loud failure for scripted invocations that pipe nothing). Ctx-aware read deliberately punted (`bufio.Scanner` doesn't honor ctx; pre-call ctx.Err() check handles the pre-cancelled case; SIGINT mid-read interrupts the syscall and surfaces as ErrUnexpectedEOF).

runBackup signature grows an `io.Reader stdin` param; `main.go run()` signature follows; tests pass `bytes.NewBufferString("DELETE\n")`. `TestBackup_MoveModeRefused` renamed to `TestBackup_MoveModeBadMountpoint` (the gate no longer flat-refuses). 9 unit tests in backup_prompt_test.go (1 happy + 1 table-driven decline with 11 sub-cases + EOF + pre-cancelled ctx + warning text + no-trailing-newline) plus 3 new e2e tests (declined-empty, declined-wrong-token, accepted-invokes-runner). cmd/flashbackup coverage 78.3% (above 70% bar). All 8 pre-commit gates green.

Exit-code map: accept → runner exit; decline → exit 2 (`aborted by operator (DELETE not typed)`); EOF → exit 1 (`move confirmation failed: unexpected EOF`).

Commits this segment: `7123a81` (Task 37 impl), this commit (BACKLOG/memory through Task 37).

### 2026-06-05 (even later): Tasks 35 + 36 + Task 34 review fix

Task 34 review came back minor-fixes-needed with one important (I1: subcommand-to-task labels off-by-one for verify/status/profiles/help; the master plan maps backup to Task 36 + Task 37 move-confirm, then verify=38, status=39, profiles=40, help=41) and several minors. I1 fixed in `477e24a`; minor m1+A1 also applied: amended spec section 6 from "second signal of the same type" to "any second SIGINT or SIGTERM" so the simpler rule matches the implementation and the user mental model (people press Ctrl-C twice; the type-swap case is rare).

Task 35 (`cmd/flashbackup/init.go`) shipped commit `3644204` with 14 test functions covering AC-1 (happy path APFS) and AC-2 (exFAT refused with reformat recipe). Filesystem validation reuses `internal/preflight/filesystem.Validate`; rsync extracts via `rsync.EnsureExtracted` (sha-keyed path; matches the runner's preflight gate 9 path so init + run agree without a hardcoded literal). `--reset-keys` is defense-in-depth: init.go pre-checks via os.Stat with a friendlier message ("WARNING: invalidates every prior manifest on this USB"), state.InitVersionFile re-enforces internally. `--help` returns exit 0. Three pure-Go macOS tests use `t.TempDir()` (APFS on macOS) so cmd/flashbackup coverage stays at 83.1% without the FLASHBACKUP_E2E gate. ExFAT test skips in sandboxed agent envs (hdiutil ExFAT denied); runs on dev machine.

Task 35 review verdict: approve. Three minors (M1: doc-step numbering skew between header and body; M2: cleanup helper duplication across 4 tests; M3: Version package-var awareness for Plan 2). M1 applied inline (renumbered header from 6 to 7 steps to match body). M2 + M3 tracked. Plus two plan amendments (A1: extract hdiutil helpers to internal/testutil before Task 38 makes copy #7; A2: refactor subcommandList to carry a handler field before Task 38 adds the third real-handler arm). Both A1 and A2 tracked as latent infrastructure debt above; not blocking Task 37 (move-confirm modal touches only backup.go).

Task 36 (`cmd/flashbackup/backup.go`) shipped commit `bf99233`: runs runner.Run end-to-end with plain renderer. Argv: `flashbackup backup <profile> <USB> [--move]`. `--move` prints a Task 37 redirect and exits 2 for v0.1. ExitStatus mapping: ok=0, partial=1, copy_only_aborted_delete=1, crashed_resumed=1, preflight_failed=2. Profile lookup uses the canonical single-document `<USB>/.flashbackup/profiles.json` path (NOT per-profile JSON files; the brief was slightly aspirational, the actual store layout is the canonical one). isTTY detection uses `os.File.Stat()` + `os.ModeCharDevice` (no golang.org/x/term dep). 12 unit + 2 e2e tests; cmd/flashbackup coverage 80.8%. Added a `FLASHBACKUP_RSYNC_PATH_FOR_TEST` env-var seam to internal/runner/runner.go so external test packages (cmd/flashbackup, future test/e2e) can substitute the system GNU rsync (the package-private `rsyncPathOverrideForTest` is unreachable from external test packages). **First commit where the verify-release symbol-scan gate has a real positive control** — `flashbackup-faultinject` now links the runner package and `go tool nm` confirms `runner.faultinjectBuildTagPresent` is present in the faultinject build.

Commits this segment: `477e24a` (Task 34 review I1 + A1), `3644204` (Task 35 impl), `bf99233` (Task 36 impl), this commit (Task 35 review M1 + BACKLOG/memory through Task 36).

### 2026-06-05 (later): Task 33 + Task 34 + Task 32-33 review fixes

Task 32 review came back minor-fixes-needed with two important findings:
- `results.ndjson` per-file forensic record was missing. Design spec section 5 calls for BOTH `summary.json` (aggregate) AND `results.ndjson` (per-file); the master plan Task 32 entry only mentioned the aggregate. Without the per-file trail an operator who sees `FilesHashMismatch=N` in summary has no way to identify WHICH files failed. Added `writeResultsNDJSON` (NDJSON, mode 0o644, atomic via `state.WriteTmpThenRename`) called from `verifyOneRun` BEFORE writeSummaryFile so both records land together; mkdir happens once. Test `TestWriteResultsNDJSON_RoundTrip` locks the on-disk schema with mixed-status PerFile slice.
- ExitStatus constants duplicated as package-local strings instead of importing from `runner/types`. Drift trap. Fixed: import `types.ExitStatusOK` and `types.ExitStatusPreflightFailed`; keep only `ExitStatusIntegrityFailed` verify-specific (runner has no integrity_failed exit). Commit `09b6943`.

Task 33 (`internal/plain` runner.Renderer terminal implementation) shipped commit `988ba49`: `NewPlainRenderer(out, isTTY) types.Renderer` with TTY/non-TTY modes; TTY suppresses file-started/completed lines and rate-limits progress at 10 Hz with `\r` overwrite; non-TTY emits per-event lines but drops UIEvtProgress to avoid pipe spam; file_failed always emits in both modes. Unknown UIEventKind fails open with `?? <kind> ...` line. `sync.Mutex` covers `out.Write` and shared state; 100-goroutine race test under `-race`. 21 top-level tests / 28 subtests, 97.2% coverage. Concurrency contract documented in doc.go ("plain owns ALL CLI output formatting").

Task 33 review came back minor-fixes-needed with one substantive finding (M1): the summary block emitted literal `<USB>/.flashbackup/runs/<RunID>/events.ndjson` placeholder strings instead of the real path, breaking spec section 6 principle #2 (full paths, not relative). Fix applied inline: added `RunDir string` field to `types.RunResult` populated by `runner.Run` after T0 succeeds; `emitSummary` propagates it to `UIEvent.Path`; renderer substitutes the real path when Path is non-empty (falls back to placeholder when T0 failed). Plan API Contracts amendment (A1): clarified `NewPlainRenderer` returns `types.Renderer` not `runner.Renderer` (the prefix in the prior wording was shorthand; the interface lives in `internal/runner/types` post-PS3). Test `summary with real run dir path` locks the substitution.

Task 34 (cmd/flashbackup CLI entry) shipped commit `19a8573`: signal.NotifyContext for SIGINT/SIGTERM with second-signal-within-5s force-exit at code 130; subcommand dispatcher (stubs for init/backup/verify/status/profiles/help); --version prints ldflag-injected Version/RsyncVersion/CommitSHA/BuildEpoch line plus GPLv3 warranty disclaimer; refactored to testable `run(ctx, argv, stdout, stderr) int`. Makefile gained CMD_VERSION_LDFLAGS using `-X main.<name>=...` (Go linker mangles package-main vars under `main.` regardless of import path; verified empirically). `.gitignore` root-anchored so the cmd/flashbackup/ source isn't shadowed. 9 tests + 21 subtests, 90.9% coverage. **First time `make verify-release` and `make build` ran end-to-end**; both green ("OK: release binary clean of faultinject symbols").

Commits this segment: `09b6943` (Task 32 review fixes), `988ba49` (Task 33 impl), `19a8573` (Task 34 impl), this commit (Task 33 review M1 + A1 + BACKLOG).

### 2026-06-05 (post-billing-rescue): Task 32 (verify top-level) + Task 31 review + CI split + repo public

After the GitHub Actions billing block halted the cycle for several hours, MM flipped the repo public to permanently fix the Actions quota issue (public repos get unlimited minutes across all runner OSes). Secret-scan over full git history (grep for api keys, tokens, passwords, PEM blocks, AWS/Stripe key formats) was clean before the flip. Repo is now public at `https://github.com/maheshmirchandani/Backup-Pro`; GPLv3 license unchanged.

CI split landed in two commits during the halt and was validated post-flip:
- `c3f2ca0`: split portable tests to ubuntu (`test-linux` job for hash/state/profiles/paths/selection/runner-types/verify), keep macOS-only packages (drives/preflight-subtree/rsync/runner) on the `test` job, move `bench` to ubuntu, add `paths-ignore: docs/**` so docs-only commits skip CI.
- `b297cc4`: narrow the Linux `go vet` step to the portable package list and add a `GOOS=darwin go vet ./...` cross-check (the original `go vet ./...` on Linux tripped on macOS-only test files that reference darwin-only symbols).

Task 30 review minor fixes landed in `12204ae`: doc.go now documents the pipeline-order deviation (ReadVersionFile BEFORE manifest gzip open) and master plan amendment is queued; `TestLoad_WrongSchemaVersionMidStream` locks the abort-mid-loop behavior so a V=99 line after a clean V=1 prefix cannot be silently collected; `TestLoad_EntriesScannedInvariant` exercises the mixed-outcome case (3 verified + 1 tampered + 1 bad JSON) and asserts `EntriesScanned == len(Entries) + len(IntegrityErrors) + len(SchemaErrors)`.

Task 31 (`internal/verify/rehash`) review came back **approve** with three minor findings only:
- Minor #1 (cosmetic): `nolint:gosec G304` comment under-stated upstream HMAC validation. Reworded inline to explicitly cite invariant #33 and the bounded-input justification.
- Minor #2 (low-priority): cancelled-mid-hash file is classified `StatusUnreadable` instead of detecting `ctx.Canceled`. Mirrors t3's existing pattern; at most one file mis-classified per cancel; deferred.
- Minor #3 (Task 33 concern): `UIEvent.Phase` cast for verify uses a string outside the runner Phase enum; renderer-side note when Task 33 lands.

Task 32 (`internal/verify/verify.go`) shipped commit `8a5047b`: top-level `Verify(ctx, opts) (*VerifyResult, error)` stitches preflight → resolve RunID (latest/explicit/All) → load.Load → rehash.Rehash → optional CheckExtras walk → aggregate VerifyResult → write summary.json → emit UIEvtSummary. 27 tests (11 unit + 16 e2e behind `FLASHBACKUP_E2E=1`). Coverage 85.1%. `FilesIntegrityFailed` sources from `LoadResult.IntegrityErrors` (AC-19 path); `FilesHashMismatch` etc. from `Result`. ExitStatus is `integrity_failed` when any per-file failure or load schema error fired, `preflight_failed` on preflight error, `ok` otherwise. summary.json lands at `<DotDir>/runs/<runID>/verifications/<verifyID>/summary.json` with the locked schema; mode 0o644.

Design decisions worth recording:
- All-mode aggregate uses RunID sentinel "all" (distinguishable from any canonical timestamp-prefixed RunID).
- Per-run pipeline error in All mode does NOT abort the batch; bad run is skipped, aggregate ExitStatus degrades to integrity_failed.
- CheckExtras is a `filepath.WalkDir` over the namespaced dest comparing to the manifest path set; just produces a count.
- Verify reuses preflight (single call at top; `defer pc.Release()`).
- Rehash mid-loop cancellation: partial Result still writes summary.json (forensic), then surfaces wrapped error.

Punted items for later:
- `internal/testutil` shared mount helpers (duplicated across runner_test.go + verify_test.go; at 2 copies, deferred until 3).
- `runIDPattern` + `manifestBaseFilename` duplicated between `verify.go` and `runner/t5_finalize.go`; if drift becomes real, a guard test or shared constants package would address it.
- Coverage gate Makefile target doesn't yet include `internal/verify`; add when Task 38 wires the CLI.
- `results.ndjson` (per-file verify outcomes) is in design spec section 5 but NOT in Task 32's master plan deliverable; left for an explicit spec callout.

Commits this segment: `c3f2ca0` (CI split), `62a8f06` (BACKLOG reconciliation), `b297cc4` (CI vet narrow), `12204ae` (Task 30 minors), `8a5047b` (Task 32 impl).

### 2026-06-04 (later night): Task 31 (verify/rehash)

Task 31 (`internal/verify/rehash`) implements the per-file rehash + classify stage of the verify pipeline. Consumes `LoadResult.Entries` from Task 30; re-hashes each destination file under the namespaced path `<DestRoot>/<paths.Prefix(host, user)>/<RelPath>`; classifies into one of five Status values (`verified` / `size_mismatch` / `hash_mismatch` / `missing` / `unreadable`). Per-file errors do NOT abort the loop. Aggregates counters into a Result that Task 32 will fold into VerifyResult.

Design decisions worth recording:
- **Size-check-before-hash:** the spec's cheap-fail-fast rule (a 10GB file truncated to 100 bytes should not waste a hash pass) is enforced by `os.Lstat → compare to manifest size → short-circuit on mismatch BEFORE Open + StreamSHA256`. `bytesReadForStatus` returns 0 for size-mismatched files so `BytesRead` truthfully reflects what was streamed.
- **Status is a separate type from `state.FileStatus`:** verify-side has `missing` and `unreadable` (combining permission + IO error) that don't exist on the backup side; conversely it lacks `source_mutated` and `not_transferred`. A separate `Status` keeps each side's vocabulary honest. The underlying error is preserved on `FileResult.Err` so a caller wanting to subdivide unreadable can do so without re-running.
- **Phase wire string is `"verify"`:** the runner-side phase taxonomy (T0..T4) describes the backup state machine; verify is a separate flow. Introduced `const phaseWire = "verify"` (file-local, not exported through `types.Phase`) so the renderer's phase-filter logic gets an unambiguous tag without forcing the runner Phase set to absorb a non-backup concept. Persisting "verify" to events.ndjson is a Task 32 decision.
- **Namespace via `paths.Namespaced`:** does NOT pre-namespace `DestRoot`; the rehash package owns the namespace join via the same helper the runner uses (single source of truth restored after the Task 29 review fix #2 caught the duplicated logic bug in runner.go).
- **`lstat` not `stat`:** matches `t3_hash_compare.go`'s choice; a destination that has been replaced by a symlink is a corruption signal, not a "follow the symlink and hash whatever it points to" instruction.
- **Progress event: one per file (no throttling):** the spec's 200 ms tick target applies to T1 rsync transfer where rsync emits thousands of byte-level updates per second; verify is hash-bound and a per-file granularity gives the renderer one update per multi-second hash, well below the throttle threshold.
- **Cancellation contract:** entry-time cancel returns `(nil, wrapped err)` matching `load.Load`; mid-loop cancel returns `(partial Result, wrapped err)` so the caller does not lose work already done. Test `TestRehash_CancelledMidStream` uses a `cancelAfterFirstRenderer` to construct a real mid-loop cancellation that exercises the partial-result path; `TestRehash_CancelledAtEntry` covers the nil-result entry path.
- **`BytesTotal` pre-computed:** one O(n) sweep up-front so the renderer holds a stable progress denominator. Alternative (incrementally growing denominator) would force the renderer to redraw a moving max.

13 tests covering: happy path, size mismatch with hash-NOT-computed assertion, hash mismatch, missing, unreadable (with POSIX skip + root skip), aggregate counters covering all 5 outcomes, empty entries, progress events with phase=verify + monotonic counters + final-event = run-result assertion, mid-loop cancellation, namespace respect (including decoy file at unnamespaced path that must NOT satisfy verify; and wrong-host run returns Missing not Verified), nil renderer (counters identical to recorder run), empty DestRoot rejection, entry-time cancellation, renderer-errors-swallowed (PS3), PerFile order preservation. Package coverage 95.9% (above 85% sub-target).

All five pre-commit gates pass: `go vet ./...` clean; `gofmt -s -l .` empty; `go test -race -count=1 ./...` all pass (17 packages green); `make coverage` ok (runner 83.4%, hash 81.8%, state 83.0%, preflight 84.9%); `go build -tags faultinject ./internal/...` clean.

### 2026-06-04 (later night): Task 30 (verify/load) + Task 29 review fixes

Task 30 (`internal/verify/load`) dispatched in parallel with the Task 29 spec+quality review. The package reads the per-run gzipped manifest, verifies each entry's HMAC inline against the per-USB key loaded via `state.ReadVersionFile` (fail-closed; no init), and returns `(Entries, IntegrityErrors, SchemaErrors)`. Tampered entries surface as IntegrityErrors per PS1 / AC-19, not silent skip; per-line errors don't abort the load; pipeline errors (file open, gzip read, version-file fail-closed) DO abort with wrapped errors.

Implementer commit `14f73e0`; 14+ tests covering happy path, tampered entry (AC-19 path), bad JSON line, wrong schema_version, missing manifest, missing/corrupt version file, empty manifest, cancel mid-stream, HMAC canonical encoding edge cases (pipe-collision twins, UTF-8, large size, empty fields, 1000-char path), pipe-separator-forgery regression for invariant #33, missing HMAC field, empty paths, cancellation at boundary, non-gzip input, empty-line tolerance. New `internal/verify/load` package coverage 87.7%. All four CI jobs green first try.

No helpers exported from `internal/state`: the loader reuses `state.VerifyHMAC(entry, key)` which itself uses the same length-prefixed canonical helper as the writer. Single source of truth for the HMAC encoding preserved per invariant #33. Per-entry V mismatch (a structural corruption since the writer always uses V=1) aborts with pipeline error rather than landing in SchemaErrors. Missing-HMAC line surfaces as a SchemaError (scoped per-line); a missing-HMAC field IS the writer's signature, so its absence implies the line wasn't produced by an authorized writer.

Task 29 spec+quality review (against commit `da24cd1`) surfaced four important findings, all applied inline in commit `06a4255`:

1. **atomic_gate_blocked event silenced by runner short-circuit**: runner.go was returning before invoking RunT4DeleteSource when the gate would close, killing the central forensic signal of invariant #1. T4 already had the emission path via `t4FinishGateBlocked`. Fix: always call RunT4DeleteSource in move mode; the phase short-circuits before any unlink so no source is touched either way. Run-level ExitStatus still distinguishes the gated path.
2. **Namespace prefix duplicated logic**: runner.go did `filepath.Join(destAbs, fmt.Sprintf("%s-%s", pc.Hostname, pc.Username))` instead of calling `paths.Prefix(...)`. Silent divergence bug on hosts with dots in hostname (e.g., `macbook.local` → runner wrote to `macbook.local-mahesh/` while verify/status/etc. computed `macbook-local-mahesh/`). Fix: import `internal/paths` and use `paths.Prefix`. Single source of truth restored per invariant #15.
3. **T1 hook phase strings all "T1"**: the three T1 fault-injection sites passed `HookArgs.Phase = string(types.PhaseTransfer)` (= "T1") to PreRsync, Progress, AND Post. The Task 28 review plan amendment explicitly locked the canonical phase wire strings as `T1-pre` / `T1` / `T1-post` so e2e tests could selectively target each. Drift trap surfaced immediately. Fix: pass `string(PointT1PreRsync)` / `PointT1Progress` / `PointT1Post` to the three sites.
4. **Support-bundle path list never propagated**: Task 29 brief required `T2Result.RsyncLogPath + T4Result.DeletionLogPath` flow through to `RunResult` and runs.ndjson "finished" line. Original impl discarded T2Result entirely. Fix: added `SupportPaths []string` field to `types.RunResult`, `state.FinishedRun` (json `support_paths,omitempty`), and `T5Input`. Runner captures both paths after their phases (including on T2 failure path so forensic data lands even on aborted runs).

Task 29 review also surfaced minor findings logged for future polish but not blocking:
- Deferred `ms.Gzip(context.Background())` on early-abort paths finalizes a `.gz` for a run that never reached T5. The missing runs.ndjson "finished" line is the authoritative orphan signal, so technically not a bug, but a forensic reader who sees a `.gz` without a finished line may briefly be confused. Acceptable; current comment is candid.
- `SkipCodesign` flag on T0Input not threaded from RunOptions. Dead until cmd/main wants a `--skip-codesign-for-test` escape hatch. Worth a TODO; not blocking.
- `emitPreflightFailedSummary` reads slightly awkwardly. Functionally correct.

Task 30 + Task 29 review-fix commits this segment: `14f73e0` (Task 30 impl), `06a4255` (Task 29 review fixes).

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
