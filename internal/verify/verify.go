package verify

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"syscall"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/preflight"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/load"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/rehash"
)

// ExitStatus wire strings persisted in summary.json and returned on
// VerifyResult.ExitStatus. Kept as untyped constants (matching the runner
// ExitStatus* convention) so call sites can compare against named values
// rather than inlining string literals.
const (
	ExitStatusOK              = "ok"
	ExitStatusIntegrityFailed = "integrity_failed"
	ExitStatusPreflightFailed = "preflight_failed"
)

// allRunIDSentinel is the VerifyResult.RunID returned when All=true.
// Distinct from any actual RunID (the canonical RunID pattern is timestamp-
// prefixed; "all" cannot collide). Lets the caller distinguish a batch
// aggregate from a single-run result without inspecting len(opts.RunID).
const allRunIDSentinel = "all"

// manifestBaseFilename mirrors the runner-side constant; verify needs to
// reach the canonical per-run manifest path without importing the runner
// package (which would pull in heavyweight orchestration types).
// Single-source-of-truth nit: a future rename of manifest.ndjson.gz must
// touch BOTH runner.manifestBaseFilename AND this constant. The drift
// trap is documented here and reaffirmed by the happy-path test, which
// builds a manifest through the runner and reads it back through verify.
const manifestBaseFilename = "manifest.ndjson"

// runIDPattern matches the canonical RunID format. Duplicated from
// runner/t5_finalize.go for the same reason as manifestBaseFilename: avoid
// importing the runner package from verify. The two regexes MUST stay
// identical; a TestRunIDPatternsMatch guard could be added if drift is
// observed in practice.
var runIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}Z-[0-9a-fA-F]{4}$`)

// summaryFilename is the canonical name of the per-verify aggregate file.
// One source of truth; the test fixture references the same constant.
const summaryFilename = "summary.json"

// summarySchemaVersion is the schema version of the per-verify summary.json
// payload. Bump before changing field shape rather than silently re-shaping
// (invariant #13 mindset; verify summary is not formally schema_version'd
// alongside the manifest but the same discipline applies to a release
// artifact downstream tooling reads).
const summarySchemaVersion = 1

// VerifyOptions configures a Verify call. Mirrors the API Contracts shape
// (master plan lines 314 to 339); all fields are read-only after the call
// begins.
type VerifyOptions struct {
	// RunID identifies which run to verify. Empty string means "latest"
	// (latest by lexical sort of runIDPattern-matching dirs under
	// <DotDir>/runs/). Mutually exclusive with All=true.
	RunID string

	// All requests verification of every run-dir on the USB. Mutually
	// exclusive with a non-empty RunID. The returned VerifyResult is the
	// AGGREGATE across all runs (RunID="all"); per-run summary.json files
	// still land under each run's verifications/ subdir.
	All bool

	// CheckExtras enables a destination-side walk to count files present
	// at dest that are NOT named in the manifest. The count lands in
	// VerifyResult.FilesExtraInDest. Default-off because per invariant #6
	// FlashBackup never touches user-added files; flagging them as errors
	// would be a false alarm.
	CheckExtras bool

	// DestRoot is the absolute USB mountpoint (e.g. "/Volumes/FLASHBKP").
	// Required.
	DestRoot string

	// UIRenderer is the optional renderer (PS3 best-effort). Nil is valid;
	// no UI events are emitted in that case. Forwarded to rehash.Rehash
	// and used here to emit the terminal UIEvtSummary.
	UIRenderer types.Renderer

	// SkipCodesign forces dev-mode behavior in preflight gate 1; test-only
	// escape hatch. Release builds must NOT set this true (the cmd CLI
	// does not expose it).
	SkipCodesign bool
}

// VerifyResult mirrors the API Contracts shape. Counter semantics:
//
//	FilesChecked        sum(rehashResult.FilesChecked) across runs
//	FilesVerified       sum(rehashResult.FilesVerified)
//	FilesHashMismatch   sum(rehashResult.FilesHashMismatch)
//	FilesIntegrityFailed AC-19: sum(len(loadResult.IntegrityErrors))
//	FilesMissing        sum(rehashResult.FilesMissing)
//	FilesSizeMismatch   sum(rehashResult.FilesSizeMismatch)
//	FilesUnreadable     sum(rehashResult.FilesUnreadable)
//	FilesExtraInDest    sum(extra-files-walk) when CheckExtras=true; else 0
type VerifyResult struct {
	RunID                string
	FilesChecked         int
	FilesVerified        int
	FilesHashMismatch    int
	FilesIntegrityFailed int
	FilesMissing         int
	FilesSizeMismatch    int
	FilesUnreadable      int
	FilesExtraInDest     int
	DurationSeconds      int
	BytesRead            int64
	ExitStatus           string
}

// summaryRecord is the on-disk shape persisted at
// <DotDir>/runs/<runID>/verifications/<verifyID>/summary.json. Field names
// are snake_case per invariant #45 / project convention (kebab-case applies
// to filenames, snake_case to JSON keys).
type summaryRecord struct {
	V                    int       `json:"v"`
	VerifyID             string    `json:"verify_id"`
	ForRunID             string    `json:"for_run_id"`
	VerifiedAt           time.Time `json:"verified_at"`
	DurationSeconds      int       `json:"duration_seconds"`
	FilesChecked         int       `json:"files_checked"`
	FilesVerified        int       `json:"files_verified"`
	FilesHashMismatch    int       `json:"files_hash_mismatch"`
	FilesIntegrityFailed int       `json:"files_integrity_failed"`
	FilesMissing         int       `json:"files_missing"`
	FilesSizeMismatch    int       `json:"files_size_mismatch"`
	FilesUnreadable      int       `json:"files_unreadable"`
	FilesExtraInDest     int       `json:"files_extra_in_dest"`
	BytesRead            int64     `json:"bytes_read"`
	ExitStatus           string    `json:"exit_status"`
}

// Verify is the top-level verify entry point. Pipeline:
//
//  1. validate opts; install SIGINT/SIGTERM handler via signal.NotifyContext
//  2. preflight.Preflight (acquires the shared backup lock; loads version.json)
//  3. resolve RunID(s): explicit, latest, or All
//  4. for each run: load.Load -> rehash.Rehash -> optional CheckExtras walk
//     -> aggregate counters -> write summary.json
//  5. emit UIEvtSummary; release the lock; return VerifyResult
//
// Cancellation: ctx is wrapped via signal.NotifyContext so SIGINT/SIGTERM
// translates to ctx cancellation, which propagates through load and rehash.
// Per-file errors do NOT abort the verify; the aggregate counters reflect
// every file that was classifiable up to the cancellation point.
//
// invariant #11 / #33: preflight + load enforce fail-closed version.json
// and keyed HMAC. invariant #5: preflight acquires the exclusive lock.
// invariant #19: writes summary.json so `flashbackup status` can surface
// the latest verify outcome.
func Verify(ctx context.Context, opts VerifyOptions) (*VerifyResult, error) {
	// 1. Input validation. Reject empty DestRoot (cannot be silently
	// substituted with cwd; would point verify at the wrong volume).
	if opts.DestRoot == "" {
		return &VerifyResult{ExitStatus: ExitStatusPreflightFailed},
			errors.New("verify: DestRoot is empty")
	}
	// All and RunID are mutually exclusive: "all" means "every run" and
	// passing both is a caller bug (which run did you mean?). Distinct
	// from RunID="" (which means "latest"), so the check is on RunID != "".
	if opts.All && opts.RunID != "" {
		return &VerifyResult{ExitStatus: ExitStatusPreflightFailed},
			errors.New("verify: All=true and RunID are mutually exclusive")
	}

	startedAt := time.Now().UTC()

	// 2. Signal handler. Matches runner.Run's pattern: SIGINT (Ctrl-C) +
	// SIGTERM cancel ctx; the per-run load + rehash check ctx.Err() at
	// their own boundaries.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// 3. Preflight. invariant #5: the shared lock prevents a concurrent
	// backup. invariant #11: ReadVersionFile inside preflight is
	// fail-closed (missing/corrupt version.json aborts).
	pc, err := preflight.Preflight(ctx, preflight.Options{
		DestRoot:     opts.DestRoot,
		SkipCodesign: opts.SkipCodesign,
	})
	if err != nil {
		result := &VerifyResult{
			ExitStatus:      ExitStatusPreflightFailed,
			DurationSeconds: int(time.Since(startedAt).Seconds()),
		}
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("verify: preflight: %w", err)
	}
	defer func() { _ = pc.Release() }()

	// 4. Resolve RunIDs. The slice is ordered oldest-first so per-run
	// summary.json files land in chronological order under each run-dir;
	// counters aggregate identically regardless of order.
	runIDs, err := resolveRunIDs(pc.DotDir, opts)
	if err != nil {
		result := &VerifyResult{
			ExitStatus:      ExitStatusPreflightFailed,
			DurationSeconds: int(time.Since(startedAt).Seconds()),
		}
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("verify: resolve run ids: %w", err)
	}

	// 5. Per-run pipeline. Counters aggregate; ExitStatus takes the worst
	// case across all runs (any integrity_failed wins).
	aggregate := &VerifyResult{
		ExitStatus: ExitStatusOK,
	}

	versionPath := filepath.Join(pc.DotDir, "version.json")

	for _, runID := range runIDs {
		// ctx check between runs so a SIGINT during a multi-run batch
		// halts before starting the next run (the per-run loops also
		// check ctx but mid-loop cancellation is the rehash loop's
		// concern).
		if err := ctx.Err(); err != nil {
			// Aggregate what we have; do NOT wipe partial counters. The
			// caller receives the wrapped ctx error alongside.
			aggregate.RunID = aggregatedRunID(opts, runIDs)
			aggregate.DurationSeconds = int(time.Since(startedAt).Seconds())
			emitSummary(ctx, opts.UIRenderer, aggregate)
			return aggregate, fmt.Errorf("verify: cancelled before run %s: %w", runID, err)
		}

		runResult, runErr := verifyOneRun(ctx, oneRunInputs{
			runID:       runID,
			dotDir:      pc.DotDir,
			destRoot:    pc.DestRoot,
			hostname:    pc.Hostname,
			username:    pc.Username,
			versionPath: versionPath,
			checkExtras: opts.CheckExtras,
			renderer:    opts.UIRenderer,
		})

		// A pipeline error on one run (e.g., load failure) does NOT abort
		// the batch when All=true. The aggregate counters skip the
		// uncompletable run; ExitStatus reflects the worst case seen.
		// For the single-run case, a pipeline error is the result the
		// caller wanted.
		if runErr != nil {
			if !opts.All {
				// Single-run failure: surface immediately.
				aggregate.RunID = runID
				aggregate.ExitStatus = ExitStatusPreflightFailed
				aggregate.DurationSeconds = int(time.Since(startedAt).Seconds())
				emitSummary(ctx, opts.UIRenderer, aggregate)
				return aggregate, fmt.Errorf("verify: run %s: %w", runID, runErr)
			}
			// All=true: continue with the remaining runs; the missing run
			// degrades the batch ExitStatus to integrity_failed (the
			// safer signal; the operator should investigate).
			aggregate.ExitStatus = ExitStatusIntegrityFailed
			continue
		}

		// Per-run counters fold into the aggregate.
		aggregate.FilesChecked += runResult.FilesChecked
		aggregate.FilesVerified += runResult.FilesVerified
		aggregate.FilesHashMismatch += runResult.FilesHashMismatch
		aggregate.FilesIntegrityFailed += runResult.FilesIntegrityFailed
		aggregate.FilesMissing += runResult.FilesMissing
		aggregate.FilesSizeMismatch += runResult.FilesSizeMismatch
		aggregate.FilesUnreadable += runResult.FilesUnreadable
		aggregate.FilesExtraInDest += runResult.FilesExtraInDest
		aggregate.BytesRead += runResult.BytesRead

		if runResult.ExitStatus == ExitStatusIntegrityFailed {
			aggregate.ExitStatus = ExitStatusIntegrityFailed
		}
	}

	// 6. RunID resolution for the returned aggregate.
	aggregate.RunID = aggregatedRunID(opts, runIDs)
	aggregate.DurationSeconds = int(time.Since(startedAt).Seconds())

	// 7. Final UIEvtSummary. Renderer errors swallowed per PS3.
	emitSummary(ctx, opts.UIRenderer, aggregate)

	return aggregate, nil
}

// oneRunInputs bundles the per-run pipeline parameters into one struct so
// the per-run helper signature stays readable; positional args grew to 8
// before this refactor.
type oneRunInputs struct {
	runID       string
	dotDir      string
	destRoot    string
	hostname    string
	username    string
	versionPath string
	checkExtras bool
	renderer    types.Renderer
}

// verifyOneRun runs the full per-run pipeline (load -> rehash -> optional
// extras walk -> summary.json write) and returns a VerifyResult populated
// for THIS run only. The aggregator in Verify sums these across runs.
//
// Errors here are pipeline errors (load failure, summary write failure);
// per-file failures land in counters, not in the returned error.
func verifyOneRun(ctx context.Context, in oneRunInputs) (*VerifyResult, error) {
	runStartedAt := time.Now().UTC()

	runDir := filepath.Join(in.dotDir, "runs", in.runID)
	manifestPath := filepath.Join(runDir, manifestBaseFilename+".gz")

	// 1. Load manifest with HMAC verification. invariant #33 / AC-19:
	// IntegrityErrors here populate FilesIntegrityFailed; schema errors
	// downgrade the run to integrity_failed (a structurally malformed
	// manifest is not "ok" even if no per-file hash failed).
	loadResult, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    manifestPath,
		VersionFilePath: in.versionPath,
	})
	if err != nil {
		// Pipeline error: surface to the caller. Note that load returns
		// pipeline errors for things the verify command cannot recover
		// from (missing manifest, gzip corruption, unsupported schema).
		return nil, fmt.Errorf("load: %w", err)
	}

	// 2. Rehash dest files. Per-file failures land in counters; the only
	// returned error is a pipeline-level cancellation (ctx) or DestRoot
	// validation error.
	rehashResult, err := rehash.Rehash(ctx, rehash.Options{
		Entries:    loadResult.Entries,
		DestRoot:   in.destRoot,
		Hostname:   in.hostname,
		Username:   in.username,
		UIRenderer: in.renderer,
	})
	if err != nil {
		// Mid-loop cancellation: rehash returns the partial Result
		// alongside the error. We still want to write a summary record
		// for forensic purposes so a partial verify is observable on
		// disk. Aggregator semantics: a cancellation does NOT make the
		// run "ok"; counters reflect work done, ExitStatus stays at the
		// worst case actually observed.
		if rehashResult == nil {
			return nil, fmt.Errorf("rehash: %w", err)
		}
		// fall through with partial rehashResult; surface the wrapped
		// error to the caller after summary write.
	}

	// 3. Optional CheckExtras walk. Counts dest-side files that are NOT
	// in the manifest. Just a count: invariant #6 says we never touch
	// user-added files, so a non-zero extras count is informational, not
	// an integrity failure.
	var extras int
	if in.checkExtras {
		extras, err = countExtras(ctx, in.destRoot, in.hostname, in.username, loadResult.Entries)
		if err != nil {
			// A CheckExtras walk failure is non-fatal: the verify can
			// still report on the manifest-side outcome. The count
			// stays at 0 (the false-positive direction; we'd rather
			// under-report extras than fail the whole verify).
			extras = 0
		}
	}

	// 4. Aggregate this run's counters into a VerifyResult.
	runResult := &VerifyResult{
		RunID:                in.runID,
		FilesChecked:         rehashResult.FilesChecked,
		FilesVerified:        rehashResult.FilesVerified,
		FilesHashMismatch:    rehashResult.FilesHashMismatch,
		FilesIntegrityFailed: len(loadResult.IntegrityErrors),
		FilesMissing:         rehashResult.FilesMissing,
		FilesSizeMismatch:    rehashResult.FilesSizeMismatch,
		FilesUnreadable:      rehashResult.FilesUnreadable,
		FilesExtraInDest:     extras,
		BytesRead:            rehashResult.BytesRead,
		DurationSeconds:      int(time.Since(runStartedAt).Seconds()),
		ExitStatus:           classifyExitStatus(rehashResult, loadResult),
	}

	// 5. Write summary.json. Pipeline error here is FATAL for the run:
	// the verify produced numbers but cannot land them on disk, so the
	// `flashbackup status` "last_verify" lookup will not find them.
	// Returning an error here intentionally degrades the aggregate path
	// (the All-mode loop will treat this run as integrity_failed) so
	// the operator notices.
	verifyID := newVerifyID(time.Now().UTC())
	if writeErr := writeSummary(in.dotDir, in.runID, verifyID, runResult); writeErr != nil {
		return runResult, fmt.Errorf("write summary: %w", writeErr)
	}

	// 6. Surface the deferred rehash cancellation error (if any) AFTER
	// the summary record is on disk. The partial counters are still
	// useful evidence even when the verify did not finish.
	if err != nil {
		return runResult, fmt.Errorf("rehash: %w", err)
	}

	return runResult, nil
}

// classifyExitStatus returns the per-run ExitStatus from the load + rehash
// outputs. Any per-file failure OR any load-side schema error degrades the
// run from "ok" to "integrity_failed". The intent matches the design spec
// section 5 exit code table: 0 only when every file is fully verified and
// the manifest itself is structurally clean.
func classifyExitStatus(rr *rehash.Result, lr *load.LoadResult) string {
	if rr == nil || lr == nil {
		return ExitStatusIntegrityFailed
	}
	if len(lr.IntegrityErrors) > 0 ||
		len(lr.SchemaErrors) > 0 ||
		rr.FilesHashMismatch > 0 ||
		rr.FilesSizeMismatch > 0 ||
		rr.FilesMissing > 0 ||
		rr.FilesUnreadable > 0 {
		return ExitStatusIntegrityFailed
	}
	return ExitStatusOK
}

// resolveRunIDs returns the slice of RunIDs to verify based on opts. Cases:
//
//   - opts.All: every runIDPattern-matching dir under <DotDir>/runs/,
//     sorted lexically (== chronologically).
//   - opts.RunID != "": that exact RunID; rejects with a typed error when
//     the dir does not exist (silent skip would let a typo verify
//     nothing).
//   - opts.RunID == "": the latest run by lexical sort.
//
// Returns a typed error when no runs are present (an empty USB is not
// "verified"; it has nothing to verify).
func resolveRunIDs(dotDir string, opts VerifyOptions) ([]string, error) {
	runsDir := filepath.Join(dotDir, "runs")

	if opts.RunID != "" {
		// Reject malformed RunIDs at the entrypoint so the caller does
		// not have to round-trip through filepath.Stat to discover an
		// obvious typo. Matches the runIDPattern guard inside
		// runner.pruneOldRunDirs (defensive against an operator who
		// passes a wrong-shape value).
		if !runIDPattern.MatchString(opts.RunID) {
			return nil, fmt.Errorf("RunID %q does not match canonical pattern", opts.RunID)
		}
		runDir := filepath.Join(runsDir, opts.RunID)
		info, err := os.Stat(runDir)
		if err != nil {
			return nil, fmt.Errorf("stat run dir %q: %w", runDir, err)
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("run dir %q is not a directory", runDir)
		}
		return []string{opts.RunID}, nil
	}

	// Enumerate. Both All and "latest" share this step.
	all, err := listRunIDs(runsDir)
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("no runs found on this USB")
	}

	if opts.All {
		return all, nil
	}
	// Latest = newest by lexical sort. listRunIDs sorts ascending; pick
	// the tail.
	return []string{all[len(all)-1]}, nil
}

// listRunIDs returns the sorted (ascending) slice of canonical RunID names
// found under runsDir. Skips non-dir entries and non-canonical names
// (defensive against arbitrary operator-added content).
func listRunIDs(runsDir string) ([]string, error) {
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			// An empty USB (no runs dir yet) is not an error per se; the
			// caller (resolveRunIDs) translates a zero-length result
			// into a typed "no runs" error.
			return nil, nil
		}
		return nil, fmt.Errorf("read runs dir %q: %w", runsDir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !runIDPattern.MatchString(e.Name()) {
			continue
		}
		out = append(out, e.Name())
	}
	sort.Strings(out)
	return out, nil
}

// aggregatedRunID returns the RunID field that lands on the returned
// VerifyResult. For single-run cases (explicit RunID or "latest"), this is
// the one RunID actually verified. For All=true, this is the
// allRunIDSentinel ("all") so the caller can distinguish a batch
// aggregate from a single-run result.
func aggregatedRunID(opts VerifyOptions, runIDs []string) string {
	if opts.All {
		return allRunIDSentinel
	}
	if len(runIDs) == 0 {
		return ""
	}
	return runIDs[0]
}

// newVerifyID returns a fresh VerifyID in the canonical run-id format
// ("YYYY-MM-DDTHHMMZ-XXXX") so the per-verify dir name is grep-compatible
// with run dirs and chronologically sortable. The 4-hex suffix matches
// runner.newRunID.
func newVerifyID(at time.Time) string {
	return at.Format("2006-01-02T1504Z") + "-" + randomHex4()
}

// randomHex4 returns a 4-hex-char nonce. Crypto-rand is appropriate here:
// VerifyID collisions would clobber an earlier verify's summary.json on a
// busy USB. Test seam not needed (the format is stable; collision would
// surface as a directory-exists error on summary write).
func randomHex4() string {
	// Match runner.newRunID's pattern for consistency. crypto/rand is
	// the standard library entropy source on macOS/Linux; if it fails
	// we degrade to "0000" (loud sentinel in the dir name).
	var nonce [2]byte
	if _, err := readRand(nonce[:]); err != nil {
		return "0000"
	}
	const hex = "0123456789abcdef"
	return string([]byte{
		hex[nonce[0]>>4], hex[nonce[0]&0x0f],
		hex[nonce[1]>>4], hex[nonce[1]&0x0f],
	})
}

// readRand is a tiny seam over crypto/rand.Read so tests could substitute
// a deterministic source. Currently always calls the standard library;
// kept as a package-private function so the substitution (if ever needed)
// touches one place. Production callers MUST NOT override.
var readRand = rand.Read

// writeSummary atomically writes the per-verify summary.json at the
// canonical path:
//
//	<DotDir>/runs/<runID>/verifications/<verifyID>/summary.json
//
// Uses state.WriteTmpThenRename for the atomic-write semantics (invariant #4:
// no partial files on disk). File mode 0o644 (no secrets; reachable by
// support tooling without sudo, matching the deletion-log convention from
// design spec section 4).
func writeSummary(dotDir, runID, verifyID string, r *VerifyResult) error {
	verifyDir := filepath.Join(dotDir, "runs", runID, "verifications", verifyID)
	if err := os.MkdirAll(verifyDir, 0o700); err != nil {
		return fmt.Errorf("mkdir verify dir: %w", err)
	}
	rec := summaryRecord{
		V:                    summarySchemaVersion,
		VerifyID:             verifyID,
		ForRunID:             runID,
		VerifiedAt:           time.Now().UTC(),
		DurationSeconds:      r.DurationSeconds,
		FilesChecked:         r.FilesChecked,
		FilesVerified:        r.FilesVerified,
		FilesHashMismatch:    r.FilesHashMismatch,
		FilesIntegrityFailed: r.FilesIntegrityFailed,
		FilesMissing:         r.FilesMissing,
		FilesSizeMismatch:    r.FilesSizeMismatch,
		FilesUnreadable:      r.FilesUnreadable,
		FilesExtraInDest:     r.FilesExtraInDest,
		BytesRead:            r.BytesRead,
		ExitStatus:           r.ExitStatus,
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal summary: %w", err)
	}
	summaryPath := filepath.Join(verifyDir, summaryFilename)
	if err := state.WriteTmpThenRename(summaryPath, data, 0o644); err != nil {
		return fmt.Errorf("write summary file: %w", err)
	}
	return nil
}

// countExtras walks the namespaced dest dir and counts files that are NOT
// present in entries. Implementation walks lazily and short-circuits ctx
// every 256 files (matches the load + t1 enumerate cadence) so a giant
// dest tree does not block cancellation.
//
// Symlinks and directories are not counted. Only regular files. A future
// extension could surface the slice of names; today the count is enough
// for the CheckExtras counter contract.
func countExtras(ctx context.Context, destRoot, hostname, username string, entries []state.ManifestEntry) (int, error) {
	known := make(map[string]struct{}, len(entries))
	for _, e := range entries {
		// Manifest paths are slash-separated; convert to OS-native so the
		// comparison against filepath.Walk output (which is OS-native)
		// matches exactly.
		known[filepath.FromSlash(e.Path)] = struct{}{}
	}
	root := filepath.Join(destRoot, paths.Prefix(hostname, username))

	count := 0
	walked := 0
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			// A read error on the namespaced dest root means the backup
			// never ran on this USB; skip the whole walk by returning
			// the error to halt the recursion (the caller treats walk
			// errors as soft-fail per the verifyOneRun comment).
			return walkErr
		}
		walked++
		if walked%256 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			// Symlinks under the namespace are not manifest entries
			// (the manifest only stores regular files). Don't count.
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		if _, ok := known[rel]; !ok {
			count++
		}
		return nil
	})
	if err != nil {
		return count, fmt.Errorf("walk extras: %w", err)
	}
	return count, nil
}

// emitSummary fans the terminal UIEvtSummary event to the renderer. Nil
// renderer is a no-op; renderer errors are swallowed per PS3. Mirrors the
// runner.emitSummary helper one-for-one so a future Renderer extension
// (e.g. progress totals) lands in both call sites.
func emitSummary(ctx context.Context, r types.Renderer, res *VerifyResult) {
	if r == nil {
		return
	}
	_ = r.OnEvent(ctx, types.UIEvent{
		Kind:      types.UIEvtSummary,
		Status:    res.ExitStatus,
		Timestamp: time.Now().UTC(),
	})
}
