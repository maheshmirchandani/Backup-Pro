---
title: FlashBackup Design Specification
created: 2026-06-03
last_modified: 2026-06-03
author: Mahesh Mirchandani
status: draft-complete
supersedes: null
related: 2026-06-03-1338-flashbackup-prd.md
---

# FlashBackup Design Specification

> **Status: draft-complete.** All 9 design sections from the Superpowers brainstorming flow are complete. Next steps: (1) spec-development-discipline retrofit for 12-section structure per global CLAUDE.md, (2) spec self-review, (3) user review, (4) full multi-hat review via subagents, (5) transition to writing-plans skill for implementation plan generation.

> **Source PRD:** [2026-06-03-1338-flashbackup-prd.md](./2026-06-03-1338-flashbackup-prd.md)

## Context

This spec captures the design produced through the Superpowers brainstorming flow for FlashBackup, a portable USB-runnable macOS backup utility. The PRD established the high-level intent (safe + portable + multi-source backup with hash validation). This design pins down the architecture, on-disk layout, run-time flow, move semantics, verify command, error handling, TUI shape, testing strategy, and packaging.

Two adversarial review passes were conducted during brainstorming: a Hacker + Pre-mortem sanity check after Section 1, and a 5-hat review (CTO, Enterprise Architect, DevOps/SRE, QA, End User) after Sections 1-3. A UX-focused 5-hat review (UX, End User, Hacker, QA, DevOps) was conducted after Section 7. The full 11-hat multi-hat review per global CLAUDE.md menu is reserved for after this spec is finalized.

## Wedge and goals (locked clarifying answers)

| # | Question | Answer |
|---|---|---|
| 1 | Who is FlashBackup for? | Inner circle of 5 to 20 known users. Signed + notarized binary. No commercial intent. |
| 2 | UX shape? | TUI (full-screen terminal UI) via Bubble Tea. |
| 3 | Validation contract? | Strict + atomic. Manifest captures source-side SHA256. Separate `verify` subcommand. |
| 4 | Run-to-run semantics? | Mirror at destination + append-only manifest log. |
| 5 | Filter UX? | Presets + include/exclude patterns + saved profiles stored on USB. |

### Build-vs-buy analysis

We evaluated existing alternatives before deciding to build.

| Alternative | Cost | Why not |
|---|---|---|
| rsync from Terminal | $0 | No portable single-binary, no validation contract, no profile system, no friendly UI for CLI-shy friends. |
| Carbon Copy Cloner | $40 one-time + $40/yr updates | Mature desktop app; not portable on USB; aims at full-disk cloning, not selective backup. |
| Time Machine | $0 | Built-in. Single-Mac, dedicated drive, snapshot-based. PRD explicitly excludes as a non-goal. |
| Arq | $50 one-time | Excellent. Cloud-focused. USB target supported but format is proprietary encrypted blobs; USB not usable as a normal data drive (we want the mirror layout so files are drag-and-drop recoverable from Finder). |
| BorgBackup | $0 | Open source, mature. Output is deduplicated archive (not mirror); no drag-and-drop recovery without Borg. No native macOS GUI/TUI. |
| Restic | $0 | Same archive limitation as Borg. |
| rclone | $0 | Cloud sync primary; USB secondary. No mirror+manifest model; filter UX unfriendly. |
| Duplicacy | $20/yr | Snapshot-based; doesn't fit mirror requirement. |

**Justification for build:** No alternative combines all of: (a) portable single-binary on USB, (b) mirror layout (USB usable as data drive), (c) atomic-gate move semantics with mutation detection, (d) source+dest hash validation, (e) friend-friendly TUI, (f) no install/admin required. The wedge is the intersection; no existing tool hits it.

## Personas

Three archetypes the target audience samples from. The TUI design and documentation must accommodate all three.

| Persona | Terminal literacy | Source data shape | Current backup status | Failure tolerance |
|---|---|---|---|---|
| **Dev-Sibling** | High; lives in Terminal | Code projects (`~/Projects`), Documents | Some git remotes; no full-system backup | Low; will debug and report |
| **Photographer-Parent** | Low; uses Finder, has opened Terminal once | Lightroom catalogs, ~Pictures (large), Documents | iCloud Photos; nothing for catalogs | Medium; will email MM when stuck |
| **Retiree-Friend** | None; doesn't know Terminal exists | Documents, photos, scanned tax records | None | High; will give up silently if friction hits |

Wedge hypothesis #6 (below) is most at risk against Retiree-Friend. If we cannot deliver unaided first-run success for this persona, the spec downgrades that persona to "MM hands over a pre-initialized USB face-to-face" via the phase rollout plan.

## Distribution and enrollment model

Two distinct paths to a working FlashBackup USB:

- **Self-serve (default):** MM publishes a GitHub Release. Friend downloads, copies to USB, runs `init`. Suitable for Dev-Sibling and possibly Photographer-Parent. Quickstart documentation is the load-bearing component.
- **Pre-initialized handoff (phase 1 onwards):** MM hands over a USB already-formatted-and-init'd. Suitable for Photographer-Parent and Retiree-Friend. Onboarding becomes "plug it in and launch flashbackup; pick a profile."

Phase rollout (see below) starts with self-serve to Dev-Sibling archetypes; pre-initialized handoff is introduced once self-serve is validated.

## Stakeholder map

| Stakeholder | Role | Single-point-of-failure? |
|---|---|---|
| MM | Maintainer, distributor, support desk, incident responder | Yes; bus factor of 1 (see Bus factor section) |
| Inner-circle users (5 to 20) | End users | No |
| GNU rsync project | Upstream dependency | Mitigated by pinning + Renovate (invariant #38) |
| Apple Developer Program | Notarization gatekeeper | Mitigated by unsigned-fallback CI path (invariant #54) |
| GitHub | Distribution platform | Acceptable; switching cost low |
| Bubble Tea / Charm | TUI framework upstream | Pinned by Go module versioning |

## Hypotheses to track (qualitative signals)

The no-telemetry rule (Hard ethical rule #3) means we cannot measure rates of adoption, failure, or detection statistically across users. Hypotheses are therefore expressed as **qualitative signals** observed through voluntary reports, dogfooding, and MM's direct contact with friends, not as measurable rates. Validation is best-effort.

1. **Wedge signal.** Friends choose to keep using FlashBackup over their prior approach. Observed via: voluntary reports during the 3-month phase-1 window, MM's check-ins, and whether friends keep running backups after the novelty wears off. Negative signal: friends quietly stop using it.
2. **Footgun-proof signal.** No reports of unintended data loss. Observed via: voluntary reports, GitHub issues, MM's direct contact. Validated continuously by the fault-injection test suite (which substitutes for missing telemetry on the safety properties themselves).
3. **Validation-catches-corruption signal.** Hash mismatch events occur (or don't) in real-world use. Observed only when a friend voluntarily reports a `hash_mismatch` shown in a run summary. The technical hypothesis (our hash logic correctly detects injected corruption) is validated by the test suite; the empirical hypothesis (real USB sticks corrupt files at non-zero rates) is unfalsifiable without telemetry and is accepted as such.
4. **rsync cross-version signal.** No reports of macOS-version-specific bugs. Observed via voluntary reports + the CI matrix passing on all supported macOS versions.
5. **APFS-required-friction signal.** Friends complete `init` without giving up at the reformat step. Observed via voluntary reports during phase 1. If most friends bounce at exFAT refusal, the pre-initialized-handoff distribution path becomes mandatory rather than optional.
6. **TUI-acceptable-UX signal.** Friends complete a first backup without MM walking them through it. Observed via voluntary reports + the pre-initialized-handoff fallback for users where self-serve fails.

### Abandonment criteria

If after 3 months of phase 1 (3 trusted friends, technical) and 6 months of phase 2 (rest of inner circle), no friend voluntarily reports continued use of FlashBackup, the project is archived with a final tag and a final README banner. Forks remain welcome; MM does not commit to ongoing maintenance.

## Cost projections

Annual costs (single developer, open-source project):

| Item | Year 1 | Year 2+ | Notes |
|---|---|---|---|
| Apple Developer Program | $99 | $99 | Required for code signing + notarization. |
| GitHub (public repo) | $0 | $0 | Free for public OSS. |
| GitHub Actions CI | $0 | $0 | Free for public repos (2000 min/month). |
| Domain (optional) | $0 | $12 | Skip in v0.1; revisit if landing page is needed. |
| Hosting (releases) | $0 | $0 | GitHub Releases. |
| Hosting (docs) | $0 | $0 | README, GitHub-rendered. |
| **Total cash** | **$99** | **$99 to $111** | |

**Compute cost:** developer time. Re-baselined to **3 to 4 months part-time** to v0.1 (Multi-Hat hat finding: original 4 to 6 weeks undercosted given 58 invariants, 18 ACs, 6 test layers, cross-version CI matrix, notarization, GPL audit, TUI with 8 screens, fault-injection harness, docs as v0.1 done-criteria). Then sporadic maintenance.

**License obligations:** GPLv3 inheritance from embedded GNU rsync. FlashBackup ships under GPLv3 (or a compatible license); MM accepts this since no commercial intent.

**Hard caps:** if costs exceed $200/yr, re-evaluate the project's scope or sustainability model.

## Hard ethical rules

Non-negotiable. Applies to all code, configurations, and release builds. No flag, env var, or runtime setting may override these.

1. **Never delete source files except as the documented atomic move-mode outcome.** No `--force-delete`, no `--skip-validation`, no shortcut path to deletion.
2. **Never silently lose data.** Any failure to copy, hash, or verify is recorded in the manifest and surfaced in the run summary. Failures never go unreported.
3. **Never collect telemetry or phone home.** Zero network connections at any time. No analytics, no crash reports, no version checks, no feature pings.
4. **Never write outside the USB volume.** State, logs, profiles, lock file all live under `<USB>/.flashbackup/`. No `~/Library/`, `/usr/local/`, or other off-USB writes.
5. **Never require admin privileges (`sudo`).** Operations that would require admin (e.g., reading system-protected files) are skipped with a recorded warning, not escalated.
6. **Never modify destination files outside of intended backup writes.** `--delete` only considers FlashBackup-written paths (invariant #6).
7. **Never depend on network access at runtime.** All functionality works offline. Notarization stapling validates Gatekeeper offline.
8. **Never expose flags that disable safety machinery.** No way to skip the DELETE confirmation, validation, mutation re-stat, or atomic gate.

## Behavioral design principles

What we are actively designing AGAINST.

1. **Against muscle-memory acceptance.** Move-mode requires typing literal `DELETE` (case-sensitive). Confirmation is text-input, not button-click; prevents accidental Enter-key acceptance.
2. **Against false confidence.** Status counts always visible. Zero-failure runs explicitly show "0 mismatches, 0 missing, 0 unreadable" (confirms by enumeration, not by absence).
3. **Against fast-mode shortcuts.** No `--quick` for verify (size+mtime defeats the safety property). No `--no-confirm` for move. No "trust me" flags.
4. **Against opacity.** Three concurrent logs per run (`rsync.log`, `events.ndjson`, `manifest.ndjson.gz`). User can always reconstruct what happened.
5. **Against silent state changes.** Lock file with PID + host UUID + nonce; `version.json` with last-touched binary version; `runs.ndjson` with two-line model so crashed runs are visible.
6. **Against engagement-maximization.** No "great job!" messages, no streaks, no notifications. Tool reports facts; user decides.
7. **Against premature optimization that compromises safety.** Three full source-data passes in normal operation (rsync read, T2 source hash, T2 dest hash). We deliberately don't optimize this down to fewer passes if it weakens the "source-bytes-at-read = dest-bytes-as-written" verification.

These principles inform: the TUI (DELETE typing prompt, always-visible status counts, no auto-acceptance timers); the runner state machine (no shortcut paths); the CLI surface (no `--force`, `--quick`, or `--no-validate` flags).

## Section 1: Architecture

Single Go binary, statically linked, built as a universal2 lipo (Intel + Apple Silicon). The binary lives at the USB root. On first run, it extracts an embedded GNU rsync 3.x binary to `<USB>/.flashbackup/bin/rsync` (SHA256-verified). All state lives under `<USB>/.flashbackup/`. Nothing is written off-USB.

Embedded rsync is GNU rsync 3.x (not openrsync, which lacks `--partial`, `--xattrs`, `--progress`, and other features we depend on). License: GPLv3. Acceptable for our wedge since FlashBackup itself will be open-source.

Internal package decomposition:

| Package | Responsibility |
|---|---|
| `drives` | Enumerate mounted volumes; report capacity. |
| `profiles` | Load and save filter profiles from `profiles.json`. |
| `paths` | Compute namespace prefix (`<hostname>-<username>`). Single source of truth. |
| `selection` | Walk source tree; apply include/exclude filters. |
| `preflight` | Lock acquisition, filesystem-type check, symlink check, version migration, rsync verification. |
| `runner` | Top-level state machine across phases T0 to T4. |
| `rsync` | Wrapper around the embedded rsync binary. |
| `hash` | Streaming SHA256. |
| `state` | NDJSON I/O for manifests + run log + events. |
| `verify` | Standalone subcommand. Loads manifest, re-hashes destination. |
| `tui` | Bubble Tea program. |

## Section 2: On-disk layout

```
<USB-root>/
├── flashbackup                          # universal2 Go binary, ~10 MB, chmod 555
├── .metadata_never_index                # empty; suppresses macOS Spotlight indexing
├── .flashbackup/                        # all state, dotfile-hidden
│   ├── version.json                     # schema versions + last-touched flashbackup version
│   ├── lock                             # {pid, start_time_unix, host_uuid, nonce}
│   ├── bin/
│   │   └── rsync                        # extracted GNU rsync 3.x; SHA256-verified on launch
│   ├── profiles.json                    # saved filter profiles
│   ├── runs.ndjson                      # append-only run summary log (2 lines per run)
│   └── runs/                            # per-run detail
│       └── 2026-06-03T1430Z-a7f2/
│           ├── manifest.ndjson.gz       # gzipped at T4
│           ├── events.ndjson            # structured events from our own code
│           ├── rsync.log                # captured rsync stdout/stderr
│           ├── deletion-log.ndjson      # move-mode only; appended per unlink
│           └── verifications/
│               └── 2026-06-04T0900Z-c9f4/
│                   ├── results.ndjson
│                   └── summary.json
└── <hostname>-<username>/               # auto-namespaced; mirrored user files
    └── Documents/foo.pdf
```

### Schemas

**Manifest line (NDJSON, one per file):**
```json
{"v":1,"path":"Documents/foo.pdf","size":12345,"mtime_ns":1718000000000000000,"sha256_source":"abc...","copied_at":"2026-06-03T14:30:15Z","status":"verified","deletion_status":null}
```

**Run summary line (`runs.ndjson`):**
```json
{"v":1,"flashbackup_version":"0.1.0","run_id":"...","started_at":"...","finished_at":"...","mode":"copy","profile":"my-docs","source_root":"...","dest_root":"...","files_total":1234,"files_succeeded":1234,"files_failed":0,"bytes_total":987654321,"deletions_skipped_due_to_mutation":0,"exit_status":"ok"}
```

**`mode` enum:** `copy` | `move` | `verify` | `init`.

**`exit_status` enum:** `ok` (all verified, all deletions completed if move-mode), `partial` (some files failed verification; no deletions in move-mode), `copy_only_aborted_delete` (move-mode user typed anything other than DELETE), `crashed_resumed` (orphaned run was finalized by a later preflight), `preflight_failed` (lock contention, exFAT, etc.).

## Section 3: Backup-run data flow

State machine in `runner`:

```
PREFLIGHT (T0) → ENUMERATE (T0+) → TRANSFER (T1) → HASH + COMPARE (T2) → DELETE-SOURCE (T3, move only) → FINALIZE (T4)
```

| Phase | Operations | On crash |
|---|---|---|
| T0 | Codesign self-verify (invariant #29); filesystem check; symlink baseline (invariant #17); VolumeUUID capture (invariant #30); lock acquisition; version.json read (FAIL-CLOSED per invariant #11); rsync extraction; namespace compute. **Write "started" line to runs.ndjson at end of T0 success path** (commits the run to history once the lock is held). | "started" line absent if preflight aborted before lock acquisition; if acquired then aborted, abort-path Checkpoint of events.ndjson captures the failure but no started line is written. |
| T0+ | Walk source; apply filters; capture (size, mtime_ns) signatures; disk-space precheck; optional dry-run preview; profile load. | "started" line already present; T0+ failure leaves an orphan-finalizable run (will be finalized as `crashed_resumed` on next preflight). When the audit store itself fails mid-phase, the events.ndjson trail may terminate without a `phase_aborted` line; the missing closing event is itself the crashed-signal (invariant #10). |
| T1 | Single rsync invocation over full file list with `--archive --partial --xattrs --progress`. Parse progress events; capture rsync output to rsync.log. | Partial dest tree; rsync `--partial` resumes on re-run. |
| T2 | For each file: hash source again, hash dest, re-stat source against T0 signature. Classify status: verified / hash_mismatch / source_mutated / not_transferred. Append manifest line per file. | Partial manifest; un-hashed files are not_transferred on next inspection. |
| T3 | (Move mode only.) If any T2 status is not `verified` → skip phase entirely (atomic gate). Otherwise per-file: re-stat source; if signature unchanged unlink; if changed record deletion_skipped_mutated. Append deletion-log.ndjson per unlink with fsync. | deletion-log captures progress; TUI surfaces "previous run did not complete; inspect / resume?" |
| T4 | Gzip manifest; append "finished" line to runs.ndjson; prune old run dirs beyond retention; release lock. | "finished" line absent; orphaned run dir detected and finalized on next preflight. |

## Section 4: Move semantics

**Atomic gate.** T3 runs if and only if every file in the enumerated set is `verified` at T2. Any single non-verified file → zero deletions, exit_status `partial`, TUI names the failed files.

**Per-file mutation re-stat at T3.** Even when the atomic gate passes, individual files are checked again before unlink. If `(size, mtime_ns)` differs from the T0 signature, deletion is skipped for that file. Counter `deletions_skipped_due_to_mutation` recorded in run summary.

**Deletion mechanics.**
- Unlink via `os.Remove` (POSIX `unlink(2)`). Permanent, not Trash. Rationale: move mode exists to free source disk space; Trash defeats the purpose.
- Symlinks: remove the symlink, never the target.
- Hard links: unlink drops one ref; siblings outside source root survive.
- Empty source directories: left in place. Aggressive directory cleanup risks losing data if enumeration missed a file.
- Immutable files (`uchg`, `schg`): record `failed_immutable`, leave alone, continue.

**Upfront confirmation prompt.** After T2 succeeds, before T3 starts, the TUI presents a centered modal with red border. User must type literal `DELETE` (case-sensitive) and press Enter to proceed. Any other input aborts cleanly; verified copies stay at destination. Exit_status `copy_only_aborted_delete`. No CLI flag to skip this prompt, not even in `--quiet`. The friction is the feature.

**Move-mode manifest fields.** Each manifest line gains `deletion_status`: deleted / skipped_mutated / failed_immutable / failed_permission / null (copy mode).

**`deletion-log.ndjson` line schema (one JSON object per attempted unlink, append-only, fsync per line):**

```
{"v":1,"path":"Documents/foo.pdf","status":"deleted","attempted_at":"2026-06-03T14:48:24.123456789Z"}
{"v":1,"path":"Documents/bar.pdf","status":"skipped_mutated","attempted_at":"2026-06-03T14:48:24.234567890Z"}
{"v":1,"path":"Documents/baz.pdf","status":"failed_permission","attempted_at":"2026-06-03T14:48:24.345678901Z","errno":"EACCES","error":"remove /Volumes/.../baz.pdf: permission denied"}
```

Fields: `v:int` (schema version), `path:string` (NFC-canonical relative path matching the manifest), `status:string` (one of the four `deletion_status` wire strings above), `attempted_at:string` (RFC3339 with nanoseconds, UTC), `errno?:string` (POSIX errno name when applicable: EACCES / EPERM / ENOENT / EBUSY / EROFS / ENOTEMPTY / EIO; absent when status=deleted or status=skipped_mutated), `error?:string` (wrapped Go error string for forensic context; absent when status=deleted or status=skipped_mutated). File mode 0644 (no secrets; reachable by support tooling without sudo). The line-per-unlink + fsync-per-line cadence is the canonical crash-recovery record (invariant #1 atomic gate, plus orphan-finalizer on next preflight): a partial T3 interrupted by a kernel panic MUST be reconcilable from this file alone.

## Section 5: Verify command

`flashbackup verify [<run-id> | --all | --check-extras]` is a standalone subcommand. State machine:

```
PREFLIGHT → LOAD MANIFEST(S) → RE-HASH DEST → SUMMARIZE → WRITE RECORD
```

Uses same exclusive lock as backup; cannot run concurrently with a backup.

For each file in the manifest: stat → size check (fail-fast if size differs) → stream-hash dest → compare to `sha256_source` from manifest. Classify: verified / hash_mismatch / size_mismatch / missing / unreadable.

Result written to `<run-dir>/verifications/<verify-id>/` as `results.ndjson` (per-file) and `summary.json` (aggregate). `flashbackup status` surfaces "last verified: ..." per locked invariant #19.

**No `--quick` size+mtime-only mode** in v0.1. Spec it as a non-goal: the whole point of verify is catching silent bit-rot that size+mtime can't detect.

**Extra files at destination** (present at dest, not in manifest) are counted silently in the summary. `--check-extras` lists them. Default-no rationale: per locked invariant #6, FlashBackup never touches user-added files; flagging them as errors would create false alarms.

**Exit codes:** 0 (all verified), 1 (any integrity failure), 2 (preflight failure).

**Schema version handling:** rejects manifest lines where `v != 1` (no migration code in v0.1, per locked invariant #13).

**Performance:** USB-bandwidth-bound, not CPU-bound. ~33 min for 500 GB. TUI shows progress bar. No `--parallel` flag in v0.1.

## Section 6: Error handling and failure modes

### Phase tables

For each phase, the recovery model is fully tabulated by trigger / message / state-left / recovery-action. See the brainstorming transcript and BACKLOG.md for full detail. Key principles below.

### Error message principles

1. Three-part structure: what / where / next step. Bad: "rsync failed." Good: "rsync exited with status 23 while transferring 'Documents/big.mp4'. See `<run-dir>/rsync.log` for details. Re-run to resume."
2. Full paths, not relative.
3. errno mapped to human language (`EACCES` → "permission denied"; `ENOSPC` → "destination full"; `EIO` → "I/O error, possible bad sector").
4. Raw error preserved in `events.ndjson` for debugging from afar; user sees the friendly version.

### Signal handler contract

SIGINT / SIGTERM caught at process start. Each phase has a designated cancellation point:

- T0 (preflight): clean abort, no state changes, lock released.
- T1 (transfer): SIGTERM to rsync subprocess, 5s grace period, partial state captured, lock released.
- T2 (hash): complete current file's hash (1-2s), halt.
- T3 (delete): complete current unlink (atomic syscall), halt; deletion-log captures progress.

Second signal of the same type within 5 seconds forces immediate exit.

### Sleep prevention

Runs spawn under `caffeinate -i` (or set IOPMAssertion programmatically) to suppress system sleep during long operations.

### Explicit non-goals

- Cosmic-ray or RAM bit-flips.
- Compromised rsync binary (TOCTOU after our SHA256 verify; the USB itself is the trust boundary).
- Network filesystems as source (untested; behavior undefined).
- Source on a filesystem whose max path length exceeds destination's.
- Manifests are not cryptographically signed (integrity against bit-rot, not against an adversary with USB write access).

## Section 7: TUI shape

### Locked layout decisions

| Screen | Locked variant | Rationale |
|---|---|---|
| Main screen | C (two-pane: persistent left menu + right context) | Power users repeat-using prefer the orientation; sequential wizards happen in the right pane. |
| Backup wizard | A (linear 4-step: profile → confirm paths → mode + options → review) | Friction is the feature; explicit beat at mode-selection step prevents accidental Move. |
| Progress screen | A (compact metrics + recent files; phase indicator in header) | At-a-glance "60% done, 4 min left" without focus; status counts in T2 read cleanly. |
| Move-mode confirmation modal | A (minimal centered modal, counts only, type DELETE) | Concise; DELETE typing is the real gate. |
| Run summary | B (detailed inline: stats breakdown + failed-files list visible) | Failure case dominates; saves a click when user is anxious; "source files are intact" reassurance line. |
| History | List view with hotkeys (Enter open, V verify, F filter, / search) | Standard pattern; reads `runs.ndjson`. |
| Verify wizard | Two-step (pick scope → live progress reusing progress screen) | Reuses validated patterns. |
| Profiles | List + inline editor (N new, Enter edit, D delete, V validate) | Standard pattern. |

### Empty states

- **Fresh USB, no runs:** dashboard shows "No backup history on this USB yet. Press B to start your first backup." Hotkeys: B, P, I, Q.
- **Never verified:** dashboard "Last verify" row: "never run · press V to verify the latest backup".
- **No profiles:** profiles screen body "No profiles yet. Press N to create your first one."

### Dry-run preview screen

Inserts between wizard Step 4 (Review) and actual T1 when "Dry-run first" is checked. Shows: file count + bytes, largest-5 sample, count + bytes of filter-excluded files, [Start real backup] / [Back] buttons.

### Profile editor

Triggered by N (new) or Enter on a profile row. Fields: Name, Source, Include patterns (multi-line), Exclude patterns (multi-line). Buttons: [Save] / [Cancel] / [Validate]. Validate runs the same pattern-validation as preflight; surfaces errors inline per bad pattern.

### Headroom warning behavior

In wizard Step 4 (Review):
- Free space ≥ 2x source size: green "Free space: X GB (Yx headroom)". no warning.
- Free space 1.0x to 2.0x source size: amber "Tight: less than 2x headroom. Consider freeing space first.". Start button still available.
- Free space < 1.0x source size: red "Insufficient: need X GB, have Y GB free.". Start button disabled.

### Hotkey map

| Key | Meaning (global) | Context-specific |
|---|---|---|
| Esc | Cancel / back | (always) |
| Ctrl-C | Hard abort | (always) |
| ? | Help / hotkey overlay | (always) |
| Enter | Confirm / select | (always) |
| Tab | Next field | (in forms) |
| ↑ ↓ | Navigate list | (in lists) |
| / | Search | (in lists) |

Section-specific letters (`B`, `V`, `P`, `H`, `F`, `D`, `N`) are scoped to their screens; never globally bound.

## Section 8: Testing strategy

### Test pyramid

| Layer | Count | Speed | What it catches |
|---|---|---|---|
| Unit | ~150 tests | < 5s total | Per-package correctness: filter logic, manifest serialization, hash output, namespace path computation, profile validation, schema-version handling |
| Integration | ~30 tests | < 60s total | Multi-package interactions: rsync wrapper + hash + manifest writer; preflight gates against in-memory filesystems |
| End-to-end | ~10 tests | < 5min total | Full backup runs against fixture trees on real filesystems (per-test temp APFS images) |
| Fault-injection | ~15 tests | < 2min total | Phase-specific failure simulation (compiled-out in release; gated by `//go:build faultinject`) |
| TUI snapshot | ~15 tests | < 5s total | Per-screen rendered frames against golden files (via `teatest`) |
| Cross-version | runs in CI matrix | varies | Per-macOS-version regression guards (macOS 13, 14, 15, 16/anticipated) |

### Unit test priorities (data-loss risk order)

1. `runner` state machine: T3 atomic gate logic, T2 status classification, T3 mutation re-stat.
2. `hash` package: streaming SHA256 against golden vectors; edge cases (zero-byte, large, mid-read modification).
3. `state` package: manifest serialization, schema-version rejection, torn-write recovery.
4. `paths` package: namespace round-trip.
5. `selection`: filter matching, unicode normalization, case sensitivity.
6. `profiles`: schema validation, error messages.
7. `preflight`: lock acquisition + stale detection, filesystem-type detection, symlink-in-path refusal.

### Integration test priorities

1. rsync wrapper end-to-end against the embedded binary.
2. Hash + compare against rsync-written files.
3. Manifest gzip + verify roundtrip.
4. Preflight against APFS / FAT / exFAT image fixtures.

### End-to-end test priorities

(Run against ephemeral APFS disk images created via `hdiutil`.)

1. Happy path copy.
2. Happy path move.
3. Atomic gate triggered (one file with corrupted dest → zero deletions).
4. Source mutation between T0+ and T1 → status `source_mutated`.
5. Source mutation between T2 and T3 → `deletion_skipped_mutated`.
6. Concurrency lock: second instance aborts.
7. Stale lock recovery (dead PID).
8. Verify against intact backup.
9. Verify against tampered backup (corrupt dest file).
10. `--delete` only touches FB-written paths (manual file untouched).

### Fault injection harness

Build tag `faultinject` enables release-stripped hooks:

| Hook | Effect |
|---|---|
| `--inject=kill:phase=T1` | Force exit mid-T1 after N% of files |
| `--inject=kill:phase=T2:file=foo.pdf` | Exit when about to hash specific file |
| `--inject=kill:phase=T3:file=foo.pdf` | Exit mid-T3 after unlinking N files |
| `--inject=corrupt:phase=T1:file=foo.pdf` | Overwrite dest file with garbage after rsync writes |
| `--inject=mutate-source:phase=T2-pre:file=foo.pdf` | Touch source before T2 hash |
| `--inject=mutate-source:phase=T3-pre:file=foo.pdf` | Touch source between T2 and T3 |
| `--inject=unmount:phase=T1` | Force USB unmount mid-T1 |
| `--inject=disk-full:phase=T1` | Fill dest free space partway |

### TUI-specific testing (from UX hat review)

- Snapshot tests for every screen via `teatest`.
- Terminal resize tests (shrink to 60×20 → refusal; expand to 120×40 → layout adapts).
- Path-sanitization tests: fixtures with filenames containing 0x1B, 0x07, ANSI sequences; assert TUI renders without escape-sequence interpretation.
- SSH + tmux compatibility: CI runs e2e suite inside `ssh localhost` and `tmux new-session -d` wrappers.
- `flashbackup status --json` parseability: unit test asserts JSON validates against a schema.

### Cross-macOS-version CI matrix

| OS | Notes |
|---|---|
| macOS 13 (Ventura) | Has GNU rsync 2.6.9 at `/usr/bin/rsync`. |
| macOS 14 (Sonoma) | Still GNU rsync 2.6.9. |
| macOS 15 (Sequoia) | First with openrsync at `/usr/bin/rsync`. Confirm our embedded rsync doesn't conflict. |
| macOS 16 (anticipated 2026) | Forward compatibility guard. |

### Benchmark scaffolding

| Benchmark | Target |
|---|---|
| `BenchmarkHashThroughput` | ≥ 1 GB/s SHA256 on Apple Silicon |
| `BenchmarkCopyAndHash` (1 GB fixture) | ≥ 200 MB/s end-to-end |
| `BenchmarkManifestRead` (1M lines NDJSON gzipped) | < 2 seconds |
| `BenchmarkVerify` (1 GB fixture against fresh manifest) | ≥ 250 MB/s |

CI fails on > 15% regression.

### Filesystem fixtures

- `tiny/`: 10 files for fast tests.
- `realistic/`: ~1000 files mirroring real Documents folder.
- `pathological/`: Unicode normalization, long paths, special chars, sparse files, hard links, immutable files, extended attributes.
- `huge/`: 10 GB sparse file (env-gated, perf testing only).

### Coverage targets

- Per-package: 80% line coverage minimum.
- 90% for `runner`, `state`, `hash`, `preflight` (data-safety-critical).
- Tracked in CI; PR with regression fails the build.

## Section 9: Packaging and distribution

### Build pipeline

GitHub Actions workflow triggered on tag push (`v0.1.0`). Runs on `macos-latest`. Two universal2 binaries built:

| Binary | How built |
|---|---|
| `flashbackup` | `go build -trimpath -ldflags "-w -buildid=" -tags release` per arch, then `lipo -create`. |
| `rsync` (embedded) | Build GNU rsync 3.x from pinned source per arch, then `lipo -create`. Build script: `scripts/build-rsync.sh`. |

Build flags rationale: `-trimpath` + `-buildid=` for reproducibility; `-w` to strip DWARF debug info; **the `-s` flag is intentionally NOT passed** so the Go symbol table survives for the invariant #35 release gate to scan via `go tool nm`. Stripping the symbol table would defeat the release gate (Task 28 review, 2026-06-04). `-tags release` excludes `faultinject` hooks (locked invariant #20).

rsync binary is embedded via Go's `embed.FS`. SHA256 of embedded rsync is recorded in build-time constant for verification at extraction.

### Code signing

Each binary signed independently with Developer ID Application certificate before lipo / embedding:

```bash
codesign --sign "Developer ID Application: Mahesh Mirchandani (TEAMID)" \
         --options runtime --timestamp \
         --entitlements entitlements.plist \
         flashbackup
```

`--options runtime` enables hardened runtime (required for notarization). `--timestamp` embeds an Apple timestamp (required). Entitlements file is minimal (default hardened runtime sufficient for CLI tool).

### Notarization

After signing:

```bash
ditto -c -k --keepParent flashbackup flashbackup.zip
xcrun notarytool submit flashbackup.zip \
  --apple-id $APPLE_ID --team-id $TEAM_ID \
  --password $APP_SPECIFIC_PASSWORD --wait
xcrun stapler staple flashbackup
```

After stapling, Gatekeeper validates offline at launch (no internet check). No warnings for friends.

CI secrets needed: `DEVELOPER_ID_CERT_BASE64`, `DEVELOPER_ID_CERT_PASSWORD`, `APPLE_ID`, `TEAM_ID`, `APP_SPECIFIC_PASSWORD`.

### Distribution channel

v0.1: GitHub Releases on public repo. Each tagged release contains:

- `flashbackup-v0.1.0-darwin-universal` (raw binary)
- `flashbackup-v0.1.0-darwin-universal.zip` (zipped, preserves executable bit)
- `SHA256SUMS` (checksums)
- `SHA256SUMS.sig` (optional minisign signature)

Inner-circle workflow: MM creates release → sends friends the URL → they download, copy to USB, run `flashbackup init <usb-path>` once.

Not in v0.1: Homebrew tap (deferred), in-app update check (deferred indefinitely), Linux/Windows builds (out of scope), App Store (commercial territory).

### Versioning

Semantic versioning. Pre-1.0: breaking changes OK between minor versions (e.g., 0.1 → 0.2 may break manifest format; user re-inits USB). Post-1.0: backward compatibility honored.

`flashbackup --version` prints `flashbackup v0.1.0 (rsync 3.4.1, commit abc123, built 2026-06-15)`.

### GPLv3 compliance for embedded rsync

GPL requires either source distribution OR a written offer. Using the written-offer route:

- `README.md` includes a "Third-party components" section naming GNU rsync 3.x with upstream link and a written offer (valid 3 years) to provide corresponding source on request.
- `THIRD_PARTY_LICENSES.md` ships in repo with full GPLv3 text + upstream rsync source URL.
- `scripts/build-rsync.sh` documents how the embedded rsync was built (also serves reproducibility).

### Update mechanism (v0.1)

None. Friends manually download new versions; copy binary to USB (overwriting old).

Design accommodations: `version.json` records last-touched flashbackup version; new binary's preflight refuses to write if state is from a newer flashbackup (locked invariant #13). Users may need to re-`init` between major versions.

### Release checklist (per version)

1. Tag commit (`git tag v0.1.0 && git push --tags`).
2. CI builds, signs, notarizes, staples, uploads to GitHub Releases.
3. CI generates SHA256SUMS.
4. CI publishes draft release with auto-generated changelog.
5. MM reviews draft, edits release notes, publishes.
6. MM emails the link to inner circle.

### `init` reformat behavior

`init` does NOT reformat the USB if filesystem is wrong. It refuses with a printed `diskutil eraseDisk APFS FLASHBKP /dev/diskN` recipe. Rationale: reformatting from inside our tool requires correctly identifying the disk; getting that wrong destroys data. User invokes `diskutil` deliberately.

## Trust signals as first-class UX

Surfaces that let users know the system is healthy without having to ask. For a backup tool, trust IS the product.

| Signal | Where it appears | Purpose |
|---|---|---|
| "Last verify: 2026-06-02 19:00 UTC (all 1,234 verified)" | Main dashboard | At-a-glance integrity confirmation |
| Run status icon (✓ ⚠ ✗ ·) | History list, run summary | Color + ASCII pairing (invariant #24) |
| Status count breakdown (verified / hash_mismatch / source_mutated / not_transferred) | Run summary, T2 progress | Confirms by enumeration, not by absence |
| "Source files are intact" line | Move-mode partial-failure summary | Reassures the atomic gate caught the failure |
| Per-file SHA256 in `manifest.ndjson.gz` | Per-run state | User can re-verify any file with standard tools |
| `events.ndjson` per run | Per-run state | Full audit trail of our decisions |
| `rsync.log` per run | Per-run state | Raw rsync output for postmortem |
| `flashbackup status` subcommand | CLI | At-rest snapshot of USB state without running a backup |
| `flashbackup verify` subcommand | CLI | On-demand integrity re-check |
| `schema_version` field on every manifest line | Per-line | Forward/backward compat signal |
| Build provenance (`flashbackup --version` shows commit + rsync version + build date) | CLI | User can verify they have the right build |

We never hide what happened. Failures are visible, audited, and recoverable.

## Cheap-now-expensive-later decisions

Decisions we lock in now because retrofit cost is many multiples of the upfront cost.

| Decision | Cost now | Cost to retrofit later | Why locking now |
|---|---|---|---|
| Hash source AND dest, compare; manifest records source hash (invariant #1) | +30% T2 CPU | Re-hash every prior backup; manifest format change | Without it, transfer corruption is undetectable. Catastrophic if discovered after a year of backups. |
| Namespace prefix `<hostname>-<username>` (invariant #5) | +20 chars per path | Migrate every prior backup tree; manifest format change | Without it, multi-machine collisions are silent data loss. |
| Manifest `schema_version` field (invariant #13) | +5 bytes per line | Migration code OR breaking change forces re-init | Without it, future format changes have no detection mechanism. |
| `events.ndjson` structured log (invariant #17) | +1 line per phase transition | We have no debug info from early runs that crashed | Without it, we're blind to early-version failures. |
| Shared `paths` package (invariant #15) | Tiny | Refactor every call site that computes destination paths | Without it, namespace logic drifts across packages. |
| Non-TTY fallback (invariant #28) | One isatty() call + plain-text renderer | Breaks scripts that pipe output | Without it, can't run from cron/CI; users hack around. |
| `state.EventStore` interface (see next section) | One interface declaration | Refactor every audit-write site | Without it, future storage backends require touching many files. |
| Fault-injection hooks (invariant #20) | 15 hooks + build tag | Re-architect runner for testability | Without it, the most safety-critical paths can't be tested. |

## Audit storage abstraction

All audit and durable writes go through interfaces, even though backed by simple NDJSON implementations today. This lets us swap backends (encrypted at rest, central aggregator, chained signed log, etc.) without touching call sites.

```go
// package state

type Event struct {
    V         int            `json:"v"`           // schema version
    Timestamp time.Time      `json:"timestamp"`
    Phase     string         `json:"phase"`       // T0, T1, T2, T3, T4
    Kind      string         `json:"kind"`        // gate_passed, file_completed, error, etc.
    Path      string         `json:"path,omitempty"`
    Details   map[string]any `json:"details,omitempty"`
}

type EventStore interface {
    Append(ev Event) error
    Close() error
}

type ManifestStore interface {
    AppendEntry(e ManifestEntry) error
    Gzip() error  // T4 finalization
}

type RunLogStore interface {
    AppendStarted(s StartedRun) error
    AppendFinished(f FinishedRun) error
}
```

NDJSON implementations are the only ones in v0.1. Future encrypted/remote/signed implementations swap in without changing the runner state machine or any phase logic.

## Service level objectives, error budgets, and incident classification

### SLOs (measurable targets)

| SLO | Target | How measured |
|---|---|---|
| Backup throughput | ≥ 200 MB/s end-to-end (USB-bound in practice; measures CPU+filesystem overhead) | `BenchmarkCopyAndHash` against 1 GB fixture |
| Verify throughput | ≥ 250 MB/s | `BenchmarkVerify` against 1 GB fixture |
| TUI startup latency | < 1 second from `flashbackup` invocation to first screen | `BenchmarkTUIStartup` |
| Silent data-loss rate | 0% | Validated by full e2e + fault-injection test suite |
| Hash mismatch detection | 100% of injected corruptions | `e2e/test_hash_mismatch_detection` |
| Atomic gate correctness | 100% (never deletes source when ANY file is non-verified) | `e2e/test_atomic_gate_*` |
| Mutation detection | 100% of mid-run source modifications detected | `e2e/test_source_mutation_*` |

### Error budget policy

Personal/inner-circle tool; no traditional uptime SLO. But:

- **Correctness budget:** zero. Any data-loss bug halts all feature work until fixed.
- **Performance budget:** > 15% regression on benchmarks fails CI. Sustained regression blocks the next release.
- **Coverage budget:** drops below 80% line / 90% for safety-critical packages → blocks PR merge.

### Incident severity classification

| Severity | Definition | Response time | Action |
|---|---|---|---|
| Sev1 | Confirmed data loss: file silently deleted, file silently corrupted post-verify, atomic gate bypassed | Same day | Halt feature work; hot-patch release; notify all users. |
| Sev2 | Integrity claim falsely passing: verify says OK when file is corrupted | Within 1 week | Block next release; patch + new test before resume. |
| Sev3 | UX bug, performance regression, edge case in error handling | Backlog | Fix in next regular release. |
| Sev4 | Cosmetic issues, documentation gaps | When convenient | No release blocking. |

Reports come via: GitHub issues, direct email from friends.

## Acceptance criteria

Given/When/Then form. Each AC is humanly verifiable in under 30 seconds and forms part of the v0.1 "done" definition.

**AC-1: First-time init on APFS.**
GIVEN a freshly-formatted APFS USB at `/Volumes/USB`, WHEN user runs `flashbackup init /Volumes/USB`, THEN `<USB>/.flashbackup/` is created, embedded rsync is extracted to `bin/rsync`, `version.json` is written, and exit code is 0.

**AC-2: exFAT refusal at init.**
GIVEN a USB formatted as exFAT, WHEN user runs `flashbackup init`, THEN the tool exits with code 2, prints a `diskutil eraseDisk APFS` recipe, and writes no files to the USB.

**AC-3: Happy-path copy.**
GIVEN a profile pointing at `/Users/me/Documents` (1234 files, 982 MB), WHEN user runs the backup wizard and selects copy mode, THEN every file appears under `<USB>/<hostname>-me/Documents/`, every file's SHA256 matches between source and dest, the manifest records 1234 entries with status=verified, `runs.ndjson` gains two lines (started + finished), and exit code is 0.

**AC-4: Atomic gate blocks deletion.**
GIVEN move-mode is selected and a fault-injection hook causes 1 of 1234 files to fail hash compare, WHEN T2 completes, THEN T3 is skipped entirely, zero source files are deleted, manifest records 1233 verified + 1 hash_mismatch, runs.ndjson `finished` line has exit_status=`partial`, and exit code is 1.

**AC-5: Source mutation detected between T0 and T2.**
GIVEN a source file is modified by an external process between T0+ enumeration and T2 hash, WHEN T2 completes, THEN that file's status is `source_mutated`, exit_status is `partial`, no deletion of that file even in move-mode, and the user sees a clear message naming the file.

**AC-6: Mutation re-stat at T3.**
GIVEN move-mode passes the atomic gate and all files are verified, WHEN one file's mtime changes between T2 and T3 (via fault-injection hook), THEN T3 unlinks the unchanged files but skips that one file, deletion-log records `skipped_mutated` for that file, `deletions_skipped_due_to_mutation = 1` in the run summary, and exit code is 0.

**AC-7: DELETE confirmation honored (abort).**
GIVEN move-mode T2 completes and the confirmation modal appears, WHEN user types `delete` (lowercase), THEN T3 is skipped, exit_status=`copy_only_aborted_delete`, no source files deleted, and exit code is 0.

**AC-8: DELETE confirmation accepted.**
GIVEN the confirmation modal appears, WHEN user types `DELETE` (uppercase) and presses Enter, THEN T3 proceeds with per-file mutation re-stat and exits 0 if all unlinks succeed.

**AC-9: Verify intact backup.**
GIVEN a backup that completed successfully 1 hour ago, WHEN user runs `flashbackup verify`, THEN every file is rehashed, every hash matches the manifest, summary shows `files_verified == files_checked`, and exit code is 0.

**AC-10: Verify tampered backup.**
GIVEN a backup completed successfully, then one destination file was modified externally, WHEN user runs `flashbackup verify`, THEN that file is reported `hash_mismatch`, summary shows `files_hash_mismatch >= 1`, and exit code is 1.

**AC-11: Concurrency lock (second instance refused).**
GIVEN a backup is running (lock held), WHEN a second `flashbackup` invocation is attempted against the same USB, THEN the second invocation detects the live lock and exits with code 2 plus a clear message naming the live PID.

**AC-12: Stale lock recovery.**
GIVEN a lock file exists but the recorded PID is not running (or PID exists but with a different start_time/host_uuid/nonce), WHEN user runs `flashbackup`, THEN preflight detects the stale lock, takes the lock, and proceeds normally.

**AC-13: Crash recovery via rsync --partial.**
GIVEN a backup was killed mid-T1 with partial files copied, WHEN user re-runs the same backup, THEN preflight detects the orphaned run, rsync resumes via `--partial` and completes the partial files, T2 verifies all files, and exit code is 0.

**AC-14: --delete protects user-added files.**
GIVEN destination contains a manually-copied `<USB>/<hostname>-me/Documents/manual.txt` never written by FlashBackup, WHEN user runs backup with `--delete` (mirror mode), THEN `manual.txt` remains untouched, summary shows `files_extra_in_dest = 1`, and exit code is 0.

**AC-15: Non-TTY fallback to plain text.**
GIVEN user runs `flashbackup backup my-docs --start | tee log.txt`, WHEN the run proceeds, THEN no ANSI control sequences are written to the pipe, `log.txt` contains plain-text line-per-event progress, and the run completes normally.

**AC-16: Terminal-too-small refusal.**
GIVEN user launches the TUI in a terminal smaller than 80x24, WHEN the TUI starts, THEN it prints a "please resize to at least 80x24 and re-run" message and exits with code 2 without showing any UI.

**AC-17: Color + icon pairing in NO_COLOR mode.**
GIVEN any colored status indicator appears in the TUI, WHEN the same content is rendered with `NO_COLOR=1` set, THEN the ASCII icon (✓/⚠/✗/·) is still present and meaning is preserved.

**AC-18: Path sanitization defeats escape-sequence injection.**
GIVEN a source file with a filename containing ANSI escape sequences (e.g., `\x1b[2J`), WHEN that filename is rendered anywhere in the TUI, THEN escape sequences are stripped/escaped, the terminal is not affected, and the filename appears with a visible substitute for unprintable characters.

**AC-19: Manifest tamper-rejection in verify.**
GIVEN a completed backup, then a manifest entry's `sha256_source` field is externally modified (e.g., by an attacker rewriting the gzipped manifest), WHEN user runs `flashbackup verify`, THEN that line is reported as `integrity_failed`, summary shows `files_integrity_failed >= 1`, and exit code is 1. The keyed checksum (invariant #33) catches the modification even though the dest file itself is unchanged.

These 19 ACs form the v0.1 "done" definition. Implementation cannot be called complete until every AC passes its corresponding test.

### AC-to-code traceability (invariant #48)

Every AC has a recorded implementation and test location. Maintained alongside the AC table.

| AC | Package (impl) | Test file |
|---|---|---|
| AC-1 init APFS | `internal/preflight` | `test/e2e/init_test.go` |
| AC-2 exFAT refusal | `internal/preflight` | `test/e2e/init_exfat_test.go` |
| AC-3 happy copy | `internal/runner` | `test/e2e/copy_happy_test.go` |
| AC-4 atomic gate | `internal/runner` | `test/e2e/atomic_gate_test.go` |
| AC-5 source mutated T0-T2 | `internal/runner` | `test/e2e/mutation_t0_t2_test.go` |
| AC-6 mutation re-stat T3 | `internal/runner` | `test/e2e/mutation_t3_test.go` |
| AC-7 DELETE abort | `internal/tui` + `internal/runner` | `test/e2e/delete_confirm_abort_test.go` |
| AC-8 DELETE accept | `internal/tui` + `internal/runner` | `test/e2e/delete_confirm_accept_test.go` |
| AC-9 verify intact | `internal/verify` | `test/e2e/verify_intact_test.go` |
| AC-10 verify tampered | `internal/verify` | `test/e2e/verify_tampered_test.go` |
| AC-11 concurrency lock | `internal/preflight` | `test/e2e/lock_contention_test.go` |
| AC-12 stale lock recovery | `internal/preflight` | `test/e2e/lock_stale_test.go` |
| AC-13 crash recovery rsync --partial | `internal/runner` + `internal/rsync` | `test/e2e/crash_resume_test.go` |
| AC-14 --delete protects user files | `internal/runner` | `test/e2e/delete_flag_protects_test.go` |
| AC-15 non-TTY fallback | `internal/tui` | `test/e2e/non_tty_test.go` |
| AC-16 terminal too small | `internal/tui` | `test/e2e/terminal_size_test.go` |
| AC-17 color + icon pairing (NO_COLOR) | `internal/tui` | `test/unit/colors_no_color_test.go` |
| AC-18 path sanitization | `internal/tui` | `test/unit/path_sanitize_test.go` |
| AC-19 manifest tamper-rejection | `internal/verify` | `test/e2e/verify_tampered_manifest_test.go` |

## Phase rollout plan

Phased rollout protects against shipping broken v0.1 to all 20 friends at once.

| Phase | Audience | Duration | Go/no-go gate |
|---|---|---|---|
| **0: Dogfood** | MM only, 1 Mac, 1 USB | 2 weeks | All 18 ACs pass in CI; 50 cumulative backup runs without data-loss bug; weekly verify clean. |
| **1: Trusted technical** | 3 hand-picked Dev-Sibling-archetype friends | 1 month | No Sev1/Sev2 reports; at least one voluntary report of continued use; install quickstart needs ≤ 1 round of clarification per friend. |
| **2: Broader inner circle** | Rest of inner circle (up to 17 more) | 6 months | No Sev1 reports; no architectural rewrites; signal-tracking per Hypotheses section. |
| **Archive trigger** | After phase 2 + 3 months | n/a | If no friend voluntarily reports continued use, project archives per Abandonment criteria. |

Pre-initialized USB handoff (versus self-serve download) is permitted starting phase 1 if self-serve onboarding fails Hypothesis #6 signal.

## Repository layout (invariant #45)

```
flashbackup/
├── cmd/
│   └── flashbackup/
│       └── main.go                    # CLI entry; minimal, dispatches to internal/
├── internal/
│   ├── drives/                        # Drive enumeration
│   ├── profiles/                      # Profile load/save + schema validation
│   ├── paths/                         # Namespace prefix (single source of truth)
│   ├── selection/                     # Source tree walk + filter application
│   ├── preflight/                     # Lock, FS-type, symlink, version, codesign, namespace
│   ├── runner/                        # Phase state machine T0 to T4
│   ├── rsync/                         # Wrapper around embedded rsync subprocess
│   ├── hash/                          # Streaming SHA256
│   ├── state/                         # EventStore + ManifestStore + RunLogStore + NDJSON impls
│   ├── verify/                        # verify subcommand
│   └── tui/                           # Bubble Tea program + colors.go + plain-text renderer
├── test/
│   ├── e2e/                           # End-to-end tests against hdiutil APFS images
│   ├── unit/                          # Cross-package unit tests
│   └── fixtures/
│       ├── tiny/                      # 10 files for fast smoke tests
│       ├── realistic/                 # ~1000 files, real Documents shape
│       ├── pathological/              # Unicode, long paths, special chars, immutable, xattrs
│       └── huge/                      # 10 GB sparse file (env-gated)
├── scripts/
│   ├── build-rsync.sh                 # Pinned GNU rsync 3.x build for universal2
│   ├── notarize.sh                    # codesign + notarytool + stapler
│   └── entitlements.plist             # Minimal hardened-runtime entitlements
├── docs/
│   ├── BACKLOG.md                     # Rolling project log
│   ├── specs/                         # Design specs (this file)
│   ├── archive/                       # Historical artifacts
│   ├── INSTALL.md                     # Friend-facing install + first-run quickstart
│   ├── TROUBLESHOOTING.md             # Symptom-keyed recovery steps
│   ├── FAQ.md                         # Common questions
│   ├── GLOSSARY.md                    # T0/T1/T2/T3/T4, atomic gate, preflight, etc.
│   └── ERROR_CATALOG.md               # Every error path with user-facing string
├── .github/
│   ├── workflows/                     # CI: build, test, lint, release
│   ├── ISSUE_TEMPLATE/                # Bug, feature, security, data-loss-report
│   └── PULL_REQUEST_TEMPLATE.md
├── README.md                          # Public landing; project overview, install, safety claims
├── CONTRIBUTING.md
├── CODE_OF_CONDUCT.md
├── SECURITY.md
├── LICENSE                            # GPLv3 full text
├── THIRD_PARTY_LICENSES.md            # Generated by go-licenses + rsync attribution
├── Makefile                           # Task-runner contract per invariant #46
└── go.mod
```

## Build, test, lint commands (invariant #46)

The `Makefile` provides the canonical local-and-CI contract.

| Command | What it does |
|---|---|
| `make build` | `go build -trimpath -ldflags "-s -w -buildid=" -tags release ./cmd/flashbackup` |
| `make build-faultinject` | Same as build, but with `-tags faultinject` for local debug |
| `make test` | `go test ./...` (unit + integration; ~60s) |
| `make test-faultinject` | `go test -tags faultinject ./test/e2e/...` (~2min) |
| `make e2e` | Full end-to-end against `hdiutil` APFS image fixtures (~5min) |
| `make bench` | `go test -bench=. -benchmem ./...` with median-of-5 sampling |
| `make snapshot-update` | Regenerate `teatest` golden frames |
| `make lint` | `gofmt -s -d .` + `go vet ./...` + `golangci-lint run` |
| `make ci-local` | All of: lint, test, test-faultinject, e2e (mirrors CI) |
| `make release-dry-run` | Build + sign + notarize-dry-run (no upload) for verifying release flow |

## Documentation deliverables (v0.1 done criteria)

The following docs are part of the v0.1 acceptance bar. Code-complete without docs is not v0.1.

| Doc | Audience | Purpose |
|---|---|---|
| `README.md` | First-time visitor to GitHub repo | What FlashBackup is; quick install; safety claims; license; pointers |
| `docs/INSTALL.md` | Friend installing for the first time | Step-by-step: download, Gatekeeper handling, USB prep, init, first backup |
| `docs/TROUBLESHOOTING.md` | Friend stuck or seeing an error | Symptom-keyed: "Gatekeeper says cannot open" → "exFAT detected" → "lock contention" → "verify reports hash_mismatch" |
| `docs/FAQ.md` | Friend with questions | Restore, multi-machine, lost-USB consequences, why APFS required, what to do if backup fails |
| `docs/GLOSSARY.md` | Anyone reading TUI output | Plain English for T0/T1/T2/T3/T4, atomic gate, preflight, mutation re-stat, namespace, stale lock |
| `docs/ERROR_CATALOG.md` | Maintainer + contributors | Every event Kind with user-facing string and recovery action. Single source of truth for error wording. |
| `flashbackup --help` (CLI) | Friend running help | Top-level overview, subcommand list |
| `flashbackup <sub> --help` (CLI) | Friend running help on subcommand | Per-subcommand usage, flags, examples |
| TUI `?` overlay content | TUI user | Context-sensitive hotkey table + glossary excerpt + link to GLOSSARY.md |
| Release notes per version | Friends downloading update | Template: What's new / Breaking changes / Action required (re-init?) / Known issues |

The error-message principles in Section 6 are enforced via `docs/ERROR_CATALOG.md` as the single source of truth (invariant: every error surfaced to the user must use a string defined in the catalog; no inline `fmt.Errorf` user-visible strings).

## Encryption at rest (recommended, not required)

The USB device contains a mirrored copy of user data. If lost, that data is exposed.

FlashBackup does **not** require an encrypted destination filesystem in v0.1, because requiring it breaks the "USB is usable as a drag-and-drop data drive in Finder" wedge (a recipient without the password cannot read the files even when intended).

**Recommended in user-facing docs (`docs/FAQ.md` and `docs/INSTALL.md`):** if you carry the USB outside your home, format the destination as an **APFS Encrypted** volume in Disk Utility, OR use FileVault-on-removable-volume. FlashBackup operates identically on encrypted APFS as on unencrypted APFS.

This is a conscious trade-off recorded as a non-goal in v0.1; documented prominently in user-facing docs.

## Restore (manual via Finder)

v0.1 does not ship a `flashbackup restore` subcommand. The mirror layout IS the restore mechanism:

1. Connect the USB.
2. Open the USB in Finder.
3. Navigate into `<USB>/<hostname>-<username>/` to see the mirrored tree.
4. Drag any file or folder back to its source location.
5. Done.

This is intentional: friends understand drag-and-drop recovery without training, and adding a `restore` subcommand would be a non-trivial new design surface. Documented in `docs/FAQ.md` with the rolling-mirror caveat: deleted files do not survive the next backup. For multi-version history, use Time Machine alongside FlashBackup.

Limitation: a single USB is a single point of failure. `docs/FAQ.md` recommends a second USB rotated weekly, OR Time Machine for full-system recovery.

## Operational runbook (incident triage)

When a friend reports an issue, MM has a structured triage process. Stored in `docs/RUNBOOK.md` (maintainer-facing, not friend-facing).

1. **Ask the friend** for: macOS version (`sw_vers -productVersion`), `flashbackup --version`, and the run ID from the run summary (`2026-06-03T1430Z-a7f2` format).
2. **Request the run bundle:** ask friend to zip `<USB>/.flashbackup/runs/<run-id>/` (events.ndjson, rsync.log, manifest.ndjson.gz if present) and email it.
3. **Classify severity** per the Sev1-Sev4 table.
4. **Reproduce** locally against a matching fixture if possible.
5. **Patch + ship** per the response-time table.
6. **Postmortem** for Sev1 and Sev2; write up in `docs/postmortems/YYYY-MM-DD-<topic>.md`.

Calendar reminders (MM's own calendar, not in code):
- Apple Developer Program renewal date.
- Developer ID Application certificate expiry (5-year cadence).
- Quarterly review of GNU rsync upstream for security patches.
- Annual review: macOS beta channel for forward compatibility.

## Bus factor and succession

FlashBackup is currently a single-maintainer project. If MM becomes unavailable:

1. **Existing notarized binaries continue to work** at runtime (Apple does not revoke tickets without cause). Users are not stranded.
2. **No new releases ship.** Embedded rsync may accumulate unpatched CVEs over time.
3. **The repository remains on GitHub** for forks. The `LICENSE` (GPLv3) permits anyone to take over maintenance.
4. **Archival trigger:** if 12 months pass with no MM commits, MM (or a designated party with repo access) tags a final `vX.Y-archive` release and updates the README with a banner pointing to forks.
5. **The Developer-ID-lapse fallback path** (invariant #54) ensures unsigned builds remain producible by anyone with macOS access; friends would need to right-click Open once per launch, as documented.

Fork-friendly stance: external forks are welcome. Scope-acceptance criteria for contributions to the canonical repo (when active): bug fixes always; new features must align with the wedge (portable USB, mirror layout, atomic safety, friend-friendly TUI). Off-wedge proposals get a "please fork" response.

## Deferred to v0.2 or later (recorded with awareness)

Items raised in review but consciously deferred. Recorded so they aren't lost.

| Item | Source | Why deferred |
|---|---|---|
| Audio bell on completion (`--bell` opt-in) | Accessibility hat | Opt-in cue useful for blind users; not blocking v0.1 |
| Mouse support in TUI | Accessibility hat | Bubble Tea supports it; useful for low-motor-skill users; defer |
| macOS Increase-Contrast detection | Accessibility hat | No clean API from Go TUI; defer |
| i18n / non-English message catalog | Accessibility hat | English-only confirmed scope; defer |
| Parallel T2 hashing of small files | Performance hat | Defer to v0.2 once baseline measured |
| TUI Recent-files render cost cap | Performance hat | Preempt quadratic redraw; defer until observed problem |
| `flashbackup glossary` subcommand | Tech Writer hat | Convenience; `docs/GLOSSARY.md` suffices for v0.1 |
| `.superpowers/` git-attribute hiding | OSS hat | Cosmetic; revisit in 2 years |
| Generic `Appender[T]` collapsing three Store interfaces | Maintainability hat | Premature abstraction; defer |
| macOS dev-beta CI soak job | SRE hat | Cost > benefit at v0.1; defer until v0.2 |
| `flashbackup --license` subcommand | OSS hat | Convenience; LICENSE file at root + README link suffices |
| `runs.ndjson` rotation at 10 MB | Performance hat | Unbounded growth acceptable for v0.1 (∼50 KB after 5 years of weekly runs); revisit |

## Locked invariants (master list)

58 design invariants accumulated across all sections and review rounds, including 30 added during the full 11-hat multi-hat review pass (2026-06-03).

| # | Invariant | Source |
|---|---|---|
| 1 | Hash source at read time + hash destination after rsync + compare. Manifest records source hash. | Hacker hat (sanity check round 1) |
| 2 | Re-stat source at delete-time; skip deletion if `(size, mtime_ns)` mutated since T0. | Hacker hat |
| 3 | Notarization is in scope. Apple Developer Program membership required for build pipeline. | Pre-mortem + Q2 (round 2) |
| 4 | Require APFS or HFS+; refuse exFAT with reformat recipe. | Drawback analysis (Section 2) |
| 5 | Auto-namespace destination by `<hostname>-<username>`. | Drawback analysis |
| 6 | Track FlashBackup-written paths in manifests; `--delete` only considers those. | Drawback analysis |
| 7 | Concurrency lock at `<USB>/.flashbackup/lock` with stale-PID detection. | Drawback analysis |
| 8 | Gzip manifests at rest (`manifest.ndjson.gz`). | Drawback analysis |
| 9 | UTC ISO timezone in run IDs (`YYYY-MM-DDTHHMMZ-<hex>`). | Drawback analysis |
| 10 | Torn-write recovery: skip unparseable runs.ndjson lines with warning. | Drawback analysis |
| 11 | version.json corruption: FAIL-CLOSED. `ReadVersionFile` returns an error on parse failure, schema mismatch, or HMAC-key length mismatch; the runner refuses to start. The only re-creation path is an explicit `InitVersionFile(path, version, force=true)` invoked by `flashbackup init --reset-keys`, which rotates the HMAC key and invalidates all prior manifests. NEVER silently re-init on corruption. | Drawback analysis + Plan 1 multi-hat review 2026-06-03 (Senior Security Engineer + CISO) |
| 12 | `chmod 555` on the binary at install time. | Drawback analysis |
| 13 | No in-binary forward-compat migration code in v0.1. Keep `schema_version` field only. | CTO hat (round 2) |
| 14 | Split `runner` into `preflight` + `runner` packages. | Enterprise Architect hat |
| 15 | Shared `paths` package for namespace logic. | Enterprise Architect hat |
| 16 | Collapse `manifest` + `runlog` into `state` package. | Enterprise Architect hat |
| 17 | `events.ndjson` per-run structured log from our own code. | DevOps hat |
| 18 | Strong stale-lock detection: `{pid, start_time_unix, host_uuid, nonce}`. | DevOps hat |
| 19 | `flashbackup status` subcommand for at-rest inspection. | DevOps hat |
| 20 | Fault-injection hooks in `runner` (release-stripped via `//go:build faultinject`). | QA hat |
| 21 | `flashbackup init <usb-path>` subcommand for first-time setup. | End User hat |
| 22 | v0.1 ships full copy + move modes. | Q3 round 2 |
| 23 | Apple Developer Program account required for notarized release builds. | Same as #3 |
| 24 | Never rely on color alone; always pair with ASCII icon (✓ ⚠ ✗ ·). | UX hat (round 3) |
| 25 | Minimum terminal size 80×24. Below that: refuse with "please resize" message. | UX hat |
| 26 | Empty states designed for: first run, never verified, no profiles. | End User hat |
| 27 | Sanitize all path strings before TUI render (strip control chars + ANSI sequences). | Hacker hat (round 3) |
| 28 | Detect non-TTY stdout; fall back to plain-text progress for pipes / cron / CI. | DevOps hat |
| 29 | Re-verify codesign of `flashbackup` binary on every launch before trusting embedded constants. | Security hat |
| 30 | Capture USB VolumeUUID at T0; re-verify at every phase boundary; abort on change. | Security hat |
| 31 | Lock file opened with `O_EXCL \| O_CREAT \| O_NOFOLLOW` plus `flock(LOCK_EX\|LOCK_NB)` on the FD. | Security hat |
| 32 | All paths canonicalized to NFC before manifest write and lookup; reject duplicate normalized paths at selection time. | Security hat |
| 33 | Manifest lines carry a keyed integrity checksum (HMAC-SHA256, per-USB key in `version.json`). Sufficient to detect accidental corruption, bit-rot, and bugs in our own writer. **Not** a defense against an adversary with USB write access (such an attacker can rewrite both the manifest and the key); see SECURITY.md threat model. Canonical encoding is length-prefixed, not pipe-separated. | Integrity check (rewritten 2026-06-03 after Plan 1 multi-hat review caught pipe-separator forgeability and overstated authentication claim) |
| 34 | Mandatory minisign signature on `SHA256SUMS`; public key in README and on maintainer site (out-of-band distribution). | Security + SRE hats |
| 35 | CI release gate: symbol-scan signed release binary for `faultinject` symbols; fail build on hit. Gate implementation: `go tool nm <binary> \| grep -E '(^\|[._/])faultinject'`. Build flags MUST preserve the Go symbol table (omit `-s` from `-ldflags`; `-w` is fine) so `nm` can read it. The release stub's `ErrFaultinjectStripped` sentinel uses mixed-case `Errfaultinject` prefix that does not match the lowercase regex, by design. | QA hat (refined 2026-06-04 after Task 28 review) |
| 36 | Every Go source file carries `SPDX-License-Identifier: GPL-3.0-or-later`; `LICENSE` file at repo root. | OSS hat |
| 37 | Release CI fails if `go-licenses` reports any non-GPL-3-compatible license among Go deps. | OSS hat |
| 38 | Embedded rsync source pin tracked by Renovate/Dependabot; security patches integrated within 30 days of upstream release. | OSS + SRE hats |
| 39 | Pin `macos-14` runner (not `macos-latest`); pin all GitHub Actions to commit SHAs; environment-protected approval gate on release workflow. | SRE hat |
| 40 | Mutation testing on `runner`, `hash`, `state`, `preflight` packages via `go-mutesting`; mutation score ≥ 80%. | QA hat |
| 41 | Property-based testing (`rapid` or `gopter`) for hash chunk-boundary invariance, manifest unicode round-trip, mutation re-stat monotonicity. | QA hat |
| 42 | Branch coverage ≥ 85% for `runner`, `hash`, `state`, `preflight`. | QA hat |
| 43 | Contract test for rsync stdout `--progress` parser with pinned golden file; upgrading embedded rsync breaks this test loudly. | QA hat |
| 44 | Filesystem axis in CI matrix per macOS version: APFS, HFS+, exFAT-refused, FAT32-refused. | QA hat |
| 45 | Locked repository layout: `cmd/flashbackup/`, `internal/<11 packages>/`, `test/{e2e,fixtures/{tiny,realistic,pathological,huge}}/`, `scripts/`, `docs/`, `.github/workflows/`. | Maintainability hat |
| 46 | Makefile contract: `make build`, `make test`, `make test-faultinject`, `make e2e`, `make bench`, `make snapshot-update`, `make lint`, `make ci-local`. | Maintainability hat |
| 47 | Mermaid `stateDiagram-v2` for runner state machine (T0 to T4 + crash-recovery edges) maintained in Section 3 of this spec. | Maintainability hat |
| 48 | AC-to-code traceability index: every AC has a recorded (package, test file) location maintained alongside the AC table. | Maintainability hat |
| 49 | Explicit 256-color ANSI indices plus 16-color fallback in `internal/tui/colors.go`; CI test asserts ≥ 4.5:1 contrast against default light Terminal.app (`#FFFFFF`) and default dark iTerm2 (`#1E1E1E`). | Accessibility hat |
| 50 | `FLASHBACKUP_PLAIN=1` env var or `--plain` flag forces non-TTY renderer even on a TTY (for VoiceOver and screen-reader users). | Accessibility hat |
| 51 | Below 80×24 terminal size: automatically engage plain-text renderer instead of hard refusal. Refusal reserved for when stdout is not writable. Supersedes invariant #25's refusal behavior. | Accessibility hat |
| 52 | Repository conventions in repo root: `CONTRIBUTING.md`, issue templates (bug/feature/security/data-loss-report), PR template, `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1), `SECURITY.md` (private vuln disclosure to MM email, 90-day window). Branch strategy: trunk-based with semver tags. | OSS hat |
| 53 | Sev1/Sev2 notification channel: GitHub Releases pinned advisory, README pinned issue, advisory broadcast in release notes. No telemetry, no email blast. | OSS hat |
| 54 | Bus-factor / Developer-ID-lapse fallback path: CI emits unsigned-build alternative; README updates with right-click-Open instructions; project archives if maintainer unavailable. | OSS hat |
| 55 | SLO targets re-baselined: T1 throughput ≥ 200 MB/s (USB-write-bound); T2 hash+compare aggregate ≥ 350 MB/s (source-SSD-bound). Realistic end-to-end wall-clock for 500 GB: ~90 to 100 minutes (3 passes). Old "200 MB/s end-to-end" SLO retired. | Performance hat |
| 56 | SHA256 over BLAKE3 choice recorded with interop justification ("user can re-verify with `shasum -a 256` from any Mac without installing tools"). Locked. | Performance hat |
| 57 | Manifest gzip stream-written during T2 (not batch at T4); removes a finalization-phase failure window. | Performance hat |
| 58 | Headroom yellow threshold bumped from 5% to 10% (accounts for xattrs / AppleDouble overhead measured empirically). | Performance hat |

## Next steps (in order)

1. **Apply spec-development-discipline** to retrofit this spec with the 12 required structural sections (hypotheses, wedge analysis, locked decisions table, behavioral principles, trust signals, cheap-now-expensive-later, audit abstraction, test pyramid, SLOs, acceptance criteria, etc.). Per global CLAUDE.md, these are non-negotiable for substantial specs.
2. **Spec self-review:** placeholder scan, internal consistency check, scope check, ambiguity check.
3. **User review** by MM; apply requested changes.
4. **Full multi-hat review** via subagents, parallel: CTO, Enterprise Architect, BA, CIO, CISO, Hacker, UX, DevOps/SRE, DPO, QA, End User. Consolidate findings by severity. Apply approved amendments.
5. **Transition to `superpowers:writing-plans`** to generate the implementation plan from this spec.
6. **Multi-hat review** of the implementation plan (CISO, Hacker, DevOps/SRE, QA, Senior Developer, DX).
7. **Implement** via `superpowers:subagent-driven-development`.
