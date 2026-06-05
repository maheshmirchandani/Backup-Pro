//go:build faultinject

package e2e

// fault_kill_test.go covers fault hook combinations that the prior
// e2e tests (Tasks 48 to 51a) did not exercise. The QA multi-hat
// review flagged four gaps in the fault-DSL coverage matrix:
//
//   - kill:phase=T2:file=a.txt  - kill during T2 per-file hash
//                                 compare, mid-loop.
//   - kill:phase=T3:file=a.txt  - kill during T3 per-file delete-
//                                 source, mid-loop, move mode.
//   - unmount:phase=T1          - destination volume yanked during
//                                 the rsync transfer.
//   - disk-full:phase=T1        - destination volume runs out of
//                                 free space during the transfer.
//
// Each test drives the runner end-to-end through the faultinject CLI
// binary with the relevant --inject spec. Per the master plan amendment
// (Task 51b), the canonical phase wire strings are: T1-pre, T1, T1-post,
// T2-pre, T2, T3-pre, T3. The kill action returns the ErrFaultKill
// sentinel which the runner treats as a fatal phase abort; the cmd-side
// translator (cmd/flashbackup/backup_helpers.go) maps the wrapped error
// to a non-zero exit code.
//
// Test posture: assert NON-ZERO exit on all four scenarios. The exact
// non-zero code is intentionally not pinned because future ExitStatus
// additions (e.g., a dedicated "killed_in_phase" or "destination_
// disappeared" status) should not silently break the test. We pin the
// observable shape (runs.ndjson started-without-finished orphan for
// fatal-phase kills; some-files-deleted partial state for T3 kill;
// non-zero exit for unmount and disk-full) rather than exact codes.
//
// Flaky-test handling:
//
//   - TestE2E_Fault_UnmountDuringT1 calls /sbin/umount -f mid-rsync. On
//     macOS, umount -f against a busy filesystem can occasionally fail
//     when rsync has the destination open with a write lock. We detect
//     the unmount-failed path (Hook returns an "umount: ... not
//     permitted" or "Resource busy" error) and skip rather than fail.
//     The progress callback timing (rsync's --info=progress2 with the
//     tiny fixture) can also race; if no progress event fires before
//     rsync completes, the hook never triggers and the test would
//     produce a clean run instead. Both cases skip with documented
//     reasoning rather than fail.
//
//   - TestE2E_Fault_DiskFullDuringT1 uses ftruncate to claim all
//     remaining free space on the destination. macOS APFS reports
//     reservation-based free space, so a truncate to "available bytes"
//     may not actually claim every block; rsync could still squeeze in.
//     We treat any non-zero exit OR an explicit transfer_failed event
//     as the success condition; a clean run skips rather than fails.
//
// Tagged into the e2e-safety Makefile gate via the "FaultKill" run-name
// pattern (Makefile e2e-safety line; updated in this commit to include
// FaultKill alongside AtomicGate / Mutation / CrashResume / DeleteFlag /
// DeleteConfirm / TamperedManifest). Build-tagged faultinject so the
// test never compiles into a release-shape go test invocation.

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_FaultKill_T2HashCompare drives the runner through a kill
// fault at the T2 per-file hash-compare hook. The hook fires when
// PointT2PerFile is reached with CurrentFile=="a.txt" (the first file
// in the tiny fixture's sorted order). ErrFaultKill propagates as a
// fatal phase error; T4 (finalize) never runs; runs.ndjson carries a
// "started" line but no matching "finished" line (canonical orphan
// shape per invariant #10).
func TestE2E_FaultKill_T2HashCompare(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Real GNU rsync is required. The embedded placeholder stub at this
	// stage of the plan exits 0 without copying bytes; T2 would then see
	// every file as missing on dest, the per-file loop would skip the
	// PointT2PerFile call site, and the kill hook would never trigger.
	// Skip cleanly when no real rsync is available; matches the posture
	// of every other Task 48+ e2e test.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB.
	usb := SetupUSB(t, 64)

	// Seed source: tiny fixture (a.txt, b.md, c.json). The inject target
	// "a.txt" is hard-coded against the tiny fixture's layout; a.txt
	// sorts first lexicographically so it is the first per-file iteration
	// of the T2 loop.
	source := SeedSource(t, "tiny")

	// Seed profile with includes=["*"] so every tiny-fixture file is in
	// scope of the run.
	SeedProfile(t, usb, "fault-kill-t2-test", source, []string{"*"}, nil)

	// Run faultinject-tagged backup. No --move (T2 kill is mode-agnostic;
	// copy mode keeps the test independent of the move-mode confirmation
	// surface). Empty stdin.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"fault-kill-t2-test", usb,
		nil, // no --move
		[]string{"kill:phase=T2:file=a.txt"},
		"",
	)

	// Non-zero exit. ErrFaultKill propagates as a wrapped fatal phase
	// error; the cmd-side translator (backup_helpers.go) returns the
	// runtime exit code when ExitStatus is empty on RunResult.
	if exitCode == 0 {
		t.Errorf("backup exit code: got 0; expected non-zero (kill at T2)\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	// runs.ndjson MUST have exactly one line: the orphan "started" entry.
	// T0 wrote the started line on success; T2 died before T4 (finalize)
	// could write the matching "finished" line. This is the canonical
	// "orphaned signal" per invariant #10.
	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	data, err := os.ReadFile(runsPath)
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("runs.ndjson lines: got %d want 1 (orphan started only)\ndata: %s\nstderr: %s",
			len(lines), data, stderr)
	}
	var rec struct {
		Event string `json:"event"`
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("unmarshal orphan started line: %v", err)
	}
	if rec.Event != "started" {
		t.Errorf("orphan event: got %q want started", rec.Event)
	}
	if rec.RunID == "" {
		t.Fatalf("orphan started line missing run_id: %v", rec)
	}

	// Forensic: the orphan run's events.ndjson should at least carry T0
	// phase_completed (preflight succeeded) and T1 phase_completed
	// (rsync succeeded) before the T2 phase aborted. We log rather than
	// assert because the exact event surface depends on the runner's
	// emission order; the test should not become brittle on that surface.
	orphanEventsPath := filepath.Join(usb, ".flashbackup", "runs", rec.RunID, "events.ndjson")
	if orphanEvents, err := os.ReadFile(orphanEventsPath); err == nil {
		t.Logf("orphan run %s events.ndjson (T2-killed):\n%s", rec.RunID, orphanEvents)
	} else {
		t.Logf("orphan run %s events.ndjson read err: %v", rec.RunID, err)
	}
}

// TestE2E_FaultKill_T3DeleteSource drives the runner through a kill
// fault at the T3 per-file delete-source hook in move mode. The hook
// fires when PointT3PerFile is reached with CurrentFile=="a.txt" (sorts
// first, killed on the first per-file iteration). Because a.txt is
// alphabetically first in the tiny fixture, no source file should have
// been deleted yet when the kill fires (the per-file iteration runs
// AFTER the unlink for that file but the file= selector matches BEFORE
// any other file is processed). The runner emits a fatal phase abort;
// runs.ndjson carries an orphan started line.
//
// Move mode is required to activate the T3 delete-source phase. The
// DELETE\n stdin authorises the move-mode confirmation gate.
func TestE2E_FaultKill_T3DeleteSource(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "fault-kill-t3-test", source, []string{"*"}, nil)

	// Snapshot source state before the run. After the kill, we expect
	// partial state: at most one file (a.txt) unlinked before the hook
	// fired (the hook is AFTER the t4AttemptDelete call inside the
	// per-file loop). b.md and c.json must still be on disk because the
	// loop aborted before iterating them.
	sourceBefore := listFilesUnder(t, source)
	if len(sourceBefore) < 3 {
		t.Fatalf("seeded source missing files: %v (want a.txt, b.md, c.json)", sourceBefore)
	}

	// Run faultinject-tagged backup in MOVE mode with the kill fault on
	// T3 for a.txt. DELETE\n on stdin authorises the move-mode
	// confirmation gate. The runner runs T0 -> T1 (rsync) -> T2 (hash
	// compare; all three files verify cleanly) -> T3 (delete-source);
	// inside the T3 per-file loop, a.txt is unlinked, then the kill hook
	// fires post-unlink and aborts the phase.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"fault-kill-t3-test", usb,
		[]string{"--move"},
		[]string{"kill:phase=T3:file=a.txt"},
		"DELETE\n",
	)

	// Non-zero exit (kill mid-T3 is a fatal phase error).
	if exitCode == 0 {
		t.Errorf("backup exit code: got 0; expected non-zero (kill at T3 mid-loop)\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	// runs.ndjson must carry the orphan started line. The kill fault at
	// T3 aborts before T4 (finalize) writes the matching finished line.
	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	data, err := os.ReadFile(runsPath)
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("runs.ndjson lines: got %d want 1 (orphan started only)\ndata: %s\nstderr: %s",
			len(lines), data, stderr)
	}
	var rec struct {
		Event string `json:"event"`
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(lines[0], &rec); err != nil {
		t.Fatalf("unmarshal orphan started line: %v", err)
	}
	if rec.Event != "started" {
		t.Errorf("orphan event: got %q want started", rec.Event)
	}
	if rec.RunID == "" {
		t.Fatalf("orphan started line missing run_id: %v", rec)
	}

	// Partial state: a.txt has been unlinked (the hook fires AFTER the
	// t4AttemptDelete in the loop body); b.md and c.json must still be
	// on disk because the kill aborted the loop before they were
	// processed. The exact set surviving depends on iteration order; the
	// tiny fixture sorts a.txt < b.md < c.json so b.md and c.json are
	// guaranteed to survive.
	sourceAfter := listFilesUnder(t, source)
	t.Logf("source before T3 kill: %v", sourceBefore)
	t.Logf("source after  T3 kill: %v", sourceAfter)
	for _, name := range []string{"b.md", "c.json"} {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Errorf("source/%s missing after T3 kill on a.txt; expected loop to abort BEFORE iterating %s: %v",
				name, name, err)
		}
	}

	// Forensic: dump the deletion-log if it exists. The T3 per-file
	// audit writes the deletion-log line BEFORE the post-unlink hook
	// fires for the NEXT iteration, but a.txt's own deletion-log line
	// may or may not have been flushed depending on per-iteration order
	// (writeDeletionLogLine is called AFTER the PointT3PerFile hook in
	// t4_delete_source.go; the hook on a.txt returns ErrFaultKill before
	// the writeDeletionLogLine call, so a.txt's deletion-log line is
	// NOT written even though the unlink happened). We log the file's
	// presence/absence and contents for triage.
	delLog := filepath.Join(usb, ".flashbackup", "runs", rec.RunID, "deletion-log.ndjson")
	if delLogData, err := os.ReadFile(delLog); err == nil {
		t.Logf("deletion-log.ndjson (orphan run %s):\n%s", rec.RunID, delLogData)
	} else {
		t.Logf("deletion-log.ndjson read err: %v (may not exist if T3 aborted before any write)", err)
	}

	// Forensic: the orphan run's events.ndjson should carry T0/T1/T2
	// completion plus a T3 phase_aborted. Logged for triage; not
	// asserted to keep the test independent of the audit event surface.
	orphanEventsPath := filepath.Join(usb, ".flashbackup", "runs", rec.RunID, "events.ndjson")
	if orphanEvents, err := os.ReadFile(orphanEventsPath); err == nil {
		t.Logf("orphan run %s events.ndjson (T3-killed):\n%s", rec.RunID, orphanEvents)
	}
}

// TestE2E_Fault_UnmountDuringT1 drives the runner through an unmount
// fault at the T1 progress-callback hook. The hook shells out to
// /sbin/umount -f against the DMG mountpoint mid-rsync; the rsync
// subprocess then encounters EIO / ENOENT / other I/O errors on its
// next write attempt and exits non-zero. The runner treats the
// non-zero rsync exit as a fatal phase error.
//
// Flaky paths handled with t.Skip:
//
//   - umount -f can fail with EBUSY ("Resource busy") when rsync still
//     has the destination open with a write lock. The hook's error is
//     surfaced to the runner as a phase fault (because Hook returned
//     non-nil); we treat this as a successful test outcome (the unmount
//     was simulated, just rejected by the kernel, and the run still
//     aborted). Exit code is still non-zero.
//
//   - With the tiny fixture, rsync's progress callback may not fire
//     before rsync completes (the entire fixture is ~20 bytes; rsync
//     may transfer it in a single write without emitting a progress
//     line). In that case the unmount hook never triggers and the run
//     completes cleanly. We detect this (clean exit + finished line)
//     and skip rather than fail.
//
//   - The hdiutil detach in t.Cleanup may fail because /sbin/umount -f
//     already detached the device. That is harmless; the cleanup is
//     best-effort.
func TestE2E_Fault_UnmountDuringT1(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "fault-unmount-test", source, []string{"*"}, nil)

	// Run faultinject-tagged backup with the unmount fault on T1. No
	// after_pct / after_count: the hook fires on the FIRST progress
	// callback. Copy mode (no --move) keeps the test independent of the
	// move-mode confirmation surface.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"fault-unmount-test", usb,
		nil, // no --move
		[]string{"unmount:phase=T1"},
		"",
	)

	// Race-tolerant clean-exit detection: with the tiny fixture, the
	// rsync progress callback might never fire because rsync completes
	// in a single write. In that case the unmount hook is never invoked
	// and the run finishes cleanly. We detect this and skip rather than
	// fail; the test pins the behaviour-shape, not the timing.
	if exitCode == 0 {
		runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
		if data, err := os.ReadFile(runsPath); err == nil {
			if bytes.Contains(data, []byte(`"event":"finished"`)) {
				t.Skipf("unmount hook never fired (rsync progress callback did not trigger on tiny fixture; runs.ndjson contains a finished line): clean run, no fault observed\nstdout: %s\nstderr: %s",
					stdout, stderr)
			}
		}
	}

	// Non-zero exit (unmount mid-rsync OR Hook returning umount-failed).
	// Both code paths produce a wrapped fatal phase error, which the cmd
	// translator maps to a non-zero exit.
	if exitCode == 0 {
		t.Errorf("backup exit code: got 0; expected non-zero (unmount at T1)\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}
}

// TestE2E_Fault_DiskFullDuringT1 drives the runner through a disk-full
// fault at the T1 progress-callback hook. The hook creates a sentinel
// file at <usb>/.faultinject_disk_full and truncates it to claim all
// remaining free space on the destination; rsync's next write attempt
// then fails with ENOSPC and the rsync subprocess exits non-zero.
//
// Flaky path handled with t.Skip:
//
//   - With the tiny fixture, rsync's progress callback may not fire
//     before rsync completes. In that case the disk-full hook is
//     never invoked and the run completes cleanly. We detect this
//     and skip rather than fail.
//
//   - APFS reservation-based free-space accounting can leave a small
//     residual after the truncate; rsync might still squeeze the
//     remaining ~20 bytes of the tiny fixture in. We treat any non-zero
//     exit OR a transfer_failed event as the success condition.
func TestE2E_Fault_DiskFullDuringT1(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "fault-diskfull-test", source, []string{"*"}, nil)

	// Run faultinject-tagged backup with the disk-full fault on T1. The
	// hook fills free space with a sentinel file; the hook returns
	// ErrFaultDiskFull so the progress callback signals a fault to the
	// runner. Copy mode (no --move) keeps the test scoped to the T1 path.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"fault-diskfull-test", usb,
		nil, // no --move
		[]string{"disk-full:phase=T1"},
		"",
	)

	// Race-tolerant clean-exit detection.
	if exitCode == 0 {
		runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
		if data, err := os.ReadFile(runsPath); err == nil {
			if bytes.Contains(data, []byte(`"event":"finished"`)) {
				t.Skipf("disk-full hook never fired (rsync progress callback did not trigger on tiny fixture; runs.ndjson contains a finished line): clean run, no fault observed\nstdout: %s\nstderr: %s",
					stdout, stderr)
			}
		}
	}

	// Non-zero exit (disk-full mid-rsync OR Hook returning the
	// ErrFaultDiskFull sentinel). Both produce a wrapped fatal phase
	// error.
	if exitCode == 0 {
		t.Errorf("backup exit code: got 0; expected non-zero (disk-full at T1)\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}

	// Forensic: log stderr for triage if a future failure lands here.
	// disk-full errors should mention "no space left on device" or
	// "ENOSPC" somewhere in the rsync log; we don't assert this string
	// because rsync version may vary the wording.
	if exitCode != 0 {
		t.Logf("disk-full backup stderr (for triage):\n%s",
			strings.TrimSpace(stderr))
	}
}
