# FlashBackup Backlog

> Rolling log of design decisions, open items, and historical context for the FlashBackup project. Updated as the project evolves. Lives at `docs/BACKLOG.md`.

## Project status (2026-06-03)

**Phase:** Plan 1 (core engine + minimal CLI) saved. Ready for execution via subagent-driven development. Plan 2 (TUI + packaging + docs) to be written when Plan 1 nears completion.

**Plans:**
- `docs/planning/2026-06-03-flashbackup-core-engine.md` (Plan 1, 2477 lines after multi-hat amendments, ~58 tasks including 12a/42a/51a/51b, ~3 to 4 months part-time after re-baseline)
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
