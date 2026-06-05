//go:build faultinject

package e2e

// mutation_test.go covers AC-5 (T2 source-mutation gate; file edited
// during the T1 -> T2 window is classified source_mutated and skipped)
// and AC-6 (T3 mutation re-stat; file mutated again between T2 and the
// T3 unlink is detected and NOT deleted). Both tests drive the runner
// end-to-end through the faultinject CLI binary with mutate-source DSL
// specs against the tiny fixture.
//
// Test mechanics shared by both functions:
//
//  1. Mount + init a fresh APFS DMG, seed the tiny fixture into a
//     tempdir, and store a profile that includes the source.
//  2. Snapshot the source file list and per-file sizes BEFORE the run so
//     the post-run assertions can prove which files were touched.
//  3. Run `flashbackup-faultinject backup --inject=mutate-source:
//     phase=<P>:file=a.txt <mode>? <profile> <usb>` with the right
//     stdin for the mode (DELETE\n for move, empty for copy).
//  4. Assert exit code, runs.ndjson exit_status, the manifest /
//     deletion-log forensic surface, and source-file presence.
//
// Phase selection notes:
//
//   - AC-5 uses phase=T2-pre. The Hook fires at PointT2PreHash per
//     file; when args.CurrentFile == "a.txt" the mutate-source action
//     appends a byte to the source. The runner then re-stats the source
//     at the top of t3ClassifyFile, sees (size, mtime_ns) drift from
//     the T0+ baseline Signature, and returns StatusSourceMutated.
//     Manifest line for a.txt has status="source_mutated"; the other
//     two files verify cleanly. FilesVerified (2) < FilesTotal (3),
//     FilesFailed (1) > 0 AND FilesSucceeded (2) > 0 → ExitStatusPartial
//     → exit code 1.
//
//   - AC-6 uses phase=T3-pre. The Hook fires at PointT3PreUnlink AFTER
//     T2 has verified every file (no T2 fault); when args.CurrentFile
//     == "a.txt" the mutate-source action appends a byte to the source.
//     The runner then calls t4AttemptDelete which Lstats the source,
//     compares (size, mtime_ns) against the baseline Signature, sees
//     drift, and returns DeletionSkippedMutated. The fault is one-shot
//     (single armed bit per spec, cleared after first fire) and the
//     file=a.txt selector pins the match, so b.md and c.json proceed
//     through their unlinks unaffected. Manifest is finalized OK; the
//     deletion-log records a.txt as skipped_mutated and b.md + c.json
//     as deleted. Exit status: 3 verified, 0 failed (deletion failures
//     do NOT count as FilesFailed at the runner level; FilesFailed
//     tracks T2 outcomes) → ExitStatusOK → exit code 0.
//
// Tagged into the e2e-safety Makefile gate via the "Mutation" run-name
// pattern. Build-tagged faultinject so the test never compiles into a
// release-shape go test invocation.

import (
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_Mutation_T2GateClassifiesSourceMutated is the AC-5 end-to-end
// assertion. See file-level comment for the full mechanic; the test
// body is a tight init -> seed -> run -> assert sequence.
func TestE2E_Mutation_T2GateClassifiesSourceMutated(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Real GNU rsync is required. Apple's openrsync (2.6.9 compatible)
	// lacks the flags the runner needs (--from0, --xattrs); the embedded
	// placeholder stub at this stage of the plan exits 0 without copying
	// bytes which would make T2 see every file as not_transferred rather
	// than verified, and the AC-5 invariant under test would never get a
	// chance to fire.
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
	SeedProfile(t, usb, "mutation-t2-test", source, []string{"*"}, nil)

	// Snapshot source state BEFORE the run. We use this only to confirm
	// that the source set has not lost any files (copy mode never
	// touches the source; the AC-5 assertion is about classification,
	// not about source touch).
	sourceBefore := listFilesUnder(t, source)
	if len(sourceBefore) < 3 {
		t.Fatalf("seeded source missing files: %v (want a.txt, b.md, c.json)", sourceBefore)
	}

	// Run faultinject-tagged backup in COPY mode (no --move) with the
	// T2-pre mutate-source fault on a.txt. Empty stdin: copy mode does
	// not gate behind the DELETE confirmation.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"mutation-t2-test", usb,
		nil, // no --move; AC-5 is copy-mode
		[]string{"mutate-source:phase=T2-pre:file=a.txt"},
		"",
	)

	// Exit code 1 = backupExitCodeRuntime, the translation of
	// ExitStatusPartial (per cmd/flashbackup/backup_helpers.go).
	if exitCode != 1 {
		t.Errorf("backup exit code: got %d want 1 (partial)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// runs.ndjson finished line must carry exit_status=partial.
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
	if status, _ := finished["exit_status"].(string); status != "partial" {
		t.Errorf("runs.ndjson exit_status: got %q want partial\nfinished line: %v",
			status, finished)
	}

	// Manifest line for a.txt must record status=source_mutated. The
	// other two entries must be status=verified. We gunzip the per-run
	// manifest and decode every line into a typed ManifestEntry so a
	// future schema bump surfaces here as a parse failure, not as a
	// silent map-lookup drift.
	entries := readManifestEntries(t, usb, runID)
	if len(entries) != 3 {
		t.Fatalf("manifest entries: got %d want 3 (a.txt, b.md, c.json)\nentries: %+v",
			len(entries), entries)
	}
	byPath := make(map[string]state.ManifestEntry, len(entries))
	for _, e := range entries {
		byPath[e.Path] = e
	}
	aEntry, ok := byPath["a.txt"]
	if !ok {
		t.Fatalf("manifest missing entry for a.txt; saw paths: %v", manifestPaths(entries))
	}
	if aEntry.Status != state.StatusSourceMutated {
		t.Errorf("a.txt manifest status: got %q want %q", aEntry.Status, state.StatusSourceMutated)
	}
	// source_mutated entries carry an empty SHA256Source (the runner
	// short-circuits before hashing); pin that here so a future relax of
	// the t3ClassifyFile contract is caught.
	if aEntry.SHA256Source != "" {
		t.Errorf("a.txt SHA256Source on source_mutated entry: got %q want \"\"", aEntry.SHA256Source)
	}
	for _, name := range []string{"b.md", "c.json"} {
		e, ok := byPath[name]
		if !ok {
			t.Errorf("manifest missing entry for %s; saw paths: %v", name, manifestPaths(entries))
			continue
		}
		if e.Status != state.StatusVerified {
			t.Errorf("%s manifest status: got %q want %q (unaffected by a.txt fault)",
				name, e.Status, state.StatusVerified)
		}
	}

	// events.ndjson must contain a source_mutated kind for a.txt.
	eventsPath := filepath.Join(usb, ".flashbackup", "runs", runID, "events.ndjson")
	events := readNDJSON(t, eventsPath)
	sawSourceMutated := false
	for _, ev := range events {
		if k, _ := ev["kind"].(string); k != "source_mutated" {
			continue
		}
		if p, _ := ev["path"].(string); p == "a.txt" {
			sawSourceMutated = true
			break
		}
	}
	if !sawSourceMutated {
		kinds := make([]string, 0, len(events))
		for _, ev := range events {
			if k, _ := ev["kind"].(string); k != "" {
				kinds = append(kinds, k)
			}
		}
		t.Errorf("events.ndjson missing source_mutated kind for a.txt; saw kinds: %v", kinds)
	}

	// Copy mode does NOT touch the source. The fault appended a byte to
	// a.txt so listFilesUnder will return the same names but a.txt's
	// size will have grown. We assert both invariants: every source file
	// is still on disk, AND no NEW file appeared and none disappeared.
	sourceAfter := listFilesUnder(t, source)
	if !stringSlicesEqual(sourceBefore, sourceAfter) {
		t.Errorf("copy-mode run changed source file LIST; AC-5 implies classification only\nbefore: %v\nafter:  %v",
			sourceBefore, sourceAfter)
	}
}

// TestE2E_Mutation_T3ReStatSkipsUnlink is the AC-6 end-to-end assertion.
// See file-level comment for the full mechanic.
func TestE2E_Mutation_T3ReStatSkipsUnlink(t *testing.T) {
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
	SeedProfile(t, usb, "mutation-t3-test", source, []string{"*"}, nil)

	// Snapshot source state BEFORE the run. The AC-6 assertion is that
	// a.txt SURVIVES (T3 re-stat catches the post-T2 mutation) while
	// b.md and c.json are unlinked by the move-mode T3 phase.
	sourceBefore := listFilesUnder(t, source)
	if len(sourceBefore) < 3 {
		t.Fatalf("seeded source missing files: %v (want a.txt, b.md, c.json)", sourceBefore)
	}

	// Run faultinject-tagged backup in MOVE mode with --move and the
	// T3-pre mutate-source fault on a.txt. DELETE\n on stdin authorizes
	// the move-mode confirmation gate. Move mode is what activates the
	// T3 delete-source phase that AC-6 exercises.
	exitCode, stdout, stderr := RunBackupFaultinject(t,
		"mutation-t3-test", usb,
		[]string{"--move"},
		[]string{"mutate-source:phase=T3-pre:file=a.txt"},
		"DELETE\n",
	)

	// Expected: ExitStatusOK -> exit code 0. The T2 phase verifies all
	// three files cleanly (no T2 fault); the T3 atomic gate opens
	// (FilesVerified == FilesTotal). Inside the T3 per-file loop, the
	// fault mutates a.txt before its Remove; t4AttemptDelete's re-stat
	// catches the drift and returns DeletionSkippedMutated. Skipped-
	// mutated does NOT count as FilesFailed at the runner level
	// (FilesFailed tracks T2 outcomes only; see runner.go:301-302), so
	// the run is OK from the run-level perspective.
	if exitCode != 0 {
		t.Errorf("backup exit code: got %d want 0 (ok)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}
	runs := readNDJSON(t, filepath.Join(usb, ".flashbackup", "runs.ndjson"))
	finished := runs[len(runs)-1]
	if status, _ := finished["exit_status"].(string); status != "ok" {
		t.Errorf("runs.ndjson exit_status: got %q want ok\nfinished line: %v",
			status, finished)
	}

	// Source-side ground truth:
	//   - a.txt MUST still exist (T3 re-stat protected it).
	//   - b.md and c.json MUST be gone (unaffected by the file=a.txt
	//     selector and unlinked normally).
	if _, err := os.Stat(filepath.Join(source, "a.txt")); err != nil {
		t.Errorf("source/a.txt missing after run; AC-6 re-stat gate failed: %v", err)
	}
	for _, name := range []string{"b.md", "c.json"} {
		p := filepath.Join(source, name)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("source/%s still present after move-mode run; expected unlink", name)
		} else if !os.IsNotExist(err) {
			t.Errorf("source/%s stat error (expected ENOENT): %v", name, err)
		}
	}

	// deletion-log.ndjson must record a.txt as skipped_mutated and
	// b.md + c.json as deleted. We decode every line and bucket by path
	// so an extra/missing entry surfaces with a clear count failure.
	delLog := filepath.Join(usb, ".flashbackup", "runs", runID, "deletion-log.ndjson")
	lines := readNDJSON(t, delLog)
	if len(lines) != 3 {
		t.Fatalf("deletion-log entries: got %d want 3\nlines: %v", len(lines), lines)
	}
	byPath := make(map[string]string, len(lines))
	for _, l := range lines {
		p, _ := l["path"].(string)
		s, _ := l["status"].(string)
		byPath[p] = s
	}
	if got := byPath["a.txt"]; got != string(state.DeletionSkippedMutated) {
		t.Errorf("deletion-log a.txt status: got %q want %q", got, state.DeletionSkippedMutated)
	}
	for _, name := range []string{"b.md", "c.json"} {
		if got := byPath[name]; got != string(state.DeletionDeleted) {
			t.Errorf("deletion-log %s status: got %q want %q",
				name, got, state.DeletionDeleted)
		}
	}

	// events.ndjson should carry a delete_skipped_mutated for a.txt and
	// delete_completed for the other two. This is the audit counterpart
	// to the deletion-log; the two surfaces must agree.
	eventsPath := filepath.Join(usb, ".flashbackup", "runs", runID, "events.ndjson")
	events := readNDJSON(t, eventsPath)
	sawSkipped := false
	deletedSeen := map[string]bool{}
	for _, ev := range events {
		kind, _ := ev["kind"].(string)
		path, _ := ev["path"].(string)
		switch kind {
		case "delete_skipped_mutated":
			if path == "a.txt" {
				sawSkipped = true
			}
		case "delete_completed":
			deletedSeen[path] = true
		}
	}
	if !sawSkipped {
		t.Errorf("events.ndjson missing delete_skipped_mutated for a.txt")
	}
	for _, name := range []string{"b.md", "c.json"} {
		if !deletedSeen[name] {
			t.Errorf("events.ndjson missing delete_completed for %s", name)
		}
	}

	// Sanity: source-list drift before/after should equal {b.md, c.json}
	// removed. Computed as a set diff so the assertion is order-agnostic.
	sourceAfter := listFilesUnder(t, source)
	gone := stringSliceDiff(sourceBefore, sourceAfter)
	want := []string{"b.md", "c.json"}
	if !stringSlicesEqual(gone, want) {
		t.Errorf("source files removed: got %v want %v", gone, want)
	}
}

// readManifestEntries gunzips <usb>/.flashbackup/runs/<runID>/manifest.
// ndjson.gz, decodes each line into a state.ManifestEntry, and returns
// the slice in file order. Used by AC-5 to assert per-file Status
// classifications. Helper duplicated from verify_test.go's
// firstManifestPath (that one returns only the first Path) because the
// AC-5 assertion needs every entry.
func readManifestEntries(t *testing.T, usb, runID string) []state.ManifestEntry {
	t.Helper()
	manifestGz := filepath.Join(usb, ".flashbackup", "runs", runID, "manifest.ndjson.gz")
	f, err := os.Open(manifestGz)
	if err != nil {
		t.Fatalf("open manifest %s: %v", manifestGz, err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader %s: %v", manifestGz, err)
	}
	defer gr.Close()
	raw, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read manifest body: %v", err)
	}
	var entries []state.ManifestEntry
	for _, line := range strings.Split(strings.TrimRight(string(raw), "\n"), "\n") {
		if line == "" {
			continue
		}
		var e state.ManifestEntry
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			t.Fatalf("unmarshal manifest entry %q: %v", line, err)
		}
		entries = append(entries, e)
	}
	return entries
}

// manifestPaths returns the Path field of each entry; used only for
// failure-message context so an "entry missing for X" error names the
// paths that ARE present.
func manifestPaths(entries []state.ManifestEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Path)
	}
	return out
}

// stringSliceDiff returns the sorted slice of strings that appear in
// `before` but not in `after`. Caller must pass pre-sorted slices
// (listFilesUnder already sorts). Used by the AC-6 assertion to compute
// "which source files disappeared" without coupling to map iteration
// order.
func stringSliceDiff(before, after []string) []string {
	have := make(map[string]struct{}, len(after))
	for _, s := range after {
		have[s] = struct{}{}
	}
	var out []string
	for _, s := range before {
		if _, ok := have[s]; !ok {
			out = append(out, s)
		}
	}
	return out
}
