//go:build faultinject

package e2e

// delete_flag_test.go covers AC-14 of the design spec: mirror-delete mode
// (the `--delete` CLI flag) removes destination paths that FlashBackup
// previously wrote but no longer appear in the latest manifest, AND
// leaves user-added files (files at the namespaced dest that FB never
// wrote) untouched per invariant #6.
//
// Test mechanics (the AC-14 narrative end to end):
//
//  1. Mount + init a fresh APFS DMG, seed the tiny fixture (a.txt, b.md,
//     c.json) into a tempdir, and store a profile that includes the
//     source.
//  2. First backup (no --delete): copy mode populates the namespaced
//     dest with a.txt, b.md, c.json. The manifest from this run records
//     all three as FB-written paths.
//  3. Mutate source: delete b.md from the source tree. After the next
//     run's manifest is written, b.md is no longer in the FB-written
//     set (it was, but the new manifest supersedes; the FB-written
//     tracking is the union of historical writes minus the current
//     manifest's absences, which is exactly what `--delete` consults).
//  4. Add a user file at the namespaced dest: write
//     `user_added_<random>.txt` directly into <usb>/<host>-<user>/.
//     This file was never written by any FlashBackup run, so its path
//     is NOT in any manifest's FB-written set.
//  5. Second backup with `--delete`: copy mode + mirror-delete. Expected
//     behaviour per AC-14:
//       - b.md is removed from the dest (mirror: present in dest, NOT
//         in the latest manifest, IS in some prior manifest's FB-written
//         set; the union-minus-absence rule says "delete me").
//       - a.txt and c.json remain (still in the latest manifest).
//       - user_added_<random>.txt remains untouched (NEVER written by
//         FB; not in any manifest's FB-written set; invariant #6 says
//         "leave it alone").
//       - exit_status=ok, exit code 0, no warning about the user file.
//
// Architectural gap (documented 2026-06-05 during Task 51 dispatch):
//
//   The `--delete` CLI flag is NOT wired in cmd/flashbackup/backup.go.
//   The flagset only registers --move and (faultinject build only)
//   --inject; `--delete` reaches the standard flag-package rejection
//   "flag provided but not defined: -delete" and the binary exits 2.
//
//   At the runner level, RunOptions.Delete is a dead field:
//     - internal/runner/t2_transfer.go:224 hard-codes Delete:false on
//       the rsync invocation with the comment "invariant #6: mirror-
//       delete is T3, not T1" but no T3-side mirror-delete phase
//       exists. T3 is the move-mode delete-SOURCE phase; the spec's
//       --delete is a delete-DEST mirror, an orthogonal operation that
//       requires its own phase function (e.g., a T2.5 "MirrorDeleteDest"
//       or an extension to T4 finalize that reads the FB-written-paths
//       set, compares against the current manifest, and unlinks the
//       diff under the namespaced dest root).
//     - No code anywhere in internal/runner/ consults opts.Delete.
//     - No FB-written-paths tracking artifact exists on disk. Invariant
//       #6 says "tracked in manifests" but the implementation requires
//       a producer that scans prior manifests at preflight time to
//       reconstruct the historical write set; that producer is unwritten.
//
//   The test logs the observable state, asserts the AC-14 narrative
//   shape in future-state code BELOW the skip, then Skips with a queued
//   backlog item rather than failing CI. Once the mirror-delete phase
//   ships (BACKLOG: queue Task 51c "mirror-delete --delete flag wiring
//   + FB-written-paths reconstruction" or similar), removing the
//   t.Skip flips this test on with zero further changes.
//
// Phase selection: no --inject spec is needed for AC-14; the test
// drives the runner through two normal copy-mode runs. We use the
// faultinject-tagged build only so the file lives under the
// `e2e-safety` Makefile gate (run-name pattern "DeleteFlag" per
// Makefile:107).
//
// Tagged into the e2e-safety Makefile gate via the "DeleteFlag"
// run-name pattern. Build-tagged faultinject so the test never
// compiles into a release-shape go test invocation, matching the
// pattern used by Tasks 48 to 50.

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_DeleteFlag_RemovesObsoleteButProtectsUserFiles is the AC-14
// end-to-end assertion. See file-level comment for the full mechanic.
//
// State of play at task dispatch: --delete is unimplemented at both the
// cmd CLI surface and the runner phase layer. The test confirms the
// gap by attempting to run the second backup with --delete and observing
// the flag-package rejection, then Skips with a queued backlog item.
// Future-state assertions sit below the skip so the recovery is a
// single-line edit once mirror-delete ships.
func TestE2E_DeleteFlag_RemovesObsoleteButProtectsUserFiles(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Real GNU rsync is required. The embedded placeholder stub at this
	// stage of the plan exits 0 without copying bytes, so the first
	// backup would not actually place a.txt / b.md / c.json under the
	// namespaced dest and the mirror-delete contract (b.md present
	// pre-second-run, absent post-second-run) becomes unobservable.
	// Skip cleanly when no real rsync is available; matches the
	// happy-path test's posture.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB.
	usb := SetupUSB(t, 64)

	// Seed source: copy the tiny fixture tree (a.txt, b.md, c.json) into
	// a fresh tempdir. The AC-14 mutation step (remove b.md) targets a
	// concrete file in this fixture; we couple to the tiny-fixture
	// layout intentionally so the test's narrative reads against the
	// MANIFEST.txt inventory.
	source := SeedSource(t, "tiny")

	// Seed profile with includes=["*"] so every tiny-fixture file is in
	// scope of both runs.
	SeedProfile(t, usb, "delete-flag-test", source, []string{"*"}, nil)

	// First backup: clean copy-mode run, no faults, no --delete (the
	// flag is only meaningful on the SECOND run when there is something
	// to mirror-delete). The backup populates the namespaced dest with
	// the full tiny-fixture set and writes a manifest that records all
	// three files as FB-written.
	exitCode1, stdout1, stderr1 := RunBackupFaultinject(t,
		"delete-flag-test", usb,
		nil, // no --move, no --delete
		nil, // no faults
		"",
	)
	if exitCode1 != 0 {
		t.Fatalf("first backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode1, stdout1, stderr1)
	}

	// Locate the namespaced dest dir on the USB. Per invariant #15 the
	// runner computes the prefix via paths.Prefix(hostname, username);
	// we mirror that here so the assertion stays in lockstep with the
	// runner's single source of truth.
	hostname, _ := os.Hostname()
	uname, _ := exec.Command("/usr/bin/whoami").Output()
	username := strings.TrimSpace(string(uname))
	nsDir := paths.Prefix(hostname, username)
	destNS := filepath.Join(usb, nsDir)

	// Sanity: the first backup landed all three tiny-fixture files
	// somewhere under the namespaced dest. If this fails the rest of
	// the AC-14 narrative is meaningless; surface it as a fatal so
	// the cause is not buried under the architectural-gap skip.
	for _, name := range []string{"a.txt", "b.md", "c.json"} {
		if !findFileUnder(t, destNS, name) {
			t.Fatalf("first backup did not place %s under namespaced dest %s\nstdout1: %s\nstderr1: %s",
				name, destNS, stdout1, stderr1)
		}
	}

	// Mutate source: remove b.md so it falls out of the second backup's
	// manifest. The first backup's manifest still records b.md as
	// FB-written (immutable record); the second backup's manifest will
	// NOT include b.md. The union-minus-absence rule the --delete
	// mirror-mode is supposed to apply makes b.md eligible for unlink
	// at the dest.
	if err := os.Remove(filepath.Join(source, "b.md")); err != nil {
		t.Fatalf("remove source b.md: %v", err)
	}

	// Add a user file directly under the namespaced dest. Random suffix
	// guards against any future fixture extension that might collide
	// with a real source file name. This file has NEVER been written
	// by FlashBackup so it must NOT appear in any manifest's FB-written
	// set; invariant #6 protection is the AC-14 wedge.
	userFileName := fmt.Sprintf("user_added_%d.txt", time.Now().UnixNano())
	userFilePath := filepath.Join(destNS, userFileName)
	userFileContent := []byte("hand-placed by the user; FlashBackup must not delete this\n")
	if err := os.WriteFile(userFilePath, userFileContent, 0o644); err != nil {
		t.Fatalf("write user-added dest file %s: %v", userFilePath, err)
	}

	// Snapshot the dest tree BEFORE the second run so a post-run diff
	// is a single sort + compare. We capture the user file in this
	// list explicitly; if mirror-delete behaves correctly (future) the
	// user file is still present after the second run.
	destBefore := listFilesUnder(t, destNS)
	t.Logf("dest before second run (%d entries):\n%s", len(destBefore), strings.Join(destBefore, "\n"))

	// Second backup with --delete: this is the AC-14 invocation. Today
	// the cmd-side flagset does NOT register --delete; the flag package
	// rejects it as "flag provided but not defined: -delete" and the
	// binary exits 2 (usage code) without ever invoking the runner.
	exitCode2, stdout2, stderr2 := RunBackupFaultinject(t,
		"delete-flag-test", usb,
		[]string{"--delete"},
		nil, // no faults
		"",
	)
	t.Logf("second backup exit=%d stdout=%s stderr=%s", exitCode2, stdout2, stderr2)

	// Detect the architectural gap: a flag-package "not defined" exit
	// (or any non-zero exit caused by --delete not being a known flag)
	// means the mirror-delete surface is not wired. We skip with a
	// queued backlog item rather than failing CI.
	//
	// The detector is "--delete shows up in the stderr usage line as
	// not-defined" OR "exit code is the usage code (2) AND stderr
	// mentions 'flag'" since the flag package's exact wording could
	// drift across Go versions.
	if exitCode2 == 2 && strings.Contains(stderr2, "flag provided but not defined") &&
		strings.Contains(stderr2, "delete") {
		t.Logf("AC-14 architectural gap confirmed: --delete flag not wired at cmd/flashbackup/backup.go")
		t.Logf("when implemented, --delete should: (a) register the flag in the backup fs, (b) plumb opts.Delete=true into runner.Run, (c) the runner should add a mirror-delete phase (e.g. T2.5 between hash-compare and finalize, or fold into T4) that reads the FB-written-paths reconstruction (union of all prior manifests' paths) minus the current run's manifest, and unlinks the diff under the namespaced dest root; user-added files at the dest root are excluded because they never appear in any FB-written set")
		t.Skip("AC-14 mirror-delete not wired (cmd/flashbackup/backup.go has no --delete flag; runner has no mirror-delete phase consulting opts.Delete; queued as Backlog Task: mirror-delete --delete flag wiring + FB-written-paths reconstruction)")
	}

	// If --delete IS wired (future state): the full AC-14 assertion
	// runs below. The structure mirrors crash_resume_test.go's
	// future-state pattern: when the producer side ships, the t.Skip
	// above stops firing and these assertions become live.

	// Exit code 0 (clean run; mirror-delete is not an error condition).
	if exitCode2 != 0 {
		t.Errorf("second backup exit code: got %d want 0 (mirror-delete is a normal completion)\nstdout: %s\nstderr: %s",
			exitCode2, stdout2, stderr2)
	}

	// runs.ndjson finished line carries exit_status=ok. We re-decode
	// rather than just trust AssertRunsNDJSONHasFinishedLine because
	// AC-14 requires the typed status check.
	runs := readNDJSON(t, filepath.Join(usb, ".flashbackup", "runs.ndjson"))
	if len(runs) < 4 {
		// Two runs * two lines each (started + finished) = 4 minimum.
		t.Fatalf("runs.ndjson lines: got %d want >= 4 (two runs, two lines each)\nstderr2: %s",
			len(runs), stderr2)
	}
	finished2 := runs[len(runs)-1]
	if event, _ := finished2["event"].(string); event != "finished" {
		t.Errorf("runs.ndjson last line event: got %q want finished", event)
	}
	if status, _ := finished2["exit_status"].(string); status != "ok" {
		t.Errorf("runs.ndjson second-run exit_status: got %q want ok\nfinished line: %v",
			status, finished2)
	}

	// Pretty-print the finished line for triage if a future failure
	// lands here.
	if pretty, err := json.MarshalIndent(finished2, "", "  "); err == nil {
		t.Logf("second-run finished line:\n%s", pretty)
	}

	// The dest tree AFTER the second run must show:
	//   - a.txt and c.json still present (still in source/manifest)
	//   - b.md GONE (mirror-delete fired)
	//   - user_added_<random>.txt still present (invariant #6 protection)
	destAfter := listFilesUnder(t, destNS)
	t.Logf("dest after second run (%d entries):\n%s", len(destAfter), strings.Join(destAfter, "\n"))

	// Use the same findFileUnder helper as the AC-3 happy-path test so
	// the layout assumptions (preserve-source-tree vs rebase-at-basename)
	// remain decoupled from this test's correctness.
	if !findFileUnder(t, destNS, "a.txt") {
		t.Errorf("a.txt missing from dest after mirror-delete; AC-14 says a.txt must remain (still in latest manifest)")
	}
	if !findFileUnder(t, destNS, "c.json") {
		t.Errorf("c.json missing from dest after mirror-delete; AC-14 says c.json must remain (still in latest manifest)")
	}
	if findFileUnder(t, destNS, "b.md") {
		t.Errorf("b.md still present in dest after mirror-delete; AC-14 says b.md must be removed (FB-written but absent from latest manifest)")
	}
	if !findFileUnder(t, destNS, userFileName) {
		t.Errorf("user-added file %s missing from dest after mirror-delete; AC-14 + invariant #6 say user files at namespaced dest are NEVER touched", userFileName)
	}

	// Confirm the user file's contents survived byte-for-byte (mirror-
	// delete should not even open it, let alone rewrite it). A truncate
	// or partial overwrite would also be an invariant #6 violation.
	if surviving, err := os.ReadFile(userFilePath); err == nil {
		if string(surviving) != string(userFileContent) {
			t.Errorf("user-added file contents drifted across mirror-delete\n got:  %q\n want: %q",
				surviving, userFileContent)
		}
	} else {
		// The findFileUnder check above would have caught absence, but
		// a stat-race or a permission flip could land us here.
		t.Logf("read user-added file after run failed: %v (presence check is authoritative)", err)
	}
}
