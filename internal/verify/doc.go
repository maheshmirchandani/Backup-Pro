// Package verify implements the top-level `flashbackup verify` state machine:
//
//	PREFLIGHT -> LOAD MANIFEST(S) -> RE-HASH DEST -> SUMMARIZE -> WRITE RECORD
//
// It composes Task 30 (internal/verify/load) and Task 31 (internal/verify/rehash)
// into the end-to-end pipeline exposed via the API Contracts shape:
//
//	Verify(ctx, opts) (*VerifyResult, error)
//
// Invariants enforced:
//
//   - Invariant #11 (fail-closed version file): preflight + load.Load reject
//     missing/corrupt version.json. No silent re-init.
//   - Invariant #19 (last-verified surfaced): writes a per-verify summary.json
//     to <DotDir>/runs/<RunID>/verifications/<VerifyID>/summary.json so that
//     `flashbackup status` can surface the most recent verify outcome.
//   - Invariant #33 (keyed integrity checksum / AC-19): tampered manifest
//     lines populate VerifyResult.FilesIntegrityFailed from load.IntegrityErrors
//     and force ExitStatus="integrity_failed". This is the AC-19 path; a
//     silent skip would defeat the keyed checksum threat model.
//   - Invariant #14/#15 (namespace single source of truth): the namespaced
//     dest derivation is delegated to rehash.Rehash via Hostname/Username
//     from preflight; this package never reimplements paths.Prefix.
//   - Invariant #5 (exclusive lock): the verify run shares the backup lock
//     via preflight.Preflight so verify cannot race a concurrent backup.
//     Released via defer pc.Release() on every return path.
//
// Exit status table:
//
//	ok                 every file verified, manifest HMACs all clean
//	integrity_failed   any per-file failure or any load schema/integrity error
//	preflight_failed   preflight returned (no per-run work attempted)
//
// All=true vs single-run: when All=true the function processes every
// runIDPattern-matching dir under <DotDir>/runs/, aggregates counters into
// one VerifyResult, and sets RunID="all". The worst-case ExitStatus wins
// (any integrity_failed across the batch makes the whole batch
// integrity_failed). Errors loading or rehashing one run do NOT short-circuit
// the batch; the aggregate counters still reflect every run we got to.
//
// See docs/planning/2026-06-03-flashbackup-core-engine.md lines 314 to 339
// for the locked API contract and line 2481 for the Task 32 definition;
// docs/specs/2026-06-03-1532-flashbackup-design.md Section 5 for the verify
// state machine.
package verify
