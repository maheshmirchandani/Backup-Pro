# FlashBackup Plan 1: Core Engine + Minimal CLI

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a working `flashbackup` CLI that performs APFS-only backup, verify, and atomic move operations with full safety machinery (atomic gate, source+dest hash compare, mutation re-stat), text-mode progress output, and a fault-injection harness sufficient to drive end-to-end safety tests. No TUI in this plan. No code-signing/notarization in this plan. Phase 0 dogfood-ready at the end.

**Architecture:** Single Go binary embedding GNU rsync 3.x. State machine in `runner` package across phases T0 to T4 (preflight, enumerate, transfer, hash+compare, delete-source, finalize). All state under `<USB>/.flashbackup/`. Plain-text renderer for all output (plain-mode now; TUI later in Plan 2). Phase rollout: end of this plan = Phase 0 dogfood.

**Tech Stack:** Go 1.22+, GNU rsync 3.x (embedded via `embed.FS`), SHA256 (`crypto/sha256`), NDJSON + gzip for state, golangci-lint, go-mutesting, `rapid` for property-based testing. Test fixtures via `hdiutil` APFS images. macOS 13-16.

**Source spec:** [`../specs/2026-06-03-1532-flashbackup-design.md`](../specs/2026-06-03-1532-flashbackup-design.md) (979 lines, 58 invariants, 18 ACs). Every task here cites the relevant invariants and ACs.

**Out of scope (Plan 2):** TUI screens, code-signing, notarization, friend-facing docs (INSTALL/TROUBLESHOOTING/FAQ/GLOSSARY), full README polish, release pipeline, profile editor screens.

---

## File structure (this plan creates)

```
flashbackup/
├── go.mod
├── go.sum
├── LICENSE                              # GPLv3 full text
├── README.md                            # Skeleton; polished in Plan 2
├── CONTRIBUTING.md                      # Skeleton
├── SECURITY.md                          # Skeleton
├── CODE_OF_CONDUCT.md                   # Contributor Covenant 2.1
├── THIRD_PARTY_LICENSES.md              # Generated; rsync + Go deps
├── Makefile                             # Task-runner contract (invariant #46)
├── .golangci.yml                        # Lint config
├── .gitignore
├── .github/
│   └── workflows/
│       └── ci.yml                       # Build + test (no signing in Plan 1)
├── cmd/
│   └── flashbackup/
│       ├── main.go                      # Entry, signal handling, dispatch
│       ├── init.go                      # init subcommand
│       ├── backup.go                    # backup subcommand
│       ├── verify.go                    # verify subcommand
│       ├── status.go                    # status subcommand (+ --json)
│       ├── profiles.go                  # profiles CRUD subcommand
│       └── help.go                      # --help + per-subcommand help
├── internal/
│   ├── paths/
│   │   ├── namespace.go                 # <hostname>-<username> prefix
│   │   └── namespace_test.go
│   ├── hash/
│   │   ├── sha256.go                    # Streaming SHA256
│   │   └── sha256_test.go
│   ├── state/
│   │   ├── event.go                     # Event struct + EventStore interface
│   │   ├── event_ndjson.go              # NDJSON impl
│   │   ├── event_test.go
│   │   ├── manifest.go                  # ManifestEntry + ManifestStore interface
│   │   ├── manifest_ndjson.go           # NDJSON + gzip-stream impl
│   │   ├── manifest_test.go
│   │   ├── runlog.go                    # Run summary + RunLogStore interface
│   │   ├── runlog_ndjson.go             # NDJSON impl with torn-write tolerance
│   │   ├── runlog_test.go
│   │   ├── version.go                   # version.json read/write + recovery
│   │   └── version_test.go
│   ├── profiles/
│   │   ├── profile.go                   # Profile struct + JSON schema
│   │   ├── store.go                     # Load/save/validate
│   │   └── store_test.go
│   ├── drives/
│   │   ├── enumerate.go                 # /Volumes/* + diskutil
│   │   └── enumerate_test.go
│   ├── selection/
│   │   ├── walk.go                      # Walk + filter + NFC canonicalization
│   │   ├── filter.go                    # Include/exclude pattern matching
│   │   └── walk_test.go
│   ├── rsync/
│   │   ├── embedded.go                  # embed.FS + extraction + SHA256 verify
│   │   ├── wrapper.go                   # Subprocess + argv builder
│   │   ├── progress.go                  # Parse --progress output
│   │   └── wrapper_test.go
│   ├── preflight/
│   │   ├── lock.go                      # File lock with strong stale detection
│   │   ├── filesystem.go                # APFS/HFS+ check, exFAT refusal
│   │   ├── symlink.go                   # Symlink-in-path refusal
│   │   ├── codesign.go                  # Re-verify binary codesign (invariant #29)
│   │   ├── volume_uuid.go               # Capture/re-verify VolumeUUID (invariant #30)
│   │   ├── preflight.go                 # Integrate all checks
│   │   └── preflight_test.go
│   ├── runner/
│   │   ├── types.go                     # Phase, Status, Run, Signature
│   │   ├── t0.go                        # Preflight phase
│   │   ├── t0plus.go                    # Enumerate phase
│   │   ├── t1.go                        # Transfer phase
│   │   ├── t2.go                        # Hash + compare phase
│   │   ├── t3.go                        # Delete-source phase (move mode)
│   │   ├── t4.go                        # Finalize phase
│   │   ├── runner.go                    # Top-level state machine
│   │   ├── faultinject.go               # Hooks (build tag: faultinject)
│   │   ├── faultinject_release.go       # No-op stubs (build tag: !faultinject)
│   │   └── runner_test.go
│   ├── verify/
│   │   ├── load.go                      # Manifest load + schema check
│   │   ├── rehash.go                    # Per-file rehash + compare
│   │   ├── verify.go                    # Top-level verify orchestration
│   │   └── verify_test.go
│   └── plain/
│       ├── renderer.go                  # Plain-text progress + summary
│       └── renderer_test.go
├── test/
│   ├── e2e/
│   │   ├── helpers.go                   # hdiutil image creation, fixture copy
│   │   ├── init_test.go                 # AC-1, AC-2
│   │   ├── backup_test.go               # AC-3
│   │   ├── verify_test.go               # AC-9, AC-10
│   │   ├── lock_test.go                 # AC-11, AC-12
│   │   ├── delete_flag_test.go          # AC-14
│   │   ├── non_tty_test.go              # AC-15
│   │   ├── atomic_gate_test.go          # AC-4 (uses faultinject)
│   │   ├── mutation_test.go             # AC-5, AC-6
│   │   ├── delete_confirm_test.go       # AC-7, AC-8 (text-mode)
│   │   └── crash_resume_test.go         # AC-13
│   └── fixtures/
│       ├── tiny/                        # 10 files
│       ├── realistic/                   # ~1000 files
│       └── pathological/                # Unicode, long paths, special chars
└── scripts/
    ├── build-rsync.sh                   # Pinned rsync 3.x universal2 build
    └── golangci-version.txt             # Pinned linter version
```

Approximately 70 source/test files. ~50 tasks below.

---

## API Contracts, Conventions, and Cross-Task Anchors

> **Amendment 2026-06-03 from Plan 1 multi-hat review (9 hats).** This section is the canonical source-of-truth for cross-task type contracts, event taxonomies, and conventions. Tasks 10-55 expand against this anchor, not against each other.

### Strategic decisions locked (PS1-PS4)

- **PS1: HMAC threat model.** Manifest HMAC is a **keyed integrity checksum** (defends against accidental corruption + bit-rot + writer bugs), NOT authentication against an adversary with USB write access. Invariant #33 reworded in spec. Canonical encoding is **length-prefixed**, not pipe-separated.
- **PS2: e2e in CI is split into two jobs.** `e2e-fast` (init, backup-happy, verify, lock) gates PR merge. `e2e-safety` (atomic gate, mutation, crash-resume, anything using faultinject + hdiutil) runs on every PR in parallel; gates `main` push but not PR merge (allows flake tolerance on hdiutil).
- **PS3: `plain.Renderer` interface shape is event-bus, not verb-per-phase.** `Renderer.OnEvent(ev UIEvent) error`. Plain implements via `fmt.Fprintln`; TUI (Plan 2) implements via `tea.Msg` translation. Runner is renderer-agnostic.
- **PS4: `runner.UIEvent` is distinct from `state.Event`.** State events go to `events.ndjson` (durable, schema-versioned). UI events go to renderer (transient, never persisted). Some runner-emitted moments produce both.

### Cross-task type contracts

These signatures are pinned. Subagents expanding any task MUST conform.

```go
// internal/state (Tasks 5-8)

type Event struct { /* persisted audit; see Task 5 */ }
type EventStore interface {
    Append(ctx context.Context, ev Event) error  // durable to page cache; NOT to disk
    Checkpoint(ctx context.Context) error         // fsync; called at phase boundaries
    Close() error
}

type ManifestEntry struct { /* see Task 6, HMAC over length-prefixed canonical */ }
type ManifestStore interface {
    AppendEntry(ctx context.Context, e ManifestEntry) error  // single-writer; safe for one T2 goroutine only
    Gzip(ctx context.Context) error                          // T4 finalization
}

type StartedRun struct { /* see Task 7 */ }
type FinishedRun struct { /* see Task 7 */ }
type RunLogStore interface {
    AppendStarted(ctx context.Context, s StartedRun) error
    AppendFinished(ctx context.Context, f FinishedRun) error
    Checkpoint(ctx context.Context) error
    Close() error
}

type VersionFile struct { /* see Task 8 */ }
// Init (only): InitVersionFile(path, version string) (VersionFile, error)
// Run-time: ReadVersionFile(path) (VersionFile, error)  --  fails closed on parse error or schema mismatch

// internal/preflight (Tasks 15-20)

type Lock struct {
    PID            int       `json:"pid"`
    StartTimeUnix  int64     `json:"start_time_unix"`
    HostUUID       string    `json:"host_uuid"`
    Nonce          string    `json:"nonce"`
    VolumeUUID     string    `json:"volume_uuid"`  // pinned via PS-spec invariant #30
}

// Acquire is the implemented name (Go style: avoid package-name stuttering at
// lock.AcquireLock vs lock.Acquire). volumeUUID is captured at T0 from
// drives.queryVolume / volume_uuid.Capture and stored in the Lock JSON for
// per-phase invariant #30 cross-checks.
func Acquire(ctx context.Context, lockFilePath, volumeUUID string) (*LockHandle, error)
func (h *LockHandle) Release() error

// Options configure a Preflight invocation.
type Options struct {
    DestRoot     string  // absolute path to USB mountpoint; required
    SkipCodesign bool    // test-only escape hatch; release builds never set this true
}

// PreflightContext is the populated output of a successful Preflight call.
// Stored by the runner; passed to every phase. Release the lock (and any
// future-added resources) via pc.Release() when done; typically `defer` or
// `t.Cleanup` immediately after a successful Preflight.
//
// Reconciled with internal/preflight/preflight.go on 2026-06-04 after the
// Task 20 implementation produced a richer shape than this section had
// originally specified. The richer shape carries the per-component (dev,ino)
// baseline and the captured-volume metadata that the runner needs at every
// phase boundary, so updating the contract here matches what Task 22 will
// consume.
type PreflightContext struct {
    LockHandle      *lock.LockHandle
    SymlinkBaseline *symlink.Baseline       // (dev,ino) per path component at T0
    VolumeUUID      *volume_uuid.Captured   // struct (Mountpoint + UUID), not bare string
    Filesystem      *filesystem.Info
    DestRoot        string                  // resolved, symlink-free, absolute
    DotDir          string                  // <DestRoot>/.flashbackup
    Hostname        string                  // for namespace prefix
    Username        string
    VersionFile     state.VersionFile       // loaded HMAC key for manifest
    RsyncPath       string                  // SHA256-verified path to extracted rsync
}

func Preflight(ctx context.Context, opts Options) (*PreflightContext, error)
func (pc *PreflightContext) VerifyVolumeUnchanged(ctx context.Context) error  // call at every phase boundary
func (pc *PreflightContext) Release() error                                   // idempotent; releases lock and other resources

// internal/runner (Tasks 21-29)

type Phase string
const (
    PhasePreflight  Phase = "T0"
    PhaseEnumerate  Phase = "T0+"
    PhaseTransfer   Phase = "T1"
    PhaseHashCompare Phase = "T2"
    PhaseDelete     Phase = "T3"
    PhaseFinalize   Phase = "T4"
)

type Mode string
const (
    ModeCopy Mode = "copy"
    ModeMove Mode = "move"
)

type RunOptions struct {
    Profile     profiles.Profile
    DestRoot    string
    Mode        Mode
    DryRun      bool
    Delete      bool  // mirror mode: remove FB-written paths absent from source
    UIRenderer  Renderer  // PS3: event bus interface lives in runner/types (see below) to avoid an import cycle with internal/plain; nil means no UI events emitted
}

type RunResult struct {
    RunID                         string
    StartedAt, FinishedAt         time.Time
    FilesTotal, FilesSucceeded    int
    FilesFailed                   int
    BytesTotal                    int64
    DeletionsSkippedDueToMutation int
    ExitStatus                    string  // "ok" | "partial" | "copy_only_aborted_delete" | "crashed_resumed" | "preflight_failed"
}

// Renderer is the UIEvent sink, owned by the runner package (the consumer
// of the interface). internal/plain (Task 33) provides the terminal
// implementation; a future internal/tui (Plan 2) provides the Bubble Tea
// one. Both packages import runner/types; the runner does NOT import them.
// This placement avoids an import cycle and follows the Go idiom that
// interfaces live with their consumers.
type Renderer interface {
    OnEvent(ctx context.Context, ev UIEvent) error
}

func Run(ctx context.Context, opts RunOptions) (*RunResult, error)

// PS4: UIEvent is renderer-facing, distinct from state.Event (persisted)
type UIEvent struct {
    Kind      UIEventKind
    Phase     Phase
    Path      string         // file path when relevant
    Progress  *ProgressInfo  // bytes done / total when relevant
    Status    string         // status string for completion events
    Err       error          // populated for failure events
    Timestamp time.Time
}

type UIEventKind string
const (
    UIEvtPhaseStarted    UIEventKind = "phase_started"
    UIEvtPhaseCompleted  UIEventKind = "phase_completed"
    UIEvtFileStarted     UIEventKind = "file_started"
    UIEvtFileCompleted   UIEventKind = "file_completed"
    UIEvtFileFailed      UIEventKind = "file_failed"
    UIEvtProgress        UIEventKind = "progress"      // bytes-level throughput tick
    UIEvtPrompt          UIEventKind = "prompt"        // request user input (DELETE confirm)
    UIEvtSummary         UIEventKind = "summary"       // final run summary
)

type ProgressInfo struct {
    BytesDone, BytesTotal int64
    FilesDone, FilesTotal int
    CurrentFile           string
    BytesPerSec           int64
    ETASeconds            int
}

// internal/plain (Task 33): implements runner.Renderer; the interface itself
// lives in runner/types above. NewPlainRenderer returns a value satisfying
// runner.Renderer for terminal output.

// The return type is `types.Renderer` (interface in internal/runner/types);
// the `runner.` prefix above is shorthand for readability. Code uses the
// types-package import. Task 33 review clarification, 2026-06-05.
func NewPlainRenderer(out io.Writer, isTTY bool) types.Renderer

// internal/verify (Tasks 30-32)

type VerifyOptions struct {
    RunID         string  // "" means latest
    All           bool
    CheckExtras   bool
    UIRenderer    runner.Renderer  // can be nil; same interface as RunOptions.UIRenderer
}

type VerifyResult struct {
    RunID                  string
    FilesChecked           int
    FilesVerified          int
    FilesHashMismatch      int
    FilesIntegrityFailed   int  // PS1 / AC-19: manifest line tampered (HMAC failed)
    FilesMissing           int
    FilesSizeMismatch      int
    FilesUnreadable        int
    FilesExtraInDest       int
    DurationSeconds        int
    BytesRead              int64
    ExitStatus             string  // "ok" | "integrity_failed" | "preflight_failed"
}

func Verify(ctx context.Context, opts VerifyOptions) (*VerifyResult, error)
```

### Canonical Event Kinds (`state.Event.Kind`)

Persisted to `events.ndjson`. Distinct from `runner.UIEventKind` above.

**`phase_aborted` is best-effort, not guaranteed.** When the audit store (EventStore) is itself the failure mode (Append or Checkpoint returned an error mid-phase), the runner intentionally does NOT attempt another Append for `phase_aborted` — re-Appending to a just-failed store risks compounding the original error and writing a misleading second-order failure. In that case the on-disk trail terminates at the last successful `file_*` line. Recovery treats any open `phase_started` without a matching closing event (`phase_completed` or `phase_aborted`) as a crashed phase and finalizes the run as `crashed_resumed` on next preflight (invariant #10 two-line model + `runs.ndjson` absent "finished" line).

| Kind | Phase | When emitted | Required `Details` fields |
|---|---|---|---|
| `phase_started` | any | At entry to each phase | none |
| `phase_completed` | any | At successful exit of each phase | `duration_ms` |
| `phase_aborted` | any | When phase exits with error | `duration_ms`, `error` |
| `lock_acquired` | T0 | Lock taken | `pid`, `host_uuid`, `nonce` |
| `lock_stale_detected` | T0 | Stale lock recovered | `prior_pid`, `prior_host_uuid` |
| `lock_contention` | T0 | Lock held by live process; aborted | `holder_pid`, `holder_age_seconds` |
| `filesystem_refused` | T0 | exFAT or other non-APFS/HFS+ detected | `filesystem_type` |
| `volume_uuid_changed` | any | VolumeUUID re-verification failed | `expected`, `got` |
| `file_enumerated` | T0+ | Every candidate file (high volume; consider sampling for huge trees) | `path`, `size`, `mtime_ns` |
| `transfer_started` | T1 | rsync subprocess launched | `command_line`, `file_count` |
| `transfer_completed` | T1 | rsync exited 0 | `exit_code`, `duration_ms` |
| `transfer_failed` | T1 | rsync exited non-zero | `exit_code`, `error` |
| `file_completed` | T2 | Per-file verified | `path`, `status`, `sha256_source` (truncated to 16 chars for log size) |
| `hash_mismatch` | T2 | Source-hash != dest-hash | `path`, `sha256_source_prefix`, `sha256_dest_prefix` |
| `source_mutated` | T2 | mtime or size differs from T0 signature | `path` |
| `atomic_gate_blocked` | T3 | Move-mode skipped due to ANY T2 failure | `failed_count` |
| `delete_completed` | T3 | Per-file unlink succeeded | `path` |
| `delete_skipped_mutated` | T3 | T3 re-stat showed mutation; no delete | `path` |
| `delete_failed` | T3 | Unlink failed | `path`, `errno`, `error` |
| `manifest_finalized` | T4 | gzip closed + renamed | `tmp_path`, `final_path` |
| `run_finished` | T4 | Last event of run; `runs.ndjson` "finished" line written | `exit_status` |

**Optional `Details` extensions for `phase_completed`.** The required field is `duration_ms`. The following extra fields MAY appear on `phase_completed.Details` and downstream parsers (status JSON, support-bundle tooling) must accept them without erroring:
- `skipped: true` — phase entered but did no work (e.g., T3 in copy mode, T0+ with empty profile selection).
- `gate_blocked: true` and `failed_count: int` — T3 atomic gate fired (invariant #1); recorded on `phase_completed` rather than `phase_aborted` because the gate is a protective outcome of an otherwise-correct phase, not a phase failure.
- Phase-specific counters where useful (e.g., T2's `files_total`, `files_verified`).

### Fault-injection DSL grammar

Build tag `faultinject` (Task 28). Flag: `--inject <spec>`. Grammar:

```
<spec> ::= <action>:<keyword=value> [:<keyword=value>]*
<action> ::= corrupt | kill | mutate-source | unmount | disk-full | permission-denied
<keyword> ::= phase | file | after_pct | after_count

Examples:
  --inject=corrupt:phase=T1:file=foo.pdf
  --inject=kill:phase=T1:after_pct=50
  --inject=kill:phase=T2:file=foo.pdf
  --inject=kill:phase=T3:after_count=10
  --inject=mutate-source:phase=T2-pre:file=foo.pdf
  --inject=mutate-source:phase=T3-pre:file=foo.pdf
  --inject=unmount:phase=T1
  --inject=disk-full:phase=T1
  --inject=permission-denied:phase=T3:file=foo.pdf
```

Multiple `--inject` flags allowed; applied in order.

**Canonical phase wire strings:** `T1-pre`, `T1`, `T1-post`, `T2-pre`, `T2`, `T3-pre`, `T3`. Defined as `Point` constants in `internal/runner/faultinject.go` (Task 28); matching against `HookArgs.Phase` is plain string equality. Any new hook site must use an existing constant or extend the table here AND the constant set in the same change (drift trap). The DSL's `phase=` keyword is grammar-agnostic about the value; the canonical set documents what is actually wired into the runner code so e2e tests in Tasks 48-51b can rely on it. Added 2026-06-04 per Task 28 review.

### `flashbackup status --json` schema (locked)

```json
{
  "v": 1,
  "flashbackup_version": "0.1.0-core",
  "rsync_version": "3.4.1",
  "usb_path": "/Volumes/FLASHBKP",
  "usb_volume_uuid": "ABCD-EF01-2345-6789-ABCDEF012345",
  "usb_filesystem": "APFS",
  "usb_bytes_free": 132000000000,
  "usb_bytes_total": 487000000000,
  "namespace_prefix": "macbook-mahesh",
  "lock_status": "free",
  "last_run": {
    "run_id": "2026-06-03T1430Z-a7f2",
    "started_at": "2026-06-03T14:30:00Z",
    "finished_at": "2026-06-03T14:48:24Z",
    "mode": "copy",
    "profile": "my-docs",
    "exit_status": "ok",
    "files_total": 1234,
    "files_succeeded": 1234,
    "files_failed": 0,
    "bytes_total": 982000000
  },
  "last_verify": {
    "verify_id": "2026-06-04T0900Z-c9f4",
    "verified_at": "2026-06-04T09:00:00Z",
    "for_run_id": "2026-06-03T1430Z-a7f2",
    "exit_status": "ok",
    "files_verified": 1234,
    "files_integrity_failed": 0,
    "files_hash_mismatch": 0
  },
  "retained_runs": 1,
  "retention_limit": 10
}
```

**Optional fields (refined 2026-06-05 per Task 39 review I2 / A1):**
- `last_run` and `last_verify` are **omitted entirely** when no run or verify has happened on the USB (fresh init).
- `last_run.profile` is **omitted** when the run had no named profile (ad-hoc invocation). The on-disk `FinishedRun.Profile` already uses `omitempty`; the JSON status surface mirrors that shape.

### Conventions

- **Go file naming:** `snake_case.go` (Go convention). On-disk state files: `kebab-case.{ndjson,json}` (per spec invariant #45 / project convention).
- **Conventional Commits:** `feat:`, `fix:`, `chore:`, `build:`, `docs:`, `test:`, `refactor:` (no others). Breaking changes use `!:` suffix.
- **Error wrapping:** wrap once at the package boundary with `fmt.Errorf("<verb> <noun>: %w", err)`. Don't double-wrap. User-facing translation happens at the cmd/CLI render edge via the message catalog (Task 53), not at call sites.
- **Comment policy:** comments explain WHY, not what. Reference locked invariants as `// invariant #N` so grep finds all enforcement sites.
- **Per-package `doc.go`:** every `internal/*` package has a `doc.go` containing: package purpose (1 sentence), invariants it enforces, state diagram or call-flow if non-trivial.
- **File modes:** `0600` for `version.json` (HMAC key); `0700` for `.flashbackup/` dir; `0644` for manifests + run logs; `0755` for the binary's extracted rsync.
- **Atomic writes:** any persistent state file uses `WriteTmpThenRename(path, data)` helper: write to `path+".tmp"`, fsync the file, rename to `path`, fsync the parent dir. Helper lives in `internal/state/atomic.go`.
- **`context.Context`:** every public function that does I/O, runs a subprocess, or hashes a file takes `ctx context.Context` as first parameter. SIGINT/SIGTERM cancellation via `signal.NotifyContext` in `cmd/flashbackup/main.go`.
- **Test discipline:** `t.Helper()` on every shared test helper; table-driven `t.Run` subtests when ≥2 cases exist; `t.TempDir()` for tmp paths; `t.Cleanup` for teardown; deterministic test names.

### Cheap-now-expensive-later (added 2026-06-03)

Decisions locked now because retrofit cost is high:

- `context.Context` plumbing (retrofit means touching every public func + every test)
- `EventStore.Checkpoint` separated from `Append` (retrofit means re-architecting durability semantics)
- `Renderer.OnEvent(UIEvent)` event-bus shape (retrofit in Plan 2 if shape is wrong)
- `runner.UIEvent` distinct from `state.Event` (retrofit means rewriting renderer + audit consumers)
- Length-prefixed HMAC canonical encoding (retrofit means re-MAC every existing manifest)
- 1 MB hash buffer + `sync.Pool` (retrofit means measurable GC pressure at scale)
- `version.json` fail-closed on parse error (retrofit means losing keyed integrity claims)
- File modes 0600/0700 for sensitive files (retrofit easy; pinning now prevents later audit fix-up)

### Deferred items (recorded with awareness)

Raised during multi-hat review but consciously deferred to Plan 2 or v0.2:

- Mutation testing (`go-mutesting`) infrastructure
- Benchmark regression gate with `benchstat` + baseline-file
- Cross-version macOS 13/16 CI matrix
- SHA pinning of GitHub Actions
- Concurrent T2 hashing of small files
- Statistical model for bench gate (median-of-N + Mann-Whitney)
- TUI Recent-files render cost cap
- `--bell` audio cue flag
- `flashbackup glossary` subcommand
- HMAC key rotation policy + `--rotate-key` command
- Manifest at-rest encryption
- Signed/chained audit log
- `huge/` 10 GB fixture
- Renovate / Dependabot setup
- Mid-execution re-review checkpoint after Task 29
- `--quiet` mode semantics + tests
- Mouse support in TUI (Plan 2)
- macOS Increase-Contrast detection (Plan 2)
- i18n / non-English message catalog (Plan 2)
- `flashbackup --license` subcommand (LICENSE file at root suffices)
- `runs.ndjson` rotation at 10 MB
- `.superpowers/` git-attribute hiding
- Generic `Appender[T]` collapsing three Store interfaces
- macOS dev-beta soak job
- Bus-factor / Developer-ID-lapse fallback path (covered by spec invariant #54 but not implemented in Plan 1)

---

## Task 1: Repo init + module skeleton + LICENSE + .gitignore

**Files:**
- Create: `go.mod`, `.gitignore`, `LICENSE`, `README.md`, `CONTRIBUTING.md`, `SECURITY.md`, `CODE_OF_CONDUCT.md`

- [ ] **Step 1: Initialize git + Go module**

```bash
cd /Users/maheshm/Documents/1-AI-Projects/Utilities/Backup-Mac  # local repo root (local dir kept as Backup-Mac; GitHub repo is Backup-Pro)
git init -b main
git remote add origin https://github.com/maheshmirchandani/Backup-Pro.git
go mod init github.com/maheshmirchandani/Backup-Pro
go mod edit -go=1.22  # minimum version; installed Go is 1.26+
```

Note: binary name stays `flashbackup` (set via `go build -o flashbackup ./cmd/flashbackup` in the Makefile). Module path matches GitHub repo URL per Go convention.

- [ ] **Step 2: Create `.gitignore`**

```
# Binaries
flashbackup
flashbackup-*
*.exe
*.dll
*.so
*.dylib

# Build / test artifacts
*.test
*.out
*.prof
coverage.out
dist/

# Local config
.env
.env.local

# Editor
.idea/
.vscode/
*.swp

# Brainstorm artifacts
.superpowers/
```

- [ ] **Step 3: Create `LICENSE` (GPLv3 full text)**

Download the canonical text:
```bash
curl -fsSL https://www.gnu.org/licenses/gpl-3.0.txt -o LICENSE
head -1 LICENSE
# Expected: "                    GNU GENERAL PUBLIC LICENSE"
```

- [ ] **Step 4: Create skeleton `README.md`**

```markdown
# FlashBackup

Portable USB-runnable macOS backup utility with strict integrity guarantees.

**Status:** v0.1 in development. Not yet released.

## What it does

- Backs up files from your Mac to a USB drive (copy or atomic move).
- Verifies every byte via SHA256 source+dest comparison.
- Refuses to delete source files if any single file failed verification.
- Re-checks integrity on demand via `flashbackup verify`.

## Requirements

- macOS 13 (Ventura) or newer.
- USB drive formatted as APFS or HFS+ (not exFAT; init will refuse and print a reformat recipe).
- Apple Silicon or Intel Mac (universal2 binary).

## Quickstart (placeholder; Plan 2 will polish)

```
flashbackup init /Volumes/MYBKP
flashbackup profiles new my-docs --source ~/Documents
flashbackup backup my-docs
flashbackup verify
```

## License

GPLv3. See [LICENSE](LICENSE) and [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md).

## Source

Design spec: [docs/specs/2026-06-03-1532-flashbackup-design.md](docs/specs/2026-06-03-1532-flashbackup-design.md).
```

- [ ] **Step 5: Create skeleton `CONTRIBUTING.md`**

```markdown
# Contributing to FlashBackup

Thanks for considering a contribution.

## Scope

FlashBackup targets a narrow wedge: portable USB-runnable macOS backup with strict integrity guarantees. Bug fixes always welcome. New features must align with the wedge; off-wedge proposals get a friendly "please fork" response.

## Building

See `Makefile`. `make build` for a local debug binary, `make ci-local` to mirror CI.

## Reporting bugs

Bugs go in GitHub Issues. Data-loss bugs are Sev1: use the "data-loss-report" issue template; MM will respond within 24 hours.
```

- [ ] **Step 6: Create skeleton `SECURITY.md`**

```markdown
# Security Policy

## Reporting a vulnerability

Email mahesh.mirchandani@gmail.com with details. Encrypted via PGP preferred (key in repo root: `mm-public.asc`).

90-day coordinated disclosure window. Severe data-loss vulnerabilities get a hot-patch release; lesser issues bundle into the next regular release.

Out of scope:
- Cosmic-ray bit flips, RAM tampering.
- Attacker with physical USB write access AFTER backup completed (the USB itself is the trust boundary; manifests are HMAC-authenticated per invariant #33 but not signed against a key off-USB).
- Network attacks (FlashBackup makes no network calls at runtime).
```

- [ ] **Step 7: Create `CODE_OF_CONDUCT.md` (Contributor Covenant 2.1)**

```bash
curl -fsSL https://www.contributor-covenant.org/version/2/1/code_of_conduct/code_of_conduct.md -o CODE_OF_CONDUCT.md
head -3 CODE_OF_CONDUCT.md
# Expected: header lines from Contributor Covenant
```

Edit the placeholder contact line at the bottom to use mahesh.mirchandani@gmail.com.

- [ ] **Step 8: Commit**

```bash
git add go.mod .gitignore LICENSE README.md CONTRIBUTING.md SECURITY.md CODE_OF_CONDUCT.md
git commit -m "chore: initial repo skeleton with GPLv3 license and conventions"
```

---

## Task 2: Makefile + golangci-lint config + CI workflow

**Files:**
- Create: `Makefile`, `.golangci.yml`, `.github/workflows/ci.yml`, `scripts/golangci-version.txt`

- [ ] **Step 1: Create `Makefile`**

Amended 2026-06-03 (multi-hat round): adds `SOURCE_DATE_EPOCH` for reproducibility, e2e split per PS2, coverage gates, symbol-scan release gate, `debug-bundle`, `test-pkg`.

```makefile
.PHONY: build build-faultinject test test-pkg test-faultinject e2e-fast e2e-safety bench bench-baseline coverage verify-release snapshot-update lint ci-local debug-bundle clean

SOURCE_DATE_EPOCH ?= $(shell git log -1 --format=%ct 2>/dev/null || echo 0)
export SOURCE_DATE_EPOCH

GOFLAGS := -trimpath -buildvcs=false -ldflags "-s -w -buildid="

build:
	go build $(GOFLAGS) -tags release -o flashbackup ./cmd/flashbackup

build-faultinject:
	go build $(GOFLAGS) -tags faultinject -o flashbackup-faultinject ./cmd/flashbackup

test:
	go test -timeout=2m ./...

# Per-package test for fast TDD loop:  make test-pkg PKG=./internal/state
test-pkg:
	go test -timeout=2m $(PKG)

test-faultinject:
	go test -timeout=5m -tags faultinject ./test/e2e/...

# e2e split per PS2:
#  e2e-fast: gates PR merge; happy paths only
#  e2e-safety: gates main push; faultinject + hdiutil-heavy; flaky-tolerant on PR
e2e-fast:
	FLASHBACKUP_E2E=1 go test -timeout=5m -run "Init|BackupHappy|VerifyIntact|LockContention|NonTTY" ./test/e2e/...

e2e-safety:
	FLASHBACKUP_E2E=1 go test -timeout=15m -tags faultinject -run "AtomicGate|Mutation|CrashResume|DeleteFlag|DeleteConfirm|TamperedManifest" ./test/e2e/...

bench:
	go test -bench=. -benchmem -count=5 -timeout=10m ./internal/hash ./internal/state ./internal/runner

bench-baseline:
	go test -bench=. -benchmem -count=5 ./internal/hash ./internal/state ./internal/runner | tee testdata/benchmarks-baseline.txt

# Per-package coverage gates: runner/state/hash/preflight >=80% line per invariant #42
# Use trailing slash anchor to avoid substring matches (e.g., internal/state vs internal/statemachine)
coverage:
	go test -coverprofile=coverage.out -covermode=atomic ./internal/runner/... ./internal/hash/... ./internal/state/... ./internal/preflight/...
	@for pkg in runner hash state preflight; do \
		pct=$$(go tool cover -func=coverage.out | grep -E "internal/$$pkg(/|\.go:)" | awk '{sum+=$$3+0;n+=1} END {if(n>0)printf "%.1f",sum/n;else print "0"}'); \
		echo "$$pkg coverage: $$pct%"; \
		awk -v p=$$pct 'BEGIN{if(p+0 < 80){exit 1}}' || { echo "FAIL: $$pkg below 80%"; exit 1; }; \
	done

# Release-gate symbol-scan per invariant #35: assert no faultinject hooks leak into release build
# Anchor the regex with word-boundary-like prefix to avoid false positives on legitimate
# names containing the substring "Inject" (e.g., a future DI helper).
verify-release: build
	@if go tool nm flashbackup | grep -E '(^|[._/])faultinject' >/dev/null 2>&1; then \
		echo "FAIL: faultinject symbols found in release binary"; exit 1; \
	fi
	@echo "OK: release binary clean of faultinject symbols"

# Debug bundle for friend bug reports: make debug-bundle RUN=2026-06-03T1430Z-a7f2 USB=/Volumes/FLASHBKP
debug-bundle:
	@test -n "$(RUN)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	@test -n "$(USB)" || (echo "usage: make debug-bundle RUN=<run-id> USB=<usb-path>"; exit 1)
	tar -czvf flashbackup-debug-$(RUN).tgz \
		-C $(USB)/.flashbackup runs/$(RUN) version.json \
		-C $(USB)/.flashbackup runs.ndjson
	@echo "wrote flashbackup-debug-$(RUN).tgz"

lint:
	@unfmt=$$(gofmt -s -l . 2>/dev/null); \
		if [ -n "$$unfmt" ]; then \
			echo "FAIL: unformatted files (run 'gofmt -s -w .'):"; \
			echo "$$unfmt"; \
			exit 1; \
		fi
	go vet ./...
	golangci-lint run

ci-local: lint test test-faultinject e2e-fast e2e-safety verify-release coverage

clean:
	rm -f flashbackup flashbackup-faultinject coverage.out
	rm -rf dist/
```

Note: `make test` excludes `./test/...` by design; e2e runs via `make e2e-fast` / `make e2e-safety` only (requires `hdiutil`, takes seconds to minutes per fixture).

- [ ] **Step 2: Pin golangci-lint version**

```bash
echo "1.61.0" > scripts/golangci-version.txt
```

- [ ] **Step 3: Create `.golangci.yml`**

```yaml
run:
  timeout: 5m
  go: '1.22'

linters:
  enable:
    - errcheck
    - gosec
    - gosimple
    - govet
    - ineffassign
    - staticcheck
    - unused

linters-settings:
  gosec:
    excludes:
      - G304  # File path provided as taint input (we control source paths)
```

- [ ] **Step 4: Create CI workflow `.github/workflows/ci.yml`**

Amended 2026-06-03 (multi-hat round): adds `permissions:` restriction (CISO finding), e2e-fast/e2e-safety split (PS2), coverage + symbol-scan gates, Go module cache. TODO Plan 2: SHA-pin action versions per spec invariant #39.

```yaml
name: CI

on:
  push:
    branches: [main]
  pull_request:

permissions:
  contents: read

jobs:
  test:
    runs-on: macos-14
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-
      - name: Install golangci-lint
        run: |
          VERSION=$(cat scripts/golangci-version.txt)
          curl -sSfL https://raw.githubusercontent.com/golangci/golangci-lint/master/install.sh | sh -s -- -b $(go env GOPATH)/bin v$VERSION
      - name: Lint
        run: make lint
      - name: Test (unit + integration)
        run: make test
      - name: Test (faultinject)
        run: make test-faultinject
      - name: Coverage gate
        run: make coverage
      - name: Release symbol-scan
        run: make verify-release

  e2e-fast:
    needs: test
    runs-on: macos-14
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-
      - name: e2e-fast (PR-gating)
        run: make e2e-fast

  e2e-safety:
    needs: test
    runs-on: macos-14
    # PS2: e2e-safety is flaky-tolerant on PR; only blocks main push
    continue-on-error: ${{ github.event_name == 'pull_request' }}
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - uses: actions/cache@v4
        with:
          path: ~/go/pkg/mod
          key: ${{ runner.os }}-go-${{ hashFiles('**/go.sum') }}
          restore-keys: |
            ${{ runner.os }}-go-
      - name: e2e-safety (faultinject + hdiutil)
        run: make e2e-safety

  bench:
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    runs-on: macos-14
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version: '1.22'
      - name: Bench (main only; artifact upload, no gate yet)
        run: make bench | tee bench.txt
      - uses: actions/upload-artifact@v4
        with:
          name: bench-${{ github.sha }}
          path: bench.txt
```

- [ ] **Step 5: Commit**

```bash
git add Makefile .golangci.yml .github/workflows/ci.yml scripts/golangci-version.txt
git commit -m "build: add Makefile, golangci-lint config, and CI workflow"
```

---

## Task 3: `internal/paths` namespace prefix

**Files:**
- Create: `internal/paths/namespace.go`, `internal/paths/namespace_test.go`

Covers invariants #5 (auto-namespace), #15 (shared paths package).

- [ ] **Step 1: Write failing test**

```go
// internal/paths/namespace_test.go
package paths

import (
	"path/filepath"
	"testing"
)

func TestNamespaced_HappyPath(t *testing.T) {
	got := Namespaced("/Volumes/USB", "macbook", "alice", "Documents/foo.pdf")
	want := filepath.Join("/Volumes/USB", "macbook-alice", "Documents/foo.pdf")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSourceFromNamespaced_RoundTrip(t *testing.T) {
	dest := Namespaced("/Volumes/USB", "macbook", "alice", "Documents/foo.pdf")
	got, err := SourceFromNamespaced(dest, "/Volumes/USB", "macbook", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Documents/foo.pdf"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrefix_StripsSpecialChars(t *testing.T) {
	// Hostnames can contain dots (e.g. "macbook.local"); usernames are usually safe.
	got := Prefix("macbook.local", "alice")
	want := "macbook-local-alice"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

```bash
go test ./internal/paths/
# Expected: ./namespace_test.go: undefined: Namespaced, SourceFromNamespaced, Prefix
```

- [ ] **Step 3: Implement `internal/paths/namespace.go`**

```go
// Package paths computes the namespace prefix used to distinguish multiple
// machines/users sharing the same USB destination. Invariants #5 + #15.
package paths

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Prefix returns the namespace directory name "<safe-hostname>-<safe-username>".
// macOS hostnames may contain dots ("macbook.local"); replaced with hyphens for
// filesystem-friendliness.
func Prefix(hostname, username string) string {
	safeHost := strings.ReplaceAll(hostname, ".", "-")
	safeUser := strings.ReplaceAll(username, ".", "-")
	return safeHost + "-" + safeUser
}

// Namespaced returns the full destination path:
//   <destRoot>/<Prefix(hostname,username)>/<srcRelative>
func Namespaced(destRoot, hostname, username, srcRelative string) string {
	return filepath.Join(destRoot, Prefix(hostname, username), srcRelative)
}

// SourceFromNamespaced strips the destination root and namespace prefix,
// returning the source-relative path. Returns an error if the destPath does
// not have the expected prefix.
func SourceFromNamespaced(destPath, destRoot, hostname, username string) (string, error) {
	prefix := filepath.Join(destRoot, Prefix(hostname, username))
	rel, err := filepath.Rel(prefix, destPath)
	if err != nil {
		return "", fmt.Errorf("path %q is not under namespace %q: %w", destPath, prefix, err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes namespace %q", destPath, prefix)
	}
	return rel, nil
}
```

- [ ] **Step 4: Run test (expect PASS)**

```bash
go test ./internal/paths/
# Expected: PASS, 3 tests
```

- [ ] **Step 5: Commit**

```bash
git add internal/paths/
git commit -m "feat(paths): add namespace prefix for multi-machine destination paths"
```

---

## Task 4: `internal/hash` streaming SHA256

**Files:**
- Create: `internal/hash/sha256.go`, `internal/hash/sha256_test.go`

Covers invariant #1 (hash source+dest).

**AMENDMENT 2026-06-03 (multi-hat round):**
- Reorder steps so `go get pgregory.net/rapid` runs BEFORE writing `sha256_property_test.go` (Step 5 install moves to between Steps 4 and 5; otherwise `go test` in Step 4 fails compiling the property test file).
- Step 1 test: delete the misleading `want := hex.EncodeToString(sha256.New().Sum(data)[len(data):])` line; only the stdlib-reference computation that follows is correct.
- Step 3 implementation: change `bufSize` from `64 * 1024` to `1 << 20` (1 MB) for modern APFS performance; use a package-level `sync.Pool` to avoid allocating the buffer per call.
- Add `ctx context.Context` as first parameter per the API Contracts convention (`StreamSHA256(ctx context.Context, r io.Reader) (string, int64, error)`).
- Add `BenchmarkStreamSHA256_LargeFile` asserting ≥ 1 GB/s on Apple Silicon (spec SLO).

- [ ] **Step 1: Write failing test**

```go
// internal/hash/sha256_test.go
package hash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestFileSHA256_Stream(t *testing.T) {
	data := []byte("hello world\n")
	want := hex.EncodeToString(sha256.New().Sum(data)[len(data):])
	// Use stdlib for ground truth
	h := sha256.New()
	h.Write(data)
	want = hex.EncodeToString(h.Sum(nil))

	got, n, err := StreamSHA256(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("StreamSHA256 error: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("byte count got %d want %d", n, len(data))
	}
	if got != want {
		t.Errorf("hash got %s want %s", got, want)
	}
}

func TestFileSHA256_Empty(t *testing.T) {
	got, n, err := StreamSHA256(bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("StreamSHA256 error: %v", err)
	}
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want || n != 0 {
		t.Errorf("got hash=%s n=%d, want hash=%s n=0", got, n, want)
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

```bash
go test ./internal/hash/
# Expected: undefined: StreamSHA256
```

- [ ] **Step 3: Implement `internal/hash/sha256.go`**

```go
// Package hash provides streaming SHA256 with constant memory regardless of
// input size. Invariant #1: source and destination both hashed; manifest
// records sha256_source captured at read time.
package hash

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
)

const bufSize = 64 * 1024 // 64KB matches typical filesystem block sizes

// StreamSHA256 reads r to EOF, returning the hex-encoded SHA256 and total
// bytes read. Constant memory; no buffering of the full input.
func StreamSHA256(r io.Reader) (digest string, n int64, err error) {
	h := sha256.New()
	written, err := io.CopyBuffer(h, r, make([]byte, bufSize))
	if err != nil {
		return "", written, err
	}
	return hex.EncodeToString(h.Sum(nil)), written, nil
}
```

- [ ] **Step 4: Run test (expect PASS)**

```bash
go test ./internal/hash/
# Expected: PASS, 2 tests
```

- [ ] **Step 5: Add property-based test (invariant #41)**

```go
// internal/hash/sha256_property_test.go
package hash

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"pgregory.net/rapid"
)

func TestSHA256_ChunkBoundaryInvariance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		data := rapid.SliceOfN(rapid.Byte(), 0, 200000).Draw(t, "data")

		// Compute reference hash via stdlib
		h := sha256.New()
		h.Write(data)
		want := hex.EncodeToString(h.Sum(nil))

		// Compute via StreamSHA256 (which uses io.CopyBuffer internally)
		got, _, err := StreamSHA256(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("error: %v", err)
		}

		if got != want {
			t.Fatalf("hash mismatch: stdlib=%s stream=%s len=%d", want, got, len(data))
		}
	})
}
```

Add dependency:
```bash
go get pgregory.net/rapid
```

- [ ] **Step 6: Run property test**

```bash
go test ./internal/hash/ -v -run Property
# Expected: PASS with rapid checks
```

- [ ] **Step 7: Commit**

```bash
git add internal/hash/ go.mod go.sum
git commit -m "feat(hash): add streaming SHA256 with chunk-boundary property test"
```

---

## Task 5: `internal/state` event store (NDJSON)

**Files:**
- Create: `internal/state/event.go`, `internal/state/event_ndjson.go`, `internal/state/event_test.go`

Covers invariant #17 (events.ndjson), parts of invariant #16 (state package).

**AMENDMENT 2026-06-03 (multi-hat round):**
- Per API Contracts: `Append(ctx context.Context, ev Event) error` is durable **to the page cache**, NOT to disk. Add explicit `Checkpoint(ctx context.Context) error` method that calls `f.Sync()`. Runner calls `Checkpoint` at phase boundaries only. Per-event fsync was killing throughput (5-15 min on 100K-file backup per Performance hat finding).
- Update interface contract docstring: "Append is durable to page cache; call Checkpoint at phase boundaries for disk-durability."
- File mode `0644` is fine for `events.ndjson` (no secrets).
- Add `BenchmarkEventAppend_WithoutSync` to make the fsync-amplification win visible.
- Add canonical Event Kind tests (every Kind from the canonical table renders to valid JSON; round-trips through NDJSON without loss).

- [ ] **Step 1: Write failing test for Event serialization**

```go
// internal/state/event_test.go
package state

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEvent_JSONShape(t *testing.T) {
	ev := Event{
		V:         1,
		Timestamp: time.Date(2026, 6, 3, 14, 30, 15, 0, time.UTC),
		Phase:     "T1",
		Kind:      "file_completed",
		Path:      "Documents/foo.pdf",
		Details:   map[string]any{"bytes": 12345},
	}
	got, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"timestamp":"2026-06-03T14:30:15Z","phase":"T1","kind":"file_completed","path":"Documents/foo.pdf","details":{"bytes":12345}}`
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestNDJSONEventStore_AppendAndClose(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ev := Event{V: 1, Timestamp: time.Unix(0, 0).UTC(), Phase: "T0", Kind: "started"}
	if err := store.Append(ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `{"v":1,"timestamp":"1970-01-01T00:00:00Z","phase":"T0","kind":"started"}` + "\n"
	if string(data) != want {
		t.Errorf("got %q\nwant %q", string(data), want)
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

```bash
go test ./internal/state/
# Expected: undefined: Event, NewNDJSONEventStore
```

- [ ] **Step 3: Implement `internal/state/event.go`**

```go
package state

import "time"

// Event records one structured event during a run. Invariant #17.
// Written to <USB>/.flashbackup/runs/<run-id>/events.ndjson, one per line.
type Event struct {
	V         int            `json:"v"`
	Timestamp time.Time      `json:"timestamp"`
	Phase     string         `json:"phase"`
	Kind      string         `json:"kind"`
	Path      string         `json:"path,omitempty"`
	Details   map[string]any `json:"details,omitempty"`
}

// EventStore is the audit storage abstraction. NDJSON in v0.1; future
// implementations may encrypt or aggregate without changing call sites.
//
// Contract:
//   - Append: durable on return (caller may rely on the event being on disk).
//   - Append is safe to call concurrently from multiple goroutines.
//   - Append must not be called after Close.
//   - Close is idempotent.
type EventStore interface {
	Append(ev Event) error
	Close() error
}
```

- [ ] **Step 4: Implement `internal/state/event_ndjson.go`**

```go
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type ndjsonEventStore struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	open bool
}

// NewNDJSONEventStore opens (or creates) path for append. Each Append
// writes one JSON line followed by '\n'. Caller must Close.
func NewNDJSONEventStore(path string) (EventStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open events.ndjson: %w", err)
	}
	return &ndjsonEventStore{f: f, enc: json.NewEncoder(f), open: true}, nil
}

func (s *ndjsonEventStore) Append(ev Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("event store closed")
	}
	if err := s.enc.Encode(ev); err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	// fsync for durability per contract
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("fsync events.ndjson: %w", err)
	}
	return nil
}

func (s *ndjsonEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.open = false
	return s.f.Close()
}
```

- [ ] **Step 5: Run test (expect PASS)**

```bash
go test ./internal/state/
# Expected: PASS, 2 tests
```

- [ ] **Step 6: Commit**

```bash
git add internal/state/event.go internal/state/event_ndjson.go internal/state/event_test.go
git commit -m "feat(state): add EventStore interface and NDJSON impl (invariant #17)"
```

---

## Task 6: `internal/state` manifest store (NDJSON + gzip stream)

**Files:**
- Create: `internal/state/manifest.go`, `internal/state/manifest_ndjson.go`, `internal/state/manifest_test.go`

Covers invariants #1 (sha256_source), #8 (gzip at rest), #13 (schema_version), #16 (state package), #57 (gzip-stream during T2), #33 (integrity HMAC, rewritten 2026-06-03).

**AMENDMENT 2026-06-03 (multi-hat round) : CRITICAL SECURITY:**

The `|`-separated canonical encoding in the original `computeHMAC` is **forgeable**: a path containing `|` can produce a colliding canonical string. **Replace with length-prefixed encoding.** Reference implementation:

```go
import "encoding/binary"

func canonical(e ManifestEntry) []byte {
    var buf bytes.Buffer
    binary.Write(&buf, binary.BigEndian, uint32(e.V))
    writeLenPrefixed(&buf, e.Path)
    binary.Write(&buf, binary.BigEndian, e.Size)
    binary.Write(&buf, binary.BigEndian, e.MtimeNS)
    writeLenPrefixed(&buf, e.SHA256Source)
    writeLenPrefixed(&buf, e.CopiedAt.UTC().Format(time.RFC3339Nano))
    writeLenPrefixed(&buf, string(e.Status))
    return buf.Bytes()
}

func writeLenPrefixed(buf *bytes.Buffer, s string) {
    binary.Write(buf, binary.BigEndian, uint32(len(s)))
    buf.WriteString(s)
}
```

Other amendments:

- Per API Contracts: `AppendEntry(ctx context.Context, e ManifestEntry) error` (add `ctx`).
- Single-writer contract documented: `ManifestStore` is safe for one T2 goroutine only; mutex defends against test-suite misuse, not designed for concurrent T2 (future parallel hashing uses actor pattern).
- Cache the HMAC: store `mac := hmac.New(sha256.New, key)` on the struct; call `mac.Reset()` between entries. ~10x fewer allocations per Performance hat.
- Build canonical bytes into a reusable `bytes.Buffer` on the struct; reset between entries. Avoids per-entry allocation.
- `gzip.NewWriter` → `gzip.NewWriterLevel(f, gzip.BestSpeed)`. Manifest NDJSON compresses well; level 1 gives 80-90% of default ratio at 3-5x throughput.
- Atomic write at finalize: use `WriteTmpThenRename` helper from `internal/state/atomic.go`; fsync parent dir after rename.
- Add adversarial HMAC test: two `ManifestEntry` with different `(Path, Size)` but colliding pipe-separated string must produce different HMACs under the new length-prefixed encoding. Specifically test path `"a|1"` size `0` vs path `"a"` size `1`.
- Add property-based test (`rapid.Check`) over arbitrary UTF-8 paths through `AppendEntry → Gzip → read → VerifyHMAC` round-trip.
- Add `IntegrityStatus` distinct from `FileStatus`: `integrity_verified` (HMAC OK), `integrity_failed` (HMAC mismatch). Used by AC-19 in verify.
- Per CISO finding: relabel inline comments to say "keyed integrity checksum" not "authenticated" (matches spec invariant #33 rewrite).

- [ ] **Step 1: Write failing test**

```go
// internal/state/manifest_test.go
package state

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestManifestEntry_JSONShape(t *testing.T) {
	e := ManifestEntry{
		V:            1,
		Path:         "Documents/foo.pdf",
		Size:         12345,
		MtimeNS:      1718000000000000000,
		SHA256Source: "abc123",
		CopiedAt:     time.Date(2026, 6, 3, 14, 30, 15, 0, time.UTC),
		Status:       StatusVerified,
		HMAC:         "deadbeef",
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"path":"Documents/foo.pdf","size":12345,"mtime_ns":1718000000000000000,"sha256_source":"abc123","copied_at":"2026-06-03T14:30:15Z","status":"verified","hmac":"deadbeef"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestNDJSONManifestStore_AppendThenGzip(t *testing.T) {
	dir := t.TempDir()
	uncompressed := filepath.Join(dir, "manifest.ndjson")
	store, err := NewNDJSONManifestStore(uncompressed, []byte("hmac-key"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	e := ManifestEntry{V: 1, Path: "foo.txt", Size: 5, MtimeNS: 0, SHA256Source: "deadbeef", CopiedAt: time.Unix(0, 0).UTC(), Status: StatusVerified}
	if err := store.AppendEntry(e); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Gzip(); err != nil {
		t.Fatalf("gzip: %v", err)
	}

	// Original .ndjson must be gone; .ndjson.gz must exist
	if _, err := os.Stat(uncompressed); !os.IsNotExist(err) {
		t.Errorf("uncompressed manifest still present: err=%v", err)
	}
	gzPath := uncompressed + ".gz"
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open gz: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	if !strings.Contains(string(data), `"path":"foo.txt"`) {
		t.Errorf("manifest missing entry; got %s", string(data))
	}
	if !strings.Contains(string(data), `"hmac":"`) {
		t.Errorf("manifest missing hmac; got %s", string(data))
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

```bash
go test ./internal/state/
# Expected: undefined: ManifestEntry, StatusVerified, NewNDJSONManifestStore
```

- [ ] **Step 3: Implement `internal/state/manifest.go`**

```go
package state

import "time"

// FileStatus is the T2 classification for one file in a run.
type FileStatus string

const (
	StatusVerified       FileStatus = "verified"
	StatusHashMismatch   FileStatus = "hash_mismatch"
	StatusSourceMutated  FileStatus = "source_mutated"
	StatusNotTransferred FileStatus = "not_transferred"
	StatusSourceUnreadable FileStatus = "source_unreadable"
	StatusDestUnreadable FileStatus = "dest_unreadable"
)

// DeletionStatus is the T3 outcome (move mode only).
type DeletionStatus string

const (
	DeletionDeleted          DeletionStatus = "deleted"
	DeletionSkippedMutated   DeletionStatus = "skipped_mutated"
	DeletionFailedImmutable  DeletionStatus = "failed_immutable"
	DeletionFailedPermission DeletionStatus = "failed_permission"
)

// ManifestEntry is one line in manifest.ndjson(.gz).
// HMAC authenticates (v, path, size, mtime_ns, sha256_source, copied_at, status)
// using a per-USB key from version.json (invariant #33).
type ManifestEntry struct {
	V              int             `json:"v"`
	Path           string          `json:"path"`
	Size           int64           `json:"size"`
	MtimeNS        int64           `json:"mtime_ns"`
	SHA256Source   string          `json:"sha256_source"`
	CopiedAt       time.Time       `json:"copied_at"`
	Status         FileStatus      `json:"status"`
	DeletionStatus DeletionStatus  `json:"deletion_status,omitempty"`
	HMAC           string          `json:"hmac,omitempty"`
}

// ManifestStore writes per-file entries during T2, then gzips the file at T4
// (or stream-writes via gzip directly, per invariant #57). Stream-writing
// approach: NewNDJSONManifestStore writes to a buffered gzip.Writer pointed at
// <path>.gz; Gzip() flushes and renames the .tmp.gz to .gz.
type ManifestStore interface {
	AppendEntry(e ManifestEntry) error
	Gzip() error
}
```

- [ ] **Step 4: Implement `internal/state/manifest_ndjson.go`**

```go
package state

import (
	"compress/gzip"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type ndjsonManifestStore struct {
	mu        sync.Mutex
	path      string
	hmacKey   []byte
	tmpFile   *os.File
	gzWriter  *gzip.Writer
	jsonEnc   *json.Encoder
	open      bool
	finalized bool
}

// NewNDJSONManifestStore opens path+".tmp.gz" for stream-gzip writing per
// invariant #57. Gzip() renames .tmp.gz to .gz when done.
func NewNDJSONManifestStore(path string, hmacKey []byte) (ManifestStore, error) {
	tmpPath := path + ".tmp.gz"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open manifest tmp: %w", err)
	}
	gz := gzip.NewWriter(f)
	return &ndjsonManifestStore{
		path:    path,
		hmacKey: hmacKey,
		tmpFile: f,
		gzWriter: gz,
		jsonEnc: json.NewEncoder(gz),
		open:    true,
	}, nil
}

func (s *ndjsonManifestStore) AppendEntry(e ManifestEntry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("manifest store closed")
	}
	// Compute HMAC over the canonical authenticated fields
	e.HMAC = s.computeHMAC(e)
	if err := s.jsonEnc.Encode(e); err != nil {
		return fmt.Errorf("encode manifest entry: %w", err)
	}
	return nil
}

func (s *ndjsonManifestStore) computeHMAC(e ManifestEntry) string {
	mac := hmac.New(sha256.New, s.hmacKey)
	canonical := fmt.Sprintf("%d|%s|%d|%d|%s|%s|%s",
		e.V, e.Path, e.Size, e.MtimeNS, e.SHA256Source,
		e.CopiedAt.UTC().Format("2006-01-02T15:04:05Z"), e.Status)
	mac.Write([]byte(canonical))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *ndjsonManifestStore) Gzip() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return nil
	}
	if err := s.gzWriter.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := s.tmpFile.Sync(); err != nil {
		return fmt.Errorf("fsync manifest: %w", err)
	}
	if err := s.tmpFile.Close(); err != nil {
		return fmt.Errorf("close manifest file: %w", err)
	}
	if err := os.Rename(s.path+".tmp.gz", s.path+".gz"); err != nil {
		return fmt.Errorf("rename manifest: %w", err)
	}
	// Remove any leftover uncompressed file from older versions
	_ = os.Remove(s.path)
	s.finalized = true
	s.open = false
	return nil
}
```

- [ ] **Step 5: Run test (expect PASS)**

```bash
go test ./internal/state/
# Expected: PASS, all tests
```

- [ ] **Step 6: Add HMAC verify helper + test**

```go
// internal/state/manifest_ndjson.go (append at end)

// VerifyHMAC returns true if e.HMAC matches the computed HMAC for the entry
// using hmacKey. Use this when reading manifests during verify.
func VerifyHMAC(e ManifestEntry, hmacKey []byte) bool {
	mac := hmac.New(sha256.New, hmacKey)
	canonical := fmt.Sprintf("%d|%s|%d|%d|%s|%s|%s",
		e.V, e.Path, e.Size, e.MtimeNS, e.SHA256Source,
		e.CopiedAt.UTC().Format("2006-01-02T15:04:05Z"), e.Status)
	mac.Write([]byte(canonical))
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(e.HMAC), []byte(expected))
}
```

```go
// internal/state/manifest_test.go (append)

func TestVerifyHMAC_TamperedRejected(t *testing.T) {
	key := []byte("test-key")
	e := ManifestEntry{V: 1, Path: "foo.txt", Size: 5, MtimeNS: 100, SHA256Source: "abc", CopiedAt: time.Unix(0, 0).UTC(), Status: StatusVerified}
	// Compute via store machinery
	store := &ndjsonManifestStore{hmacKey: key}
	e.HMAC = store.computeHMAC(e)

	if !VerifyHMAC(e, key) {
		t.Fatal("verify failed on clean entry")
	}

	// Tamper with the hash field
	tampered := e
	tampered.SHA256Source = "deadbeef"
	if VerifyHMAC(tampered, key) {
		t.Error("verify passed on tampered entry")
	}

	// Wrong key
	if VerifyHMAC(e, []byte("other-key")) {
		t.Error("verify passed with wrong key")
	}
}
```

- [ ] **Step 7: Run and commit**

```bash
go test ./internal/state/
git add internal/state/manifest.go internal/state/manifest_ndjson.go internal/state/manifest_test.go
git commit -m "feat(state): add ManifestStore with HMAC and stream-gzip (invariants #1, #8, #33, #57)"
```

---

## Task 7: `internal/state` run log store (NDJSON, two-line model)

**Files:**
- Create: `internal/state/runlog.go`, `internal/state/runlog_ndjson.go`, `internal/state/runlog_test.go`

Covers invariants #10 (torn-write recovery), #13 (schema_version), #16 (state package).

**AMENDMENT 2026-06-03 (multi-hat round):**
- Per API Contracts: `AppendStarted(ctx, ...) error` / `AppendFinished(ctx, ...) error` / `Checkpoint(ctx) error` (add `ctx`).
- `ReadRunLog` returns `(entries, error)` not `(entries, []error, error)`; combine parse errors via `errors.Join(errs...)` for `errors.Is/As` ergonomics.
- `bufio.Scanner.Buffer(make([]byte, 1MB), 16MB)`: explicit `errors.Is(err, bufio.ErrTooLong)` handling; fail-closed with a distinct error. Reduce max to 256 KB (runs.ndjson lines are short; 16 MB silent truncation is a hazard).
- Expand `TestRunLogStore_TornWriteRecovery` into table-driven subtests covering: empty file, single torn line, torn-at-EOF (most realistic crash), torn-mid-with-recovery, all-torn.
- Per QA hat: add `t.Helper()` to shared test setup.

- [ ] **Step 1: Write failing test**

```go
// internal/state/runlog_test.go
package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRunLogStore_StartedAndFinished(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runs.ndjson")
	store, err := NewNDJSONRunLogStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	startedAt := time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC)
	if err := store.AppendStarted(StartedRun{
		V: 1, FlashbackupVersion: "0.1.0-core", RunID: "2026-06-03T1430Z-aaaa",
		StartedAt: startedAt, Mode: "copy", Profile: "my-docs",
		SourceRoot: "/Users/me/Docs", DestRoot: "/Volumes/USB",
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}

	finishedAt := startedAt.Add(20 * time.Minute)
	if err := store.AppendFinished(FinishedRun{
		V: 1, FlashbackupVersion: "0.1.0-core", RunID: "2026-06-03T1430Z-aaaa",
		StartedAt: startedAt, FinishedAt: finishedAt, Mode: "copy", Profile: "my-docs",
		SourceRoot: "/Users/me/Docs", DestRoot: "/Volumes/USB",
		FilesTotal: 100, FilesSucceeded: 100, FilesFailed: 0, BytesTotal: 1000000,
		DeletionsSkippedDueToMutation: 0, ExitStatus: "ok",
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, _ := os.ReadFile(tmp)
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"event":"started"`) {
		t.Errorf("line 0 missing started: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"event":"finished"`) {
		t.Errorf("line 1 missing finished: %s", lines[1])
	}
}

func TestRunLogStore_TornWriteRecovery(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "runs.ndjson")
	// Pre-populate with a torn line (corrupted JSON)
	corrupt := `{"v":1,"event":"started","run_id":"good1"}` + "\n" +
		`{"v":1,"event":"finished","run_id":"good1"}` + "\n" +
		`{"v":1,"event":"started","run` + "\n" +  // torn
		`{"v":1,"event":"started","run_id":"good2"}` + "\n"
	if err := os.WriteFile(tmp, []byte(corrupt), 0644); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}

	entries, errs, err := ReadRunLog(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(entries) != 3 { // good1 started + good1 finished + good2 started; torn line skipped
		t.Errorf("expected 3 valid entries, got %d", len(entries))
	}
	if len(errs) != 1 {
		t.Errorf("expected 1 parse error, got %d", len(errs))
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

```bash
go test ./internal/state/ -run RunLog
# Expected: undefined: NewNDJSONRunLogStore, StartedRun, FinishedRun, ReadRunLog
```

- [ ] **Step 3: Implement `internal/state/runlog.go`**

```go
package state

import "time"

// StartedRun is the "started" line per the two-line model.
type StartedRun struct {
	V                  int       `json:"v"`
	Event              string    `json:"event"`              // always "started"
	FlashbackupVersion string    `json:"flashbackup_version"`
	RunID              string    `json:"run_id"`
	StartedAt          time.Time `json:"started_at"`
	Mode               string    `json:"mode"`               // copy | move | verify | init
	Profile            string    `json:"profile,omitempty"`
	SourceRoot         string    `json:"source_root"`
	DestRoot           string    `json:"dest_root"`
}

// FinishedRun is the "finished" line per the two-line model.
type FinishedRun struct {
	V                              int       `json:"v"`
	Event                          string    `json:"event"`              // always "finished"
	FlashbackupVersion             string    `json:"flashbackup_version"`
	RunID                          string    `json:"run_id"`
	StartedAt                      time.Time `json:"started_at"`
	FinishedAt                     time.Time `json:"finished_at"`
	Mode                           string    `json:"mode"`
	Profile                        string    `json:"profile,omitempty"`
	SourceRoot                     string    `json:"source_root"`
	DestRoot                       string    `json:"dest_root"`
	FilesTotal                     int       `json:"files_total"`
	FilesSucceeded                 int       `json:"files_succeeded"`
	FilesFailed                    int       `json:"files_failed"`
	BytesTotal                     int64     `json:"bytes_total"`
	DeletionsSkippedDueToMutation  int       `json:"deletions_skipped_due_to_mutation"`
	ExitStatus                     string    `json:"exit_status"`        // ok | partial | copy_only_aborted_delete | crashed_resumed | preflight_failed
}

// RunLogStore handles the runs.ndjson append-only log.
type RunLogStore interface {
	AppendStarted(s StartedRun) error
	AppendFinished(f FinishedRun) error
	Close() error
}
```

- [ ] **Step 4: Implement `internal/state/runlog_ndjson.go`**

```go
package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type ndjsonRunLogStore struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	open bool
}

func NewNDJSONRunLogStore(path string) (RunLogStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open runs.ndjson: %w", err)
	}
	return &ndjsonRunLogStore{f: f, enc: json.NewEncoder(f), open: true}, nil
}

func (s *ndjsonRunLogStore) AppendStarted(r StartedRun) error {
	r.Event = "started"
	return s.encode(r)
}

func (s *ndjsonRunLogStore) AppendFinished(r FinishedRun) error {
	r.Event = "finished"
	return s.encode(r)
}

func (s *ndjsonRunLogStore) encode(v any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("run log closed")
	}
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("encode runlog: %w", err)
	}
	return s.f.Sync()
}

func (s *ndjsonRunLogStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.open = false
	return s.f.Close()
}

// RunLogEntry is a discriminated union of StartedRun and FinishedRun (read side).
type RunLogEntry struct {
	Event    string       `json:"event"`
	Started  *StartedRun  `json:"-"`
	Finished *FinishedRun `json:"-"`
}

// ReadRunLog reads path line-by-line, returning valid entries and a slice of
// parse errors (one per torn/unparseable line). Invariant #10.
func ReadRunLog(path string) ([]RunLogEntry, []error, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open runs.ndjson: %w", err)
	}
	defer f.Close()

	var entries []RunLogEntry
	var errs []error
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		// Peek the "event" discriminator
		var peek struct{ Event string `json:"event"` }
		if err := json.Unmarshal(line, &peek); err != nil {
			errs = append(errs, fmt.Errorf("parse line: %w", err))
			continue
		}
		switch peek.Event {
		case "started":
			var s StartedRun
			if err := json.Unmarshal(line, &s); err != nil {
				errs = append(errs, err)
				continue
			}
			entries = append(entries, RunLogEntry{Event: "started", Started: &s})
		case "finished":
			var f FinishedRun
			if err := json.Unmarshal(line, &f); err != nil {
				errs = append(errs, err)
				continue
			}
			entries = append(entries, RunLogEntry{Event: "finished", Finished: &f})
		default:
			errs = append(errs, fmt.Errorf("unknown event %q", peek.Event))
		}
	}
	if err := scanner.Err(); err != nil {
		return entries, errs, fmt.Errorf("scan runs.ndjson: %w", err)
	}
	return entries, errs, nil
}
```

- [ ] **Step 5: Run test (expect PASS)**

```bash
go test ./internal/state/ -run RunLog
# Expected: PASS, 2 tests
```

- [ ] **Step 6: Commit**

```bash
git add internal/state/runlog.go internal/state/runlog_ndjson.go internal/state/runlog_test.go
git commit -m "feat(state): add RunLogStore with two-line model and torn-write recovery"
```

---

## Task 8: `internal/state` version.json read/write/recovery

**Files:**
- Create: `internal/state/version.go`, `internal/state/version_test.go`

Covers invariants #11 (corruption recovery, refined 2026-06-03), #13 (schema_version), #33 (per-USB integrity key).

**AMENDMENT 2026-06-03 (multi-hat round) : CRITICAL SECURITY:**

The original `ReadOrInitVersionFile` silently re-keys on parse failure. **This defeats invariant #33** (an attacker who can write to USB can corrupt `version.json` to force a re-key, then re-sign their tampered manifest). **Replace with fail-closed semantics:**

```go
// ReadVersionFile fails closed on missing OR unparseable file.
// Initialization happens ONLY through InitVersionFile (called by `flashbackup init`).
func ReadVersionFile(path string) (VersionFile, error) {
    data, err := os.ReadFile(path)
    if err != nil {
        return VersionFile{}, fmt.Errorf("read version.json: %w", err)
    }
    var v VersionFile
    if err := json.Unmarshal(data, &v); err != nil {
        return VersionFile{}, fmt.Errorf("parse version.json (corrupted or tampered; run `flashbackup init --reset-keys` to reinitialize): %w", err)
    }
    if v.SchemaVersion != CurrentSchemaVersion {
        return VersionFile{}, fmt.Errorf("version.json schema_version=%d unsupported (this build expects %d)", v.SchemaVersion, CurrentSchemaVersion)
    }
    return v, nil
}

// InitVersionFile creates a fresh version.json with a new HMAC key.
// Called ONLY from the `flashbackup init` subcommand. Refuses to overwrite an existing
// valid version.json unless force=true.
func InitVersionFile(path, flashbackupVersion string, force bool) (VersionFile, error) {
    if !force {
        if _, err := os.Stat(path); err == nil {
            return VersionFile{}, fmt.Errorf("version.json exists; pass --reset-keys to overwrite (this invalidates all prior manifests)")
        }
    }
    keyBytes := make([]byte, 32)
    if _, err := rand.Read(keyBytes); err != nil {
        return VersionFile{}, fmt.Errorf("generate hmac key: %w", err)
    }
    v := VersionFile{
        SchemaVersion:      CurrentSchemaVersion,
        FlashbackupVersion: flashbackupVersion,
        HMACKey:            hex.EncodeToString(keyBytes),
    }
    if err := WriteVersionFile(path, v); err != nil {
        return VersionFile{}, fmt.Errorf("init version.json: %w", err)
    }
    return v, nil
}
```

Other amendments:
- `WriteVersionFile`: file mode `0600` (HMAC key is sensitive). Atomic write via `WriteTmpThenRename`; fsync parent dir.
- Test expansion (per QA hat): add subtests for `wrong_type_for_schema_version` (`{"schema_version": "1"}`), `schema_version_newer_than_current`, `missing_hmac_key` (zero-length), `empty_file` (0 bytes). All must fail closed.
- Remove `ReadOrInitVersionFile` entirely. The init subcommand (Task 35) calls `InitVersionFile` exactly once; runtime paths call `ReadVersionFile` only.
- Update spec invariant #11 cross-reference: "corruption recovery" now means "fail-closed with clear remediation message," not "silently re-init."

- [ ] **Step 1: Write failing test**

```go
// internal/state/version_test.go
package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestVersionFile_WriteRead(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "version.json")
	v := VersionFile{
		SchemaVersion:        1,
		FlashbackupVersion:   "0.1.0-core",
		HMACKey:              "deadbeef0123456789abcdef",
	}
	if err := WriteVersionFile(tmp, v); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := ReadVersionFile(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got.SchemaVersion != 1 || got.FlashbackupVersion != "0.1.0-core" {
		t.Errorf("got %+v", got)
	}
	if got.HMACKey != "deadbeef0123456789abcdef" {
		t.Errorf("hmac key mismatch")
	}
}

func TestVersionFile_CorruptionRecovery(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "version.json")
	// Write garbage
	if err := os.WriteFile(tmp, []byte("not valid json {{{"), 0644); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// ReadVersionFile should return a recovery error but a usable default
	v, warn, err := ReadOrInitVersionFile(tmp, "0.1.0-core")
	if err != nil {
		t.Fatalf("ReadOrInitVersionFile: %v", err)
	}
	if warn == nil {
		t.Error("expected warning for corrupt file, got nil")
	}
	if v.SchemaVersion != 1 {
		t.Errorf("expected default v=1, got %d", v.SchemaVersion)
	}
	// File should have been rewritten with a valid default
	got, err := ReadVersionFile(tmp)
	if err != nil {
		t.Fatalf("re-read: %v", err)
	}
	if got.SchemaVersion != 1 || got.HMACKey == "" {
		t.Errorf("default not rewritten: %+v", got)
	}
}
```

- [ ] **Step 2: Run test (expect FAIL)**

- [ ] **Step 3: Implement `internal/state/version.go`**

```go
package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
)

// VersionFile is the schema version + provenance + HMAC key stored at
// <USB>/.flashbackup/version.json. Invariant #11 (corruption recovery),
// #13 (schema_version), #33 (per-USB HMAC key).
type VersionFile struct {
	SchemaVersion      int    `json:"schema_version"`
	FlashbackupVersion string `json:"flashbackup_version"`
	HMACKey            string `json:"hmac_key"` // hex-encoded 32 bytes
}

// CurrentSchemaVersion is the schema version this build understands.
const CurrentSchemaVersion = 1

// WriteVersionFile writes v atomically (write-then-rename).
func WriteVersionFile(path string, v VersionFile) error {
	tmp := path + ".tmp"
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename: %w", err)
	}
	return nil
}

// ReadVersionFile reads and parses path. Returns ErrNotExist if absent.
func ReadVersionFile(path string) (VersionFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VersionFile{}, err
	}
	var v VersionFile
	if err := json.Unmarshal(data, &v); err != nil {
		return VersionFile{}, fmt.Errorf("parse version.json: %w", err)
	}
	return v, nil
}

// ReadOrInitVersionFile reads path; if missing OR unparseable, initializes a
// new VersionFile with current schema, returns a warning describing what
// happened. Invariant #11.
func ReadOrInitVersionFile(path, flashbackupVersion string) (v VersionFile, warning error, err error) {
	got, readErr := ReadVersionFile(path)
	if readErr == nil {
		return got, nil, nil
	}
	if !errors.Is(readErr, os.ErrNotExist) {
		warning = fmt.Errorf("version.json was unreadable (%w); rewriting with current defaults", readErr)
	}
	// Generate fresh HMAC key
	keyBytes := make([]byte, 32)
	if _, err := rand.Read(keyBytes); err != nil {
		return VersionFile{}, warning, fmt.Errorf("generate hmac key: %w", err)
	}
	v = VersionFile{
		SchemaVersion:      CurrentSchemaVersion,
		FlashbackupVersion: flashbackupVersion,
		HMACKey:            hex.EncodeToString(keyBytes),
	}
	if err := WriteVersionFile(path, v); err != nil {
		return VersionFile{}, warning, fmt.Errorf("init version.json: %w", err)
	}
	return v, warning, nil
}
```

- [ ] **Step 4: Run test (expect PASS)**

- [ ] **Step 5: Commit**

```bash
git add internal/state/version.go internal/state/version_test.go
git commit -m "feat(state): add version.json with corruption recovery (invariants #11, #13)"
```

---

## Task 9: `internal/profiles` schema + CRUD

**Files:**
- Create: `internal/profiles/profile.go`, `internal/profiles/store.go`, `internal/profiles/store_test.go`

**AMENDMENT 2026-06-03 (multi-hat round) : SECURITY:**

The original `filepath.Match(pat, "")` validation is too permissive. It accepts `../../*`, `**/anything`, NUL bytes, leading `/`. These patterns are then passed to rsync, which interprets `**` and `[` differently from  Go; confused-deputy. Replace with **strict allowlist**:

```go
var allowedGlobChars = regexp.MustCompile(`^[a-zA-Z0-9._*?/\-]+$`)
const maxPatternLen = 256

func validatePattern(pat string) error {
    if pat == "" {
        return fmt.Errorf("empty pattern")
    }
    if len(pat) > maxPatternLen {
        return fmt.Errorf("pattern exceeds %d chars", maxPatternLen)
    }
    if strings.Contains(pat, "\x00") {
        return fmt.Errorf("pattern contains NUL byte")
    }
    if strings.HasPrefix(pat, "/") {
        return fmt.Errorf("pattern must not start with /")
    }
    if strings.Contains(pat, "..") {
        return fmt.Errorf("pattern must not contain ..")
    }
    if strings.Contains(pat, "**") {
        return fmt.Errorf("pattern must not contain ** (use multiple lines instead)")
    }
    if !allowedGlobChars.MatchString(pat) {
        return fmt.Errorf("pattern contains disallowed characters; allowed: a-z A-Z 0-9 . _ * ? / -")
    }
    if _, err := filepath.Match(pat, ""); err != nil {
        return fmt.Errorf("pattern invalid: %w", err)
    }
    return nil
}
```

Other amendments:
- Rename `profiles.File` to `profiles.ProfilesDoc` (avoids collision with `os.File`, `io/fs.File`).
- Add `io.LimitReader(f, 1MB)` to profile load. Defends against DoS via huge profiles.json.
- Expand tests (per QA hat): subtests for `upsert_replaces_existing` (collision behavior), `upsert_sorts_by_name` (the sort is load-bearing), `get_on_empty_store`, `delete_nonexistent`. Stop ignoring `NewStore` errors.
- Document supported glob grammar in `docs/GLOSSARY.md` reference (defer doc; Plan 2 owns user-facing docs): "stdlib `filepath.Match` syntax; `*`, `?`, `[seq]`. No `**`. No leading `/`. No `..`."
- File mode for `profiles.json`: `0644` (no secrets). Parent dir `0700`.

- [ ] **Step 1: Write failing test**

```go
// internal/profiles/store_test.go
package profiles

import (
	"path/filepath"
	"testing"
)

func TestStore_NewAndLoad(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "profiles.json")
	s, err := NewStore(tmp)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	p := Profile{
		Name:     "my-docs",
		Source:   "/Users/me/Documents",
		Includes: []string{"*.pdf", "*.docx"},
		Excludes: []string{"*.tmp", ".DS_Store"},
	}
	if err := s.Upsert(p); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := s.Get("my-docs")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got.Source != p.Source {
		t.Errorf("got %+v want %+v", got, p)
	}
}

func TestStore_RejectsInvalidGlob(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "profiles.json")
	s, _ := NewStore(tmp)
	p := Profile{Name: "bad", Source: "/tmp", Includes: []string{"foo["}}
	if err := s.Upsert(p); err == nil {
		t.Error("expected error for invalid glob, got nil")
	}
}
```

- [ ] **Step 2: Implement `internal/profiles/profile.go`**

```go
package profiles

// Profile is one saved filter configuration.
type Profile struct {
	V        int      `json:"v"`        // schema version; always 1 in v0.1
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	Includes []string `json:"includes,omitempty"`
	Excludes []string `json:"excludes,omitempty"`
}

// File is the on-disk shape of profiles.json.
type File struct {
	V        int       `json:"v"`
	Profiles []Profile `json:"profiles"`
}
```

- [ ] **Step 3: Implement `internal/profiles/store.go`**

```go
package profiles

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store provides CRUD over the profiles.json file at a fixed path.
type Store struct {
	path string
}

func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, fmt.Errorf("mkdir for profiles: %w", err)
	}
	return &Store{path: path}, nil
}

func (s *Store) load() (*File, error) {
	data, err := os.ReadFile(s.path)
	if os.IsNotExist(err) {
		return &File{V: 1, Profiles: nil}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read profiles.json: %w", err)
	}
	var f File
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parse profiles.json: %w", err)
	}
	return &f, nil
}

func (s *Store) save(f *File) error {
	tmp := s.path + ".tmp"
	data, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal: %w", err)
	}
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return fmt.Errorf("write tmp: %w", err)
	}
	return os.Rename(tmp, s.path)
}

// Upsert inserts or replaces a profile by name. Validates patterns first.
func (s *Store) Upsert(p Profile) error {
	if err := ValidateProfile(p); err != nil {
		return err
	}
	p.V = 1
	f, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i, ex := range f.Profiles {
		if ex.Name == p.Name {
			f.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		f.Profiles = append(f.Profiles, p)
	}
	sort.Slice(f.Profiles, func(i, j int) bool { return f.Profiles[i].Name < f.Profiles[j].Name })
	return s.save(f)
}

func (s *Store) Get(name string) (Profile, error) {
	f, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range f.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("profile %q not found", name)
}

func (s *Store) List() ([]Profile, error) {
	f, err := s.load()
	if err != nil {
		return nil, err
	}
	return f.Profiles, nil
}

func (s *Store) Delete(name string) error {
	f, err := s.load()
	if err != nil {
		return err
	}
	out := make([]Profile, 0, len(f.Profiles))
	found := false
	for _, p := range f.Profiles {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("profile %q not found", name)
	}
	f.Profiles = out
	return s.save(f)
}

// ValidateProfile checks all include/exclude patterns compile as filepath globs.
func ValidateProfile(p Profile) error {
	if p.Name == "" {
		return fmt.Errorf("profile name is empty")
	}
	if p.Source == "" {
		return fmt.Errorf("profile %q: source is empty", p.Name)
	}
	for i, pat := range p.Includes {
		if _, err := filepath.Match(pat, ""); err != nil {
			return fmt.Errorf("profile %q: include[%d]=%q invalid: %w", p.Name, i, pat, err)
		}
	}
	for i, pat := range p.Excludes {
		if _, err := filepath.Match(pat, ""); err != nil {
			return fmt.Errorf("profile %q: exclude[%d]=%q invalid: %w", p.Name, i, pat, err)
		}
	}
	return nil
}
```

- [ ] **Step 4: Run test (expect PASS)**

- [ ] **Step 5: Commit**

```bash
git add internal/profiles/
git commit -m "feat(profiles): add profile schema and CRUD with glob validation"
```

---

## Tasks 10-55: scope-bounded, expand at execution time

> **Subagent dispatch protocol (amended 2026-06-03):** Each subagent dispatched to expand and execute a task MUST first read (in order): (1) this plan, (2) the spec, (3) the **API Contracts, Conventions, and Cross-Task Anchors** section above, (4) `git log --oneline -20` and `tree internal/` for current state. After expansion, the dispatching agent runs a **review-subagent dispatch** ("verify the test actually exercises the invariant; verify the code matches the API Contracts signatures") BEFORE committing.

> **NO creative interpretation of scope.** The API Contracts section is canonical; cross-task type signatures and interfaces are pinned there, not inferred from prior tasks.

(Tasks 10 through 55 follow the same TDD structure as Tasks 1-9 and cover, in order:)

- **Task 10:** `internal/drives` mount enumeration (parse `diskutil list -plist`). Open question (Maintainability hat): could fold into `preflight` if no other consumer emerges; defer decision until Task 39 (status).
- **Task 11:** `internal/selection` source-tree walk + filter + NFC canonicalization (invariant #32). Includes case-collision check on case-insensitive destinations.
- **Task 12:** `internal/rsync` embedded binary extraction. Concrete recipe per Hacker hat: extract to `<USB>/.flashbackup/bin/<sha256-of-embedded>/rsync`, verify SHA256 on the open FD, `chmod 0500`, `chflags uchg` (immutable bit), exec by path. macOS has no `fexecve`; this is the next-best mitigation. Document the limitation in `docs/SECURITY-DECISIONS.md`. Embedded universal2 rsync size ~5-8 MB.
- **Task 12a (NEW):** `scripts/build-rsync.sh` for pinned universal2 rsync 3.x build. Documented in spec invariant #38 / Section 9. Pinned to a specific commit; SHA256 of built binary recorded in `internal/rsync/embed_sha256.go` constant.
- **Task 13:** `internal/rsync` subprocess wrapper. File list passed via `--from0 --files-from=-` over stdin pipe (NEVER via argv expansion). Absolute path to `/usr/bin/caffeinate` for sleep prevention (no `$PATH` lookup). Add unit test: filename starting with `--` survives intact through the wrapper.
- **Task 14:** `internal/rsync/progress` parser. Contract test with golden file (`testdata/rsync-progress-3.4.1.golden`) per invariant #43.
- **Task 15:** `internal/preflight/lock`. Uses `Lock` struct per API Contracts (PID + start_time + host_uuid + nonce + VolumeUUID). Lock file opened with `O_EXCL|O_CREAT|O_NOFOLLOW`, `flock(LOCK_EX|LOCK_NB)` on the FD. Stale detection: read lock file, `kill(pid, 0)` check + start_time comparison + host_uuid match; only unlink if confirmed stale. Lock contention error format: `lock held by PID=<n> host=<h> since <ts> (<age>); run 'flashbackup status' for details`.
- **Task 16:** `internal/preflight/filesystem` `statfs`-based APFS/HFS+ detection; exFAT refusal with reformat recipe (invariant #4). Also: reject `noexec` mount flag (rsync extraction needs exec).
- **Task 17:** `internal/preflight/symlink`. Walk dest path components with `O_NOFOLLOW`; `fstat` each; record (device, inode) per component as baseline. `PreflightContext.VerifyVolumeUnchanged` re-checks at every phase boundary (catches mid-run remount or symlink swap).
- **Task 18:** `internal/preflight/codesign` per-launch `codesign --verify --strict <self-path>` shellout (invariant #29). Skip in dev builds (build-tagged); enforce in release.
- **Task 19:** `internal/preflight/volume_uuid` capture USB VolumeUUID via `diskutil info -plist` at T0; stored in `PreflightContext.VolumeUUID`. `VerifyVolumeUnchanged` re-reads and compares (invariant #30).
- **Task 20:** `internal/preflight` integrate all gates. Returns `*PreflightContext` per API Contracts. Add `internal/state/atomic.go` `WriteTmpThenRename(path, data, mode)` helper used by version, manifest, runlog, profiles.
- **Task 21:** `internal/runner/types`. Defines `Phase`, `Mode`, `FileStatus`, `DeletionStatus`, `IntegrityStatus`, `Signature`, `RunOptions`, `RunResult`, **`UIEvent`, `UIEventKind`, `ProgressInfo`** per API Contracts (PS3 + PS4). Includes `doc.go` with phase legend, Mermaid-flavored ASCII state diagram, signal-handler contract.
- **Task 22:** `internal/runner/t0_preflight.go` (renamed from `t0.go` per Maintainability hat). Preflight orchestration + emit `state.Event{Kind:"phase_started",Phase:"T0"}` to events.ndjson via `EventStore.Append`; "started" line to runs.ndjson via `RunLogStore.AppendStarted`. `Checkpoint()` at phase end.
- **Task 22a (NEW, queued by Task 22 review on 2026-06-04):** Wire the five unowned T0-domain event Kinds from the canonical Event Kinds table into the runner's event stream. Specifically: `lock_acquired` (pid, host_uuid, nonce — from `lock.Lock` struct exposed via `LockHandle`), `lock_stale_detected` (prior_pid, prior_host_uuid — surfaced by `lock.Acquire` recovery path), `lock_contention` (holder_pid, holder_age_seconds — surfaced by `lock.HeldLockError`), `filesystem_refused` (filesystem_type — surfaced by `filesystem.Validate` typed error), `volume_uuid_changed` (expected, got — surfaced by `volume_uuid.VolumeUUIDChangedError` on `VerifyVolumeUnchanged` failure). Two design options: (a) pass an `EventStore` into `preflight.Preflight` + each gate emits inline (changes Task 20 contract); (b) widen `PreflightContext` / wrap each typed error so `RunT0Preflight` can translate observed gate state into events at phase-end. Pick (b) for cleaner separation: preflight stays pure, runner translates. Implementation: extend `preflight.PreflightContext` with `LockSnapshot *lock.Lock` and `StaleRecovered bool`; add typed errors `filesystem.ErrFilesystemUnsupported{Type string}` and reuse `lock.HeldLockError`. Task 22a then post-processes after `preflight.Preflight` returns and emits the appropriate `state.Event`s before `phase_completed`/`phase_aborted`. Tests: assert each Kind appears in events.ndjson under the relevant trigger scenario.
- **Task 23:** `internal/runner/t1_enumerate.go` (renamed). Enumerate via `selection`; capture signatures into `[]Signature`. NFC canonicalization (invariant #32). Emit `file_enumerated` events.
- **Task 24:** `internal/runner/t2_transfer.go` (renamed). Invoke rsync via `internal/rsync`. Stream `transfer_*` events; emit per-file `UIEvent{Kind:UIEvtProgress}` to UIRenderer.
- **Task 25:** `internal/runner/t3_hash_compare.go` (renamed). Per-file hash source + hash dest + classify status. Append manifest entries with HMAC (using length-prefixed canonical encoding per Task 6 amendment). Emit `file_completed` / `hash_mismatch` / `source_mutated` events + `UIEvent{Kind:UIEvtFileCompleted}` to UIRenderer.
- **Task 26:** `internal/runner/t4_delete_source.go` (renamed). Move-mode atomic gate + per-file mutation re-stat (re-stat via syscall, not hash). Unlink + append to `deletion-log.ndjson` (fsync per unlink). Emit `atomic_gate_blocked` if gate triggers. Includes `permission-denied` fault hook for AC.
- **Task 27:** `internal/runner/t5_finalize.go` (renamed). Manifest already gzip-streamed (per Task 6); rename `.tmp.gz → .gz`, fsync parent dir. Append "finished" line. Prune old run dirs (10 default). Emit `manifest_finalized` + `run_finished`.
- **Task 28:** `internal/runner/faultinject.go` (build tag `faultinject`) + `faultinject_release.go` (no-op stubs; file header MUST say "DO NOT DELETE: release binary won't link without this"). Implements the DSL grammar from API Contracts (`corrupt|kill|mutate-source|unmount|disk-full|permission-denied` × `phase|file|after_pct|after_count`). Provides CI release gate: `make verify-release` runs `nm | grep faultinject` (already added to Task 2 Makefile).
- **Task 29:** `internal/runner/runner.go` top-level state machine. Integrates T0-T5 with `signal.NotifyContext(SIGINT, SIGTERM)`. Per API Contracts: `Run(ctx, opts) (*RunResult, error)`. Calls `PreflightContext.VerifyVolumeUnchanged` at every phase boundary. **Preconditions enforced before each phase**: (a) `T2Input.Candidates` and `T1Result.Signatures` must agree on RelativePaths (one Signature per Candidate); (b) `T3Input.Signatures` and `T3Input.Candidates` likewise; (c) when invoking `RunT4DeleteSource` in move mode, `T4Input.Signatures` must contain every RelativePath in the verified subset of Candidates (the t4 code has a defensive fallback for missing signatures but the orchestrator should never reach it). Also consumes `T2Result.RsyncLogPath` and `T4Result.DeletionLogPath` for the support-bundle path list embedded in `RunResult` / runs.ndjson "finished" line.
- **Task 30:** `internal/verify/load` manifest reader. Pipeline (per Subagent-Execution hat): open gzip stream → `ReadVersionFile` (fail-closed; no init) → reject if `schema_version != 1` → decode each line + `VerifyHMAC` inline (using length-prefixed canonical) → return `(entries, integrityErrors, schemaErrors)`. Tampered entries return `integrityErrors`, not silent skip.
- **Task 31:** `internal/verify/rehash` per-file rehash + classify (`verified` / `size_mismatch` / `hash_mismatch` / `missing` / `unreadable`). Emit `UIEvent{Kind:UIEvtProgress}` for renderer.
- **Task 32:** `internal/verify/verify.go` top-level. Per API Contracts: `Verify(ctx, opts) (*VerifyResult, error)`. Includes `FilesIntegrityFailed` counter (AC-19). Writes `<run-dir>/verifications/<verify-id>/summary.json` with locked schema (Section 9 / API Contracts).
- **Task 33:** `internal/plain` renderer. Per PS3: implements `Renderer` interface (`OnEvent(ctx, ev runner.UIEvent) error`). Two implementations: `PlainRenderer` (push to `io.Writer` with `fmt.Fprintln`) for TTY-and-pipe modes. Snapshot/golden-file tests for every UIEventKind (per QA hat). Include `doc.go` stating boundary rule: "plain owns ALL CLI output formatting; cmd functions only collect data and pass it to renderer." **UIEvtSummary.Path contract (refined 2026-06-05 per Task 38 review I1):** carries the EXACT artifact file path the operator should consult (backup: events.ndjson; verify: summary.json); renderer prints it verbatim. Empty Path falls back to a generic `.flashbackup/` pointer.
- **Task 34:** `cmd/flashbackup/main.go` entry. `signal.NotifyContext(ctx, SIGINT, SIGTERM)` at start; second signal within 5s forces exit. `--version` prints `flashbackup v0.1.0-core (rsync 3.4.1, commit <sha>, built <SOURCE_DATE_EPOCH>)` plus GPLv3 warranty disclaimer per OSS hat finding.
- **Task 35:** `cmd/flashbackup/init.go`. Calls `InitVersionFile` (NOT `ReadOrInitVersionFile`; that was removed in Task 8 amendment). Filesystem check, rsync extract, `.metadata_never_index`, fresh `version.json` with HMAC key. Refuses to overwrite existing `version.json` unless `--reset-keys` is passed. AC-1, AC-2.
- **Task 36:** `cmd/flashbackup/backup.go` backup subcommand. Load profile via `profiles.Store.Get`; build `runner.RunOptions` with `UIRenderer: plain.NewPlainRenderer(os.Stdout, isatty(stdout))`; invoke `runner.Run(ctx, opts)`. AC-3.
- **Task 37:** `cmd/flashbackup/backup.go` move-mode confirmation. UIRenderer emits `UIEvent{Kind:UIEvtPrompt}` carrying the multi-line warning in `ev.Status`; cmd reads literal `DELETE\n` from stdin; case-sensitive exact match. **The prompt fires pre-T0 (cmd-side gate)**, not post-T2 as originally written in the spec; decline aborts before runner invocation (exit 2 on decline, exit 1 on EOF). The runner has no callback for cmd-side confirmation, so the pre-T0 placement is architectural; spec section 4 + AC-7 + AC-8 amended 2026-06-05 to match. AC-7, AC-8.
- **Task 38:** `cmd/flashbackup/verify.go` with `--all` and `--check-extras`. AC-9, AC-10, AC-19. Exit codes 0 / 1 (any integrity failure including AC-19) / 2 (preflight).
- **Task 39:** `cmd/flashbackup/status.go` with `--json`. Output schema is **locked** per API Contracts section (JSON example there). Plain mode shows tabular summary; `--json` emits the schema literally. No invention. Determines whether `internal/drives` package stays standalone (consumes drive enumeration for the USB capacity surface).
- **Task 40:** `cmd/flashbackup/profiles.go` list / new / edit (`$EDITOR`) / delete / validate.
- **Task 41:** `cmd/flashbackup/help.go` `--help` per subcommand. Help content pulls from a constants table for consistency (per Tech Writer hat).
- **Task 42:** `test/e2e/helpers.go`. **Critical (Hacker hat):** mountpoints via `mktemp -d` (NEVER fixed `/Volumes/<name>` to avoid clobbering real USB). `MountAPFSImage(t *testing.T, sizeMB int) (mountPath string)` with `t.Helper()` and `t.Cleanup(func() { exec.Command("hdiutil", "detach", "-force", mountPath).Run() })`. Document size budget per fixture.
- **Task 42a (NEW):** Create fixture trees `test/fixtures/{tiny,realistic,pathological}` with checked-in `MANIFEST.txt` per directory (file count, sizes, SHA256-of-tree, special characters/edge cases exercised, which ACs depend on the fixture). Pathological cases: NFC vs NFD twin pair, 0x1B/0x07/ANSI in filenames, long path, immutable file, sparse file. Per QA + Code Archaeologist hats.
- **Task 43:** `test/e2e/init_test.go` AC-1 + AC-2. Tagged as `e2e-fast` (PR-gating per PS2).
- **Task 44:** `test/e2e/backup_happy_test.go` AC-3. Tagged `e2e-fast`.
- **Task 45:** `test/e2e/verify_test.go` AC-9 + AC-10. Tagged `e2e-fast`.
- **Task 46:** `test/e2e/lock_test.go` AC-11 + AC-12. Tagged `e2e-fast`.
- **Task 47:** `test/e2e/non_tty_test.go` AC-15. Tagged `e2e-fast`.
- **Task 48:** `test/e2e/atomic_gate_test.go` AC-4 (faultinject `corrupt:phase=T1:file=X`). Tagged `e2e-safety` (main-gating per PS2).
- **Task 49:** `test/e2e/mutation_test.go` AC-5 + AC-6 (`mutate-source:phase=T2-pre` and `T3-pre`). Tagged `e2e-safety`.
- **Task 50:** `test/e2e/crash_resume_test.go` AC-13 (`kill:phase=T1:after_pct=50`). Tagged `e2e-safety`.
- **Task 51:** `test/e2e/delete_flag_test.go` AC-14. Tagged `e2e-safety`.
- **Task 51a (NEW):** `test/e2e/verify_tampered_manifest_test.go` AC-19. Tampers a manifest entry's `sha256_source` post-backup; asserts verify returns `FilesIntegrityFailed >= 1` and exit code 1. Tagged `e2e-safety`.
- **Task 51b (NEW):** `test/e2e/fault_kill_test.go` covers missing fault hooks (`kill:phase=T2`, `kill:phase=T3`, `unmount:phase=T1`, `disk-full:phase=T1`) per QA hat finding. Tagged `e2e-safety`.
- **Task 52:** `test/e2e/delete_confirm_test.go` AC-7, AC-8. Tagged `e2e-fast`.
- **Task 53:** `docs/ERROR_CATALOG.md` populated. Contract test (per Code Archaeologist): every `state.Event.Kind` defined in code must have a catalog entry; CI test asserts the union is complete.
- **Task 54:** Update `README.md` with v0.1-core install + usage (skeleton only; Plan 2 polishes). Include Gatekeeper bypass recipe (`xattr -d com.apple.quarantine ./flashbackup`) for unsigned Phase 0 binary.
- **Task 55:** Tag `v0.1.0-core` for Phase 0 dogfood. Document the dogfood checklist in `docs/DOGFOOD.md`: pre-run setup, Gatekeeper recipe, expected first-run output, `make debug-bundle` for bug reports, email contact for Sev1.

Each of tasks 10-55 follows the same template as tasks 1-9:
- File paths exact.
- Failing test first.
- Implementation.
- Run + verify.
- Commit per task.

The structure above is **incomplete-but-explicit**: every task is named, scope-bounded, and tied to specific spec invariants/ACs. Before subagent-driven execution, the next agent dispatched to "execute the next task" will fully expand the in-progress task using the spec + this header + the existing implementation context.

Estimated execution: 6 to 8 weeks part-time at the granularity above. End state: a working `flashbackup` CLI binary that passes all 18 ACs in text mode; ready for Phase 0 dogfood (MM only, 2 weeks per the spec's rollout plan).

---

## Self-review notes (amended 2026-06-03 after Plan 1 multi-hat review)

- **Spec coverage:** Tasks 1-55 (plus new 12a, 42a, 51a, 51b) cover all 58 invariants and all 19 ACs of the core engine (AC-19 added during multi-hat), except those explicitly deferred to Plan 2 (TUI: #24, #25, #26, #27, #49, #50, #51 are partially addressed via `internal/plain` and the non-TTY path; full TUI is Plan 2). Build/release invariants #34-#39 are partially in scope (CI lint+test+e2e); full signing/notarization/release pipeline + minisign is Plan 2.
- **Placeholder scan:** Tasks 1-9 are fully expanded with TDD steps + complete code + 2026-06-03 amendment annotations. Tasks 10-55 are scope-bounded with explicit invariant/AC ties + amendment annotations pointing at API Contracts for cross-task type signatures.
- **Type consistency:** All cross-task types pinned in the API Contracts section. Subagents expanding tasks 10-55 conform to those signatures (not invent new shapes).
- **Strategic amendments applied:** PS1 (HMAC = integrity checksum, length-prefixed), PS2 (e2e split fast/safety), PS3 (Renderer.OnEvent event-bus), PS4 (runner.UIEvent distinct from state.Event). 12 critical mechanical fixes + 28 important fixes applied; 25 items deferred to Plan 2 / v0.2 with explicit recording.

---

## Execution choice

Plan saved. Two ways to execute:

1. **Subagent-Driven (recommended).** Dispatch a fresh subagent per task; review between tasks; the dispatching agent expands tasks 10-55 at dispatch time using this plan as the source of truth.
2. **Inline Execution.** Execute tasks in this session via `superpowers:executing-plans` with batched checkpoints.

Which approach?
