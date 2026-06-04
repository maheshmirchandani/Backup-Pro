// Package selection walks a source tree, applies include/exclude glob
// patterns, and returns the candidate file set used by the runner's T1
// enumeration phase.
//
// Invariants:
//   - Relative paths are canonicalized to NFC before being placed in a
//     Candidate (invariant #32). Two source entries that differ only by
//     Unicode normalization form (NFC vs NFD twin) are detected at this
//     layer and BOTH are reported in Result.CollidingPaths; NEITHER is
//     included in Result.Candidates. The runner cannot safely choose
//     between two paths whose on-disk byte sequences differ but whose
//     normalized form is identical, so we surface the ambiguity to the
//     operator instead of silently picking one.
//   - The (Size, MtimeNS) pair captured per Candidate is the source-side
//     mutation-detection signature consumed at T3 (invariant #2). A
//     re-stat at T3 that disagrees with the T1 signature flips the file
//     to `source_mutated` status; this is why the signature is captured
//     at enumeration time and not on demand later.
//   - Candidates are FILES only. Directories are walked transparently;
//     symlinks are captured as Candidates with `Mode & os.ModeSymlink`
//     set and NEVER followed (rsync re-emits the symlink on copy).
//   - The source root entry (rel="") is skipped; the walker emits its
//     contents, not the root itself.
//   - Pattern matching uses `filepath.Match` against the file basename,
//     matching rsync's default behavior on simple patterns. Full-path
//     glob support is a Plan 2 concern.
//   - Result.Candidates is sorted by RelativePath for determinism. The
//     downstream rsync `--files-from` reader is order-sensitive only in
//     the sense that progress-line correlation requires a stable order;
//     sorted candidates make T1/T2/T3 cross-checks easy to read.
//   - Every public function takes `ctx context.Context` as its first
//     parameter and checks cancellation between entries (per the API
//     Contracts convention).
//
// Consumers: `internal/runner/t1_enumerate.go` (Task 23) builds the
// runner's `[]Signature` slice from Result.Candidates. The same
// Candidate list is consumed at T2 (rsync invocation, Task 24) and T3
// (per-file hash + signature re-check, Task 25).
package selection
