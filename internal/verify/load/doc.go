// Package load reads a per-run gzipped manifest (manifest.ndjson.gz) and
// verifies each entry's keyed integrity checksum (HMAC) inline. It is the
// reader counterpart of internal/state.ndjsonManifestStore and the input
// stage of the `flashbackup verify` command (Tasks 30 to 32).
//
// Invariants enforced:
//   - Fail-closed version file load (invariant #11): missing, unparseable,
//     wrong schema_version, or invalid HMAC-key shapes abort with a wrapped
//     pipeline error. No silent re-init.
//   - schema_version != 1 is rejected (master plan line 2477 / invariant #13).
//     Both the version.json schema and the per-entry V field are checked.
//   - HMAC verification uses state.VerifyHMAC, which in turn uses the same
//     length-prefixed canonical encoding as the writer (invariant #33 and
//     TestHMAC_PipeSeparatorForgeryRejected). The loader does NOT
//     reimplement canonicalization.
//   - Tampered entries (HMAC mismatch) are surfaced as IntegrityErrors and
//     do NOT abort the load. This is the AC-19 path: `flashbackup verify`
//     must report tampered manifest lines, not silently drop them.
//   - Malformed JSON lines are surfaced as SchemaErrors (per-line) and do
//     NOT abort the load.
//   - Pipeline errors (file open, gzip read, version file fail-closed) DO
//     abort the load with a wrapped error.
//   - Context cancellation is checked every 256 entries (matches the
//     t1_enumerate cadence) and returns the wrapped ctx error.
//
// See docs/planning/2026-06-03-flashbackup-core-engine.md line 2477 for the
// pipeline specification and lines 314 to 339 for the VerifyOptions and
// VerifyResult contracts that Task 32 consumes.
package load
