package runner

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Tests reuse the helpers defined in t0_preflight_test.go (same package):
//   - canonicalRunID, captureRenderer, readNDJSON, eventKinds
//   - requireMacOS, requireDiskutil, mountTempVolume, setupDest
//   - seedTree (from t1_enumerate_test.go), seedCandidates (t2_transfer_test.go)
//
// The end-to-end tests (TestRun_HappyPathCopy, TestRun_HappyPathMove) use the
// system rsync because the embedded payload in internal/rsync is a non-copying
// placeholder; substituting via rsyncPathOverrideForTest is the documented
// test seam. These tests are gated on FLASHBACKUP_E2E=1 because they require
// hdiutil (sandbox-restricted CI environments cannot create DMGs).

const systemRsyncPath = "/usr/bin/rsync"

// requireE2E skips the test unless FLASHBACKUP_E2E=1 is in the environment.
// Matches the Makefile gate for e2e-fast / e2e-safety targets; lets `go test
// ./...` stay fast and hermetic while still allowing local + CI runs to
// exercise the mounted-DMG path.
func requireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("FLASHBACKUP_E2E") != "1" {
		t.Skip("requires FLASHBACKUP_E2E=1 (mounts a DMG, runs real rsync)")
	}
}

// requireSystemRsync skips the test if /usr/bin/rsync is missing. Belt-and-
// suspenders against future macOS releases that move or remove the binary.
// Also skips when the binary is Apple's openrsync (`rsync version 2.6.9
// compatible`), which does not support --from0 / --xattrs and so cannot
// substitute for the embedded GNU rsync 3.x. GNU rsync from Homebrew lives
// at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; if neither is present
// the test reports the limitation and skips.
func requireSystemRsync(t *testing.T) string {
	t.Helper()
	candidates := []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync", systemRsyncPath}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(out), "openrsync") {
			// Apple's openrsync claims rsync-2.6.9 compatibility but
			// lacks the modern flags FlashBackup needs. Skip rather
			// than report a spurious test failure.
			continue
		}
		return p
	}
	t.Skip("no GNU rsync available (Apple openrsync at /usr/bin/rsync lacks --from0 / --xattrs; install via `brew install rsync`)")
	return ""
}

// withRsyncPathOverride sets rsyncPathOverrideForTest and registers cleanup.
// Used by the end-to-end tests to substitute the system rsync for the
// non-copying embedded placeholder.
func withRsyncPathOverride(t *testing.T, path string) {
	t.Helper()
	prev := rsyncPathOverrideForTest
	rsyncPathOverrideForTest = path
	t.Cleanup(func() { rsyncPathOverrideForTest = prev })
}

// runIDFormatRE matches the canonical RunID produced by newRunID. Mirrors
// runIDPattern in t5_finalize.go (and the spec section 5 format).
var runIDFormatRE = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}Z-[0-9a-f]{4}$`)

// ----- unit tests (no mount required) -----

func TestNewRunID_MatchesCanonicalFormat(t *testing.T) {
	at := time.Date(2026, 6, 4, 9, 5, 0, 0, time.UTC)
	id := newRunID(at)
	if !runIDFormatRE.MatchString(id) {
		t.Errorf("newRunID = %q; want match %s", id, runIDFormatRE)
	}
	if !strings.HasPrefix(id, "2026-06-04T0905Z-") {
		t.Errorf("newRunID = %q; want prefix 2026-06-04T0905Z-", id)
	}
}

func TestNewRunID_TwoCallsDiffer(t *testing.T) {
	at := time.Now().UTC()
	a := newRunID(at)
	b := newRunID(at)
	if a == b {
		t.Errorf("newRunID returned identical IDs %q and %q for same timestamp; suffix must be random", a, b)
	}
}

func TestAssertSignaturesCoverCandidates_OK(t *testing.T) {
	cands := []selection.Candidate{
		{RelativePath: "a.txt"},
		{RelativePath: "b.txt"},
	}
	sigs := map[string]types.Signature{
		"a.txt": {Size: 1, MtimeNS: 1},
		"b.txt": {Size: 2, MtimeNS: 2},
	}
	assertSignaturesCoverCandidates(cands, sigs)
}

func TestAssertSignaturesCoverCandidates_PanicsOnMissing(t *testing.T) {
	cands := []selection.Candidate{
		{RelativePath: "a.txt"},
		{RelativePath: "b.txt"},
	}
	sigs := map[string]types.Signature{
		"a.txt": {Size: 1, MtimeNS: 1},
	}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic on missing signature")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "T2 precondition") {
			t.Errorf("panic message %q does not mention T2 precondition", r)
		}
	}()
	assertSignaturesCoverCandidates(cands, sigs)
}

func TestAssertVerifiedSubsetCovered_OK(t *testing.T) {
	cands := []selection.Candidate{
		{RelativePath: "a.txt"},
		{RelativePath: "b.txt"},
	}
	sigs := map[string]types.Signature{
		"a.txt": {Size: 1, MtimeNS: 1},
		// b.txt intentionally missing; b is not verified so the helper
		// should NOT panic.
	}
	statuses := map[string]state.FileStatus{
		"a.txt": state.StatusVerified,
		"b.txt": state.StatusHashMismatch,
	}
	assertVerifiedSubsetCovered(cands, sigs, statuses)
}

func TestAssertVerifiedSubsetCovered_PanicsOnVerifiedMissing(t *testing.T) {
	cands := []selection.Candidate{{RelativePath: "a.txt"}}
	sigs := map[string]types.Signature{}
	statuses := map[string]state.FileStatus{"a.txt": state.StatusVerified}
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected panic when verified file lacks signature")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "T3 precondition") {
			t.Errorf("panic message %q does not mention T3 precondition", r)
		}
	}()
	assertVerifiedSubsetCovered(cands, sigs, statuses)
}

// ----- runner.Run input-validation tests (no mount needed) -----

func TestRun_EmptyDestRoot(t *testing.T) {
	_, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "x", Source: "/tmp"},
		DestRoot: "",
		Mode:     types.ModeCopy,
	})
	if err == nil || !strings.Contains(err.Error(), "DestRoot is empty") {
		t.Errorf("expected DestRoot empty error; got %v", err)
	}
}

func TestRun_EmptyProfileSource(t *testing.T) {
	_, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "x"},
		DestRoot: "/tmp",
		Mode:     types.ModeCopy,
	})
	if err == nil || !strings.Contains(err.Error(), "Profile.Source is empty") {
		t.Errorf("expected Profile.Source empty error; got %v", err)
	}
}

func TestRun_SourceRootDoesNotExist(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	_, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "x", Source: missing},
		DestRoot: t.TempDir(),
		Mode:     types.ModeCopy,
	})
	if err == nil {
		t.Fatal("expected error on missing source")
	}
	// EvalSymlinks on a non-existent path returns an *PathError; we wrap it
	// with "eval source symlinks".
	if !strings.Contains(err.Error(), "eval source symlinks") {
		t.Errorf("error %q does not mention eval source symlinks", err)
	}
}

// TestRun_PreT0MkdirFailure exercises the emitPreflightFailedSummary path:
// when DestRoot points at a regular file (not a directory), os.MkdirAll on
// <DestRoot>/.flashbackup/runs/<RunID>/ fails before T0 ever runs. The
// orchestrator must still emit a UIEvtSummary with ExitStatus=preflight_failed.
func TestRun_PreT0MkdirFailure(t *testing.T) {
	// Create a regular file and use it as DestRoot; mkdir-p underneath
	// will fail because a path component is not a directory.
	tmpFile := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("y"), 0o600); err != nil {
		t.Fatal(err)
	}
	rend := &captureRenderer{}

	res, err := Run(context.Background(), types.RunOptions{
		Profile:    profiles.Profile{V: 1, Name: "p", Source: src},
		DestRoot:   tmpFile,
		Mode:       types.ModeCopy,
		UIRenderer: rend,
	})
	if err == nil {
		t.Fatal("expected mkdir failure")
	}
	if res == nil {
		t.Fatal("expected non-nil RunResult on early-abort path")
	}
	if res.ExitStatus != types.ExitStatusPreflightFailed {
		t.Errorf("ExitStatus = %q; want preflight_failed", res.ExitStatus)
	}
	// Summary must have been emitted with the same status.
	sawSummary := false
	for _, ev := range rend.seen() {
		if ev.Kind == types.UIEvtSummary {
			sawSummary = true
			if ev.Status != types.ExitStatusPreflightFailed {
				t.Errorf("summary.Status = %q; want preflight_failed", ev.Status)
			}
		}
	}
	if !sawSummary {
		t.Error("renderer did not see UIEvtSummary on pre-T0 mkdir failure")
	}
}

// TestRun_PreflightFailsBadDest exercises the orchestrator's preflight-
// failed exit-status path WITHOUT requiring a mounted DMG. The DestRoot
// exists (it's a tmp dir) but it lacks version.json, so preflight gate 8
// fails closed per invariant #11; the runner translates that into
// ExitStatus=preflight_failed and a captured UIEvtSummary.
func TestRun_PreflightFailsMissingVersionJSON(t *testing.T) {
	dest := t.TempDir()
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "x.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	rend := &captureRenderer{}

	res, err := Run(context.Background(), types.RunOptions{
		Profile:    profiles.Profile{V: 1, Name: "p", Source: src},
		DestRoot:   dest,
		Mode:       types.ModeCopy,
		UIRenderer: rend,
	})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if res == nil {
		t.Fatal("expected non-nil RunResult even on preflight failure")
	}
	if res.ExitStatus != types.ExitStatusPreflightFailed {
		t.Errorf("ExitStatus = %q; want preflight_failed", res.ExitStatus)
	}
	if res.RunID == "" || !runIDFormatRE.MatchString(res.RunID) {
		t.Errorf("RunID %q does not match canonical format", res.RunID)
	}

	// Renderer must have seen a final UIEvtSummary even on the early-abort
	// path. This is the only signal a UI gets when preflight fails before
	// any other phase runs.
	seenSummary := false
	for _, ev := range rend.seen() {
		if ev.Kind == types.UIEvtSummary {
			seenSummary = true
			if ev.Status != types.ExitStatusPreflightFailed {
				t.Errorf("summary Status = %q; want preflight_failed", ev.Status)
			}
		}
	}
	if !seenSummary {
		t.Error("renderer did not receive a UIEvtSummary on preflight failure")
	}
}

// ----- end-to-end tests (FLASHBACKUP_E2E=1 + mounted DMG + system rsync) -----

// seedSourceTree creates a small file tree at src; returns the file
// relative paths and their sizes for assertion. Sizes are non-zero so
// BytesTotal is meaningful.
func seedSourceTree(t *testing.T, src string) []string {
	t.Helper()
	files := map[string]string{
		"a.txt":          "alpha content",
		"sub/b.md":       "bravo content longer line",
		"sub/deep/c.txt": "charlie",
	}
	for rel, content := range files {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	return rels
}

func TestRun_HappyPathCopy(t *testing.T) {
	requireE2E(t)
	requireMacOS(t)
	requireDiskutil(t)

	rsyncPath := requireSystemRsync(t)
	dest := setupDest(t)
	withRsyncPathOverride(t, rsyncPath)

	src := t.TempDir()
	rels := seedSourceTree(t, src)

	rend := &captureRenderer{}
	res, err := Run(context.Background(), types.RunOptions{
		Profile:    profiles.Profile{V: 1, Name: "happy", Source: src},
		DestRoot:   dest,
		Mode:       types.ModeCopy,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if res.ExitStatus != types.ExitStatusOK {
		t.Errorf("ExitStatus = %q; want ok", res.ExitStatus)
	}
	if res.FilesTotal != len(rels) {
		t.Errorf("FilesTotal = %d; want %d", res.FilesTotal, len(rels))
	}
	if res.FilesSucceeded != len(rels) {
		t.Errorf("FilesSucceeded = %d; want %d", res.FilesSucceeded, len(rels))
	}
	if res.FilesFailed != 0 {
		t.Errorf("FilesFailed = %d; want 0", res.FilesFailed)
	}
	if res.BytesTotal == 0 {
		t.Errorf("BytesTotal = 0; want > 0")
	}

	// Manifest.gz must exist at the canonical path.
	dotDir := filepath.Join(dest, ".flashbackup")
	manifestGz := filepath.Join(dotDir, "runs", res.RunID, manifestBaseFilename+".gz")
	if _, err := os.Stat(manifestGz); err != nil {
		t.Errorf("manifest.gz missing at %s: %v", manifestGz, err)
	}

	// Source still has all files (copy mode is non-destructive).
	for _, rel := range rels {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			t.Errorf("source file %q missing after copy: %v", rel, err)
		}
	}

	// runs.ndjson has exactly two lines (started + finished) for this run.
	runs := readNDJSON(t, filepath.Join(dotDir, "runs.ndjson"))
	if len(runs) != 2 {
		t.Fatalf("runs.ndjson lines = %d; want 2 (started + finished)", len(runs))
	}
	if runs[0]["event"] != "started" || runs[1]["event"] != "finished" {
		t.Errorf("runs.ndjson event order wrong: %v", runs)
	}
	if runs[1]["exit_status"] != types.ExitStatusOK {
		t.Errorf("finished.exit_status = %v; want ok", runs[1]["exit_status"])
	}

	// Renderer saw the per-phase events plus the final summary. We assert
	// the boundary signals (phase_started + phase_completed appear N times
	// and summary appears exactly once); per-event field shapes are tested
	// by the per-phase tests.
	uiEvents := rend.seen()
	var started, completed, summary int
	for _, ev := range uiEvents {
		switch ev.Kind {
		case types.UIEvtPhaseStarted:
			started++
		case types.UIEvtPhaseCompleted:
			completed++
		case types.UIEvtSummary:
			summary++
		}
	}
	// 5 phases run (T0, T0+, T1, T2, T4 in copy mode; T3 also runs but in
	// the copy-mode skipped branch which emits phase_started + phase_completed).
	wantPhases := 6
	if started != wantPhases || completed != wantPhases {
		t.Errorf("renderer phase events: started=%d completed=%d; want %d each", started, completed, wantPhases)
	}
	if summary != 1 {
		t.Errorf("renderer saw %d UIEvtSummary; want 1", summary)
	}
}

func TestRun_HappyPathMove(t *testing.T) {
	requireE2E(t)
	requireMacOS(t)
	requireDiskutil(t)

	rsyncPath := requireSystemRsync(t)
	dest := setupDest(t)
	withRsyncPathOverride(t, rsyncPath)

	src := t.TempDir()
	rels := seedSourceTree(t, src)

	res, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "movey", Source: src},
		DestRoot: dest,
		Mode:     types.ModeMove,
	})
	if err != nil {
		t.Fatalf("Run move: %v", err)
	}
	if res.ExitStatus != types.ExitStatusOK {
		t.Errorf("ExitStatus = %q; want ok", res.ExitStatus)
	}
	if res.FilesSucceeded != len(rels) {
		t.Errorf("FilesSucceeded = %d; want %d", res.FilesSucceeded, len(rels))
	}

	// Source files must be unlinked. The directories MAY remain (the spec
	// leaves empty dirs in place); only files are checked.
	for _, rel := range rels {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err == nil {
			t.Errorf("source file %q still exists after move", rel)
		}
	}

	// Dest must have the files in the namespaced subdir. Use exec.Command
	// to find files (a simpler walk would work too; this one matches the
	// test's natural language).
	hostname, _ := os.Hostname()
	uname, _ := exec.Command("/usr/bin/whoami").Output()
	usernameStr := strings.TrimSpace(string(uname))
	for _, rel := range rels {
		destFile := filepath.Join(dest, hostname+"-"+usernameStr, filepath.FromSlash(rel))
		if _, err := os.Stat(destFile); err != nil {
			t.Errorf("dest file %q missing after move: %v", destFile, err)
		}
	}
}

// TestRun_RendererErrorIsNonFatal mirrors the per-phase tests: a renderer
// that returns an error on every OnEvent must not abort the run. This
// guards against a future change in any phase that promotes renderer
// errors to fatal.
func TestRun_RendererErrorIsNonFatal(t *testing.T) {
	dest := t.TempDir() // no version.json => preflight will fail
	src := t.TempDir()
	if err := os.WriteFile(filepath.Join(src, "a.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	rend := &captureRenderer{err: errExampleRenderer}

	res, err := Run(context.Background(), types.RunOptions{
		Profile:    profiles.Profile{V: 1, Name: "p", Source: src},
		DestRoot:   dest,
		Mode:       types.ModeCopy,
		UIRenderer: rend,
	})
	// Preflight still fails (no version.json), but the renderer error
	// must NOT mask that error: the original preflight error surfaces.
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if res.ExitStatus != types.ExitStatusPreflightFailed {
		t.Errorf("ExitStatus = %q; want preflight_failed", res.ExitStatus)
	}
}

// errExampleRenderer is a package-level sentinel so the test's captureRenderer
// can return a fresh error per OnEvent call without allocating a closure-
// local sentinel inline.
var errExampleRenderer = &exampleErr{msg: "renderer broken"}

type exampleErr struct{ msg string }

func (e *exampleErr) Error() string { return e.msg }
