// Package state provides persistent run state for FlashBackup, including the
// per-run audit event log (events.ndjson) and (in later tasks) the run log.
// Invariant #16: all run state lives under <USB>/.flashbackup/runs/<run-id>/.
package state

import (
	"context"
	"time"
)

// Event records one structured event during a run. Invariant #17.
// Written to <USB>/.flashbackup/runs/<run-id>/events.ndjson, one per line.
//
// V is the schema version (currently 1); future schema changes must bump V
// rather than silently re-shape fields.
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
//   - Append: durable to PAGE CACHE on return (NOT to disk). Caller may
//     rely on the event being persisted across the process exiting normally,
//     but not across a kernel panic or power loss until Checkpoint is called.
//   - Checkpoint: forces fsync. Call at phase boundaries (T0 done, T1 done,
//     etc.) to bound the window of data loss to ~one phase.
//   - Append + Checkpoint are safe to call concurrently from multiple goroutines.
//   - Append/Checkpoint must not be called after Close.
//   - Close is idempotent.
//
// Rationale (AMENDMENT 2026-06-03, multi-hat round): per-event fsync was
// measured at 5-15 min added wall time on a 100K-file backup against USB.
// Trading per-event durability for phase-boundary durability is the right
// call for an audit log where the worst-case loss is ~one phase of events.
type EventStore interface {
	Append(ctx context.Context, ev Event) error
	Checkpoint(ctx context.Context) error
	Close() error
}
