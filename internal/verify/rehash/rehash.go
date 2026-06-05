package rehash

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/hash"
	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Status is the per-file verify outcome. Mirrors the file-classification
// vocabulary of T2 (internal/runner/t3_hash_compare.go) but is a separate
// type because the verify-side vocabulary differs from the backup-side:
//
//   - StatusMissing is verify-specific (T2 sees not_transferred for the
//     equivalent condition, which has semantics tied to "rsync did not
//     write this file"; at verify time we cannot distinguish "rsync did
//     not write" from "file was deleted off the USB after the backup").
//   - StatusUnreadable folds both permission and IO error cases into one
//     Status; the underlying error is preserved in FileResult.Err so the
//     caller can rebuild the distinction without re-running.
//   - StatusSourceMutated does NOT exist here: verify only knows the
//     destination side. The source has long been disconnected.
type Status string

const (
	StatusVerified     Status = "verified"
	StatusSizeMismatch Status = "size_mismatch"
	StatusHashMismatch Status = "hash_mismatch"
	StatusMissing      Status = "missing"
	StatusUnreadable   Status = "unreadable"
)

// phaseWire is the wire-string carried on UIEvent.Phase for every event
// emitted by this package. The runner-side phase strings (T0..T4) describe
// the backup state machine; verify is a separate flow and does not fit any
// of them. A distinct string ("verify") keeps the renderer's phase-filter
// logic unambiguous without forcing the runner Phase taxonomy to absorb a
// non-backup concept. The constant is intentionally not exported through
// types.Phase: verify is not a runner phase, and persisting "verify" to
// events.ndjson is a separate decision Task 32 owns.
const phaseWire = "verify"

// missingSizeSentinel marks FileResult.ActualSize when the file could not
// be stat'd. -1 is unambiguous (real sizes are >= 0) and avoids the
// ambiguity of a zero-sized result which is a legitimate "empty file"
// outcome.
const missingSizeSentinel int64 = -1

// FileResult is one entry's verify outcome. Aggregated into Result.PerFile
// so the Task 32 caller can build the per-file summary that lands in
// `<run-dir>/verifications/<verify-id>/summary.json`.
type FileResult struct {
	// Entry is the original manifest entry consulted for size + sha256.
	// Carried so the caller does not need to re-correlate by RelPath.
	Entry state.ManifestEntry

	// Status is one of the five Status constants above.
	Status Status

	// ActualSize is the on-disk size as observed at verify time. -1 when
	// the file was missing or unreadable (stat failed). Compare against
	// Entry.Size to surface drift in error messages.
	ActualSize int64

	// ActualSHA256 is the destination-side hex digest recomputed at verify
	// time. Empty when the file was missing, unreadable, or short-circuited
	// by the size check (we do not hash a file whose size is already wrong).
	ActualSHA256 string

	// Err is the underlying error for StatusUnreadable. nil for all other
	// statuses (including hash_mismatch and size_mismatch, which are not
	// errors in the Go sense; they are valid verification outcomes).
	Err error
}

// Options configures a Rehash call. All fields are mandatory in the sense
// that defaults are not silently substituted; an empty DestRoot is a
// pipeline misconfiguration that yields a typed error at entry rather than
// resolving against the cwd.
type Options struct {
	// Entries is the output of internal/verify/load.Load
	// (LoadResult.Entries). Caller is responsible for filtering out
	// integrity-failed entries; rehash will faithfully re-hash whatever is
	// given.
	Entries []state.ManifestEntry

	// DestRoot is the absolute path to the USB backup root (the parent of
	// the namespace directory). Rehash applies the namespace prefix
	// internally via paths.Namespaced; do NOT pre-namespace this path.
	DestRoot string

	// Hostname and Username produce the namespace directory name via
	// paths.Prefix(Hostname, Username). They MUST match the values used at
	// backup time; otherwise every file will classify as Missing.
	Hostname string
	Username string

	// UIRenderer is the optional renderer for UIEvtProgress events. Nil is
	// valid and means "no UI events emitted" (PS3). Renderer errors are
	// swallowed (non-fatal) matching the runner's emitUI policy.
	UIRenderer types.Renderer
}

// Result is the aggregate of one Rehash call. Counters are exhaustive:
// FilesChecked == FilesVerified + FilesSizeMismatch + FilesHashMismatch +
// FilesMissing + FilesUnreadable. PerFile is parallel to Options.Entries
// up to the cancellation point.
type Result struct {
	FilesChecked      int
	FilesVerified     int
	FilesSizeMismatch int
	FilesHashMismatch int
	FilesMissing      int
	FilesUnreadable   int

	// BytesRead is the total bytes streamed through SHA256. Does NOT
	// include the bytes of size-mismatched or missing files (which were
	// never opened). Used by Task 32 to populate VerifyResult.BytesRead.
	BytesRead int64

	// PerFile preserves the per-entry outcome. Indexed in the same order
	// as the input Entries slice. On cancellation, may be shorter than
	// Entries (the cancellation index is implicit in len(PerFile)).
	PerFile []FileResult
}

// Rehash iterates opts.Entries, re-hashes each corresponding destination
// file, and classifies the outcome.
//
// Per-file pipeline:
//
//  1. Build dest path: paths.Namespaced(DestRoot, Hostname, Username,
//     entry.Path).
//  2. os.Lstat the dest. ENOENT -> StatusMissing (no hash). Any other
//     stat error -> StatusUnreadable with Err populated.
//  3. Compare on-disk size to entry.Size. Mismatch -> StatusSizeMismatch
//     (cheap fail-fast; do NOT hash a file we already know is wrong).
//  4. Open + StreamSHA256. Open error -> StatusUnreadable. Hash error ->
//     StatusUnreadable.
//  5. Compare the computed digest to entry.SHA256Source. Equal ->
//     StatusVerified. Otherwise -> StatusHashMismatch.
//
// Per-file errors do NOT abort the loop. Context cancellation is checked
// between files; on cancel, returns a wrapped ctx error with partial
// counters and PerFile populated up to the cancellation point. Returning
// the partial Result alongside the error is intentional: a verify run that
// processed 9000/10000 files before cancellation should not lose the 9000
// outcomes already classified.
//
// The renderer (UIRenderer) is called best-effort per PS3: nil is OK,
// errors are swallowed. One UIEvtProgress event is emitted per file with
// the FilesDone/FilesTotal/BytesDone/BytesTotal counters refreshed.
// BytesTotal is the sum of all entry.Size values (computed up-front so the
// progress bar shows a stable denominator).
func Rehash(ctx context.Context, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("verify rehash: %w", err)
	}
	if opts.DestRoot == "" {
		return nil, errors.New("verify rehash: DestRoot is empty")
	}

	// Pre-compute BytesTotal so the renderer can hold a stable progress
	// denominator across the run. Doing it up-front (one loop) is O(n)
	// over the entries slice; the alternative of recomputing incrementally
	// during the per-file loop would still cost the same allocations and
	// would force the renderer to redraw a moving denominator.
	var bytesTotal int64
	for _, e := range opts.Entries {
		bytesTotal += e.Size
	}

	result := &Result{
		PerFile: make([]FileResult, 0, len(opts.Entries)),
	}

	filesTotal := len(opts.Entries)

	for i, entry := range opts.Entries {
		if err := ctx.Err(); err != nil {
			return result, fmt.Errorf("verify rehash: cancelled at index %d: %w", i, err)
		}

		fr := classify(ctx, opts.DestRoot, opts.Hostname, opts.Username, entry)
		result.PerFile = append(result.PerFile, fr)
		result.FilesChecked++
		result.BytesRead += bytesReadForStatus(fr)

		switch fr.Status {
		case StatusVerified:
			result.FilesVerified++
		case StatusSizeMismatch:
			result.FilesSizeMismatch++
		case StatusHashMismatch:
			result.FilesHashMismatch++
		case StatusMissing:
			result.FilesMissing++
		case StatusUnreadable:
			result.FilesUnreadable++
		}

		emitProgress(ctx, opts.UIRenderer, entry.Path, result.FilesChecked, filesTotal, result.BytesRead, bytesTotal)
	}

	return result, nil
}

// classify runs the per-file pipeline for one manifest entry and returns
// the FileResult. Extracted from the Rehash loop so each branch is reachable
// without setting up an iteration; also makes the per-file logic
// individually testable in future regression scenarios.
func classify(ctx context.Context, destRoot, host, user string, entry state.ManifestEntry) FileResult {
	destPath := paths.Namespaced(destRoot, host, user, filepath.FromSlash(entry.Path))

	// Lstat (not Stat) matches the t3_hash_compare.go choice: a destination
	// that has been replaced by a symlink is a corruption signal, not a
	// "follow the symlink and hash whatever it points to" instruction.
	stat, err := os.Lstat(destPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return FileResult{
				Entry:      entry,
				Status:     StatusMissing,
				ActualSize: missingSizeSentinel,
			}
		}
		// Permission denied at stat time, or any other unexpected stat
		// error. We cannot prove or disprove anything about the bytes, so
		// classify as unreadable and preserve the error for the caller.
		return FileResult{
			Entry:      entry,
			Status:     StatusUnreadable,
			ActualSize: missingSizeSentinel,
			Err:        fmt.Errorf("lstat dest %q: %w", destPath, err),
		}
	}

	actualSize := stat.Size()
	if actualSize != entry.Size {
		// Cheap fail-fast: a truncated or extended destination file does
		// not need a hash to confirm corruption. Save the IO.
		return FileResult{
			Entry:      entry,
			Status:     StatusSizeMismatch,
			ActualSize: actualSize,
		}
	}

	// destPath derives from entry.Path, which load.go validated against the
	// per-USB HMAC (invariant #33) before the entry surfaced here. Upstream
	// selection.Walk produced RelativePath via filepath.Rel so the path
	// cannot escape DestRoot under the namespace prefix. Bounded input;
	// G304 false-positive.
	f, err := os.Open(destPath) //nolint:gosec // bounded: HMAC-validated entry.Path under DestRoot namespace
	if err != nil {
		return FileResult{
			Entry:      entry,
			Status:     StatusUnreadable,
			ActualSize: actualSize,
			Err:        fmt.Errorf("open dest %q: %w", destPath, err),
		}
	}
	digest, _, err := hash.StreamSHA256(ctx, f)
	_ = f.Close()
	if err != nil {
		return FileResult{
			Entry:      entry,
			Status:     StatusUnreadable,
			ActualSize: actualSize,
			Err:        fmt.Errorf("hash dest %q: %w", destPath, err),
		}
	}

	if digest != entry.SHA256Source {
		return FileResult{
			Entry:        entry,
			Status:       StatusHashMismatch,
			ActualSize:   actualSize,
			ActualSHA256: digest,
		}
	}

	return FileResult{
		Entry:        entry,
		Status:       StatusVerified,
		ActualSize:   actualSize,
		ActualSHA256: digest,
	}
}

// bytesReadForStatus returns the number of source bytes consumed by
// StreamSHA256 for this file's classification. Verified and hash_mismatch
// both required a full read; size_mismatch / missing / unreadable did not.
// Computed from the manifest's Size (not the on-disk size) because the
// rehash always reads up to Size bytes when it reaches the hash step, and
// we want a deterministic byte total even if the underlying io.CopyBuffer
// short-reads on a hostile filesystem.
func bytesReadForStatus(fr FileResult) int64 {
	switch fr.Status {
	case StatusVerified, StatusHashMismatch:
		return fr.Entry.Size
	default:
		return 0
	}
}

// emitProgress fans one UIEvtProgress event out to the renderer. nil
// renderer skipped; renderer errors swallowed (PS3, mirrors emitUI in
// internal/runner/t0_preflight.go).
//
// One progress event per file (no throttling). The spec's 200ms target tick
// rate applies to T1 transfer where rsync emits thousands of byte-level
// updates per second; verify is hash-bound and a per-file granularity gives
// the renderer one update per multi-second hash, which is well below the
// throttle threshold and does not need an additional pacing layer here.
func emitProgress(ctx context.Context, r types.Renderer, currentFile string,
	filesDone, filesTotal int, bytesDone, bytesTotal int64) {
	if r == nil {
		return
	}
	_ = r.OnEvent(ctx, types.UIEvent{
		Kind:  types.UIEvtProgress,
		Phase: types.Phase(phaseWire),
		Path:  currentFile,
		Progress: &types.ProgressInfo{
			BytesDone:   bytesDone,
			BytesTotal:  bytesTotal,
			FilesDone:   filesDone,
			FilesTotal:  filesTotal,
			CurrentFile: currentFile,
		},
		Timestamp: time.Now().UTC(),
	})
}
