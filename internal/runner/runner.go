package runner

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// flashbackupVersion is the build's version string written into the
// runs.ndjson started/finished lines for forensic correlation. Until cmd/main
// wires a -X ldflag in Task 34, this remains a compile-time constant; the
// constant is reassigned via a build-time -X flag in release pipelines.
//
// Reassigned (not const) so cmd/main and release builds can override via the
// linker without rewriting the source.
var flashbackupVersion = "0.1.0-core"

// rsyncPathOverrideForTest, when non-empty, replaces pc.RsyncPath after T0
// preflight returns. Test-only seam (matches the deletionLogTestHook pattern
// in t4_delete_source.go) so the runner's end-to-end happy-path tests can
// substitute the system rsync for the embedded placeholder; the placeholder
// is a non-copying shell stub that defeats meaningful end-to-end assertions.
//
// Production callers MUST leave this empty. The variable is package-private
// and never reassigned outside *_test.go files.
var rsyncPathOverrideForTest string

// rsyncPathEnvOverride is the name of the environment variable that, when
// set to a non-empty absolute path, replaces pc.RsyncPath at the same seam
// as rsyncPathOverrideForTest. Test-only escape hatch for external test
// packages (cmd/flashbackup, test/e2e) that cannot reach the package-private
// rsyncPathOverrideForTest var; the runner's own tests prefer the in-process
// var because it composes with t.Cleanup more naturally. Production callers
// MUST leave this unset; FlashBackup itself never reads any other env var
// that affects run behaviour (the runner's behaviour is determined by
// RunOptions + the on-disk state, not by ambient env).
//
// Documented here so a future audit reading runner.go can see that the
// env-var path exists; the actual read sits next to the in-process var
// below.
const rsyncPathEnvOverride = "FLASHBACKUP_RSYNC_PATH_FOR_TEST"

// Run is the top-level FlashBackup state machine. It stitches the six phase
// functions (T0 preflight, T0+ enumerate, T1 transfer, T2 hash+compare,
// T3 delete-source, T4 finalize) into one orchestrated invocation:
//
//  1. validate opts, resolve source root, generate RunID
//  2. install SIGINT/SIGTERM handler via signal.NotifyContext
//  3. mkdir per-run dir under <DestRoot>/.flashbackup/runs/<RunID>/
//  4. open EventStore + RunLogStore
//  5. run T0; on success: defer pc.Release(); open ManifestStore (HMAC key
//     comes from pc.VersionFile)
//  6. for every subsequent phase: pc.VerifyVolumeUnchanged(ctx) first
//  7. T0+ enumerate, T1 transfer, T2 hash+compare
//  8. move mode + atomic gate open => T3 delete-source; otherwise skip
//  9. resolve ExitStatus per the table below; T4 finalize
//  10. emit final UIEvtSummary; return RunResult.
//
// ExitStatus resolution table:
//
//	preflight_failed         T0 returned an error
//	copy_only_aborted_delete move mode AND T2 atomic gate did not pass
//	partial                  FilesFailed > 0 AND FilesSucceeded > 0
//	ok                       all files verified; in move mode also all
//	                         eligible unlinks succeeded
//
// crashed_resumed is NOT set here; the NEXT run's preflight discovers
// orphaned run-dirs and sets it during recovery (out of scope for Task 29).
//
// Cancellation: ctx is wrapped via signal.NotifyContext so SIGINT/SIGTERM
// translates to ctx cancellation, which propagates through every phase's
// own ctx.Err() checks. Second-signal-within-5s escalation will be added
// by cmd/main (Task 34); v0.1 the runner accepts the first cancel and
// lets each phase run its own abort cleanup.
//
// The PreflightContext lock is released via defer; T0 is the only phase
// that acquires it. Stores opened by Run are closed via defer.
func Run(ctx context.Context, opts types.RunOptions) (*types.RunResult, error) {
	// 1. Input validation. RunOptions does not currently expose a Source
	// field of its own; the source root lives on opts.Profile.Source.
	if opts.DestRoot == "" {
		return nil, fmt.Errorf("runner Run: DestRoot is empty")
	}
	if opts.Profile.Source == "" {
		return nil, fmt.Errorf("runner Run: Profile.Source is empty")
	}

	// Resolve source root: absolute + symlink-free. Symlinks in the source
	// path are out of scope for v0.1 (selection.Walk refuses to follow
	// them); rejecting at the orchestrator avoids downstream surprises.
	sourceRoot, err := filepath.Abs(opts.Profile.Source)
	if err != nil {
		return nil, fmt.Errorf("runner Run: resolve source root: %w", err)
	}
	sourceRoot, err = filepath.EvalSymlinks(sourceRoot)
	if err != nil {
		return nil, fmt.Errorf("runner Run: eval source symlinks: %w", err)
	}

	// 2. Signal handler. SIGINT (Ctrl-C) + SIGTERM (kill) cancel ctx;
	// every phase honours ctx.Err() at its boundaries. Task 34 will add
	// second-signal-within-5s escalation at cmd/main.
	ctx, cancel := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	startedAt := time.Now().UTC()

	result := &types.RunResult{
		RunID:     newRunID(startedAt),
		StartedAt: startedAt,
	}

	// 3. Pre-T0 dest dir prep. T0 will re-validate filesystem, lock, and
	// volume UUID; here we only need the per-run dir to exist so the audit
	// stores can open. mkdir is idempotent on the existing-dir branch.
	destAbs, err := filepath.Abs(opts.DestRoot)
	if err != nil {
		return result, emitPreflightFailedSummary(ctx, opts.UIRenderer, result, fmt.Errorf("runner Run: abs dest: %w", err))
	}
	dotDir := filepath.Join(destAbs, ".flashbackup")
	runDir := filepath.Join(dotDir, "runs", result.RunID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		return result, emitPreflightFailedSummary(ctx, opts.UIRenderer, result, fmt.Errorf("runner Run: mkdir run dir: %w", err))
	}
	// RunDir is the on-disk absolute path of <DotDir>/runs/<RunID>. Carried
	// on RunResult so the renderer's UIEvtSummary can substitute it into
	// the "where" line of the summary block (design spec section 6 full-
	// path principle; Task 33 review M1).
	result.RunDir = runDir

	// 4. Open audit + run-log stores. Closed via defer regardless of return
	// path. Manifest store opens after T0 (HMAC key arrives in pc.VersionFile).
	eventsPath := filepath.Join(runDir, "events.ndjson")
	runsPath := filepath.Join(dotDir, "runs.ndjson")

	es, err := state.NewNDJSONEventStore(eventsPath)
	if err != nil {
		return result, emitPreflightFailedSummary(ctx, opts.UIRenderer, result, fmt.Errorf("runner Run: open events store: %w", err))
	}
	defer func() { _ = es.Close() }()

	rls, err := state.NewNDJSONRunLogStore(runsPath)
	if err != nil {
		return result, emitPreflightFailedSummary(ctx, opts.UIRenderer, result, fmt.Errorf("runner Run: open runlog store: %w", err))
	}
	defer func() { _ = rls.Close() }()

	// 5. T0 preflight.
	t0, err := RunT0Preflight(ctx, T0Input{
		RunID:              result.RunID,
		FlashbackupVersion: flashbackupVersion,
		DestRoot:           destAbs,
		SourceRoot:         sourceRoot,
		Mode:               opts.Mode,
		ProfileName:        opts.Profile.Name,
		EventStore:         es,
		RunLogStore:        rls,
		UIRenderer:         opts.UIRenderer,
	})
	if err != nil {
		result.ExitStatus = types.ExitStatusPreflightFailed
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}
	pc := t0.PreflightContext
	defer func() { _ = pc.Release() }()

	// Test-only RsyncPath substitution. The in-process var takes precedence
	// over the env-var path so the runner's own tests (which use t.Cleanup
	// to restore the var) are not affected by stale env state from a parent
	// process. See rsyncPathOverrideForTest + rsyncPathEnvOverride docs.
	if rsyncPathOverrideForTest != "" {
		pc.RsyncPath = rsyncPathOverrideForTest
	} else if v := os.Getenv(rsyncPathEnvOverride); v != "" {
		pc.RsyncPath = v
	}

	// Decode the HMAC key once; ReadVersionFile already validated shape so
	// hex.DecodeString cannot fail in practice, but a defensive surface
	// keeps a future schema bump (a wider key) from silently truncating.
	hmacKey, err := hex.DecodeString(pc.VersionFile.HMACKey)
	if err != nil {
		return result, fmt.Errorf("runner Run: decode hmac key: %w", err)
	}

	// 6. Open the ManifestStore against the per-run path. The store finalizes
	// in T4 (renames .tmp.gz to .gz, fsyncs parent dir). Gzip is idempotent;
	// the defer below is the safety net for paths that abort before T4.
	manifestBase := filepath.Join(runDir, manifestBaseFilename)
	ms, err := state.NewNDJSONManifestStore(manifestBase, hmacKey)
	if err != nil {
		return result, fmt.Errorf("runner Run: open manifest store: %w", err)
	}
	defer func() {
		// Idempotent close: if T4 already ran Gzip, this call returns the
		// "already finalized" branch without re-renaming. If T4 never
		// reached Gzip (aborted earlier), this flushes the gzip writer so
		// the OS does not hold a dangling FD.
		_ = ms.Gzip(context.Background())
	}()

	// Namespaced destination subdirectory per invariant #14:
	//   <DestRoot>/<hostname>-<username>/<file>
	// Use the canonical paths.Prefix helper (strips dots and other special
	// chars so hostnames like "macbook.local" do not silently diverge from
	// what status/verify compute from the same inputs). Single source of
	// truth per invariant #15.
	namespacedDest := filepath.Join(destAbs, paths.Prefix(pc.Hostname, pc.Username))
	if err := os.MkdirAll(namespacedDest, 0o700); err != nil {
		return result, fmt.Errorf("runner Run: mkdir namespaced dest: %w", err)
	}

	// 7. T0+ enumerate.
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("runner Run: pre-T0+ verify: %w", err)
	}
	t1, err := RunT1Enumerate(ctx, T1Input{
		Profile:    opts.Profile,
		SourceRoot: sourceRoot,
		EventStore: es,
		UIRenderer: opts.UIRenderer,
	})
	if err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}
	assertSignaturesCoverCandidates(t1.Candidates, t1.Signatures)

	// Aggregate bytes from the source-side signature (the dest is verified
	// against this baseline at T2). FilesTotal is populated by T2.
	for _, c := range t1.Candidates {
		result.BytesTotal += c.Size
	}

	// 8. T1 transfer.
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("runner Run: pre-T1 verify: %w", err)
	}
	t2res, err := RunT2Transfer(ctx, T2Input{
		SourceRoot: sourceRoot,
		DestRoot:   namespacedDest,
		RsyncPath:  pc.RsyncPath,
		Candidates: t1.Candidates,
		Mode:       opts.Mode,
		DotDir:     pc.DotDir,
		RunID:      result.RunID,
		EventStore: es,
		UIRenderer: opts.UIRenderer,
	})
	if err != nil {
		if t2res != nil && t2res.RsyncLogPath != "" {
			result.SupportPaths = append(result.SupportPaths, t2res.RsyncLogPath)
		}
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}
	if t2res != nil && t2res.RsyncLogPath != "" {
		result.SupportPaths = append(result.SupportPaths, t2res.RsyncLogPath)
	}

	// 9. T2 hash + compare + classify.
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("runner Run: pre-T2 verify: %w", err)
	}
	t3res, err := RunT3HashCompare(ctx, T3Input{
		SourceRoot:    sourceRoot,
		DestRoot:      namespacedDest,
		Candidates:    t1.Candidates,
		Signatures:    t1.Signatures,
		Mode:          opts.Mode,
		ManifestStore: ms,
		EventStore:    es,
		UIRenderer:    opts.UIRenderer,
	})
	if err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}
	result.FilesTotal = t3res.FilesTotal
	result.FilesSucceeded = t3res.FilesVerified
	result.FilesFailed = t3res.FilesTotal - t3res.FilesVerified

	// 10. T3 delete-source. Always invoke RunT4DeleteSource regardless of
	// gate state so the forensic atomic_gate_blocked event lands in
	// events.ndjson when the gate fires (per the canonical Event Kinds
	// table in the master plan; the phase emits gate_blocked via
	// t4FinishGateBlocked). The phase body itself short-circuits before
	// any unlink on copy mode or on a closed gate, so no source is
	// touched in either branch.
	gateOpen := t3res.FilesVerified == t3res.FilesTotal
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("runner Run: pre-T3 verify: %w", err)
	}
	assertVerifiedSubsetCovered(t1.Candidates, t1.Signatures, t3res.PerFileStatus)
	t4res, err := RunT4DeleteSource(ctx, T4Input{
		SourceRoot: sourceRoot,
		Candidates: t1.Candidates,
		Signatures: t1.Signatures,
		Mode:       opts.Mode,
		T3Result:   t3res,
		DotDir:     pc.DotDir,
		RunID:      result.RunID,
		EventStore: es,
		UIRenderer: opts.UIRenderer,
	})
	if err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}
	if t4res != nil {
		result.DeletionsSkippedDueToMutation = t4res.FilesSkippedMutated
		if t4res.DeletionLogPath != "" {
			result.SupportPaths = append(result.SupportPaths, t4res.DeletionLogPath)
		}
	}
	if opts.Mode == types.ModeMove && !gateOpen {
		// Atomic gate closed under move mode. T4 emitted atomic_gate_blocked
		// already; runner records the run-level ExitStatus that distinguishes
		// the gated path from a clean run.
		result.ExitStatus = types.ExitStatusCopyOnlyAbortedDelete
	}

	// 11. Resolve ExitStatus (when not already set by the gate-closed branch).
	if result.ExitStatus == "" {
		switch {
		case result.FilesFailed > 0 && result.FilesSucceeded > 0:
			result.ExitStatus = types.ExitStatusPartial
		case result.FilesFailed > 0:
			// Every file failed. Still partial from the user's PoV (the run
			// ran end-to-end, just produced no usable output); a distinct
			// status here would require a new ExitStatus constant.
			result.ExitStatus = types.ExitStatusPartial
		default:
			result.ExitStatus = types.ExitStatusOK
		}
	}

	// 12. T4 finalize. Always runs (even when the gate closed) so the
	// manifest is sealed and the runs.ndjson finished line lands; the
	// resolved ExitStatus is what distinguishes a clean run from a gated one.
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, fmt.Errorf("runner Run: pre-T4 verify: %w", err)
	}
	if _, err := RunT5Finalize(ctx, T5Input{
		RunID:                         result.RunID,
		FlashbackupVersion:            flashbackupVersion,
		StartedAt:                     result.StartedAt,
		SourceRoot:                    sourceRoot,
		DestRoot:                      destAbs,
		Mode:                          opts.Mode,
		ProfileName:                   opts.Profile.Name,
		ExitStatus:                    result.ExitStatus,
		DotDir:                        pc.DotDir,
		FilesTotal:                    result.FilesTotal,
		FilesSucceeded:                result.FilesSucceeded,
		FilesFailed:                   result.FilesFailed,
		BytesTotal:                    result.BytesTotal,
		DeletionsSkippedDueToMutation: result.DeletionsSkippedDueToMutation,
		SupportPaths:                  result.SupportPaths,
		ManifestStore:                 ms,
		EventStore:                    es,
		RunLogStore:                   rls,
		UIRenderer:                    opts.UIRenderer,
	}); err != nil {
		result.FinishedAt = time.Now().UTC()
		emitSummary(ctx, opts.UIRenderer, result)
		return result, err
	}

	result.FinishedAt = time.Now().UTC()
	emitSummary(ctx, opts.UIRenderer, result)
	return result, nil
}

// emitSummary fans a final UIEvtSummary at the end of every run (success or
// failure). Renderer errors are non-fatal per PS3 (matches emitUI in
// t0_preflight.go).
func emitSummary(ctx context.Context, r types.Renderer, res *types.RunResult) {
	if r == nil {
		return
	}
	// UIEvent.Path now carries the EXACT artifact file path the operator
	// should consult for forensic detail (was previously the run dir, with
	// the renderer appending "/events.ndjson"). The renderer prints
	// "details: see <ev.Path>" verbatim. Verify produces summary.json under
	// a different dir; this contract change lets each producer name its own
	// artifact instead of the renderer hardcoding "/events.ndjson". Per
	// Task 38 review I1.
	artifactPath := ""
	if res.RunDir != "" {
		artifactPath = filepath.Join(res.RunDir, "events.ndjson")
	}
	_ = r.OnEvent(ctx, types.UIEvent{
		Kind:      types.UIEvtSummary,
		Path:      artifactPath,
		Status:    res.ExitStatus,
		Timestamp: res.FinishedAt,
	})
}

// emitPreflightFailedSummary is the early-abort path for failures BEFORE T0
// ran (mkdir, abs-resolve, open-store). The audit trail does not exist yet
// (events.ndjson has no phase_started), so we cannot rely on T0's own
// preflight_failed write; the renderer summary is the only signal.
//
// Returns the wrappedErr unchanged so the caller can `return result, err`
// and keep the (*RunResult, error) shape clean. Sets the result's ExitStatus
// to preflight_failed before emitting so the renderer surfaces the same
// string the audit log would have used.
func emitPreflightFailedSummary(ctx context.Context, r types.Renderer, res *types.RunResult, wrappedErr error) error {
	res.ExitStatus = types.ExitStatusPreflightFailed
	res.FinishedAt = time.Now().UTC()
	emitSummary(ctx, r, res)
	return wrappedErr
}

// newRunID returns a fresh RunID in the canonical format
// "YYYY-MM-DDTHHMMZ-XXXX" where XXXX is 4 lowercase hex chars from
// crypto/rand. Matches runIDPattern in t5_finalize.go so retention pruning
// recognizes the dir as a managed run.
//
// startedAt is taken as a parameter (rather than the function calling
// time.Now()) so the run's reported StartedAt and the RunID's timestamp
// portion are guaranteed identical; without this they could drift by a few
// microseconds, which would not break correctness but would confuse a human
// reading the audit trail.
func newRunID(startedAt time.Time) string {
	var nonce [2]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		// crypto/rand should not fail on macOS/Linux. If it does we are in
		// a deeply broken environment; the run will still proceed with a
		// zero suffix, which surfaces loudly in the run dir name.
		return startedAt.Format("2006-01-02T1504Z") + "-0000"
	}
	return startedAt.Format("2006-01-02T1504Z") + "-" + hex.EncodeToString(nonce[:])
}

// assertSignaturesCoverCandidates panics if T1Result.Signatures is missing
// any entry that exists in T1Result.Candidates. The check is a hard
// precondition for T2 (RunT3HashCompare) per the master plan f95a624
// amendment: every Candidate must have a matching Signature for the
// mutation gate to function. A violation indicates an upstream T0+ bug,
// not user input we should handle gracefully.
//
// Kept as a panic (not a returned error) because the orchestrator
// constructs both inputs from the SAME upstream T1Result; the only way to
// reach the failing branch is a regression inside RunT1Enumerate. We want
// that regression to surface immediately and loudly during testing rather
// than be papered over by a soft error path that downstream consumers
// might handle inconsistently.
func assertSignaturesCoverCandidates(cands []selection.Candidate, sigs map[string]types.Signature) {
	if len(sigs) < len(cands) {
		panic(fmt.Sprintf("runner Run: T2 precondition: signatures (%d) < candidates (%d); see master plan f95a624 amendment",
			len(sigs), len(cands)))
	}
	for _, c := range cands {
		if _, ok := sigs[c.RelativePath]; !ok {
			panic(fmt.Sprintf("runner Run: T2 precondition: missing signature for candidate %q; see master plan f95a624 amendment", c.RelativePath))
		}
	}
}

// assertVerifiedSubsetCovered panics if any RelativePath classified as
// StatusVerified by T2 is missing from Signatures. Mirrors the T2
// precondition (assertSignaturesCoverCandidates) but only over the
// verified subset: T3 unlink only touches verified files, so the gate
// only needs baselines for that subset. Hard precondition per master
// plan f95a624 amendment.
func assertVerifiedSubsetCovered(cands []selection.Candidate, sigs map[string]types.Signature, statuses map[string]state.FileStatus) {
	for _, c := range cands {
		if statuses[c.RelativePath] != state.StatusVerified {
			continue
		}
		if _, ok := sigs[c.RelativePath]; !ok {
			panic(fmt.Sprintf("runner Run: T3 precondition: missing signature for verified candidate %q; see master plan f95a624 amendment", c.RelativePath))
		}
	}
}
