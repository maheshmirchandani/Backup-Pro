# FlashBackup Error and Event Catalog

> Operator-facing reference for every `state.Event.Kind` constant that FlashBackup writes to `<USB>/.flashbackup/runs/<run-id>/events.ndjson`.

## How to read this document

Every backup run emits a stream of structured events to `events.ndjson`. Each event has a `kind` field that classifies what happened. This catalog enumerates every `kind` value the core engine can write, plus the kinds reserved for tasks not yet implemented in v0.1.

For each entry:

- **Phase**: which phase emits the event. Phase wire strings are `T0` (preflight), `T0+` (enumerate), `T1` (transfer), `T2` (hash-compare), `T3` (delete-source), `T4` (finalize).
- **When emitted**: the precise point in the phase that triggers the write.
- **Required `Details` fields**: keys that consumers can rely on being present.
- **Optional `Details` fields**: keys that may appear in certain conditions and must be tolerated by parsers.
- **What it means**: plain-English description of the event for an operator reading the log.
- **What the user sees** (where applicable): the CLI / TUI surface for user-visible errors.
- **Recovery action** (where applicable): what to do next.

The catalog is grouped by phase plus a run-level section for cross-phase events.

## Status legend

- **Emitted in v0.1**: written by the shipping core engine today.
- **Queued (Task NNa)**: defined by the canonical Event Kinds table in the master plan; not emitted by v0.1; tracked for the listed follow-up task.

## Catalog completeness contract

`internal/state/event_catalog_test.go` asserts that every canonical Event Kind has an entry in this file. The test fails if a Kind appears in code without documentation, or if a Kind drops out of the canonical list without removal from this file. See Task 53 in `docs/planning/2026-06-03-flashbackup-core-engine.md`.

---

## Run-level events

These bracket the entire run and the individual phases.

### `phase_started`

**Phase**: any (T0, T0+, T1, T2, T3, T4)
**When emitted**: at the entry of each phase function, immediately after the phase records its `startedAt` timestamp.
**Required `Details` fields**: none.
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: a phase has begun execution. Every `phase_started` is paired with one closing event for the same phase: `phase_completed` (success or protective abort) or `phase_aborted` (fatal phase error). A `phase_started` without a matching closing event indicates a crashed phase; the next preflight treats the run as `crashed_resumed`.

Recovery hint: no operator action required; this is a normal trace event.

### `phase_completed`

**Phase**: any.
**When emitted**: at the successful exit of each phase.
**Required `Details` fields**: `duration_ms` (int, total wall-clock milliseconds the phase ran).
**Optional `Details` fields**:
- `skipped: true` (bool): phase entered but did no work (e.g., T3 in copy mode, T0+ with an empty profile selection).
- `gate_blocked: true` (bool) + `failed_count: int`: T3 atomic gate fired (invariant #1). Recorded on `phase_completed` rather than `phase_aborted` because the gate is a protective outcome of an otherwise-correct phase, not a phase failure.
- `pruned_count: int`: T4 emits the number of old run directories pruned during finalization.
- Phase-specific counters where useful (e.g., T2's `files_total`, `files_verified`).
**Status**: emitted in v0.1.

What it means: the phase did its work and durably handed off to the next phase. Paired with the corresponding `phase_started`.

Recovery hint: no operator action required.

### `phase_aborted`

**Phase**: any.
**When emitted**: when the phase exits with a fatal error.
**Required `Details` fields**: `duration_ms` (int), `error` (string, wrapped error message).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1, **best-effort only**.

What it means: the phase encountered a fatal error and could not continue. The run is aborted; subsequent phases will not run.

Important caveat: when the audit store (EventStore) is itself the failure mode (an `Append` or `Checkpoint` returned an error mid-phase), the runner intentionally does NOT attempt another `Append` for `phase_aborted` because re-Appending to a just-failed store risks compounding the original error and writing a misleading second-order failure. In that case the on-disk trail terminates at the last successful event line; recovery treats any `phase_started` without a matching closing event as a crashed phase.

What the user sees: a non-zero exit code from the CLI; the TUI shows the phase as aborted with the error text.

Recovery action: read `events.ndjson` for the abort reason, check the per-phase log (e.g., `rsync.log`), and re-run after addressing the root cause. The two-line `runs.ndjson` model marks the run as crashed if the next preflight finds an unfinished start.

### `run_finished`

**Phase**: T4 (finalize).
**When emitted**: as the very last event of a successful run, after the `runs.ndjson` `event=finished` line is durable.
**Required `Details` fields**: `exit_status` (string, one of the canonical exit statuses: `ok`, `copy_only_aborted_delete`, `crashed_resumed`, etc.).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the run completed cleanly and finalization wrote the `runs.ndjson` `finished` line. The audit trail is terminated.

Recovery hint: no operator action required.

---

## T0 (preflight) events

The preflight phase verifies the destination volume, acquires the run lock, and snapshots filesystem identity. In v0.1 the only event emitted is the phase pair (`phase_started`/`phase_completed`/`phase_aborted`); the five preflight-domain kinds below are documented for the queued Task 22a translation layer.

### `lock_acquired`

**Phase**: T0.
**When emitted**: when the preflight lock is taken cleanly (no prior holder, or a stale lock was recovered).
**Required `Details` fields**: `pid` (int), `host_uuid` (string), `nonce` (string).
**Optional `Details` fields**: none.
**Status**: queued (Task 22a). Not emitted in v0.1; the preflight `lock` package today takes the lock silently. Task 22a wires the runner to translate the preflight snapshot into this event.

What it means: this run owns the lock and may safely proceed.

Recovery hint: no operator action required.

### `lock_stale_detected`

**Phase**: T0.
**When emitted**: when a stale lock from a prior crashed run was detected and reaped during this preflight.
**Required `Details` fields**: `prior_pid` (int), `prior_host_uuid` (string).
**Optional `Details` fields**: none.
**Status**: queued (Task 50a, also referenced by Task 22a). Not emitted in v0.1; the preflight `lock` package today silently recovers stale locks.

What it means: a previous run died without releasing the lock. The current preflight verified the prior holder is no longer alive and reclaimed the lock.

Recovery hint: no operator action required. The prior run's state under `<USB>/.flashbackup/runs/<prior-run-id>/` is preserved for post-mortem; the orphan-recovery gate (Task 50a) finalizes the prior run as `crashed_resumed`.

### `lock_contention`

**Phase**: T0.
**When emitted**: when the lock is held by a live process and the current preflight aborts.
**Required `Details` fields**: `holder_pid` (int), `holder_age_seconds` (int).
**Optional `Details` fields**: none.
**Status**: queued (Task 22a). Not emitted in v0.1; the preflight `lock` package today returns a typed error that the runner translates to a `phase_aborted`.

What it means: another FlashBackup run is currently using this USB volume.

What the user sees: CLI exits with the lock-contention error; TUI shows the holder PID and how long the prior run has been active.

Recovery action: wait for the other run to finish, or (if you are certain the other process is hung) kill it and retry. Do not force-delete the lock file by hand; the stale-lock recovery in the next preflight will reap it correctly.

### `filesystem_refused`

**Phase**: T0.
**When emitted**: when the destination volume's filesystem is not APFS or HFS+ (invariant: refuse exFAT and other non-Mac filesystems because they lack the xattr fidelity FlashBackup relies on).
**Required `Details` fields**: `filesystem_type` (string, e.g. `exfat`, `msdos`, `ntfs`).
**Optional `Details` fields**: none.
**Status**: queued (Task 22a). Not emitted in v0.1; the preflight `drives` package today returns a typed error that the runner translates to a `phase_aborted`.

What it means: FlashBackup refuses to write to a non-APFS/HFS+ filesystem.

What the user sees: CLI exits with the filesystem-refused error and prints the reformat recipe (erase the USB as APFS in Disk Utility).

Recovery action: reformat the USB volume as APFS using Disk Utility, then re-run.

### `volume_uuid_changed`

**Phase**: any (preflight at T0, mid-run re-verification at phase boundaries).
**When emitted**: when a re-stat of the destination volume's UUID does not match the value snapshotted at preflight (a different USB was swapped in mid-run, or the original was re-mounted with a different identity).
**Required `Details` fields**: `expected` (string, original UUID), `got` (string, current UUID).
**Optional `Details` fields**: none.
**Status**: queued (Task 22a). Not emitted in v0.1.

What it means: the USB volume FlashBackup was writing to is no longer the same volume. Continuing would corrupt the backup.

What the user sees: CLI exits with the volume-mismatch error.

Recovery action: re-mount the original USB and re-run. The partial state under `<USB>/.flashbackup/runs/<run-id>/` is preserved.

---

## T0+ (enumerate) events

### `file_enumerated`

**Phase**: T0+.
**When emitted**: once per candidate file produced by selection. High volume on large trees; downstream tooling may need to sample.
**Required `Details` fields**: `size` (int, bytes), `mtime_ns` (int, modification time in nanoseconds since epoch). The event's top-level `Path` field carries the relative path.
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the selection layer found a candidate file for backup. The `(size, mtime_ns)` pair is the T0 baseline used by T2 / T3 to detect source mutation between read and delete.

Recovery hint: no operator action required.

---

## T1 (transfer) events

### `transfer_started`

**Phase**: T1.
**When emitted**: immediately before the rsync subprocess is launched.
**Required `Details` fields**: `command_line` (string, the assembled rsync command), `file_count` (int, candidates to transfer).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: rsync is about to run. The recorded command-line lets a post-mortem reproduce the exact transfer.

Recovery hint: no operator action required.

### `transfer_completed`

**Phase**: T1.
**When emitted**: when the rsync subprocess exits with status 0, or when the empty-Candidates short-circuit fires (no files to transfer).
**Required `Details` fields**: `exit_code` (int, 0 on success), `duration_ms` (int).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: rsync finished cleanly. Hash-compare (T2) will run next.

Recovery hint: no operator action required.

### `transfer_failed`

**Phase**: T1.
**When emitted**: when rsync exits non-zero or the rsync subprocess is killed (Ctrl-C, fault injection, timeout).
**Required `Details` fields**: `exit_code` (int), `error` (string, wrapped error).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the rsync subprocess failed. T1 will emit `phase_aborted` next. T2, T3, T4 will not run.

What the user sees: CLI exits with the transfer error; the rsync log under `<USB>/.flashbackup/runs/<run-id>/rsync.log` carries the rsync-side diagnostics.

Recovery action: inspect `rsync.log` for the rsync exit reason (out of space, permission denied, broken pipe). Address the underlying issue and re-run; an interrupted rsync transfer resumes cleanly thanks to `--partial`.

---

## T2 (hash-compare) events

T2 reads both source and destination, computes SHA256 on each, and classifies every candidate into one of: `verified`, `hash_mismatch`, `source_mutated`, `not_transferred`, `source_unreadable`, `dest_unreadable`. The classifications collapse onto three event kinds: `hash_mismatch` and `source_mutated` for their named outcomes, plus `file_completed` for every other classification (so consumers do not need N parallel kinds per failure mode).

### `file_completed`

**Phase**: T2.
**When emitted**: once per file that finished T2 classification with any status other than `hash_mismatch` or `source_mutated`. Covers `verified`, `not_transferred`, `source_unreadable`, `dest_unreadable`.
**Required `Details` fields**: `path` (string, relative path), `status` (string, one of the FileStatus values), `sha256_source` (string, first 16 hex chars of the source-side SHA256 digest). The event's top-level `Path` field also carries the relative path.
**Optional `Details` fields**: `error` (string, present on `source_unreadable` / `dest_unreadable` classifications).
**Status**: emitted in v0.1.

What it means: this file finished classification. The status field discriminates verified vs the various failure modes.

Recovery hint: per-file `source_unreadable` / `dest_unreadable` events do not abort the phase, but they DO contribute to the atomic-gate failure count at T3 in move mode. If you see these in copy mode, the file is missing from the backup; investigate the file's source-side accessibility before retrying.

### `hash_mismatch`

**Phase**: T2.
**When emitted**: when the source-side SHA256 differs from the destination-side SHA256 for the same file.
**Required `Details` fields**: `path` (string), `sha256_source_prefix` (string, first 16 hex chars), `sha256_dest_prefix` (string, first 16 hex chars).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the destination copy does not match the source. Either rsync transferred a corrupted byte stream (extremely rare; rsync's own checksumming usually catches this), or the USB has a media error, or the destination was tampered with between rsync writing it and T2 reading it.

What the user sees: in move mode, the atomic gate (invariant #1) will fire at T3 and no source files will be deleted. In copy mode, the backup is recorded as incomplete; `verify` will re-detect the mismatch later.

Recovery action: re-run the backup; transient USB bit-flips usually resolve on retry. If the mismatch persists across multiple runs, replace the USB drive (media is failing) and treat the prior backup as suspect.

### `source_mutated`

**Phase**: T2.
**When emitted**: when the source file's `(size, mtime_ns)` at T2 read time differs from the baseline snapshotted at T0+ enumerate.
**Required `Details` fields**: `path` (string).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the user edited or replaced the source file between T0+ enumerate and T2 read. The hash on disk now does not represent the file that was transferred. Treated as a per-file failure: the file is not marked verified, the atomic gate fires in move mode, the source is preserved.

What the user sees: in move mode, no source files are deleted (the atomic gate's protective behavior). In copy mode, the file is recorded as `source_mutated` in the manifest.

Recovery action: re-run the backup after the source files settle. FlashBackup intentionally refuses to verify files the user touched mid-run.

---

## T3 (delete-source) events

T3 runs only in move mode (`--delete` flag). It re-stats every successfully-verified source file and unlinks the source if `(size, mtime_ns)` still matches the T0 baseline.

### `atomic_gate_blocked`

**Phase**: T3.
**When emitted**: at the start of T3 when ANY T2 file failed verification (status was not `verified`). The gate is the atomic-move invariant (invariant #1): non-verified files block all source deletion.
**Required `Details` fields**: `failed_count` (int, number of T2 failures).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the atomic-move gate fired. No source files were deleted. The run exits with `copy_only_aborted_delete` status; the destination copy is intact, sources are preserved.

What the user sees: CLI exits with the copy-only-aborted-delete status; TUI shows the phase as aborted with the failed-file count. The run is recorded as `phase_completed` with `gate_blocked: true` (the phase ran cleanly; the gate is a protective outcome, not a failure).

Recovery action: read the T2 events to identify failed files, re-run to retry the failures, or (if you accept the partial result) move on with sources intact.

### `delete_completed`

**Phase**: T3.
**When emitted**: when a source file's re-stat matched the baseline and the unlink succeeded.
**Required `Details` fields**: `path` (string).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the source file was permanently removed (no Trash, no staging area; the user typed `DELETE` upfront to authorize this).

Recovery hint: no operator action required.

### `delete_skipped_mutated`

**Phase**: T3.
**When emitted**: when a source file's `(size, mtime_ns)` at T3 re-stat does not match the T0 baseline (the user edited or replaced the file between T2 hashing and T3 deletion).
**Required `Details` fields**: `path` (string).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: defense-in-depth on top of T2's mutation gate. The file changed since T2 read it, so the on-disk content no longer matches the verified destination copy. The source is preserved.

Recovery action: review the listed file; if you want it backed up, re-run. The destination still has the prior version (verified at T2).

### `delete_failed`

**Phase**: T3.
**When emitted**: when the unlink syscall failed (EACCES, EPERM, EBUSY, EROFS, etc.) or the pre-unlink Lstat failed.
**Required `Details` fields**: `path` (string), `errno` (string, POSIX errno name when present), `error` (string, wrapped error).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the source could not be deleted. Per-file failures do not abort T3; the loop continues and the failure is recorded in the deletion log.

What the user sees: the manifest records the file with `deletion_status: failed_permission` or `failed_immutable`. The run's exit status reflects whether any deletions failed.

Recovery action: address the underlying permission or immutability issue (`chflags noschg`, fix ownership) and re-run; T3 is idempotent against already-deleted files.

---

## T4 (finalize) events

### `manifest_finalized`

**Phase**: T4.
**When emitted**: after the gzipped manifest has been closed and atomically renamed from `manifest.ndjson.tmp.gz` to `manifest.ndjson.gz`.
**Required `Details` fields**: `tmp_path` (string), `final_path` (string).
**Optional `Details` fields**: none.
**Status**: emitted in v0.1.

What it means: the per-file manifest is durable. Verify operations can now read it; debug bundles include the final file.

Recovery hint: no operator action required.

---

## Cross-reference: canonical Event Kinds table

The canonical list of Event Kinds lives in `docs/planning/2026-06-03-flashbackup-core-engine.md` under the "Canonical Event Kinds" heading. This catalog elaborates each entry with operator-facing context; the planning doc remains the source of truth for the required `Details` schema. If the two disagree, the planning doc wins and this file is updated to match (the contract test in `internal/state/event_catalog_test.go` enforces the union).
