package types

import (
	"context"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
)

// Phase identifies one of the six T-phases in the FlashBackup run state
// machine. Wire strings ("T0", "T0+", "T1", "T2", "T3", "T4") are persisted
// in events.ndjson and rendered to the operator, so they are part of the
// public on-disk contract: changing them is a manifest-schema bump per
// invariant #13.
type Phase string

const (
	PhasePreflight   Phase = "T0"
	PhaseEnumerate   Phase = "T0+"
	PhaseTransfer    Phase = "T1"
	PhaseHashCompare Phase = "T2"
	PhaseDelete      Phase = "T3"
	PhaseFinalize    Phase = "T4"
)

// Mode is the run's data-handling intent. ModeCopy leaves the source intact;
// ModeMove activates the T3 delete phase under the atomic-gate rule of
// invariant #1: copy then validate then delete, with any non-verified file
// blocking all source deletion in the run.
type Mode string

const (
	ModeCopy Mode = "copy"
	ModeMove Mode = "move"
)

// Signature is the at-T0+ fingerprint of a source file used by the mutation
// gate at T3 (invariant #8 source mutation gate): re-stat the source path at
// T3, compare to Signature, skip the delete if either field changed since
// enumeration time. Kept as a plain comparable value so the gate can use ==.
//
// The same (Size, MtimeNS) pair is also stored on selection.Candidate by the
// enumeration walker; Signature is the runner-side view of that fingerprint
// after Candidate has been consumed and the absolute path has been resolved.
type Signature struct {
	Size    int64
	MtimeNS int64
}

// Renderer is the UIEvent sink. Implementations live in sibling packages
// (internal/plain for the terminal renderer, future internal/tui for the
// Bubble Tea TUI). A nil Renderer in RunOptions means events are not
// rendered to a UI; they are still persisted to events.ndjson by the runner
// via state.EventStore, which is the durable record.
//
// The interface lives here, with the consumer (the runner orchestrator),
// rather than next to any single implementation. This is the idiomatic Go
// placement and it avoids an import cycle between runner and plain.
type Renderer interface {
	OnEvent(ctx context.Context, ev UIEvent) error
}

// RunOptions is the input to runner.Run. All fields are read-only after the
// call begins; the runner takes a copy of the struct, so callers may free
// the originals immediately on return.
//
// UIRenderer is a Renderer (not a concrete type) so the runner package does
// not depend on internal/plain; see the Renderer doc comment for the
// rationale. A nil UIRenderer is valid and means "no UI events emitted".
type RunOptions struct {
	Profile    profiles.Profile
	DestRoot   string
	Mode       Mode
	DryRun     bool
	Delete     bool // mirror mode: remove FB-written paths absent from source
	UIRenderer Renderer
}

// ExitStatus constants are the canonical wire strings persisted to the
// runs.ndjson "finished" line per spec section 5. The field on RunResult is
// typed string (not ExitStatus) to keep forward-compat with future spec
// additions, but call sites should compare against these constants rather
// than inlining quoted literals; a typo here is a manifest-schema regression
// per invariant #13.
const (
	ExitStatusOK                    = "ok"                       // all files verified, all deletions completed
	ExitStatusPartial               = "partial"                  // one or more files failed validation or copy
	ExitStatusCopyOnlyAbortedDelete = "copy_only_aborted_delete" // T1+T2 ok but the move-mode atomic gate fired
	ExitStatusCrashedResumed        = "crashed_resumed"          // run finalized by orphan-recovery on a later launch
	ExitStatusPreflightFailed       = "preflight_failed"         // T0 returned an error; no files were touched
)

// RunResult is the runner's return value, populated incrementally across
// phases and emitted as the final UIEvtSummary event. ExitStatus is one of
// the five ExitStatus* constants declared above.
//
// SupportPaths is the runner-aggregated list of forensic file paths
// (rsync.log from T1, deletion-log.ndjson from T3) gathered for the
// support-bundle generator. Empty paths are excluded by the runner; the
// slice can be nil for a copy-mode run that never produces a deletion log.
type RunResult struct {
	RunID                         string
	StartedAt, FinishedAt         time.Time
	FilesTotal, FilesSucceeded    int
	FilesFailed                   int
	BytesTotal                    int64
	DeletionsSkippedDueToMutation int
	ExitStatus                    string
	SupportPaths                  []string
}

// UIEventKind identifies the shape of a UIEvent. The wire strings are stable
// across versions and form the on-disk vocabulary of events.ndjson when the
// runner mirrors UI events to disk. PS4 from the plan strategic decisions:
// UIEvent is renderer-facing and distinct from state.Event (persisted).
type UIEventKind string

const (
	UIEvtPhaseStarted   UIEventKind = "phase_started"
	UIEvtPhaseCompleted UIEventKind = "phase_completed"
	UIEvtFileStarted    UIEventKind = "file_started"
	UIEvtFileCompleted  UIEventKind = "file_completed"
	UIEvtFileFailed     UIEventKind = "file_failed"
	UIEvtProgress       UIEventKind = "progress" // bytes-level throughput tick
	UIEvtPrompt         UIEventKind = "prompt"   // request user input (DELETE confirm)
	UIEvtSummary        UIEventKind = "summary"  // final run summary
)

// UIEvent is one message in the runner-to-renderer event stream. Per PS4 it
// is distinct from state.Event: this struct is the in-memory shape consumed
// by Renderer; state.Event is the durable on-disk shape persisted in
// events.ndjson. The runner builds a UIEvent first, fans it out to the
// renderer, then transforms it into state.Event for persistence.
//
// Field population by Kind (zero values for omitted fields):
//
//	UIEvtPhaseStarted     Phase, Timestamp
//	UIEvtPhaseCompleted   Phase, Status, Timestamp
//	UIEvtFileStarted      Phase, Path, Timestamp
//	UIEvtFileCompleted    Phase, Path, Status, Timestamp
//	UIEvtFileFailed       Phase, Path, Err, Timestamp
//	UIEvtProgress         Phase, Progress, Timestamp
//	UIEvtPrompt           Phase, Status (prompt text), Timestamp
//	UIEvtSummary          Status (exit status), Timestamp
//
// Progress is a pointer so non-progress events do not allocate 48 bytes of
// counter fields. Err preserves error identity (errors.Is/As both work
// through this field) rather than being stringified at emit time.
type UIEvent struct {
	Kind      UIEventKind
	Phase     Phase
	Path      string        // file path when relevant
	Progress  *ProgressInfo // bytes done / total when relevant
	Status    string        // status string for completion events
	Err       error         // populated for failure events
	Timestamp time.Time
}

// ProgressInfo carries throughput data on UIEvtProgress events. The spec
// targets one progress tick per 200 ms during T1 so the renderer can hold a
// stable bytes-per-second moving average without ticker contention. ETA is
// computed by the runner (single source of truth) rather than the renderer
// to keep TUI and plain renderers showing the same number.
type ProgressInfo struct {
	BytesDone, BytesTotal int64
	FilesDone, FilesTotal int
	CurrentFile           string
	BytesPerSec           int64
	ETASeconds            int
}
