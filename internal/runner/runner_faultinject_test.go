//go:build faultinject

package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// TestRun_AtomicGateClosesUnderCorrupt exercises the atomic-gate path: the
// corrupt fault flips one byte of one dest file between T1 rsync and T2
// hash-compare, T2 classifies that file as hash_mismatch, the orchestrator
// observes FilesVerified < FilesTotal and sets ExitStatus to
// copy_only_aborted_delete WITHOUT calling RunT4DeleteSource. We assert
// source is untouched and the manifest is finalized.
//
// Requires FLASHBACKUP_E2E=1 (mounts a DMG) and a GNU rsync (Apple's
// openrsync is incompatible).
func TestRun_AtomicGateClosesUnderCorrupt(t *testing.T) {
	requireE2E(t)
	requireMacOS(t)
	requireDiskutil(t)

	rsyncPath := requireSystemRsync(t)
	dest := setupDest(t)
	withRsyncPathOverride(t, rsyncPath)

	src := t.TempDir()
	rels := seedSourceTree(t, src)

	// Arm a corrupt fault on the first file. The fault fires after rsync
	// finishes (PointT1Post) so the file is on disk by then. T1's Post
	// hook returns the corrupt error which aborts T1; the runner's
	// orchestration then short-circuits and ExitStatus is preflight-failed
	// equivalent. To test the gate cleanly we want the corrupt to happen
	// silently (not abort T1) so T2 then sees the mismatch. We do that by
	// arming on T2-pre (per-file before hash) with action=corrupt.
	Activate([]Fault{
		{Action: ActionCorrupt, Phase: "T2-pre", File: rels[0]},
	})
	t.Cleanup(func() {
		Activate(nil)
		_ = RunCleanups()
	})

	res, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "gate-test", Source: src},
		DestRoot: dest,
		Mode:     types.ModeMove,
	})
	// The corrupt action returns nil after flipping the byte, so T2
	// proceeds normally; the hash mismatch then drives the gate.
	if err != nil {
		t.Fatalf("Run: unexpected error %v", err)
	}
	if res == nil {
		t.Fatal("nil RunResult")
	}
	if res.ExitStatus != types.ExitStatusCopyOnlyAbortedDelete {
		t.Errorf("ExitStatus = %q; want copy_only_aborted_delete", res.ExitStatus)
	}
	if res.FilesFailed == 0 {
		t.Errorf("FilesFailed = 0; expected at least 1 (the corrupted file)")
	}

	// Source files MUST still exist (gate closed; no unlink).
	for _, rel := range rels {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if _, err := os.Stat(full); err != nil {
			t.Errorf("source file %q missing despite atomic gate closing: %v", rel, err)
		}
	}

	// deletion-log MUST NOT exist (T3 was not entered).
	dotDir := filepath.Join(dest, ".flashbackup")
	delLog := filepath.Join(dotDir, "runs", res.RunID, "deletion-log.ndjson")
	if _, err := os.Stat(delLog); err == nil {
		t.Errorf("deletion-log exists at %s; T3 should not have run when the gate closed", delLog)
	}

	// Manifest STILL finalizes (T4 runs even with the gate closed so the
	// audit trail is complete and the runs.ndjson finished line lands).
	manifest := filepath.Join(dotDir, "runs", res.RunID, manifestBaseFilename+".gz")
	if _, err := os.Stat(manifest); err != nil {
		t.Errorf("manifest.gz missing at %s: %v", manifest, err)
	}
}

// TestRun_PartialUnderCopyMode exercises the partial exit-status path:
// in copy mode the atomic gate is irrelevant (copy mode does not unlink
// anything), so a single hash mismatch flows through to T4 finalize with
// ExitStatus=partial. T3 still runs but the copy-mode short-circuit
// inside T4 (delete-source phase) means no source is touched.
func TestRun_PartialUnderCopyMode(t *testing.T) {
	requireE2E(t)
	requireMacOS(t)
	requireDiskutil(t)

	rsyncPath := requireSystemRsync(t)
	dest := setupDest(t)
	withRsyncPathOverride(t, rsyncPath)

	src := t.TempDir()
	rels := seedSourceTree(t, src)

	Activate([]Fault{
		{Action: ActionCorrupt, Phase: "T2-pre", File: rels[0]},
	})
	t.Cleanup(func() {
		Activate(nil)
		_ = RunCleanups()
	})

	res, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "partial-test", Source: src},
		DestRoot: dest,
		Mode:     types.ModeCopy,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.ExitStatus != types.ExitStatusPartial {
		t.Errorf("ExitStatus = %q; want partial", res.ExitStatus)
	}
	if res.FilesFailed == 0 {
		t.Errorf("FilesFailed = 0; expected at least 1")
	}
	if res.FilesSucceeded == 0 {
		t.Errorf("FilesSucceeded = 0; expected the un-corrupted files to verify")
	}
}

// TestRun_KillFaultAbortsRun exercises the kill fault: when the fault
// matches at PointT1PreRsync, Hook returns ErrFaultKill, T1 wraps it as a
// fatal phase error, the orchestrator returns the error to the caller, and
// no further phase runs. ExitStatus is left unset by the failure path; the
// caller sees a non-nil error.
func TestRun_KillFaultAbortsRun(t *testing.T) {
	requireE2E(t)
	requireMacOS(t)
	requireDiskutil(t)

	rsyncPath := requireSystemRsync(t)
	dest := setupDest(t)
	withRsyncPathOverride(t, rsyncPath)

	src := t.TempDir()
	seedSourceTree(t, src)

	Activate([]Fault{
		{Action: ActionKill, Phase: "T1"},
	})
	t.Cleanup(func() {
		Activate(nil)
		_ = RunCleanups()
	})

	_, err := Run(context.Background(), types.RunOptions{
		Profile:  profiles.Profile{V: 1, Name: "kill-test", Source: src},
		DestRoot: dest,
		Mode:     types.ModeCopy,
	})
	if err == nil {
		t.Fatal("expected error from kill fault; got nil")
	}
	// Error message should mention the pre-rsync fault path.
	if got := err.Error(); !contains(got, "pre-rsync fault") {
		t.Errorf("error %q does not mention pre-rsync fault", got)
	}
}

// contains is a tiny strings.Contains shim so this file does not need to
// import strings just for one call.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
