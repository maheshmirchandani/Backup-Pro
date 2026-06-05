//go:build faultinject

package e2e

// atomic_gate_test.go covers AC-4 of the design spec: the move-mode
// atomic gate. Under move mode, if ANY file fails T2 hash compare, the
// runner short-circuits T3 (delete-source) before any unlink and exits
// with ExitStatus=copy_only_aborted_delete. The test drives this code
// path end-to-end through the faultinject CLI binary using the corrupt
// fault on a single tiny-fixture file.
//
// Test mechanics:
//
//  1. Mount + init a fresh APFS DMG, seed the tiny fixture into a
//     tempdir, and store a profile that includes the source.
//  2. Snapshot the source file list BEFORE the run so we can compare
//     after.
//  3. Run `flashbackup-faultinject backup --inject=corrupt:phase=T2-pre:
//     file=a.txt --move <profile> <usb>` with stdin="DELETE\n". The
//     corrupt hook fires just before T2 reads a.txt's dest copy, flips
//     one byte, and returns nil. T2 then sees a hash mismatch on a.txt
//     while b.md and c.json verify cleanly. FilesVerified (2) < FilesTotal
//     (3) closes the atomic gate. T3 emits atomic_gate_blocked + skips
//     every unlink. The runner sets ExitStatus=copy_only_aborted_delete
//     and the cmd-side translator returns exit code 1.
//  4. Assert: exit code 1, runs.ndjson finished line carries the
//     copy_only_aborted_delete status, events.ndjson contains an
//     atomic_gate_blocked kind, all three source files still exist.
//
// Phase selection: the brief's example DSL `phase=T1:file=a.txt` is the
// canonical shorthand from the master plan, but the WORKING wire-string
// for the per-file dest-corrupt hook is "T2-pre" (the runner's
// PointT2PreHash). T1's per-file granularity is inside the rsync
// progress callback (PointT1Progress, phase="T1") which races with
// rsync's own writes; T1Post is one-shot at the end of T1 and lacks the
// file= selector match because args.CurrentFile is empty there. T2-pre
// is the documented seam that the runner-level test
// (TestRun_AtomicGateClosesUnderCorrupt) also uses, and the resulting
// observable behaviour (post-rsync, pre-hash dest corruption) matches
// the brief's narrative description verbatim.
//
// Tagged into the e2e-safety Makefile gate via the "AtomicGate" run-name
// pattern. Build-tagged faultinject so the test never compiles into a
// release-shape go test invocation.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_AtomicGate_MoveModeBlocksOnHashMismatch is the AC-4 end-to-end
// assertion. See file-level comment for the full mechanic; the test
// body is a tight init -> seed -> snapshot -> run -> assert sequence.
func TestE2E_AtomicGate_MoveModeBlocksOnHashMismatch(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Real GNU rsync is required. Apple's openrsync (2.6.9 compatible)
	// lacks the flags the runner needs (--from0, --xattrs); the embedded
	// placeholder stub at this stage of the plan exits 0 without copying
	// bytes which would make the test vacuous (T2 then sees every file
	// as missing on dest, not as hash-mismatched). Skip cleanly when no
	// real rsync is available.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB.
	usb := SetupUSB(t, 64)

	// Seed source: copy the tiny fixture tree (a.txt, b.md, c.json) into
	// a fresh tempdir. The inject target "a.txt" is hard-coded against
	// the tiny fixture's layout.
	source := SeedSource(t, "tiny")

	// Seed profile with includes=["*"] so every tiny-fixture file is in
	// scope.
	SeedProfile(t, usb, "atomic-gate-test", source, []string{"*"}, nil)

	// Snapshot source state BEFORE the run. listFilesUnder returns a
	// sorted slice of paths relative to source so a sort + slice compare
	// is the equality check.
	sourceBefore := listFilesUnder(t, source)
	if len(sourceBefore) < 1 {
		t.Fatalf("seeded source has no files: %v", sourceBefore)
	}

	// Run the faultinject-tagged backup with corrupt on a.txt and --move
	// with the DELETE confirmation on stdin. Exit code 1 is the
	// copy_only_aborted_delete translation (backupExitCodeRuntime).
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"atomic-gate-test", usb,
		[]string{"--move"},
		[]string{"corrupt:phase=T2-pre:file=a.txt"},
		"DELETE\n",
	)

	if exitCode != 1 {
		t.Errorf("backup exit code: got %d want 1 (copy_only_aborted_delete)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// The runs.ndjson finished line must carry
	// exit_status=copy_only_aborted_delete. AssertRunsNDJSONHasFinishedLine
	// returns the run_id of the last finished line; we use it both to
	// open the per-run events.ndjson AND to re-decode the runs.ndjson
	// for a typed exit_status check.
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}

	runs := readNDJSON(t, filepath.Join(usb, ".flashbackup", "runs.ndjson"))
	if len(runs) < 2 {
		t.Fatalf("runs.ndjson lines: got %d want >= 2 (started + finished)\nstderr: %s",
			len(runs), stderr)
	}
	finished := runs[len(runs)-1]
	if event, _ := finished["event"].(string); event != "finished" {
		t.Errorf("runs.ndjson last line event: got %q want finished", event)
	}
	if status, _ := finished["exit_status"].(string); status != "copy_only_aborted_delete" {
		t.Errorf("runs.ndjson exit_status: got %q want copy_only_aborted_delete\nfinished line: %v",
			status, finished)
	}

	// events.ndjson must contain at least one atomic_gate_blocked kind.
	// This is the forensic event that T4 emits when the gate fires
	// (internal/runner/t4_delete_source.go:t4FinishGateBlocked).
	eventsPath := filepath.Join(usb, ".flashbackup", "runs", runID, "events.ndjson")
	events := readNDJSON(t, eventsPath)
	gateBlocked := false
	for _, ev := range events {
		if kind, _ := ev["kind"].(string); kind == "atomic_gate_blocked" {
			gateBlocked = true
			break
		}
	}
	if !gateBlocked {
		// Dump a compact summary of kinds seen for easier triage.
		kinds := make([]string, 0, len(events))
		for _, ev := range events {
			if k, _ := ev["kind"].(string); k != "" {
				kinds = append(kinds, k)
			}
		}
		t.Errorf("events.ndjson missing atomic_gate_blocked kind; saw kinds: %v", kinds)
	}

	// The deletion-log MUST NOT exist (T3 short-circuited before any
	// unlink, and the deletion-log is only created when the gate is
	// open). This pin is from the runner-level test invariant; the file
	// missing is the strongest forensic signal that no source touch
	// happened.
	deletionLog := filepath.Join(usb, ".flashbackup", "runs", runID, "deletion-log.ndjson")
	if _, err := os.Stat(deletionLog); err == nil {
		t.Errorf("deletion-log.ndjson exists at %s; gate should have prevented T3 from logging deletions",
			deletionLog)
	}

	// ALL source files survive. Compare before/after slices; any drift
	// is a gate failure regardless of which files are missing or added.
	sourceAfter := listFilesUnder(t, source)
	if !stringSlicesEqual(sourceBefore, sourceAfter) {
		t.Errorf("source files changed across run; atomic gate failed to protect them\nbefore: %v\nafter:  %v",
			sourceBefore, sourceAfter)
	}

	// Forensic: also re-decode the finished line's run_id to ensure the
	// events / runs / runID linkage is internally consistent. Mismatches
	// here would indicate a schema drift in runs.ndjson that the rest of
	// the assertions silently rely on.
	if got, _ := finished["run_id"].(string); got != runID {
		t.Errorf("finished line run_id mismatch: got %q want %q", got, runID)
	}

	// Sanity: pretty-print the finished line as JSON so a future failure
	// has more than a single map[string]any to grep through. Logged at
	// Logf level (not as an error) so a clean run stays quiet.
	if pretty, err := json.MarshalIndent(finished, "", "  "); err == nil {
		t.Logf("finished line:\n%s", pretty)
	}
}

// listFilesUnder returns a sorted slice of file paths relative to root.
// Directories are not included; only regular files. Sort is byte-wise
// so the result is stable across platforms.
func listFilesUnder(t *testing.T, root string) []string {
	t.Helper()
	var out []string
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		out = append(out, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(out)
	return out
}

// stringSlicesEqual reports whether two pre-sorted string slices have
// identical contents. Used by the atomic-gate assertion to compare
// before/after source file lists.
func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
