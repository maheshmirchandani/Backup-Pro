// Package runner orchestrates a FlashBackup run as a sequence of phase
// functions. The top-level entry point runner.Run (Task 29) opens the state
// stores, generates the RunID, and threads a *preflight.PreflightContext
// across one-file-per-phase orchestrators:
//
//	t0_preflight.go    phase T0    (this task, Task 22)
//	t1_enumerate.go    phase T0+   (Task 23)
//	t2_transfer.go     phase T1    (Task 24)
//	t3_hash_compare.go phase T2    (Task 25)
//	t4_delete_source.go phase T3   (Task 26)
//	t5_finalize.go     phase T4    (Task 27)
//
// File-number vs phase-wire-string note: the file ordinals (0..5) match the
// per-task file naming chosen in the plan, but the phase wire strings are
// "T0", "T0+", "T1", "T2", "T3", "T4". File t1_enumerate is phase T0+, NOT
// phase T1. Anyone editing a sibling tN file must read the phase header at
// the top of that file to know which wire string to emit; cross-referencing
// the file number is a trap.
//
// State-store ownership: runner.Run opens state.EventStore and
// state.RunLogStore once per run and threads the handles into every phase
// function. Phase functions Append + Checkpoint; they do not Open or Close.
// This keeps phase boundaries fsync-durable per the EventStore contract
// (invariant #17) without each phase needing to know the storage layout.
//
// Per PS3 of the plan strategic decisions, the Renderer interface lives in
// runner/types (not next to internal/plain). Phase functions emit UIEvents
// to the renderer if one is configured; renderer errors are non-fatal
// (PS3 contract) and never abort the run.
package runner
