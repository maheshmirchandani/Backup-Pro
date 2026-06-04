package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// File-number vs phase-wire-string trap: the file ordinal is "t4" to match
// sibling tN sequencing but the PHASE THIS FILE EXECUTES is T3 (move-mode
// delete-source), NOT T4. Phase T4 is finalize (Task 27, t5_finalize.go).
// All wire-string emissions here use types.PhaseDelete (underlying "T3").
// See runner/doc.go.
//
// This is the highest-stakes phase in the runner: it permanently unlinks
// source files. The two locked guards (invariant #1 atomic gate, invariant
// #8 mutation gate) MUST NOT be relaxed.

// T4Input is the minimal config for phase T3 (move-mode delete-source).
// Filename is t4_delete_source.go for sequential consistency; WIRE PHASE = T3.
type T4Input struct {
	// SourceRoot is the resolved absolute path of the source root from T0.
	// Carried for forensic context; we re-stat each Candidate.AbsolutePath.
	SourceRoot string

	// Candidates is the enumerated file set (T1Result.Candidates). For each
	// gate-passed iteration we re-stat the AbsolutePath and decide.
	Candidates []selection.Candidate

	// Signatures maps RelativePath -> {Size, MtimeNS} captured at T0+.
	// The per-file re-stat (defense in depth on top of T2) compares the
	// current stat to this baseline; any drift means skipped_mutated.
	Signatures map[string]types.Signature

	// Mode controls the entire phase:
	//   - ModeCopy: no-op short-circuit. We emit phase_started +
	//     phase_completed{skipped:true} and return T4Result{} with zero
	//     counts. No deletion-log opened, no source touched.
	//   - ModeMove: full T3 (atomic gate then per-file loop).
	Mode types.Mode

	// T3Result is the output of Task 25's phase T2. We read FilesVerified
	// vs FilesTotal for the atomic gate (invariant #1) and PerFileStatus
	// for the per-file skip-on-non-verified loop guard.
	T3Result *T3Result

	// DotDir is the on-USB <USB>/.flashbackup directory.
	// deletion-log.ndjson lives at <DotDir>/runs/<RunID>/deletion-log.ndjson.
	DotDir string

	// RunID is the canonical run identifier.
	RunID string

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil triggers a typed error at entry).
	EventStore state.EventStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid
	// per PS3; events still persist to events.ndjson.
	UIRenderer types.Renderer
}

// T4Result reports the T3 phase outcome. The runner state machine (Task 29)
// consumes this for the RunResult and for the runs.ndjson "finished" line.
type T4Result struct {
	// GateBlocked: true iff the atomic gate (invariant #1) fired. When
	// true, no per-file work happened and no source was touched.
	GateBlocked bool

	// FilesEligibleForDelete is the count of T2-verified files (the gate's
	// input set). Populated even when the gate blocks, so the runs.ndjson
	// "finished" line can report what would have been deleted.
	FilesEligibleForDelete int

	FilesDeleted        int
	FilesSkippedMutated int // T3 re-stat differed from T1 Signature
	FilesDeleteFailed   int // unlink failed (permission, busy, etc.)

	// PerFileOutcome keyed by RelativePath; nil on copy-mode short-circuit.
	// Only populated for files we ATTEMPTED to delete (the verified subset
	// when the gate passes). Non-verified files have no entry here.
	PerFileOutcome map[string]state.DeletionStatus

	// DeletionLogPath is <DotDir>/runs/<RunID>/deletion-log.ndjson; empty
	// when copy mode or when the gate fires (we never opened the file).
	// Useful for support-bundle inclusion.
	DeletionLogPath string
}

// deletionLogWriter is the minimal interface RunT4DeleteSource needs from
// the deletion-log file. Exposed as an interface (not a concrete *os.File)
// so the test suite can inject a wrapper whose Sync fails on the Nth call
// (invariant: fsync-per-line for crash recovery, NOT batched).
//
// Implementations are single-writer; not concurrency-safe.
type deletionLogWriter interface {
	Write(p []byte) (int, error)
	Sync() error
	Close() error
}

// deletionLogTestHook is the test seam for swapping the writer. nil in
// production means "use the real *os.File at path with mode 0644". Tests
// set this in a t.Cleanup-paired pattern via restoreDeletionLogTestHook.
var deletionLogTestHook func(path string) (deletionLogWriter, error)

// restoreDeletionLogTestHook captures the current hook value and returns a
// cleanup func that restores it. Used in t.Cleanup so a failed test does
// not pollute the next test's writer.
func restoreDeletionLogTestHook() func() {
	prev := deletionLogTestHook
	return func() { deletionLogTestHook = prev }
}

// deletionLogLine is one JSON object per attempted file. The schema is
// {v, path, status, attempted_at, errno?, error?}; status carries the
// state.DeletionStatus wire string. ErrnoString is the POSIX errno name
// when known (best-effort: "EACCES", "ENOENT", etc.) and is omitted when
// the underlying error did not surface a recognizable errno.
//
// fsync-per-line is the lifecycle contract: the deletion-log IS the
// crash-recovery record. A partial write that survives a crash MUST tell
// the next-run reconciliation exactly which unlinks landed.
type deletionLogLine struct {
	V           int    `json:"v"`
	Path        string `json:"path"`
	Status      string `json:"status"`
	AttemptedAt string `json:"attempted_at"`
	ErrnoString string `json:"errno,omitempty"`
	Error       string `json:"error,omitempty"`
}

// RunT4DeleteSource executes phase T3 (move-mode delete-source).
//
// Mode handling:
//   - ModeCopy: emit phase_started + phase_completed{skipped:true}, return
//     T4Result{} with nil PerFileOutcome. We never open the deletion-log
//     and never touch the source. Copy mode does not own deletion.
//   - ModeMove: full T3 below.
//
// Atomic gate (invariant #1):
//   - If T3Result.FilesVerified != T3Result.FilesTotal: GATE FIRES.
//     Emit atomic_gate_blocked event with Details{failed_count}; tell the
//     renderer the phase aborted (Status="aborted"; the gate firing is a
//     protective outcome from the user's PoV); emit phase_completed (the
//     phase itself completed cleanly; we are not in a crashed state).
//     Set T4Result.GateBlocked = true. Return without unlinking ANY file
//     and without opening the deletion-log.
//   - Else: gate passes; proceed to the per-file loop.
//
// Per-file loop (only when gate passes):
//  1. ctx.Err() check between iterations.
//  2. For each Candidate where T3Result.PerFileStatus[RelativePath] ==
//     StatusVerified:
//     a. os.Lstat the AbsolutePath. Lstat failure -> classify as
//     DeletionFailedPermission (any stat failure means we cannot prove
//     non-mutation; fail safely to "denied"). Append deletion-log line,
//     emit delete_failed event with Details{path, errno, error}, continue.
//     b. Compare current (Size, MtimeNS) against Signatures[RelativePath].
//     Differs -> DeletionSkippedMutated; append; emit
//     delete_skipped_mutated event with Details{path}; continue.
//     c. os.Remove the AbsolutePath. errors.Is(err, fs.ErrPermission) ->
//     DeletionFailedPermission. Any other error -> DeletionFailedImmutable.
//     append + emit delete_failed.
//     d. Success -> DeletionDeleted. Append deletion-log line; emit
//     delete_completed event; emit UIEvtFileCompleted(Status="deleted").
//
// Empty source dirs are LEFT IN PLACE (per spec). We only unlink leaf
// files; no rmdir.
//
// Audit-write policy:
//   - phase_started, phase_completed: standard per-phase trio.
//   - atomic_gate_blocked: emitted ONLY when the gate fires.
//   - delete_completed, delete_skipped_mutated, delete_failed: per-file
//     (the gate guarantees no per-file events when blocked).
//   - All EventStore.Append failures are FATAL (matches Task 22-25). On a
//     fatal Append failure mid-loop, we do NOT compound-error by Appending
//     phase_aborted to the same store; we tell the renderer the phase
//     aborted and best-effort Checkpoint.
//
// deletion-log.ndjson Write/Sync failures are also FATAL: the deletion
// record IS the crash-recovery contract. A run that unlinked but lost the
// record cannot be safely reconciled on the next launch.
//
// Lifecycle:
//   - deletion-log.ndjson opened only after the gate passes; closed (with
//     a final fsync) on every return path via defer.
//   - This function does not own the EventStore lifecycle.
func RunT4DeleteSource(ctx context.Context, in T4Input) (*T4Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T3: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T3: EventStore is nil")
	}
	if in.Mode == types.ModeMove && in.T3Result == nil {
		return nil, fmt.Errorf("runner T3: T3Result is nil in move mode")
	}

	phaseWire := string(types.PhaseDelete)
	startedAt := time.Now().UTC()

	// phase_started persisted event. Audit failure here is fatal.
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		return nil, fmt.Errorf("runner T3: append phase_started: %w", err)
	}
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhaseDelete,
		Timestamp: startedAt,
	})

	// Copy-mode short-circuit. Per spec section 4: copy mode never touches
	// the source. We emit the phase trio with skipped:true so the audit
	// trail correctly records that the phase ran but did nothing, rather
	// than leaving a gap.
	if in.Mode == types.ModeCopy {
		return t4FinishSkipped(ctx, in, phaseWire, startedAt)
	}

	// ----- ModeMove path -----

	// Atomic gate (invariant #1). Any non-verified file at T2 means we
	// MUST NOT touch any source. This is the central data-safety invariant
	// of the entire runner: copy then validate then delete, with the
	// validation gate refusing to delete when ANY file failed.
	if in.T3Result.FilesVerified != in.T3Result.FilesTotal {
		failedCount := in.T3Result.FilesTotal - in.T3Result.FilesVerified
		return t4FinishGateBlocked(ctx, in, phaseWire, startedAt, failedCount)
	}

	// Gate passed. Open the deletion-log writer before the loop. A failure
	// here aborts the phase before any source is touched (the deletion-log
	// IS the crash-recovery contract; we cannot proceed without it).
	delLogPath := filepath.Join(in.DotDir, "runs", in.RunID, "deletion-log.ndjson")
	logWriter, err := openDeletionLog(delLogPath)
	if err != nil {
		wrapped := fmt.Errorf("runner T3: open deletion-log: %w", err)
		runT4Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}
	defer func() {
		// Final fsync + close. Errors on the close path are not surfaced
		// to the caller (any meaningful failure was already surfaced via
		// the per-line Sync). Close ensures kernel buffers flush.
		_ = logWriter.Sync()
		_ = logWriter.Close()
	}()

	result := &T4Result{
		FilesEligibleForDelete: in.T3Result.FilesVerified,
		PerFileOutcome:         make(map[string]state.DeletionStatus, in.T3Result.FilesVerified),
		DeletionLogPath:        delLogPath,
	}

	for _, c := range in.Candidates {
		// Mid-loop cancellation check. Per-file work is short-running once
		// started (Lstat + Remove); we do not preempt mid-syscall.
		if err := ctx.Err(); err != nil {
			wrapped := fmt.Errorf("runner T3: %w", err)
			runT4Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
			return nil, wrapped
		}

		// Defensive: skip non-verified files. The gate guarantees they
		// should be absent here (FilesVerified == FilesTotal), but if a
		// future T2 invariant relaxes this, we still refuse to delete.
		if in.T3Result.PerFileStatus[c.RelativePath] != state.StatusVerified {
			continue
		}

		status, attemptErr := t4AttemptDelete(c, in.Signatures)

		// Update aggregate counters.
		result.PerFileOutcome[c.RelativePath] = status
		switch status {
		case state.DeletionDeleted:
			result.FilesDeleted++
		case state.DeletionSkippedMutated:
			result.FilesSkippedMutated++
		case state.DeletionFailedPermission, state.DeletionFailedImmutable:
			result.FilesDeleteFailed++
		}

		// Persist deletion-log line (fsync-per-line). A failure here is
		// FATAL: the deletion record is the crash-recovery contract. We
		// already may have unlinked the file (status==deleted), but the
		// next run will reconcile via the existing audit events as a
		// safety net. The abort prevents compounding the loss.
		if err := writeDeletionLogLine(logWriter, c.RelativePath, status, attemptErr); err != nil {
			wrapped := fmt.Errorf("runner T3: write deletion-log: %w", err)
			runT4Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
			return nil, wrapped
		}

		// Emit the per-file audit event. Audit failure is FATAL.
		if err := t4EmitPerFileAudit(ctx, in.EventStore, phaseWire, c.RelativePath, status, attemptErr); err != nil {
			wrapped := fmt.Errorf("runner T3: append per-file %q: %w", c.RelativePath, err)
			// Do NOT re-Append phase_aborted to the same store (the store
			// just failed). Tell the renderer so a TUI is not stranded.
			finishedAt := time.Now().UTC()
			emitUI(ctx, in.UIRenderer, types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseDelete,
				Status:    "aborted",
				Err:       wrapped,
				Timestamp: finishedAt,
			})
			auditCtx, cancel := runT1AuditCtx(ctx)
			_ = in.EventStore.Checkpoint(auditCtx)
			cancel()
			return nil, wrapped
		}

		// UIEvtFileCompleted: tell the renderer the per-file outcome.
		emitUI(ctx, in.UIRenderer, types.UIEvent{
			Kind:      types.UIEvtFileCompleted,
			Phase:     types.PhaseDelete,
			Path:      c.RelativePath,
			Status:    string(status),
			Timestamp: time.Now().UTC(),
		})
	}

	// phase_completed audit event with totals.
	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms":           durationMS,
			"files_eligible":        result.FilesEligibleForDelete,
			"files_deleted":         result.FilesDeleted,
			"files_skipped_mutated": result.FilesSkippedMutated,
			"files_delete_failed":   result.FilesDeleteFailed,
		},
	}); err != nil {
		auditCtx, cancel := runT1AuditCtx(ctx)
		_ = in.EventStore.Checkpoint(auditCtx)
		cancel()
		return nil, fmt.Errorf("runner T3: append phase_completed: %w", err)
	}

	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseDelete,
		Status:    "ok",
		Timestamp: finishedAt,
	})

	// Phase-boundary fsync (invariant #17).
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T3: checkpoint events: %w", err)
	}

	return result, nil
}

// t4FinishSkipped emits the copy-mode no-op trio (phase_started already
// landed; this writes phase_completed with skipped:true and the matching
// UI event). Returns a zero T4Result.
func t4FinishSkipped(ctx context.Context, in T4Input, phaseWire string, startedAt time.Time) (*T4Result, error) {
	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms": durationMS,
			"skipped":     true,
		},
	}); err != nil {
		return nil, fmt.Errorf("runner T3: append phase_completed (skipped): %w", err)
	}
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseDelete,
		Status:    "ok",
		Timestamp: finishedAt,
	})
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T3: checkpoint events: %w", err)
	}
	return &T4Result{}, nil
}

// t4FinishGateBlocked emits atomic_gate_blocked + phase_completed when the
// gate fires (invariant #1). The renderer is told the phase aborted
// because, from the operator's PoV, no deletion happened; the audit's
// phase_completed reflects that the PHASE finished cleanly (we are not in
// a crash state). Returns T4Result with GateBlocked=true and zero counts.
func t4FinishGateBlocked(ctx context.Context, in T4Input, phaseWire string,
	startedAt time.Time, failedCount int) (*T4Result, error) {

	now := time.Now().UTC()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: now,
		Phase:     phaseWire,
		Kind:      "atomic_gate_blocked",
		Details: map[string]any{
			"failed_count": failedCount,
		},
	}); err != nil {
		return nil, fmt.Errorf("runner T3: append atomic_gate_blocked: %w", err)
	}

	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms":  durationMS,
			"gate_blocked": true,
			"failed_count": failedCount,
		},
	}); err != nil {
		auditCtx, cancel := runT1AuditCtx(ctx)
		_ = in.EventStore.Checkpoint(auditCtx)
		cancel()
		return nil, fmt.Errorf("runner T3: append phase_completed (gate): %w", err)
	}

	// Tell the renderer the phase aborted from the user's PoV (gate is a
	// protective abort: the run will exit copy_only_aborted_delete per
	// types.ExitStatusCopyOnlyAbortedDelete).
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseDelete,
		Status:    "aborted",
		Timestamp: finishedAt,
	})

	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T3: checkpoint events (gate): %w", err)
	}

	return &T4Result{
		GateBlocked:            true,
		FilesEligibleForDelete: in.T3Result.FilesVerified,
	}, nil
}

// t4AttemptDelete runs the per-file delete decision for one Candidate:
// Lstat (mutation gate), Remove. Returns the DeletionStatus and the
// underlying error (for inclusion in audit Details and deletion-log).
// Soft errors do not abort the run: per-file errors are recorded and the
// loop continues.
//
// fs.ErrPermission discrimination: errors.Is is the right sentinel. The
// fault-injection contract (Task 28) will inject permission errors that
// satisfy errors.Is(err, fs.ErrPermission) so this branch fires the same
// way under both real chmod and synthetic faults.
func t4AttemptDelete(c selection.Candidate, sigs map[string]types.Signature) (state.DeletionStatus, error) {
	// Mutation gate: Lstat the source. os.Lstat (not Stat) matches the T2
	// pattern: a symlink that changed target between T0+ and T3 counts as
	// a mutation, and following the link would silently mask that.
	srcStat, err := os.Lstat(c.AbsolutePath)
	if err != nil {
		// Stat failed (ENOENT, EACCES, ...). We treat any stat failure as
		// failed_permission rather than skipped_mutated: we cannot prove
		// non-mutation, and the gate's safer failure mode is "denied".
		return state.DeletionFailedPermission, fmt.Errorf("lstat source: %w", err)
	}

	baseline, ok := sigs[c.RelativePath]
	if !ok {
		// Defensive: missing baseline means we have nothing to compare
		// against. Fail-closed as skipped_mutated (cannot prove non-mutation).
		return state.DeletionSkippedMutated, fmt.Errorf("missing signature for %q", c.RelativePath)
	}

	if srcStat.Size() != baseline.Size || srcStat.ModTime().UnixNano() != baseline.MtimeNS {
		// (size, mtime_ns) drifted since T0+. The user mutated the source
		// between T2 hashing and T3 deletion. Defense in depth on top of
		// T2's mutation gate: refuse to delete; the user keeps the file.
		return state.DeletionSkippedMutated, nil
	}

	// Permanent unlink. Per spec section 4: NOT Trash, NOT a move-to-pending
	// staging area. The user typed DELETE upfront to authorize this; the CLI
	// front-door confirmation lives outside this phase.
	if err := os.Remove(c.AbsolutePath); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return state.DeletionFailedPermission, fmt.Errorf("remove: %w", err)
		}
		// Other unlink failures (EBUSY, EROFS, ...) get the generic bucket.
		return state.DeletionFailedImmutable, fmt.Errorf("remove: %w", err)
	}
	return state.DeletionDeleted, nil
}

// t4EmitPerFileAudit emits the per-file audit event matching the deletion
// outcome. Per the canonical Event Kinds table:
//   - delete_completed -> Details{path}
//   - delete_skipped_mutated -> Details{path}
//   - delete_failed -> Details{path, error}
//
// Returns the EventStore.Append error so the caller can abort the phase.
func t4EmitPerFileAudit(ctx context.Context, es state.EventStore, phaseWire string,
	relPath string, status state.DeletionStatus, attemptErr error) error {

	now := time.Now().UTC()
	switch status {
	case state.DeletionDeleted:
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "delete_completed",
			Path:      relPath,
			Details: map[string]any{
				"path": relPath,
			},
		})
	case state.DeletionSkippedMutated:
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "delete_skipped_mutated",
			Path:      relPath,
			Details: map[string]any{
				"path": relPath,
			},
		})
	default:
		// failed_permission OR failed_immutable: both flow through
		// delete_failed with the underlying error string in Details.
		details := map[string]any{
			"path": relPath,
		}
		if attemptErr != nil {
			details["error"] = attemptErr.Error()
		}
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "delete_failed",
			Path:      relPath,
			Details:   details,
		})
	}
}

// openDeletionLog opens the deletion-log.ndjson for the run. Default
// behavior creates the file at mode 0644 (no secrets; matches the
// rsync.log convention) with O_CREATE|O_TRUNC|O_WRONLY. The test hook
// (deletionLogTestHook) substitutes a wrapper when set, used by tests
// that inject Sync failures.
func openDeletionLog(path string) (deletionLogWriter, error) {
	if deletionLogTestHook != nil {
		return deletionLogTestHook(path)
	}
	// 0644: deletion-log contains relative file paths (which a determined
	// reader could already get from manifest.ndjson.gz on the same volume).
	// World-readable so support tooling running as the user can collect it
	// into a debug bundle without sudo.
	return os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
}

// writeDeletionLogLine encodes one deletionLogLine, writes it to the
// writer, and calls Sync() to fsync the kernel buffer. Per-line fsync is
// the crash-recovery contract: a partial run interrupted by a kernel panic
// MUST be reconcilable from this file alone (events.ndjson is also
// durable post-Checkpoint, but the deletion-log is the canonical record
// of which unlinks landed).
//
// Errors are wrapped with the relative path for forensic correlation.
func writeDeletionLogLine(w deletionLogWriter, relPath string, status state.DeletionStatus, attemptErr error) error {
	line := deletionLogLine{
		V:           1,
		Path:        relPath,
		Status:      string(status),
		AttemptedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if attemptErr != nil {
		line.Error = attemptErr.Error()
	}
	encoded, err := json.Marshal(line)
	if err != nil {
		// json.Marshal on a flat struct of strings/ints should never fail;
		// surface it loudly if it ever does.
		return fmt.Errorf("encode deletion-log line %q: %w", relPath, err)
	}
	encoded = append(encoded, '\n')
	if _, err := w.Write(encoded); err != nil {
		return fmt.Errorf("write deletion-log %q: %w", relPath, err)
	}
	if err := w.Sync(); err != nil {
		return fmt.Errorf("fsync deletion-log %q: %w", relPath, err)
	}
	return nil
}

// runT4Abort centralizes the phase_aborted path for cancellation and fatal
// non-audit-store error branches in RunT4DeleteSource. Shape mirrors
// runT1Abort / runT3Abort: best-effort Append under the shared 5-second
// audit budget; emit UIEvtPhaseCompleted(aborted); Checkpoint best-effort.
// Reuses runT1AuditCtx.
//
// Important: callers must NOT invoke runT4Abort when the audit store
// itself just failed (Appending phase_aborted to a just-failed store
// would likely fail too, masking the original error). The Append-failure
// branches inline their own renderer notification + Checkpoint instead.
func runT4Abort(ctx context.Context, es state.EventStore, ui types.Renderer,
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
		Phase:     types.PhaseDelete,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	_ = es.Checkpoint(auditCtx)
}
