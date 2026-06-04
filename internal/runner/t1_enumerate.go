package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// File-number vs phase-wire-string trap: the file ordinal is "t1" to match
// sibling tN sequencing but the PHASE THIS FILE EXECUTES is T0+, NOT T1.
// Phase T1 is rsync transfer (Task 24, t2_transfer.go). All wire-string
// emissions here use types.PhaseEnumerate (underlying "T0+"). See
// runner/doc.go for the canonical map.

// ctxCheckInterval is how often the per-file emission loop checks
// ctx.Err(). 256 is a starting point chosen so that a 1M-file tree polls
// ~4K times during enumeration (each emit is bounded by an Append fsync
// budget); tune if observed emission rate makes this either too coarse
// or too chatty. Made a named const (not a magic number) for test visibility.
const t1EnumerateCtxCheckInterval = 256

// T1Input is the minimal config the T0+ enumerate phase needs from the
// top-level runner.Run wrapper. All fields are required unless noted.
// (Filename is t1_enumerate.go for sequential-numbering consistency with
// sibling tN files; the WIRE PHASE is T0+, not T1. See runner/doc.go.)
type T1Input struct {
	// Profile carries the include/exclude glob set. SourceRoot is taken
	// from the dedicated field below, not from Profile.Source, so the
	// caller can resolve symlinks / absolutize once and we don't re-do it.
	Profile profiles.Profile

	// SourceRoot is the resolved absolute path of the source root. Already
	// validated by the caller (Task 22's T0 preflight does not touch the
	// source, but the runner.Run wrapper, Task 29, is responsible for
	// resolving SourceRoot before calling this phase).
	SourceRoot string

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil triggers a typed error at entry).
	EventStore state.EventStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid
	// per PS3; events still persist to events.ndjson.
	UIRenderer types.Renderer
}

// T1Result carries the enumerated set forward to Task 24 (T1 transfer).
//
// Candidates is the NFC-canonicalized, filter-applied list from
// selection.Walk in the order Walk returned it (Walk sorts by
// RelativePath ascending; we do not re-sort).
//
// Signatures maps Candidate.RelativePath -> {Size, MtimeNS} for the T3
// mutation gate (invariant #8 source mutation gate). Keyed by
// RelativePath (not AbsolutePath) because the manifest path used by
// Tasks 25-26 is the RelativePath; the AbsolutePath is recoverable from
// the corresponding Candidate when the gate needs to re-stat. Keeping
// the map keyed by the same identifier the manifest uses avoids a
// second lookup table at T3.
//
// Skipped surfaces excluded paths for the run summary; CollidingPaths
// surfaces invariant #32 NFC collisions for the same purpose (operator
// visibility; the run still proceeds with the non-colliding candidates).
type T1Result struct {
	Candidates     []selection.Candidate
	Signatures     map[string]types.Signature
	Skipped        []string
	CollidingPaths []string
}

// RunT1Enumerate executes phase T0+ (source enumeration). Contract:
//
//  1. ctx.Err() check at entry. EventStore non-nil check.
//  2. phase_started state.Event appended (Phase="T0+").
//  3. UIEvtPhaseStarted UIEvent emitted (if renderer non-nil; PS3 swallows
//     renderer errors).
//  4. selection.Walk invoked with Profile.Includes + Profile.Excludes
//     + FollowSymlinks=false.
//  5. On Walk FAILURE: phase_aborted state.Event appended with
//     {duration_ms, error}; UIEvtPhaseCompleted with Status="aborted";
//     Checkpoint best-effort (shared 5s budget if ctx cancelled);
//     wrapped error returned, T1Result nil.
//  6. On Walk SUCCESS: build Signatures map from Candidates, emit one
//     file_enumerated event per Candidate (audit-only; no UIEvent. The
//     v0.1 UIEvent kinds do not include enumeration progress), then
//     phase_completed + UIEvtPhaseCompleted (Status="ok") + Checkpoint
//     + return T1Result, nil.
//
// File-enumerated event Details carry path / size / mtime_ns per the
// canonical Event Kinds table in plan API Contracts. The event's Path
// field is the same Candidate.RelativePath used as the Signatures key
// and as the manifest identifier downstream.
//
// Cancellation: ctx.Err() is checked every t1EnumerateCtxCheckInterval
// emissions so a Ctrl-C against a huge tree exits promptly. On mid-loop
// cancel the phase emits phase_aborted (NOT phase_completed) with the
// shared-budget audit writes used by the Walk-failure path.
//
// Audit-write failure policy: any EventStore.Append failure (phase_started,
// file_enumerated, phase_completed, phase_aborted) aborts the run with a
// wrapped error. Per the Task 22 contract: audit failures are fatal because
// we cannot guarantee the run is observable. file_enumerated failures stop
// emission mid-stream; events.ndjson holds whatever landed before the
// failure (truthful partial-progress data), the run does NOT emit
// phase_completed (we cannot lie that the phase finished), and no
// downstream phase will run.
//
// Lifecycle: T1Result owns no resources that need releasing. The caller
// owns the EventStore (close on run end) and the PreflightContext from T0.
func RunT1Enumerate(ctx context.Context, in T1Input) (*T1Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T0+: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T0+: EventStore is nil")
	}

	phaseWire := string(types.PhaseEnumerate)
	startedAt := time.Now().UTC()

	// 2. phase_started persisted event. Audit failure here is fatal: the
	// run is no longer observable, so subsequent file_enumerated events
	// would be unsafe to write (Task 22 contract).
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		return nil, fmt.Errorf("runner T0+: append phase_started: %w", err)
	}

	// 3. Renderer UIEvent. Errors are non-fatal per PS3 (see emitUI in
	// t0_preflight.go).
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhaseEnumerate,
		Timestamp: startedAt,
	})

	// 4. Enumerate. Walk does the heavy lifting (NFC canonicalization per
	// invariant #32, collision detection, filter application).
	walkResult, walkErr := selection.Walk(ctx, selection.Options{
		SourceRoot:     in.SourceRoot,
		Includes:       in.Profile.Includes,
		Excludes:       in.Profile.Excludes,
		FollowSymlinks: false,
	})

	if walkErr != nil {
		// 5a. Walk failed. Abort the phase with forensic data.
		return nil, runT1Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt,
			fmt.Errorf("runner T0+: enumerate: %w", walkErr))
	}

	// 5b. Walk succeeded. Build the Signatures map and emit per-file
	// audit events.
	sigs := make(map[string]types.Signature, len(walkResult.Candidates))
	for _, c := range walkResult.Candidates {
		sigs[c.RelativePath] = types.Signature{
			Size:    c.Size,
			MtimeNS: c.MtimeNS,
		}
	}

	for i, c := range walkResult.Candidates {
		// Cadenced cancellation check so a Ctrl-C during a 1M-file
		// enumeration exits promptly. The walk already returned, but
		// the emit loop can run for seconds on huge trees with each
		// Append doing buffered I/O.
		if i > 0 && i%t1EnumerateCtxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, runT1Abort(ctx, in.EventStore, in.UIRenderer, phaseWire, startedAt,
					fmt.Errorf("runner T0+: %w", err))
			}
		}

		if err := in.EventStore.Append(ctx, state.Event{
			V:         1,
			Timestamp: time.Now().UTC(),
			Phase:     phaseWire,
			Kind:      "file_enumerated",
			Path:      c.RelativePath,
			Details: map[string]any{
				"size":     c.Size,
				"mtime_ns": c.MtimeNS,
			},
		}); err != nil {
			// Mid-stream audit failure. We do NOT emit
			// phase_completed (the phase did not complete) and we
			// do NOT emit phase_aborted with the audit error in
			// the same store (the audit store is the one that
			// failed; another Append would likely fail too, and
			// we'd return a misleading error). Best-effort
			// Checkpoint then return.
			//
			// Renderer gets a phase_completed/aborted so a TUI is
			// not stuck on the "started" frame.
			finishedAt := time.Now().UTC()
			wrapped := fmt.Errorf("runner T0+: append file_enumerated %q: %w", c.RelativePath, err)
			emitUI(ctx, in.UIRenderer, types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseEnumerate,
				Status:    "aborted",
				Err:       wrapped,
				Timestamp: finishedAt,
			})
			auditCtx, cancel := runT1AuditCtx(ctx)
			_ = in.EventStore.Checkpoint(auditCtx)
			cancel()
			return nil, wrapped
		}
	}

	// phase_completed audit event.
	finishedAt := time.Now().UTC()
	durationMS := finishedAt.Sub(startedAt).Milliseconds()
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: finishedAt,
		Phase:     phaseWire,
		Kind:      "phase_completed",
		Details: map[string]any{
			"duration_ms": durationMS,
		},
	}); err != nil {
		// Orphan-completion case: phase_started + N file_enumerated
		// landed, but phase_completed did not. events.ndjson holds
		// truthful partial-progress data; the downstream phase will
		// not run.
		auditCtx, cancel := runT1AuditCtx(ctx)
		_ = in.EventStore.Checkpoint(auditCtx)
		cancel()
		return nil, fmt.Errorf("runner T0+: append phase_completed: %w", err)
	}

	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhaseEnumerate,
		Status:    "ok",
		Timestamp: finishedAt,
	})

	// Phase-boundary fsync. invariant #17 / EventStore docstring: bound
	// the crash-loss window to one phase.
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		return nil, fmt.Errorf("runner T0+: checkpoint events: %w", err)
	}

	return &T1Result{
		Candidates:     walkResult.Candidates,
		Signatures:     sigs,
		Skipped:        walkResult.Skipped,
		CollidingPaths: walkResult.CollidingPaths,
	}, nil
}

// runT1Abort centralizes the phase_aborted path so the Walk-failure and
// the mid-emit-cancellation branches share one implementation. Returns
// the wrapped error to surface to the caller.
//
// Shared-budget note (matches t0_preflight.go auditCtx pattern): the 5s
// budget covers both the Append and the Checkpoint on the abort path. On
// a wedged disk, expect at most partial audit data; preserving partial
// forensic data beats hanging the runner indefinitely.
func runT1Abort(ctx context.Context, es state.EventStore, ui types.Renderer,
	phaseWire string, startedAt time.Time, wrappedErr error) error {
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
		Phase:     types.PhaseEnumerate,
		Status:    "aborted",
		Err:       wrappedErr,
		Timestamp: finishedAt,
	})

	_ = es.Checkpoint(auditCtx)
	return wrappedErr
}

// runT1AuditCtx returns (ctx, no-op cancel) if ctx is still live, or a
// fresh 5-second budget context (with its cancel) if ctx was already
// cancelled. Callers must defer the returned cancel to avoid a timer
// leak. Mirrors the auditCtx pattern in t0_preflight.go: when the user
// pressed Ctrl-C, audit writes still need to land on disk so the abort
// reason is recoverable post-mortem; bounding the write by 5s prevents
// a wedged disk from hanging the runner.
func runT1AuditCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	if ctx.Err() == nil {
		return ctx, func() {}
	}
	return context.WithTimeout(context.Background(), 5*time.Second)
}
