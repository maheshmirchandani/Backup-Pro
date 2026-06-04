package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/preflight"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// T0Input is the minimal config the T0 preflight phase needs from the
// top-level runner orchestrator. EventStore and RunLogStore lifecycles
// belong to the caller (Task 29): RunT0Preflight only Appends and
// Checkpoints, never Opens or Closes them. UIRenderer may be nil; per the
// PS3 contract a nil renderer means "no UI events emitted, state still
// persists to events.ndjson".
type T0Input struct {
	// RunID is the canonical run identifier per the spec section 5 format
	// "<UTC-RFC3339-no-colons>Z-<4-hex>". The top-level runner generates it
	// once before any phase starts; Task 22 only stamps it into the started
	// line of runs.ndjson.
	RunID string

	// FlashbackupVersion is the build's version string (e.g. "0.1.0-core"),
	// typically wired from a -X ldflag at build time. Persisted into
	// runs.ndjson started/finished pairs for forensic correlation.
	FlashbackupVersion string

	// DestRoot is the absolute USB mountpoint. Forwarded verbatim into
	// preflight.Options.
	DestRoot string

	// SourceRoot is the absolute source path being backed up. Recorded in
	// the runs.ndjson started line; T0 itself does not touch the source.
	SourceRoot string

	// Mode is copy or move. Recorded as the string ("copy"/"move") in
	// runs.ndjson per the StartedRun.Mode contract.
	Mode types.Mode

	// ProfileName is the saved profile slug, or "" for an ad-hoc run.
	// Optional in runs.ndjson (omitempty); always present in T0Input so the
	// caller does not need to know the persistence detail.
	ProfileName string

	// SkipCodesign is the dev/test escape hatch on preflight.Options.
	// Production code must never set this true; CI release builds will fail
	// loud if it leaks through.
	SkipCodesign bool

	// EventStore is the audit log sink opened by the top-level runner.
	// Required (nil panics at the first Append).
	EventStore state.EventStore

	// RunLogStore is the runs.ndjson sink opened by the top-level runner.
	// Required.
	RunLogStore state.RunLogStore

	// UIRenderer is the optional renderer from RunOptions. Nil is valid.
	UIRenderer types.Renderer
}

// T0Result wraps the populated PreflightContext for the next phase. Carried
// as a struct (rather than returning *PreflightContext directly) as a
// forward-compat hedge: subsequent phases will likely want to surface
// phase-level metadata (e.g., a per-phase ExecutionStatus) back to the
// orchestrator. Owning the wrapper now keeps the per-phase signature stable
// across Tasks 23-27.
//
// Lifetime: PreflightContext holds the on-disk lock. The top-level runner
// defers pc.Release(); Task 22 returns the pointer and never releases it
// itself on the happy path. On preflight FAILURE the inner preflight.Preflight
// already rolled back any partial state (it releases the lock if a later
// gate fails), so Task 22 has nothing to clean up.
type T0Result struct {
	PreflightContext *preflight.PreflightContext
}

// RunT0Preflight orchestrates phase T0. The contract:
//
//  1. phase_started state.Event appended.
//  2. UIEvtPhaseStarted UIEvent emitted (if renderer non-nil; errors swallowed).
//  3. preflight.Preflight invoked.
//  4. On preflight FAILURE: phase_aborted state.Event appended with
//     {duration_ms, error} details; UIEvtPhaseCompleted UIEvent emitted
//     with Status="aborted" and Err set; both stores checkpointed
//     (best-effort, errors are not propagated because they would shadow
//     the original preflight error); wrapped Preflight error returned. No
//     "started" line is written, because the run never started in any
//     user-visible sense (invariant #10: a started/finished pair documents
//     a real run; a T0 abort leaves no started line so history correctly
//     shows no run occurred).
//  5. On preflight SUCCESS: AppendStarted to runs.ndjson with the canonical
//     StartedRun fields; phase_completed state.Event appended with
//     {duration_ms}; UIEvtPhaseCompleted UIEvent emitted with Status="ok";
//     BOTH stores checkpointed (phase-boundary fsync per the EventStore /
//     RunLogStore durability contract, which bounds the crash-loss window
//     to the current phase); T0Result{PreflightContext: pc}, nil returned.
//
// Cancellation:
//   - Checked at entry; cancelled context returns immediately with ctx.Err()
//     wrapped, no store writes.
//   - Checked again between Preflight completion and AppendStarted; if
//     cancelled at that boundary, pc.Release() is called (we acquired the
//     lock that the top-level runner was going to defer Release on, and we
//     have not yet given the pointer back) before returning the error.
//
// Renderer error handling: per PS3 of the plan strategic decisions, a
// renderer error never aborts the run. emitUI swallows the error with no
// logging plumbed yet; a future commit may route renderer errors to a
// dedicated logger. The audit trail in events.ndjson is the durable record;
// the renderer is best-effort UI.
func RunT0Preflight(ctx context.Context, in T0Input) (*T0Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("runner T0: %w", err)
	}
	if in.EventStore == nil {
		return nil, fmt.Errorf("runner T0: EventStore is nil")
	}
	if in.RunLogStore == nil {
		return nil, fmt.Errorf("runner T0: RunLogStore is nil")
	}

	phaseWire := string(types.PhasePreflight)
	startedAt := time.Now().UTC()

	// 1. phase_started persisted event. We do not return on Append errors
	// here because the next step (Preflight) is still useful even if the
	// audit line failed to write; we'll surface the audit error AFTER the
	// run as a wrapped error. In practice a write failure on a freshly
	// opened file means the storage layer is broken, but loud surfacing is
	// owned by the top-level runner which sees the wrapped chain.
	if err := in.EventStore.Append(ctx, state.Event{
		V:         1,
		Timestamp: startedAt,
		Phase:     phaseWire,
		Kind:      "phase_started",
	}); err != nil {
		// Audit write failed at the very first event. This is a hard
		// signal that something is wrong with the store; abort before
		// touching the volume.
		return nil, fmt.Errorf("runner T0: append phase_started: %w", err)
	}

	// 2. Renderer UIEvent. Errors are non-fatal per PS3.
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseStarted,
		Phase:     types.PhasePreflight,
		Timestamp: startedAt,
	})

	// 3. Run all preflight gates.
	pc, perr := preflight.Preflight(ctx, preflight.Options{
		DestRoot:     in.DestRoot,
		SkipCodesign: in.SkipCodesign,
	})

	if perr != nil {
		// 4a. Phase aborted.
		finishedAt := time.Now().UTC()
		durationMS := finishedAt.Sub(startedAt).Milliseconds()

		// Append phase_aborted. Use a fresh context for the audit write if
		// the original was cancelled: callers must still be able to see why
		// the run aborted on disk even if they pressed Ctrl-C. Using
		// context.Background() here is deliberate; the alternative (skip
		// the write on ctx.Err) loses forensic information. Bound any
		// blocking by giving the background context a short timeout.
		//
		// Shared-budget note: the 5-second budget covers all three writes on
		// the abort path (one EventStore.Append + EventStore.Checkpoint +
		// RunLogStore.Checkpoint). On a wedged disk, expect at most partial
		// audit data: the first write may exhaust the budget for the rest.
		// This is by design; preserving partial forensic data beats hanging
		// the runner indefinitely or losing all of it.
		auditCtx := ctx
		if ctx.Err() != nil {
			var cancel context.CancelFunc
			auditCtx, cancel = context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
		}
		_ = in.EventStore.Append(auditCtx, state.Event{
			V:         1,
			Timestamp: finishedAt,
			Phase:     phaseWire,
			Kind:      "phase_aborted",
			Details: map[string]any{
				"duration_ms": durationMS,
				"error":       perr.Error(),
			},
		})

		// UIEvent: phase aborted. Err preserved per UIEvent.Err contract.
		emitUI(ctx, in.UIRenderer, types.UIEvent{
			Kind:      types.UIEvtPhaseCompleted,
			Phase:     types.PhasePreflight,
			Status:    "aborted",
			Err:       perr,
			Timestamp: finishedAt,
		})

		// Checkpoint both stores. Best-effort: a checkpoint error here is
		// already overshadowed by the preflight error. We log it implicitly
		// by losing it; the alternative (errors.Join) would clutter the
		// returned chain with phase-internal plumbing errors when the user
		// cares about the original preflight failure. PS3 mindset: visible
		// errors should map to user-actionable failures.
		_ = in.EventStore.Checkpoint(auditCtx)
		_ = in.RunLogStore.Checkpoint(auditCtx)

		return nil, fmt.Errorf("runner T0: %w", perr)
	}

	// 4b. Phase succeeded. From this point pc holds the lock; if anything
	// below fails before we return T0Result to the caller, we must release
	// the lock ourselves (the caller never got the pointer to defer Release).
	releaseOnError := func() {
		_ = pc.Release()
	}

	// Cancellation check at the success boundary. The user may have hit
	// Ctrl-C while preflight was running; honor that signal before writing
	// any "started" line that would advertise a run that is about to be
	// aborted.
	if err := ctx.Err(); err != nil {
		releaseOnError()
		return nil, fmt.Errorf("runner T0: %w", err)
	}

	// Append the runs.ndjson "started" line. This is the durable signal
	// that a run began (invariant #10: started/finished pairs document
	// completion; a started line without a finished line is a crashed run).
	if err := in.RunLogStore.AppendStarted(ctx, state.StartedRun{
		V:                  1,
		FlashbackupVersion: in.FlashbackupVersion,
		RunID:              in.RunID,
		StartedAt:          startedAt,
		Mode:               string(in.Mode),
		Profile:            in.ProfileName,
		SourceRoot:         in.SourceRoot,
		DestRoot:           in.DestRoot,
	}); err != nil {
		releaseOnError()
		return nil, fmt.Errorf("runner T0: append started: %w", err)
	}

	// Append phase_completed audit event.
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
		releaseOnError()
		return nil, fmt.Errorf("runner T0: append phase_completed: %w", err)
	}

	// UIEvent: phase completed (ok).
	emitUI(ctx, in.UIRenderer, types.UIEvent{
		Kind:      types.UIEvtPhaseCompleted,
		Phase:     types.PhasePreflight,
		Status:    "ok",
		Timestamp: finishedAt,
	})

	// Phase-boundary fsync for both stores so a crash here loses at worst
	// the current phase's events (invariant #17, EventStore docstring).
	if err := in.EventStore.Checkpoint(ctx); err != nil {
		releaseOnError()
		return nil, fmt.Errorf("runner T0: checkpoint events: %w", err)
	}
	if err := in.RunLogStore.Checkpoint(ctx); err != nil {
		releaseOnError()
		return nil, fmt.Errorf("runner T0: checkpoint runlog: %w", err)
	}

	return &T0Result{PreflightContext: pc}, nil
}

// emitUI fans a UIEvent out to the renderer, silently dropping renderer
// errors. PS3 of the plan strategic decisions: a misbehaving renderer
// (terminal closed, TUI panic, slow consumer) must never abort a run.
// state.Event in events.ndjson is the durable audit trail; UIEvent is
// best-effort UI.
//
// When a logger lands in the project, this is the choke point to wire it
// up: log the error at warn-level and continue.
func emitUI(ctx context.Context, r types.Renderer, ev types.UIEvent) {
	if r == nil {
		return
	}
	// PS3: renderer errors are non-fatal.
	_ = r.OnEvent(ctx, ev)
}
