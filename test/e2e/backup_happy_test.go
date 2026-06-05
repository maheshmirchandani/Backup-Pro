package e2e

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// backup_happy_test.go covers AC-3 (design spec): a happy-path copy-mode
// backup over the tiny fixture tree produces the documented on-disk
// surface:
//
//	1. runs.ndjson has exactly TWO lines: "started" then "finished" with
//	   matching run_id and ExitStatus="ok".
//	2. .flashbackup/runs/<runID>/manifest.ndjson.gz exists.
//	3. .flashbackup/runs/<runID>/events.ndjson contains BOTH phase_started
//	   AND phase_completed kinds for every phase: T0, T0+, T1, T2, T3, T4.
//	4. The three tiny-fixture files land under <USB>/<host>-<user>/... with
//	   the namespace dir computed via paths.Prefix (single source of truth
//	   per invariant #15).
//
// Tagged into the e2e-fast Makefile gate via the "BackupHappy" run-name
// pattern. Skips cleanly without FLASHBACKUP_E2E=1, without macOS, or
// without a real GNU rsync on the box (embedded extract is a placeholder
// shell stub until Task 12a lands the real binary).

// TestE2E_BackupHappy_CopyMode is the AC-3 happy-path assertion: init +
// seed the tiny fixture + seed a profile + run backup; expect exit 0,
// manifest.ndjson.gz on disk, runs.ndjson exactly 2 lines with the
// second being the "finished" line carrying exit_status=ok and the
// matching run_id, and events.ndjson with phase_started+phase_completed
// for every phase from T0 through T4.
func TestE2E_BackupHappy_CopyMode(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Find a real GNU rsync; the embedded extract is a placeholder shell
	// stub that exits 0 without copying bytes. Without a real rsync the
	// happy-path assertions (manifest contents, dest files, exit_status=ok)
	// cannot hold, so we skip rather than pretend. Apple's openrsync at
	// /usr/bin/rsync is rsync-2.6.9 compatible and lacks --from0 / --xattrs,
	// so only the Homebrew paths are considered.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB via SetupUSB (size hint ignored; testutil
	// hard-codes 10 MB which fits the tiny fixture's 20 bytes trivially).
	usb := SetupUSB(t, 64)

	// Seed source: copy the tiny fixture tree into a fresh tempdir.
	source := SeedSource(t, "tiny")

	// Seed profile: includes=["*"] matches every top-level entry in the
	// source. The validator accepts "*" (single segment, no leading slash,
	// no "..", no NUL, no "**").
	SeedProfile(t, usb, "test-happy", source, []string{"*"}, nil)

	// Run backup.
	exitCode, stdout, stderr := RunBackup(t, "test-happy", usb)
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// runs.ndjson is exactly 2 lines: started + finished. The shared helper
	// AssertRunsNDJSONHasFinishedLine confirms the finished line is present
	// and returns its run_id; we follow up with a byte-level decode of the
	// last line into a typed FinishedRun-shaped struct so a future field
	// rename surfaces as a parse failure here.
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}

	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	runsData, err := os.ReadFile(runsPath)
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(runsData, "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("runs.ndjson lines: got %d want 2\nstdout: %s\nstderr: %s\ndata: %s",
			len(lines), stdout, stderr, runsData)
	}
	var started struct {
		Event string `json:"event"`
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(lines[0], &started); err != nil {
		t.Fatalf("unmarshal started line: %v", err)
	}
	if started.Event != "started" {
		t.Errorf("line[0] event: got %q want started", started.Event)
	}
	if started.RunID != runID {
		t.Errorf("started run_id mismatch: got %q want %q", started.RunID, runID)
	}

	// Local typed view of the FinishedRun. We intentionally do NOT import
	// internal/state here so a future schema bump is forced to update this
	// test alongside the producer; silent drift would defeat the AC-3 pin.
	var finished struct {
		Event      string `json:"event"`
		ExitStatus string `json:"exit_status"`
		RunID      string `json:"run_id"`
	}
	if err := json.Unmarshal(lines[1], &finished); err != nil {
		t.Fatalf("unmarshal finished line: %v", err)
	}
	if finished.Event != "finished" {
		t.Errorf("line[1] event: got %q want finished", finished.Event)
	}
	if finished.ExitStatus != "ok" {
		t.Errorf("exit_status: got %q want ok\nstderr: %s", finished.ExitStatus, stderr)
	}
	if finished.RunID != runID {
		t.Errorf("finished run_id mismatch: got %q want %q", finished.RunID, runID)
	}

	// manifest.ndjson.gz on disk at the per-run path.
	AssertManifestExists(t, usb, runID)

	// events.ndjson contains phase_started + phase_completed for every
	// phase T0, T0+, T1, T2, T3, T4. We parse line-by-line (rather than
	// substring-matching) because a phase-start event's "phase" field
	// could overlap with a different event's free-text payload; structured
	// inspection is the only reliable check.
	eventsPath := filepath.Join(usb, ".flashbackup", "runs", runID, "events.ndjson")
	events, err := os.ReadFile(eventsPath)
	if err != nil {
		t.Fatalf("read events.ndjson: %v", err)
	}
	startedPhases := map[string]bool{}
	completedPhases := map[string]bool{}
	for _, line := range strings.Split(string(events), "\n") {
		if line == "" {
			continue
		}
		var ev map[string]any
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Fatalf("parse events.ndjson line %q: %v", line, err)
		}
		kind, _ := ev["kind"].(string)
		phase, _ := ev["phase"].(string)
		switch kind {
		case "phase_started":
			startedPhases[phase] = true
		case "phase_completed":
			completedPhases[phase] = true
		}
	}
	for _, phase := range []string{"T0", "T0+", "T1", "T2", "T3", "T4"} {
		if !startedPhases[phase] {
			t.Errorf("events.ndjson missing phase_started for %q", phase)
		}
		if !completedPhases[phase] {
			t.Errorf("events.ndjson missing phase_completed for %q", phase)
		}
	}

	// The three tiny-fixture files (a.txt, b.md, c.json) land under the
	// namespaced dest dir <USB>/<host>-<user>/... mirroring the source
	// layout. We compute the namespace via paths.Prefix (replaces dots in
	// hostname/username with hyphens) so the assertion stays in lockstep
	// with what the runner computed (single source of truth per
	// invariant #15).
	hostname, _ := os.Hostname()
	uname, _ := exec.Command("/usr/bin/whoami").Output()
	username := strings.TrimSpace(string(uname))
	nsDir := paths.Prefix(hostname, username)

	// The source dir is a tempdir like /var/folders/.../TestE2E_.../001.
	// rsync with the runner's filter set lays files under the dest
	// keeping the source tree's relative layout; we read the source dir
	// to discover what files actually got seeded (defends against the
	// fixture growing a new file in the future) and assert each one
	// exists under the namespaced dest.
	srcEntries, err := os.ReadDir(source)
	if err != nil {
		t.Fatalf("read source dir %s: %v", source, err)
	}
	// Locate the source-rooted subpath inside the dest. The runner roots
	// the copy at the source basename relative to its parent; we scan the
	// namespaced dir tree for each fixture file by basename so the test
	// stays robust to internal layout decisions (mirror-of-source vs
	// rebased-at-basename).
	for _, e := range srcEntries {
		if e.IsDir() {
			continue
		}
		if !findFileUnder(t, filepath.Join(usb, nsDir), e.Name()) {
			t.Errorf("dest file %q not found under %s (namespaced dest dir)",
				e.Name(), filepath.Join(usb, nsDir))
		}
	}
}

// findGNURsync returns the absolute path to a real GNU rsync 3.x binary
// suitable for the FLASHBACKUP_RSYNC_PATH_FOR_TEST seam, or "" if no
// such binary is on the box. Apple's openrsync at /usr/bin/rsync is
// 2.6.9-compatible and lacks the flags the runner needs (--from0,
// --xattrs); only Homebrew paths qualify.
func findGNURsync() string {
	for _, candidate := range []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"} {
		if _, err := os.Stat(candidate); err != nil {
			continue
		}
		out, err := exec.Command(candidate, "--version").Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(out), "openrsync") {
			continue
		}
		return candidate
	}
	return ""
}

// findFileUnder reports whether a regular file named name exists anywhere
// under root. Used to confirm a fixture file landed in the namespaced
// dest dir without coupling the test to the exact subdir layout the
// runner chose; if the runner ever flips between "preserve-source-tree"
// and "rebase-at-basename" the existence check still holds.
func findFileUnder(t *testing.T, root, name string) bool {
	t.Helper()
	found := false
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			// Walk errors mid-traversal (e.g., a transient permission
			// hiccup) shouldn't fail the test outright; record and
			// continue so we still get the existence verdict.
			return nil
		}
		if !info.IsDir() && info.Name() == name {
			found = true
		}
		return nil
	})
	if err != nil {
		t.Logf("walk %s: %v (treated as not-found)", root, err)
	}
	return found
}
