package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/hash"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// File-number vs phase-wire-string trap: the file ordinal is "t3" to match
// sibling tN sequencing but the PHASE THIS FILE EXECUTES is T2 (hash +
// compare + classify), NOT T3. Phase T3 is delete-source (Task 26,
// t4_delete_source.go). All wire-string emissions here use
// types.PhaseHashCompare (underlying "T2"). See runner/doc.go.

// sha256PrefixChars is the number of leading hex characters of a SHA256
// digest persisted in events.ndjson Details. The spec's canonical Event
// Kinds table calls for "truncated to 16 chars for log size" on
// file_completed and hash_mismatch (plan line 361). 16 hex chars = 64
// bits of digest, more than enough for forensic correlation without
// bloating the audit log. Made a named const so a future schema change
// touches one place.
const sha256PrefixChars = 16

// T3Input is the minimal config the T2 hash+compare phase needs from the
// top-level runner. Filename is t3_hash_compare.go for sequential-numbering
// consistency with sibling tN files; the WIRE PHASE is T2, not T3.
type T3Input struct {
	// SourceRoot is the absolute path of the source root, resolved by T0.
	// Used only as carried-context for diagnostics here; per-file open uses
	// Candidate.AbsolutePath which already encodes the raw on-disk bytes.
	SourceRoot string

	// DestRoot is the per-run destination root (typically <USB>/<host>-<user>).
	// Per-file dest path = filepath.Join(DestRoot, RelativePath).
	DestRoot string

	// Candidates is the enumerated file set from T0+ (T1Result.Candidates).
	// The phase iterates this slice once, in order, classifying each file.
	Candidates []selection.Candidate

	// Signatures maps Candidate.RelativePath -> {Size, MtimeNS} captured at
	// T0+ enumeration time. The mutation gate (invariant #8) re-stats each
	// source at T2 and compares to this baseline; any disagreement marks
	// the file source_mutated.
	Signatures map[string]types.Signature

	// Mode is the run's data-handling intent (copy | move). Carried for
	// forward-compat (the manifest entries are identical between modes in
	// v0.1); Task 26 reads PerFileStatus from T3Result and consults Mode
	// to decide whether to attempt deletion.
	Mode types.Mode

	// ManifestStore is the per-run manifest sink opened by the top-level
	// runner. Required; one AppendEntry per Candidate. The store computes
	// the HMAC over the length-prefixed canonical encoding (invariant #33).
	ManifestStore state.ManifestStore

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil triggers a typed error at entry).
	EventStore state.EventStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid
	// per PS3; events still persist to events.ndjson.
	UIRenderer types.Renderer
}

// T3Result aggregates per-file outcomes for the T3 (delete) atomic gate.
// Task 26 consumes Verified == FilesTotal as the pass-through condition
// for move-mode deletion (invariant #1): any non-verified file means T3
// must NOT delete ANY source. PerFileStatus is the per-file decision lookup
// so Task 26's loop reads one map instead of re-reading the manifest.
type T3Result struct {
	FilesTotal            int
	FilesVerified         int
	FilesHashMismatch     int
	FilesSourceMutated    int
	FilesSourceUnreadable int
	FilesDestUnreadable   int
	FilesNotTransferred   int

	// PerFileStatus maps Candidate.RelativePath to the T2 FileStatus.
	// Task 26 iterates Candidates and reads this map to decide skip-vs-unlink
	// per file. Keyed by RelativePath (not AbsolutePath) because that is
	// the manifest identifier and the Signatures key.
	PerFileStatus map[string]state.FileStatus
}

// RunT3HashCompare executes phase T2 (hash + compare + classify).
//
// Per-file inner loop:
//
//  1. os.Lstat source. If (size, mtime_ns) differs from
//     Signatures[RelativePath]: status = source_mutated; manifest entry with
//     SHA256Source = ""; emit source_mutated event with Details{path};
//     emit UIEvtFileCompleted; do NOT hash either side.
//  2. Open the source file for reading. On error: status = source_unreadable;
//     manifest entry; file_completed event with status=source_unreadable +
//     sha256_source = ""; UIEvtFileCompleted.
//  3. StreamSHA256 the source. Record the hex digest as the manifest's
//     SHA256Source field. On error: status = source_unreadable; same
//     handling as step 2.
//  4. Stat the dest file. On ENOENT: status = not_transferred. Open + read
//     for any other error: status = dest_unreadable.
//  5. StreamSHA256 the dest. On error: dest_unreadable.
//  6. Compare digests:
//     - match -> status = verified; emit file_completed with
//     Details{path, status:"verified", sha256_source: 16-char prefix}.
//     - mismatch -> status = hash_mismatch; emit hash_mismatch with
//     Details{path, sha256_source_prefix, sha256_dest_prefix}. We do
//     NOT also emit file_completed for the mismatched case (the
//     canonical Event Kinds table reserves file_completed for verified
//     outcomes; hash_mismatch IS the per-file mismatch event).
//  7. ManifestStore.AppendEntry. The store computes the HMAC over the
//     length-prefixed canonical encoding (invariant #33).
//  8. emitUI UIEvtFileCompleted with Path + Status.
//
// Per-file errors (open / stat / read failures) are NOT fatal: they are
// recorded as the appropriate FileStatus and the loop continues. The
// atomic gate at T3 (Task 26) reads PerFileStatus and decides whether ANY
// non-verified file blocks ALL source deletion (invariant #1). So T2
// always tries to classify EVERY file.
//
// Cancellation: ctx.Err() is checked at entry AND between every file
// iteration (per-file work is not preemptible mid-hash; the streaming
// hash itself checks ctx between buffer reads, but the open / stat /
// classify sequence around each file is short and runs to completion
// once started). On ctx cancellation: emit phase_aborted via runT3Abort
// under the shared 5-second audit budget; best-effort Checkpoint;
// return wrapped err.
//
// Top-level orchestration mirrors Tasks 22-24:
//   - phase_started (Phase="T2") audit event + UIEvtPhaseStarted
//   - per-file loop classifies + emits per-file audit + UIEvent
//   - phase_completed (Details: duration_ms, files_total, files_verified)
//     OR phase_aborted (Details: duration_ms, error) on ctx cancel
//   - UIEvtPhaseCompleted ok | aborted
//   - Checkpoint the EventStore (phase-boundary fsync per invariant #17)
//
// Audit-write failure policy: any EventStore.Append failure
// (phase_started, file_completed, hash_mismatch, source_mutated,
// phase_completed) aborts the run with a wrapped error. Per Task 22
// contract: audit failures are fatal because the run is no longer
// observable. ManifestStore.AppendEntry failures are also fatal: the
// manifest is the authoritative record of what was backed up, and a
// missing entry would corrupt the T3 atomic gate's view of the run.
//
// Lifecycle: this function does NOT Open or Close the ManifestStore /
// EventStore; the top-level runner owns both. The ManifestStore is
// finalized (Gzip()) at T4 (Task 27), not here, so AppendEntry calls land
// in the gzip stream's internal buffer but the .gz file is not yet renamed.
func RunT3HashCompare(ctx context.Context, in T3Input) (*T3Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T2: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T2: EventStore is nil")
	}
	if in.ManifestStore == nil {
		return nil, fmt.Errorf("runner T2: ManifestStore is nil")
	}

	phaseWire := string(types.PhaseHashCompare)
	startedAt := time.Now().UTC()

	// phase_started persisted event. Audit failure here is fatal.
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		return nil, fmt.Errorf("runner T2: append phase_started: %w", err)
	}

	// Renderer UIEvent. Errors are non-fatal per PS3.
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhaseHashCompare,
		Timestamp: startedAt,
	})

	result := &T3Result{
		FilesTotal:    len(in.Candidates),
		PerFileStatus: make(map[string]state.FileStatus, len(in.Candidates)),
	}

	filesDone := 0
	for _, c := range in.Candidates {
		// Cancellation check between files. The per-file classify routine
		// is short-running; we do not preempt mid-hash here (the
		// streaming hash itself handles ctx between buffer reads).
		if err := ctx.Err(); err != nil {
			wrapped := fmt.Errorf("runner T2: %w", err)
			runT3Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
			return nil, wrapped
		}

		// Fault injection: PointT2PreHash. Per-file (so file= selector
		// works) hook BEFORE the classify routine reads the source. Used
		// by mutation tests to mutate the source between T0+ enumeration
		// and T2 hashing. Phase wire string is "T2-pre" to match
		// --inject:mutate-source:phase=T2-pre specs per the master plan.
		if hookErr := Hook(ctx, PointT2PreHash, HookArgs{
			Phase:       "T2-pre",
			CurrentFile: c.RelativePath,
			FilesDone:   filesDone,
			FilesTotal:  len(in.Candidates),
			SourceRoot:  in.SourceRoot,
			DestRoot:    in.DestRoot,
		}); hookErr != nil {
			wrapped := fmt.Errorf("runner T2: pre-hash fault on %q: %w", c.RelativePath, hookErr)
			runT3Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
			return nil, wrapped
		}

		status, srcDigest, dstDigest, classifyErr := t3ClassifyFile(ctx, c, in.Signatures, in.DestRoot)

		// Fault injection: PointT2PerFile. Per-file hook AFTER classify
		// but BEFORE the audit + manifest writes. Used by tests that need
		// to simulate a fault discovered while classifying a specific file.
		if hookErr := Hook(ctx, PointT2PerFile, HookArgs{
			Phase:       string(types.PhaseHashCompare),
			CurrentFile: c.RelativePath,
			FilesDone:   filesDone,
			FilesTotal:  len(in.Candidates),
			SourceRoot:  in.SourceRoot,
			DestRoot:    in.DestRoot,
		}); hookErr != nil {
			wrapped := fmt.Errorf("runner T2: per-file fault on %q: %w", c.RelativePath, hookErr)
			runT3Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
			return nil, wrapped
		}
		filesDone++

		// Emit the per-file audit event. The Kind depends on the status.
		if appendErr := t3EmitFileAudit(ctx, in.EventStore, phaseWire, c.RelativePath,
			status, srcDigest, dstDigest, classifyErr); appendErr != nil {
			// Mid-stream audit failure. Do NOT emit phase_aborted on the
			// same store (matches t1_enumerate.go pattern): the store
			// just failed, another Append likely also fails. Tell the
			// renderer the phase aborted so a TUI is not stranded on the
			// "started" frame; best-effort Checkpoint; return wrapped err.
			finishedAt := time.Now().UTC()
			wrapped := fmt.Errorf("runner T2: append per-file %q: %w", c.RelativePath, appendErr)
			emitUI(ctx, in.UIRenderer, types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseHashCompare,
				Status:    "aborted",
				Err:       wrapped,
				Timestamp: finishedAt,
			})
			auditCtx, cancel := runT1AuditCtx(ctx)
			_ = in.EventStore.Checkpoint(auditCtx)
			cancel()
			return nil, wrapped
		}

		// AppendEntry to the manifest. The manifest is the authoritative
		// record; a missing entry corrupts the T3 atomic gate's view of
		// the run, so a failure here aborts the phase.
		entry := state.ManifestEntry{
			V:            1,
			Path:         c.RelativePath,
			Size:         in.Signatures[c.RelativePath].Size,
			MtimeNS:      in.Signatures[c.RelativePath].MtimeNS,
			SHA256Source: srcDigest, // empty string when status == source_mutated
			CopiedAt:     time.Now().UTC(),
			Status:       status,
			// DeletionStatus left zero; Task 26 backfills for move mode.
			// HMAC computed by ManifestStore.AppendEntry.
		}
		if err := in.ManifestStore.AppendEntry(ctx, entry); err != nil {
			finishedAt := time.Now().UTC()
			wrapped := fmt.Errorf("runner T2: append manifest entry %q: %w", c.RelativePath, err)
			emitUI(ctx, in.UIRenderer, types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseHashCompare,
				Status:    "aborted",
				Err:       wrapped,
				Timestamp: finishedAt,
			})
			auditCtx, cancel := runT1AuditCtx(ctx)
			_ = in.EventStore.Checkpoint(auditCtx)
			cancel()
			return nil, wrapped
		}

		// UIEvtFileCompleted: tell the renderer per-file outcome regardless
		// of status. The plain renderer summarizes; the TUI updates the row.
		emitUI(ctx, in.UIRenderer, types.UIEvent{
			Kind:      types.UIEvtFileCompleted,
			Phase:     types.PhaseHashCompare,
			Path:      c.RelativePath,
			Status:    string(status),
			Timestamp: time.Now().UTC(),
		})

		// Update aggregate counters + PerFileStatus map.
		result.PerFileStatus[c.RelativePath] = status
		switch status {
		case state.StatusVerified:
			result.FilesVerified++
		case state.StatusHashMismatch:
			result.FilesHashMismatch++
		case state.StatusSourceMutated:
			result.FilesSourceMutated++
		case state.StatusSourceUnreadable:
			result.FilesSourceUnreadable++
		case state.StatusDestUnreadable:
			result.FilesDestUnreadable++
		case state.StatusNotTransferred:
			result.FilesNotTransferred++
		}
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
			"duration_ms":    durationMS,
			"files_total":    result.FilesTotal,
			"files_verified": result.FilesVerified,
		},
	}); err != nil {
		// Orphan-completion: per-file events landed but phase_completed
		// did not. Truthful: downstream will not run.
		auditCtx, cancel := runT1AuditCtx(ctx)
		_ = in.EventStore.Checkpoint(auditCtx)
		cancel()
		return nil, fmt.Errorf("runner T2: append phase_completed: %w", err)
	}

	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseHashCompare,
		Status:    "ok",
		Timestamp: finishedAt,
	})

	// Phase-boundary fsync (invariant #17: bound crash-loss window to one
	// phase). The manifest gzip stream is NOT finalized here; T4 owns that.
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T2: checkpoint events: %w", err)
	}

	return result, nil
}

// t3ClassifyFile runs the per-file classification routine: mutation gate,
// open source, hash source, open dest, hash dest, compare. Returns the
// FileStatus + source digest + dest digest + the underlying error (for
// inclusion in audit Details on the non-happy paths). Source-side digest
// is "" when status is source_mutated, source_unreadable, or when classifyErr
// is non-nil on the source side.
//
// Per the function contract: per-file errors are NOT fatal. The function
// returns (status, digests, nil) for soft failures (open / hash errors)
// and the caller continues the loop. classifyErr is set only as forensic
// data for the audit event, not as a control-flow signal.
func t3ClassifyFile(ctx context.Context, c selection.Candidate,
	sigs map[string]types.Signature, destRoot string) (state.FileStatus, string, string, error) {

	// 1. Re-stat source (mutation gate, invariant #8). os.Lstat (not Stat)
	// matches the BACKLOG memory: lstat is right for both symlink detection
	// AND mutation re-stat. A symlink that changed target between T0+ and T2
	// counts as a mutation; following the link would silently mask that.
	srcStat, err := os.Lstat(c.AbsolutePath)
	if err != nil {
		// Source is gone or unreadable at stat time. The mutation gate
		// requires a known-good baseline AND a current stat; without the
		// current stat we cannot prove non-mutation. Classify as
		// source_unreadable (we treat the failure as the source side
		// being unavailable rather than as source_mutated).
		return state.StatusSourceUnreadable, "", "", fmt.Errorf("lstat source: %w", err)
	}

	baseline, ok := sigs[c.RelativePath]
	if !ok {
		// Defensive: the runner orchestrator should always pass a Signature
		// for every Candidate. If the map is missing the entry, treat as
		// source_mutated (the gate failed-closed: we have no baseline to
		// compare against, so we cannot prove non-mutation).
		return state.StatusSourceMutated, "", "", fmt.Errorf("missing signature for %q", c.RelativePath)
	}

	if srcStat.Size() != baseline.Size || srcStat.ModTime().UnixNano() != baseline.MtimeNS {
		// (size, mtime_ns) drifted since T0+. Per invariant #8: skip both
		// hashes, mark source_mutated, do NOT touch the dest (defense:
		// a mutated source should not influence dest classification).
		return state.StatusSourceMutated, "", "", nil
	}

	// 2 + 3. Open source + StreamSHA256.
	srcFile, err := os.Open(c.AbsolutePath)
	if err != nil {
		return state.StatusSourceUnreadable, "", "", fmt.Errorf("open source: %w", err)
	}
	srcDigest, _, err := hash.StreamSHA256(ctx, srcFile)
	_ = srcFile.Close()
	if err != nil {
		// ctx cancellation surfaces here as a wrapped context error;
		// the caller's between-files ctx.Err() check on the NEXT iteration
		// will trigger the phase_aborted path. For THIS file we record
		// source_unreadable as the classification (truthful: we could
		// not read it to verify).
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return state.StatusSourceUnreadable, "", "", err
		}
		return state.StatusSourceUnreadable, "", "", fmt.Errorf("hash source: %w", err)
	}

	// 4 + 5. Stat + open + hash dest.
	destPath := filepath.Join(destRoot, filepath.FromSlash(c.RelativePath))
	if _, statErr := os.Stat(destPath); statErr != nil {
		if errors.Is(statErr, os.ErrNotExist) {
			// Dest file does not exist: rsync silently skipped this file
			// (e.g., permission denied, dest filesystem rejected name).
			// Classification: not_transferred (manifest still records the
			// source digest because we successfully hashed the source).
			return state.StatusNotTransferred, srcDigest, "", nil
		}
		return state.StatusDestUnreadable, srcDigest, "", fmt.Errorf("stat dest: %w", statErr)
	}
	dstFile, err := os.Open(destPath)
	if err != nil {
		return state.StatusDestUnreadable, srcDigest, "", fmt.Errorf("open dest: %w", err)
	}
	dstDigest, _, err := hash.StreamSHA256(ctx, dstFile)
	_ = dstFile.Close()
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return state.StatusDestUnreadable, srcDigest, "", err
		}
		return state.StatusDestUnreadable, srcDigest, "", fmt.Errorf("hash dest: %w", err)
	}

	// 6. Compare digests.
	if srcDigest == dstDigest {
		return state.StatusVerified, srcDigest, dstDigest, nil
	}
	return state.StatusHashMismatch, srcDigest, dstDigest, nil
}

// t3EmitFileAudit emits the per-file audit event matching the classification.
// Per the canonical Event Kinds table:
//   - hash_mismatch -> Details{path, sha256_source_prefix, sha256_dest_prefix}
//   - source_mutated -> Details{path}
//   - everything else -> file_completed with Details{path, status, sha256_source}
//     (sha256_source is the 16-char prefix; empty when never computed).
//
// Returns any EventStore.Append error so the caller can abort. The Path field
// on state.Event carries the path; Details mirror the spec table line 361-363.
func t3EmitFileAudit(ctx context.Context, es state.EventStore, phaseWire string,
	relPath string, status state.FileStatus, srcDigest, dstDigest string,
	classifyErr error) error {

	now := time.Now().UTC()
	switch status {
	case state.StatusHashMismatch:
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "hash_mismatch",
			Path:      relPath,
			Details: map[string]any{
				"path":                 relPath,
				"sha256_source_prefix": truncDigest(srcDigest),
				"sha256_dest_prefix":   truncDigest(dstDigest),
			},
		})
	case state.StatusSourceMutated:
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "source_mutated",
			Path:      relPath,
			Details: map[string]any{
				"path": relPath,
			},
		})
	default:
		// Verified / source_unreadable / dest_unreadable / not_transferred.
		// All flow through file_completed with the status string in Details
		// so a single audit-parsing path covers the non-mismatch branches.
		// (The Event Kinds table line 361 specifies "Per-file verified" as
		// the trigger; we extend it to cover failure classifications under
		// the same kind so consumers don't need N parallel kinds for each
		// failure mode.)
		details := map[string]any{
			"path":          relPath,
			"status":        string(status),
			"sha256_source": truncDigest(srcDigest),
		}
		if classifyErr != nil {
			details["error"] = classifyErr.Error()
		}
		return es.Append(ctx, state.Event{
			V:         1,
			Timestamp: now,
			Phase:     phaseWire,
			Kind:      "file_completed",
			Path:      relPath,
			Details:   details,
		})
	}
}

// truncDigest returns the first sha256PrefixChars of a hex digest, or the
// whole string when shorter. The empty-string input case is intentional:
// source_mutated entries have no source digest, and the resulting empty
// prefix correctly signals that absence to log readers.
func truncDigest(digest string) string {
	if len(digest) <= sha256PrefixChars {
		return digest
	}
	return digest[:sha256PrefixChars]
}

// runT3Abort centralizes the phase_aborted path for cancellation /
// fatal-error branches in RunT3HashCompare. Shape mirrors runT1Abort /
// runT2EmitAbort: best-effort Append under the shared 5-second audit
// budget; emit UIEvtPhaseCompleted Status=aborted; Checkpoint best-effort.
// Reuses runT1AuditCtx for the audit-budget logic.
func runT3Abort(ctx context.Context, es state.EventStore, ui types.Renderer,
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
		Phase:     types.PhaseHashCompare,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	_ = es.Checkpoint(auditCtx)
}
