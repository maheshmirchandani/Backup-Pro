package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/rsync"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// File-number vs phase-wire-string trap: the file ordinal is "t2" to match
// sibling tN sequencing but the PHASE THIS FILE EXECUTES is T1 (rsync
// transfer), NOT T2. Phase T2 is hash+compare (Task 25, t3_hash_compare.go).
// All wire-string emissions here use types.PhaseTransfer (underlying "T1").
// See runner/doc.go for the canonical map.

// T2Input is the minimal config the T1 transfer phase needs from the
// top-level runner.Run wrapper. All fields are required unless noted.
// (Filename is t2_transfer.go for sequential-numbering consistency with
// sibling tN files; the WIRE PHASE is T1, not T2. See runner/doc.go.)
type T2Input struct {
	// SourceRoot is the resolved absolute path of the source directory
	// (already validated by T0 / resolved by the caller). Passed to
	// rsync as the SourceRoot of the transfer.
	SourceRoot string

	// DestRoot is the per-run destination root. For v0.1 this is the
	// namespaced subdirectory under the USB mountpoint
	// (<USB>/<hostname>-<username>) per invariant #14; the caller wires
	// the namespace prefix before invoking this phase.
	DestRoot string

	// RsyncPath is the absolute path to the extracted rsync binary,
	// typically PreflightContext.RsyncPath. Required.
	RsyncPath string

	// Candidates is the enumerated file set from T0+ (T1Result.Candidates).
	// Their RelativePaths are passed to rsync via --files-from/--from0 so
	// no shell expansion ever touches a filename (security amendment).
	Candidates []selection.Candidate

	// Mode is copy or move. Affects whether --delete is permitted on the
	// rsync invocation; in v0.1 we ALWAYS pass Delete=false at T1 because
	// mirror-delete (invariant #6) is implemented at T3 (Task 26) after
	// the atomic gate, not inside the T1 rsync call.
	Mode types.Mode

	// DotDir is the on-USB <USB>/.flashbackup directory. The per-run
	// rsync.log is written under <DotDir>/runs/<RunID>/rsync.log; the
	// directory is MkdirAll'd if missing.
	DotDir string

	// RunID is the canonical run identifier; only used for the rsync.log
	// path under <DotDir>/runs/<RunID>/.
	RunID string

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil triggers a typed error at entry).
	EventStore state.EventStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid
	// per PS3; events still persist to events.ndjson.
	UIRenderer types.Renderer
}

// T2Result carries the rsync subprocess outcome forward to Task 25 (phase
// T2, hash+compare), which classifies per-file success / failure. Per-file
// granularity in T1 is the UI's domain (UIEvtProgress), not the audit's:
// the canonical Event Kinds table emits transfer_started / transfer_completed
// / transfer_failed at PHASE granularity for T1.
type T2Result struct {
	// ExitCode is the rsync subprocess exit status (0 = success). When
	// Run returned an error that is not an *exec.ExitError, ExitCode is
	// -1 (e.g., ctx cancellation killed the subprocess before it produced
	// an exit code).
	ExitCode int

	// FilesAttempted is the count of ProgressFileCompleted events the
	// parser observed. Best-effort: rsync emits the xfr#N tail only for
	// files that produced a final 100% line; skipped files (already
	// up-to-date) and files killed mid-transfer are excluded.
	FilesAttempted int

	// BytesTransferred is the last-observed cumulative byte counter from
	// ProgressTransferring / ProgressFileCompleted events. Best-effort
	// (same caveat as FilesAttempted); useful for support-bundle summary.
	BytesTransferred int64

	// RsyncLogPath is the absolute path to the per-run rsync log file.
	// Useful for inclusion in the friend-bug-report bundle (invariant #41).
	RsyncLogPath string
}

// RunT2Transfer executes phase T1 (rsync transfer). Contract:
//
//  1. ctx.Err() check at entry; EventStore non-nil check.
//  2. Open <DotDir>/runs/<RunID>/rsync.log (mode 0644; no secrets).
//     MkdirAll the run dir if missing. The log file's lifecycle is owned
//     by this function; closed on return regardless of path.
//  3. Append phase_started (Phase="T1") via EventStore.
//  4. Emit UIEvtPhaseStarted (Phase=types.PhaseTransfer) via emitUI.
//  5. Append transfer_started with Details {command_line, file_count}.
//     command_line is the rsync argv reconstructed from rsync.Options
//     (space-joined for human-reading in support bundles; NOT shell-
//     escaped because the argv is never re-executed via a shell).
//  6. Wire a rsync.Parser whose PassThrough is the rsync.log file and
//     whose OnEvent translates ProgressEvent into:
//     - ProgressTransferring  -> UIEvtProgress UIEvent with
//     BytesDone, BytesPerSec, ETASeconds, CurrentFile populated.
//     - ProgressFileCompleted -> FilesAttempted++ and
//     BytesTransferred = max(prev, ev.BytesTransferred).
//     - ProgressSummary       -> captured to the log via PassThrough,
//     no UIEvent (UI surfaces totals via UIEvtSummary at T4).
//     - ProgressFileStarted   -> dropped (no canonical Event Kind for
//     per-file enumeration at T1; per the plan's Event Kinds table,
//     per-file audit lives at T2 via file_completed).
//  7. Invoke rsync.Wrapper.Run(ctx, opts) with:
//     - Archive=true, Partial=true, Xattrs=true, Delete=false
//     (design spec section 3 row T1; invariant #6).
//     - Stdout=parser, Stderr=rsync.log file directly.
//     Files = candidate RelativePaths.
//  8. Drain parser.Flush() after Run returns.
//     9a. On Run error (non-zero exit or ctx cancellation): build T2Result
//     with ExitCode = rsync.ResolveExitCode(err); transfer_failed
//     Append (Details: exit_code, error); phase_aborted Append via
//     runT2Abort (Details: duration_ms, error); UIEvtPhaseCompleted
//     Status="aborted"; Checkpoint best-effort; return (T2Result,
//     wrapped err).
//     9b. On Run success: transfer_completed Append (Details: exit_code=0,
//     duration_ms); phase_completed Append (Details: duration_ms);
//     UIEvtPhaseCompleted Status="ok"; Checkpoint; return (T2Result, nil).
//
// Audit-write failure policy: EventStore.Append failures for
// phase_started, transfer_started, transfer_completed, phase_completed
// abort the run with a wrapped error (Task 22 contract: audit failures
// are fatal because the run is no longer observable). phase_aborted and
// transfer_failed are best-effort writes via the shared 5-second
// background context (matches runT1Abort); per the plan footnote added
// in af39928, we do NOT re-Append phase_aborted to a just-failed store.
//
// Skipping the empty-Candidates case: when len(in.Candidates) == 0 we
// short-circuit before invoking rsync. The Wrapper does not refuse an
// empty file list (rsync would recurse the source under -a, which is
// not what we want here); skipping avoids that subtle wrong behavior
// and avoids spawning a useless subprocess. The audit trail still shows
// phase_started + transfer_started (file_count=0) + transfer_completed
// (exit_code=0) + phase_completed so downstream phases see a normal
// success line.
//
// Cancellation: ctx cancellation propagates via exec.CommandContext,
// which sends SIGKILL by default. A softer SIGTERM-with-5s-grace via
// cmd.Cancel + cmd.WaitDelay is deferred to Task 29 (the top-level
// runner state machine) per the BACKLOG memory; v0.1 T1 accepts the
// hard-kill behavior to keep this phase function from owning shutdown
// politeness alone.
//
// Lifecycle: the rsync.log file is opened here and closed via defer on
// every return path; the caller owns the EventStore (open / close
// outside this function).
func RunT2Transfer(ctx context.Context, in T2Input) (*T2Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T1: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T1: EventStore is nil")
	}

	phaseWire := string(types.PhaseTransfer)
	startedAt := time.Now().UTC()

	// 2. Open the per-run rsync.log. The path is part of the audit-trail
	// contract (referenced by friend-bug-report); we open it BEFORE
	// phase_started so a path-construction failure surfaces without an
	// orphan phase_started line in events.ndjson.
	runDir := filepath.Join(in.DotDir, "runs", in.RunID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("runner T1: mkdir run dir: %w", err)
	}
	rsyncLogPath := filepath.Join(runDir, "rsync.log")
	// 0644: rsync.log contains command lines + raw stdout. No secrets
	// (the filename list is on stdin, not in argv); world-readable so
	// support tooling running as the user can collect it.
	rsyncLogFile, err := os.OpenFile(rsyncLogPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("runner T1: open rsync.log: %w", err)
	}
	defer func() { _ = rsyncLogFile.Close() }()

	// 3. phase_started persisted event. Audit failure here is fatal: if
	// the very first event of the phase cannot be written, downstream
	// audit-correlation breaks.
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		return nil, fmt.Errorf("runner T1: append phase_started: %w", err)
	}

	// 4. Renderer UIEvent. Errors are non-fatal per PS3.
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhaseTransfer,
		Timestamp: startedAt,
	})

	// 5. Build the rsync.Options up front so transfer_started can record
	// the exact command_line that will be invoked.
	opts := rsync.Options{
		ExecPath:   in.RsyncPath,
		SourceRoot: in.SourceRoot,
		DestRoot:   in.DestRoot,
		Files:      candidateRelPaths(in.Candidates),
		Archive:    true, // design spec section 3 row T1
		Partial:    true,
		Xattrs:     true,
		Delete:     false, // invariant #6: mirror-delete is T3, not T1
	}
	commandLine := rsyncCommandLine(opts)

	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: time.Now().UTC(),
		Phase:     phaseWire,
		Kind:      "transfer_started",
		Details: map[string]any{
			"command_line": commandLine,
			"file_count":   len(in.Candidates),
		},
	}); err != nil {
		// Audit-write failure on transfer_started. Same fatal policy as
		// phase_started: no further audit writes attempted.
		//
		// UI consistency: UIEvtPhaseStarted was already emitted above
		// (line 208), so a renderer would otherwise be stranded on the
		// "T1 started" frame. Emit UIEvtPhaseCompleted(aborted) so the
		// TUI can render the failure even though the audit trail is
		// truncated. Matches the t1_enumerate.go mid-stream pattern.
		wrapped := fmt.Errorf("runner T1: append transfer_started: %w", err)
		emitUI(ctx, in.UIRenderer, types.UIEvent{
			Kind:      types.UIEvtPhaseCompleted,
			Phase:     types.PhaseTransfer,
			Status:    "aborted",
			Err:       wrapped,
			Timestamp: time.Now().UTC(),
		})
		return nil, wrapped
	}

	// Empty-Candidates short-circuit: never invoke rsync with no files
	// (rsync would recurse the source under -a, which is not the T1
	// contract). Emit the success-trio of events and return zero counts.
	if len(in.Candidates) == 0 {
		return t2CloseHappy(ctx, in, phaseWire, startedAt, 0,
			0, 0, rsyncLogPath)
	}

	// Fault injection: PointT1PreRsync. Test-only hook for fault-injection
	// e2e tests (Tasks 48-51b); no-op in release builds via the !faultinject
	// stub. Wire phase string is "T1-pre" per the canonical Point table in
	// the plan amendment (docs/planning/...:399); --inject:phase=T1-pre
	// matches only this pre-rsync site, not the in-rsync progress or the
	// post-rsync site. On non-nil return: treat as fatal phase error.
	var bytesTotal int64
	for _, c := range in.Candidates {
		bytesTotal += c.Size
	}
	if hookErr := Hook(ctx, PointT1PreRsync, HookArgs{
		Phase:      string(PointT1PreRsync),
		FilesTotal: len(in.Candidates),
		BytesTotal: bytesTotal,
		DestRoot:   in.DestRoot,
		SourceRoot: in.SourceRoot,
	}); hookErr != nil {
		wrapped := fmt.Errorf("runner T1: pre-rsync fault: %w", hookErr)
		runT2EmitAbort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return nil, wrapped
	}

	// 6. Wire the progress parser. PassThrough writes raw rsync stdout
	// bytes to rsync.log; OnEvent classifies into UIEvents + counters.
	//
	// Fault injection: PointT1Progress fires from inside the per-progress-
	// line callback so AfterPct / AfterCount one-shots can trigger mid-
	// transfer. When Hook returns non-nil the callback cancels the rsync
	// child context (rsyncCtx) which kills the subprocess; rsync.Run then
	// returns the cancellation error, which we wrap as the fatal-phase
	// error after Run returns. We capture the hook error in hookFireErr.
	rsyncCtx, rsyncCancel := context.WithCancel(ctx)
	defer rsyncCancel()
	var (
		filesAttempted   int
		bytesTransferred int64
		hookFireErr      error
	)
	parser := &rsync.Parser{
		PassThrough: rsyncLogFile,
		OnEvent: func(ev rsync.ProgressEvent) {
			switch ev.Kind {
			case rsync.ProgressTransferring:
				emitUI(ctx, in.UIRenderer, types.UIEvent{
					Kind:  types.UIEvtProgress,
					Phase: types.PhaseTransfer,
					Path:  ev.Path,
					Progress: &types.ProgressInfo{
						BytesDone:   ev.BytesTransferred,
						BytesPerSec: ev.SpeedBytesPerSec,
						ETASeconds:  ev.ETASeconds,
						CurrentFile: ev.Path,
					},
					Timestamp: time.Now().UTC(),
				})
				if ev.BytesTransferred > bytesTransferred {
					bytesTransferred = ev.BytesTransferred
				}
				if hookFireErr == nil {
					if hErr := Hook(rsyncCtx, PointT1Progress, HookArgs{
						Phase:       string(PointT1Progress),
						CurrentFile: ev.Path,
						FilesDone:   filesAttempted,
						FilesTotal:  len(in.Candidates),
						BytesDone:   bytesTransferred,
						BytesTotal:  bytesTotal,
						DestRoot:    in.DestRoot,
						SourceRoot:  in.SourceRoot,
					}); hErr != nil {
						hookFireErr = hErr
						rsyncCancel()
					}
				}
			case rsync.ProgressFileCompleted:
				filesAttempted++
				if ev.BytesTransferred > bytesTransferred {
					bytesTransferred = ev.BytesTransferred
				}
			default:
				// ProgressFileStarted / ProgressSummary / ProgressUnknown:
				// passed through via PassThrough; no UIEvent / no counter.
			}
		},
	}
	opts.Stdout = parser
	opts.Stderr = rsyncLogFile // rsync stderr is human diagnostics; goes straight to the log

	// 7. Invoke rsync. Use rsyncCtx so the Progress fault-injection hook
	// can cancel the subprocess mid-flight; the parent ctx still governs
	// shutdown for non-fault cancellations.
	w := &rsync.Wrapper{}
	runErr := w.Run(rsyncCtx, opts)

	// If the Progress hook fired, prefer its error over rsync's wrapped
	// cancellation (which is just the symptom of the cancel call).
	if hookFireErr != nil {
		runErr = fmt.Errorf("runner T1: progress fault: %w", hookFireErr)
	}

	// 8. Drain any tail line the parser buffered without a terminator.
	parser.Flush()

	// 9. Branch on Run outcome.
	if runErr != nil {
		exitCode := rsync.ResolveExitCode(runErr)
		wrapped := fmt.Errorf("runner T1: rsync: %w", runErr)

		// transfer_failed audit event. Best-effort: use the shared 5-second
		// background context so a ctx cancellation does not lose the
		// forensic record of WHY the phase aborted.
		auditCtx, cancel := runT1AuditCtx(ctx)
		defer cancel()

		_ = in.EventStore.Append(auditCtx, state.Event{
			V:         1,
			Timestamp: time.Now().UTC(),
			Phase:     phaseWire,
			Kind:      "transfer_failed",
			Details: map[string]any{
				"exit_code": exitCode,
				"error":     runErr.Error(),
			},
		})

		// phase_aborted via runT1Abort. Reuses the t1_enumerate.go helper
		// because the contract is identical: append phase_aborted under
		// the shared audit budget; emit UIEvtPhaseCompleted Status=aborted;
		// best-effort Checkpoint. Cross-phase reuse is intentional;
		// nothing about runT1Abort is T0+-specific except the renderer
		// Phase field, which it accepts via the phaseWire string and the
		// types.PhaseEnumerate constant -- but we are NOT passing the
		// enumerate Phase here; we wire the transfer Phase via a thin
		// local wrapper below to keep the renderer's Phase honest.
		runT2EmitAbort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)

		return &T2Result{
			ExitCode:         exitCode,
			FilesAttempted:   filesAttempted,
			BytesTransferred: bytesTransferred,
			RsyncLogPath:     rsyncLogPath,
		}, wrapped
	}

	// Fault injection: PointT1Post. Fires AFTER rsync returns 0 so a fault
	// can simulate a post-transfer disaster (e.g., a stale lock, an unmount
	// race). Wire phase string is "T1-post" per the canonical Point table
	// (plan amendment). On non-nil return: treat as fatal phase error.
	if hookErr := Hook(ctx, PointT1Post, HookArgs{
		Phase:      string(PointT1Post),
		FilesDone:  filesAttempted,
		FilesTotal: len(in.Candidates),
		BytesDone:  bytesTransferred,
		BytesTotal: bytesTotal,
		DestRoot:   in.DestRoot,
		SourceRoot: in.SourceRoot,
	}); hookErr != nil {
		wrapped := fmt.Errorf("runner T1: post-rsync fault: %w", hookErr)
		runT2EmitAbort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt, wrapped)
		return &T2Result{
			ExitCode:         0,
			FilesAttempted:   filesAttempted,
			BytesTransferred: bytesTransferred,
			RsyncLogPath:     rsyncLogPath,
		}, wrapped
	}

	// 9b. Run succeeded.
	return t2CloseHappy(ctx, in, phaseWire, startedAt,
		filesAttempted, bytesTransferred, 0, rsyncLogPath)
}

// t2CloseHappy emits the transfer_completed + phase_completed audit
// pair plus the UIEvtPhaseCompleted (Status=ok), then Checkpoints. Used
// by both the real-rsync success path and the empty-Candidates
// short-circuit. Centralizing keeps the two paths in lockstep so a
// future bugfix to one applies to the other.
//
// Returns T2Result with the carried-forward counters and the rsync.log
// path; on any audit failure returns (nil, wrapped err).
func t2CloseHappy(ctx context.Context, in T2Input, phaseWire string,
	startedAt time.Time, filesAttempted int, bytesTransferred int64,
	exitCode int, rsyncLogPath string) (*T2Result, error) {

	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()

	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "transfer_completed",
		Details: map[string]any{
			"exit_code":   exitCode,
			"duration_ms": durationMS,
		},
	}); err != nil {
		return nil, fmt.Errorf("runner T1: append transfer_completed: %w", err)
	}

	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms": durationMS,
		},
	}); err != nil {
		// Orphan-completion: transfer_completed landed but
		// phase_completed did not. Truthful: downstream will not run.
		return nil, fmt.Errorf("runner T1: append phase_completed: %w", err)
	}

	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseTransfer,
		Status:    "ok",
		Timestamp: finishedAt,
	})

	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T1: checkpoint events: %w", err)
	}

	return &T2Result{
		ExitCode:         exitCode,
		FilesAttempted:   filesAttempted,
		BytesTransferred: bytesTransferred,
		RsyncLogPath:     rsyncLogPath,
	}, nil
}

// runT2EmitAbort is the T1 sibling of runT1Abort. Same shape, but the
// UIEvent Phase is types.PhaseTransfer rather than types.PhaseEnumerate.
// Best-effort writes under the shared 5-second audit budget (matches
// runT1AuditCtx semantics).
func runT2EmitAbort(ctx context.Context, es state.EventStore, ui types.Renderer,
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
		Phase:     types.PhaseTransfer,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	_ = es.Checkpoint(auditCtx)
}

// candidateRelPaths extracts the slash-form RelativePath of each
// Candidate, which is the form selection.Walk canonicalized to NFC and
// which the rsync.Wrapper passes via --files-from/--from0 (NUL-terminated
// stdin; no shell expansion).
func candidateRelPaths(cands []selection.Candidate) []string {
	out := make([]string, len(cands))
	for i, c := range cands {
		out[i] = c.RelativePath
	}
	return out
}

// rsyncCommandLine returns a human-readable space-joined argv for
// transfer_started.details.command_line. NOT shell-escaped because the
// resulting string is never re-executed via a shell; it exists for
// support-bundle readability. Calls rsync.BuildArgs directly so the
// audit's command_line is byte-equal to what the subprocess actually
// executes (single source of truth; no drift trap).
func rsyncCommandLine(opts rsync.Options) string {
	parts := make([]string, 0, 16)
	parts = append(parts, opts.ExecPath)
	parts = append(parts, rsync.BuildArgs(opts)...)
	return strings.Join(parts, " ")
}
