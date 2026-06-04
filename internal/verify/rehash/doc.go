// Package rehash re-hashes destination files on the USB and classifies each
// against its corresponding manifest entry produced by Task 30
// (internal/verify/load). It is the second stage of the `flashbackup verify`
// pipeline (Tasks 30 to 32).
//
// Invariants enforced and assumptions:
//
//   - Invariant #1 (move semantics: copy then validate then delete): the
//     verify command repeats the destination-side hash check that T2 ran
//     at backup time. A backup that passed T2 might still report
//     hash_mismatch here if the destination bytes drifted post-backup
//     (silent corruption, careless edit, partial write to a flapping USB).
//   - Invariant #5 / #15 (namespacing): destination paths are read from
//     `<DestRoot>/<paths.Prefix(host, user)>/<RelPath>`. The same Prefix
//     helper that the backup runner used is reused here; a divergence would
//     hide every file behind a wrong namespace and surface as 100% missing.
//   - Invariant #32 (NFC canonicalization at enumeration): RelPath in
//     ManifestEntry is already NFC-normalized; rehash does not re-normalize
//     and joins the path as-is.
//   - Per-file errors (missing dest, permission denied, mid-hash IO) do NOT
//     abort the loop. Each file is classified into one of five Status
//     values and the aggregate Result is returned. This matches T2's policy
//     (t3_hash_compare.go) and is required for AC-19: a single tampered
//     file must not blind the operator to the remaining N-1 outcomes.
//   - Cheap fail-fast on size: the size check happens BEFORE the hash. A
//     10 GB file truncated to 100 bytes is classified size_mismatch
//     without spending ~30 seconds hashing the truncated remainder.
//   - PS3 (renderer is best-effort): UIRenderer errors are swallowed; nil
//     UIRenderer is valid. Emits one UIEvtProgress per file with
//     Phase="verify" (a verify-specific wire string distinct from the T0-T4
//     runner phases; see phaseWire in rehash.go).
//   - Context cancellation: checked between files; returns wrapped ctx
//     error with partial counters populated up to the cancellation point.
//
// See docs/planning/2026-06-03-flashbackup-core-engine.md line 2480 for the
// task definition and lines 314 to 339 for the VerifyOptions / VerifyResult
// contracts that Task 32 consumes.
package rehash
