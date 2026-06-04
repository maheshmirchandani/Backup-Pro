package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// File-number vs phase-wire-string trap: the file ordinal is "t5" to match
// sibling tN sequencing but the PHASE THIS FILE EXECUTES is T4 (finalize),
// NOT T5. There is no T5 phase in the spec; the phase legend ends at T4.
// All wire-string emissions here use types.PhaseFinalize (underlying "T4").
// See runner/doc.go.
//
// This is the LAST runner phase. After this returns, the state machine is
// complete; the top-level runner.Run (Task 29) releases the PreflightContext
// and exits with the resolved ExitStatus.

// DefaultRetentionLimit is the number of completed runs to keep on the USB
// before pruning the oldest. Matches the spec / status JSON
// "retention_limit" constant. Future versions may make this configurable
// per-USB via a CLI flag or version.json field; the constant lives here so
// changing the default touches one place.
const DefaultRetentionLimit = 10

// manifestBaseFilename is the canonical on-disk name of the gzipped
// per-run manifest BEFORE the .gz suffix. Used by the T2 manifest store
// (which writes .tmp.gz) and by T4 finalize (which reads the .tmp.gz path,
// emits Details{tmp_path, final_path}, and triggers the atomic rename).
//
// One source of truth: any future rename (e.g. manifest.v2.ndjson) flows
// through this constant.
const manifestBaseFilename = "manifest.ndjson"

// runIDPattern matches the canonical RunID format "YYYY-MM-DDTHHMMZ-XXXX"
// where XXXX is a 4-character hex suffix. Used by the prune pass to filter
// run-dir candidates from <DotDir>/runs/: only entries whose names match
// are eligible to be pruned (defensive against arbitrary user content under
// the runs dir; never accidentally remove a file/dir we did not create).
//
// Lexical sort of matching names gives chronological order because the
// timestamp prefix is fixed-width and UTC.
var runIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}Z-[0-9a-fA-F]{4}$`)

// T5Input is the minimal config for phase T4 (finalize).
// Filename is t5_finalize.go for sequential consistency; WIRE PHASE = T4.
type T5Input struct {
	// RunID identifies the current run; written as-is into the runs.ndjson
	// "finished" line and never pruned regardless of position in the
	// retention list.
	RunID string

	// FlashbackupVersion is the build identifier written to the runs.ndjson
	// "finished" line for forensic correlation across upgrades.
	FlashbackupVersion string

	// StartedAt is the run's T0 start timestamp, threaded through from
	// runner.Run for the runs.ndjson "finished" line. NOT the T4 phase
	// start (the phase computes its own startedAt for duration_ms).
	StartedAt time.Time

	// SourceRoot and DestRoot mirror the values written to the "started"
	// line so the started+finished pair is symmetric.
	SourceRoot string
	DestRoot   string

	// Mode and ProfileName are carried through for the "finished" line.
	// ProfileName may be empty for ad-hoc runs.
	Mode        types.Mode
	ProfileName string

	// ExitStatus is the run-level exit status resolved by runner.Run from
	// T2 verification counts and T3 atomic-gate state. Written verbatim to
	// the "finished" line and to the run_finished event Details.
	ExitStatus string

	// DotDir is the on-USB <USB>/.flashbackup directory. Used to derive
	// the runs/ list for retention pruning and the manifest path scheme
	// for the manifest_finalized event Details.
	DotDir string

	// Aggregated counters from earlier phases. T2 (RunT3HashCompare)
	// produced FilesVerified; runner.Run aggregates these into the
	// runs.ndjson "finished" line. Source of truth for downstream
	// status JSON consumers.
	FilesTotal                    int
	FilesSucceeded                int
	FilesFailed                   int
	BytesTotal                    int64
	DeletionsSkippedDueToMutation int

	// ManifestStore is the per-run manifest sink opened by the top-level
	// runner. This phase calls Gzip() exactly once to finalize the .gz
	// file (flush gzip trailer, fsync, atomic rename .tmp.gz -> .gz,
	// fsync parent dir). Required (nil triggers a typed error at entry).
	ManifestStore state.ManifestStore

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil triggers a typed error at entry).
	EventStore state.EventStore

	// RunLogStore is the runs.ndjson sink opened by the top-level runner.
	// This phase calls AppendFinished + Checkpoint to land the durable
	// "finished" line (invariant #10 two-line model). Required.
	RunLogStore state.RunLogStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid
	// per PS3; events still persist to events.ndjson.
	UIRenderer types.Renderer

	// RetentionLimit is the number of completed runs to keep before
	// pruning the oldest. Zero means use DefaultRetentionLimit (10).
	// Negative values are treated as zero (pruning still bounded by
	// the default) so a future buggy CLI flag does not catastrophically
	// remove every run dir.
	RetentionLimit int
}

// T5Result reports the finalize phase outcome. The runner state machine
// (Task 29) consumes this to populate RunResult and to emit the
// support-bundle file list.
type T5Result struct {
	// ManifestPath is the final .gz path of the per-run manifest (NOT
	// .tmp.gz). Useful for support-bundle inclusion. Empty only if the
	// finalize aborted before the Gzip call returned successfully.
	ManifestPath string

	// PrunedRunIDs is the slice of run-dir names that were removed by
	// the retention pass. May be empty if no pruning was needed or if
	// pruning failures left no successful removals.
	PrunedRunIDs []string
}

// RunT5Finalize executes phase T4 (finalize).
//
// Order:
//
//  1. ctx.Err() check at entry; required-store non-nil checks.
//  2. phase_started (Phase=T4) via EventStore + UIEvtPhaseStarted via Renderer.
//  3. ManifestStore.Gzip(ctx): flush gzip trailer, fsync .tmp.gz, close,
//     atomically rename .tmp.gz to .gz, fsync parent dir. All inside the
//     store implementation (state.ndjsonManifestStore.Gzip).
//  4. Emit manifest_finalized event with Details{tmp_path, final_path}.
//  5. RunLogStore.AppendFinished with the full FinishedRun fields.
//  6. RunLogStore.Checkpoint(ctx): the two-line model invariant #10
//     depends on durable persistence of the started+finished pair.
//  7. Prune old run dirs under <DotDir>/runs/:
//     - filter to entries whose names match the canonical RunID regex;
//     - sort lexically (== chronological);
//     - the current RunID is NEVER pruned regardless of position;
//     - keep newest RetentionLimit, RemoveAll the rest;
//     - prune failures are NON-FATAL and audit-silent (the stale dir
//     remains on disk for the next run's preflight to retry).
//  8. phase_completed (Phase=T4) with Details{duration_ms, pruned_count}.
//  9. UIEvtPhaseCompleted (Status="ok") via Renderer.
//
// 10. run_finished event with Details{exit_status}. Last event of the run.
// 11. EventStore.Checkpoint one more time so run_finished is durable.
//
// Ordering rationale:
//   - manifest_finalized fires BEFORE AppendFinished because the "finished"
//     line marks the run as observably-complete to downstream verify
//     tooling; that tooling reads the manifest, so the manifest must be
//     by-then on disk.
//   - run_finished fires AFTER phase_completed because phase_completed is
//     the standard per-phase boundary marker; run_finished is the
//     run-level terminal event whose presence indicates "this is the very
//     last event of this run."
//
// Fatality policy:
//   - ManifestStore.Gzip failure is FATAL: the manifest IS the run's
//     authoritative file-by-file record; a finalize that cannot persist
//     it cannot honestly claim the run finished.
//   - RunLogStore.AppendFinished failure is FATAL: without the "finished"
//     line, the run looks crashed to the next preflight (invariant #10).
//   - EventStore.Append failures for the four events here are FATAL
//     (matches Tasks 22-26 fatality policy).
//   - Prune failures are NON-FATAL: a failed RemoveAll leaves the stale
//     dir on disk, the prune pass continues, and PrunedRunIDs reports
//     only the runs that WERE removed.
//
// This function does NOT release the PreflightContext (the top-level
// runner.Run owns the lifecycle).
func RunT5Finalize(ctx context.Context, in T5Input) (*T5Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T4: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T4: EventStore is nil")
	}
	if in.ManifestStore == nil {
		return nil, fmt.Errorf("runner T4: ManifestStore is nil")
	}
	if in.RunLogStore == nil {
		return nil, fmt.Errorf("runner T4: RunLogStore is nil")
	}

	phaseWire := string(types.PhaseFinalize)
	startedAt := time.Now().UTC()

	// 1. phase_started. Audit failure here is fatal.
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		return nil, fmt.Errorf("runner T4: append phase_started: %w", err)
	}
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhaseFinalize,
		Timestamp: startedAt,
	})

	// 2. ManifestStore.Gzip: flush + fsync + atomic rename + fsync parent.
	// A failure here is fatal: the manifest IS the run's authoritative
	// record. We surface the wrapped error to runner.Run without emitting
	// run_finished (it would be misleading to claim the run finished when
	// the manifest could not be sealed).
	if err := in.ManifestStore.Gzip(ctx); err != nil {
		wrapped := fmt.Errorf("runner T4: gzip manifest: %w", err)
		t5Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// Manifest paths for the manifest_finalized Details. Reconstruct from
	// the spec's on-disk layout (<DotDir>/runs/<RunID>/manifest.ndjson*).
	// The ManifestStore's own knowledge of the path is private; we keep
	// the derivation symmetric with the rest of the run.
	runDir := filepath.Join(in.DotDir, "runs", in.RunID)
	tmpPath := filepath.Join(runDir, manifestBaseFilename+".tmp.gz")
	finalPath := filepath.Join(runDir, manifestBaseFilename+".gz")

	// 3. manifest_finalized event. Required Details {tmp_path, final_path}
	// per the canonical Event Kinds table (plan line 368).
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: time.Now().UTC(),
		Phase:     phaseWire,
		Kind:      "manifest_finalized",
		Details: map[string]any{
			"tmp_path":   tmpPath,
			"final_path": finalPath,
		},
	}); err != nil {
		wrapped := fmt.Errorf("runner T4: append manifest_finalized: %w", err)
		// Audit just failed; do NOT re-Append phase_aborted to the same
		// store (matches the Task 22-26 compound-error guard). Tell the
		// renderer and best-effort Checkpoint.
		t5AbortOnAuditFail(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// 4. RunLogStore.AppendFinished. Failure is fatal: without this line
	// the run looks crashed to the next preflight (invariant #10).
	finishedAt := time.Now().UTC()
	if err := in.RunLogStore.AppendFinished(ctx, state.FinishedRun{
		V:                             1,
		Event:                         "finished",
		FlashbackupVersion:            in.FlashbackupVersion,
		RunID:                         in.RunID,
		StartedAt:                     in.StartedAt,
		FinishedAt:                    finishedAt,
		Mode:                          string(in.Mode),
		Profile:                       in.ProfileName,
		SourceRoot:                    in.SourceRoot,
		DestRoot:                      in.DestRoot,
		FilesTotal:                    in.FilesTotal,
		FilesSucceeded:                in.FilesSucceeded,
		FilesFailed:                   in.FilesFailed,
		BytesTotal:                    in.BytesTotal,
		DeletionsSkippedDueToMutation: in.DeletionsSkippedDueToMutation,
		ExitStatus:                    in.ExitStatus,
	}); err != nil {
		wrapped := fmt.Errorf("runner T4: append finished line: %w", err)
		t5Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// 5. Checkpoint runlog so the started+finished pair survives a kernel
	// panic / power loss. Two-line model invariant #10 durability.
	if err := in.RunLogStore.Checkpoint(ctx); err != nil {
		wrapped := fmt.Errorf("runner T4: checkpoint runlog: %w", err)
		t5Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// 6. Prune old run dirs. Non-fatal; PrunedRunIDs records what WAS
	// removed. Failures leave stale dirs on disk for next-run retry.
	pruned := pruneOldRunDirs(in.DotDir, in.RunID, effectiveRetentionLimit(in.RetentionLimit))

	// 7. phase_completed. Audit failure is fatal.
	completedAt := time.Now().UTC()
	durationMS := completedAt.Sub(startedAt).Milliseconds()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: completedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms":  durationMS,
			"pruned_count": len(pruned),
		},
	}); err != nil {
		wrapped := fmt.Errorf("runner T4: append phase_completed: %w", err)
		t5AbortOnAuditFail(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseFinalize,
		Status:    "ok",
		Timestamp: completedAt,
	})

	// 8. run_finished event. Last event of the run. Required Details
	// {exit_status} per the canonical Event Kinds table (plan line 369).
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: time.Now().UTC(),
		Phase:     phaseWire,
		Kind:      "run_finished",
		Details: map[string]any{
			"exit_status": in.ExitStatus,
		},
	}); err != nil {
		wrapped := fmt.Errorf("runner T4: append run_finished: %w", err)
		// run_finished is the terminal event; if it failed to land we
		// still report the wrapped error to runner.Run. The runs.ndjson
		// "finished" line IS already durable (Checkpoint above), so the
		// next preflight sees the run as cleanly finished; the audit
		// log will simply lack the terminal run_finished line.
		t5AbortOnAuditFail(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// 9. Final EventStore.Checkpoint so run_finished is durable on disk.
	// Phase-boundary fsync (invariant #17).
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T4: checkpoint events: %w", err)
	}

	return &T5Result{
		ManifestPath: finalPath,
		PrunedRunIDs: pruned,
	}, nil
}

// effectiveRetentionLimit applies the documented defaulting policy: a zero
// or negative input means DefaultRetentionLimit (10). Negative is treated
// as "use the default" rather than "keep zero" so a future buggy CLI flag
// (e.g. parse error returning -1) does not catastrophically prune every
// run dir.
func effectiveRetentionLimit(n int) int {
	if n <= 0 {
		return DefaultRetentionLimit
	}
	return n
}

// pruneOldRunDirs scans <DotDir>/runs/, filters to canonical-RunID-named
// entries, sorts lexically (== chronological), and removes the oldest
// entries beyond the retention limit. The current RunID is NEVER pruned
// regardless of its position.
//
// Returns the slice of RunIDs that were SUCCESSFULLY removed. Per-dir
// removal failures are silently swallowed (audit-silent policy: the stale
// dir remains on disk, and the next run's preflight or operator-driven
// support tooling can investigate). PrunedRunIDs does not include
// dirs whose RemoveAll returned an error.
func pruneOldRunDirs(dotDir, currentRunID string, retentionLimit int) []string {
	runsDir := filepath.Join(dotDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		// If the runs dir does not exist (e.g. test that bypassed setup),
		// nothing to prune. Audit-silent.
		return nil
	}

	// Filter to canonical RunID-named directory entries. Defensive
	// against arbitrary files (a future operator script could place
	// notes under .flashbackup/runs/); we only consider entries we
	// recognize as run dirs.
	var runIDs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !runIDPattern.MatchString(e.Name()) {
			continue
		}
		runIDs = append(runIDs, e.Name())
	}
	if len(runIDs) <= retentionLimit {
		return nil
	}

	// Sort lexically. The canonical RunID format ("YYYY-MM-DDTHHMMZ-XXXX")
	// is timestamp-prefixed and fixed-width, so lexical order matches
	// chronological order: index 0 is the oldest, index len-1 is newest.
	sort.Strings(runIDs)

	// Compute how many to remove. Skip the current RunID even when it
	// would otherwise be in the prune set; in that case we still attempt
	// to keep len(runIDs) - retentionLimit + 1 OLDEST candidates? No --
	// the simpler invariant is: prune len(runIDs) - retentionLimit OLDEST,
	// and silently skip the current if it lands in that slice. The user
	// then keeps retentionLimit dirs (one less prune than the count says).
	//
	// Concretely: with 11 dirs and limit 10, we want to prune 1 (the
	// oldest). If that oldest happens to be the current RunID (artificial
	// test case), we skip it and the prune count drops to 0.
	want := len(runIDs) - retentionLimit
	var pruned []string
	for i := 0; i < want && i < len(runIDs); i++ {
		name := runIDs[i]
		if name == currentRunID {
			// Current RunID is never pruned.
			continue
		}
		dirPath := filepath.Join(runsDir, name)
		if err := os.RemoveAll(dirPath); err != nil {
			// Audit-silent: stale dir remains, will be retried on next
			// run. We deliberately do not surface this as a fatal error
			// or new event Kind (the canonical table does not enumerate
			// run_pruned; adding one would be a plan amendment).
			continue
		}
		pruned = append(pruned, name)
	}
	return pruned
}

// t5Abort centralizes the phase_aborted path for non-audit-store fatal
// branches (Gzip failure, AppendFinished failure, runlog Checkpoint
// failure). Shape mirrors runT1Abort / runT4Abort: best-effort Append
// under the shared 5-second audit budget; emit UIEvtPhaseCompleted
// (aborted); Checkpoint best-effort.
//
// Callers must NOT invoke t5Abort when the audit store itself just failed
// (Appending phase_aborted to a just-failed store would likely fail too,
// masking the original error). Those branches use t5AbortOnAuditFail
// below, which skips the Append and only notifies the renderer.
func t5Abort(ctx context.Context, es state.EventStore, ui types.Renderer,
	phaseWire string, startedAt time.Time, wrappedErr error) {
	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()

	auditCtx, cancel := runT1AuditCtx(ctx)
	defer cancel()

	_ = es.Append(auditCtx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_aborted",
		Details: map[string]any{
			"duration_ms": durationMS,
			"error":       wrappedErr.Error(),
		},
	})

	emitUI(ctx, ui, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseFinalize,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	_ = es.Checkpoint(auditCtx)
}

// t5AbortOnAuditFail is the abort path used when the EventStore itself
// just failed an Append. It skips the phase_aborted Append (since the
// store is the failure mode) and only notifies the renderer + best-effort
// Checkpoint. Matches the Task 22-26 compound-error guard.
func t5AbortOnAuditFail(ctx context.Context, es state.EventStore, ui types.Renderer,
	phaseWire string, startedAt time.Time, wrappedErr error) {
	_ = phaseWire // kept for signature parity with t5Abort
	finishedAt := time.Now().UTC()

	emitUI(ctx, ui, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseFinalize,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	auditCtx, cancel := runT1AuditCtx(ctx)
	defer cancel()
	_ = es.Checkpoint(auditCtx)
}
