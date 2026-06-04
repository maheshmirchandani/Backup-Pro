package state

import (
	"context"
	"time"
)

// StartedRun is the "started" line per the two-line model (invariant #10:
// crashed runs are visible from history because the "finished" line is
// absent). Written to <USB>/.flashbackup/runs.ndjson, one per started run.
//
// V is the schema version (currently 1); future schema changes must bump V
// rather than silently re-shape fields (invariant #13).
type StartedRun struct {
	V                  int       `json:"v"`
	Event              string    `json:"event"` // always "started"
	FlashbackupVersion string    `json:"flashbackup_version"`
	RunID              string    `json:"run_id"`
	StartedAt          time.Time `json:"started_at"`
	Mode               string    `json:"mode"` // copy | move | verify | init
	Profile            string    `json:"profile,omitempty"`
	SourceRoot         string    `json:"source_root"`
	DestRoot           string    `json:"dest_root"`
}

// FinishedRun is the "finished" line per the two-line model. Absence of a
// "finished" line for a given RunID indicates a crashed run.
type FinishedRun struct {
	V                             int       `json:"v"`
	Event                         string    `json:"event"` // always "finished"
	FlashbackupVersion            string    `json:"flashbackup_version"`
	RunID                         string    `json:"run_id"`
	StartedAt                     time.Time `json:"started_at"`
	FinishedAt                    time.Time `json:"finished_at"`
	Mode                          string    `json:"mode"`
	Profile                       string    `json:"profile,omitempty"`
	SourceRoot                    string    `json:"source_root"`
	DestRoot                      string    `json:"dest_root"`
	FilesTotal                    int       `json:"files_total"`
	FilesSucceeded                int       `json:"files_succeeded"`
	FilesFailed                   int       `json:"files_failed"`
	BytesTotal                    int64     `json:"bytes_total"`
	DeletionsSkippedDueToMutation int       `json:"deletions_skipped_due_to_mutation"`
	ExitStatus                    string    `json:"exit_status"` // ok | partial | copy_only_aborted_delete | crashed_resumed | preflight_failed
}

// RunLogStore handles the runs.ndjson append-only log.
//
// Contract mirrors EventStore:
//   - AppendStarted / AppendFinished: durable to PAGE CACHE on return (NOT
//     to disk). Caller may rely on the line being persisted across the
//     process exiting normally, but not across a kernel panic or power loss
//     until Checkpoint is called.
//   - Checkpoint: forces fsync. Call at run-boundary points so that a crash
//     does not lose the started/finished pair.
//   - All methods are safe to call concurrently from multiple goroutines.
//   - AppendStarted / AppendFinished / Checkpoint must not be called after
//     Close (they return an error containing "closed").
//   - Close is idempotent.
type RunLogStore interface {
	AppendStarted(ctx context.Context, s StartedRun) error
	AppendFinished(ctx context.Context, f FinishedRun) error
	Checkpoint(ctx context.Context) error
	Close() error
}
