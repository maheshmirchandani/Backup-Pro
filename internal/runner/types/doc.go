// Package types holds the runner state-machine types: Phase, Mode,
// RunOptions, RunResult, UIEvent, ProgressInfo, Signature, and the Renderer
// interface. The runner package owns these because run orchestration is
// what binds them together; storage types (FileStatus, DeletionStatus,
// IntegrityStatus) intentionally remain in internal/state, where they are
// part of the HMAC-canonical manifest schema (invariant #33).
//
// PS3 of the plan strategic decisions: the Renderer interface lives here,
// with the consumer (the runner), not next to any single implementation.
// PS4: UIEvent is renderer-facing and distinct from state.Event, which is
// the persisted, HMAC-bound on-disk record.
//
// Phase legend:
//
//	T0  preflight    acquire lock, verify USB identity, extract rsync,
//	                 load version key, run codesign self-verify
//	T0+ enumerate    walk source tree under profile, NFC-canonicalize paths
//	                 (invariant #32), record per-file Signature at T0+ for
//	                 the source-mutation gate (invariant #8)
//	T1  transfer     invoke embedded rsync 3.x over the files-from list;
//	                 cancellation maps to SIGTERM + 5s grace then SIGKILL
//	T2  hash+compare hash source AND dest per file, classify FileStatus,
//	                 populate manifest with HMAC per entry (invariant #33)
//	T3  delete       move-mode only: per-file re-stat (mutation gate) then
//	                 unlink. Atomic gate from invariant #1 means any non-
//	                 verified file blocks ALL source deletion for the run.
//	T4  finalize     gzip the manifest, append the runs.ndjson "finished"
//	                 line, release the lock, emit UIEvtSummary
//
// ASCII state diagram. The hard-fail arrow on T1 represents an rsync
// invocation that exited before producing any per-file output (e.g.,
// embedded-binary extraction failed, immediate I/O error). Per-file failures
// inside a running rsync are NOT a T1 hard-fail; they flow through T2 where
// each file is classified.
//
//	[T0 preflight]
//	     |  ok
//	     v
//	[T0+ enumerate]
//	     |  ok
//	     v
//	[T1 transfer]  ---hard-fail--->  [T4 finalize as partial]
//	     |  ok (or partial; T2 will classify)
//	     v
//	[T2 hash+compare]  -----------> classify each file as verified /
//	     |                          hash_mismatch / source_mutated /
//	     |                          not_transferred / *_unreadable
//	     |  copy mode  ----------->  [T4 finalize]
//	     |  move mode
//	     v
//	[T3 delete (atomic gate)]
//	     |
//	     v
//	[T4 finalize]
//
// Signal-handler contract (per spec section 6 and the cancellation
// invariants): SIGINT and SIGTERM map to a phase-specific cancel-and-flush.
//
//	T0, T0+   abort immediately; release the lock; no on-disk effect.
//	T1        send SIGTERM to the rsync child, wait 5 seconds, then
//	          SIGKILL. The runner exits with partial status; any files
//	          rsync already wrote stay on disk for the next run to
//	          reconcile.
//	T2, T3    finish the current file, then exit with partial status.
//	          The atomic gate from invariant #1 still applies: any T3
//	          unlink already executed stays executed; the gate fires
//	          on the remaining files.
//	T4        finish the finalize then exit. A partial T4 is the worst
//	          case because manifest gzip and runs.ndjson append are not
//	          a single atomic operation; the runner orders them so a
//	          crash mid-T4 leaves a recoverable state (manifest written
//	          before runs.ndjson finished line).
//
// A second signal of the same type within 5 seconds of the first forces
// immediate exit, bypassing the graceful flush above (spec section 6). The
// runner package owns this state machine; types.go just declares the
// constants and the data the renderer sees.
package types
