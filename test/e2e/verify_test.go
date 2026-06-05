package e2e

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// verify_test.go covers AC-9 (verify intact backup) and AC-10 (verify
// surfaces correct counters for files with outcomes) end-to-end through
// the cmd layer. Each test mounts a fresh APFS DMG, runs init + backup
// against the tiny fixture (via the shared runBackupForVerify helper),
// then exercises one verify scenario:
//
//	TestE2E_VerifyIntact_HappyPath    (AC-9): backup + verify; exit 0,
//	                                          summary.json + results.ndjson
//	                                          on disk with expected counters.
//	TestE2E_VerifyIntact_MissingFile  (AC-10): backup, delete one dest file,
//	                                           verify; exit 1, files_missing=1.
//	TestE2E_VerifyIntact_HashMismatch (AC-10): backup, mutate one dest file's
//	                                           bytes (same size), verify;
//	                                           exit 1, files_hash_mismatch=1.
//	TestE2E_VerifyIntact_LatestRun:           two backups (two profiles);
//	                                          verify with no run-id picks
//	                                          the latest by lexical sort.
//
// Tagged into the e2e-fast Makefile gate via the "VerifyIntact" run-name
// pattern. Skips cleanly without FLASHBACKUP_E2E=1, without macOS, or
// without a real GNU rsync (the embedded extract is a placeholder shell
// stub until Task 12a; without it, no bytes land on the dest and the
// AC-9/AC-10 assertions cannot hold).
//
// We define summaryRecord LOCALLY rather than import internal/verify so
// a future producer-side schema change must touch this test too;
// silently diverging field names would defeat the AC-9/AC-10 pin.

// summaryRecord is a local mirror of the on-disk per-verify summary.json
// shape. Field names track internal/verify.summaryRecord by hand;
// importing the producer type would let a silent rename pass.
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

// tinyFileCount is the expected files_checked / files_verified count
// after a happy-path backup of the tiny fixture (a.txt, b.md, c.json).
// Pinned so a fixture growth surfaces here rather than masking under a
// "whatever the rehash counted" comparison.
const tinyFileCount = 3

// runBackupForVerify is the shared bootstrap for every verify scenario:
// mount + init the USB, seed the tiny fixture into a fresh source tree
// + a profile, run backup, assert exit 0, return (usb, runID). All
// per-test customizations (extra files, mutation, second backup) layer
// on top of this baseline.
//
// profile is the profile name used for the backup; tests that need two
// distinct backups call this twice with different names.
func runBackupForVerify(t *testing.T, profile string) (string, string) {
	t.Helper()
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, profile, source, []string{"*"}, nil)

	exitCode, stdout, stderr := RunBackup(t, profile, usb)
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}
	return usb, runID
}

// TestE2E_VerifyIntact_HappyPath covers AC-9: a verify against a freshly
// backed-up USB returns exit 0, writes summary.json and results.ndjson
// at the canonical paths, reports all tiny-fixture files as verified,
// and has every failure counter at zero.
func TestE2E_VerifyIntact_HappyPath(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	usb, runID := runBackupForVerify(t, "verify-happy")

	exitCode, stdout, stderr := RunVerify(t, usb)
	if exitCode != 0 {
		t.Fatalf("verify exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	summaryPath := AssertVerifySummaryExists(t, usb, runID)
	rec := readSummaryRecord(t, summaryPath)

	if rec.ExitStatus != "ok" {
		t.Errorf("exit_status: got %q want %q", rec.ExitStatus, "ok")
	}
	if rec.ForRunID != runID {
		t.Errorf("for_run_id: got %q want %q", rec.ForRunID, runID)
	}
	if rec.FilesVerified != tinyFileCount {
		t.Errorf("files_verified: got %d want %d", rec.FilesVerified, tinyFileCount)
	}
	if rec.FilesChecked != tinyFileCount {
		t.Errorf("files_checked: got %d want %d", rec.FilesChecked, tinyFileCount)
	}
	if rec.FilesIntegrityFailed != 0 {
		t.Errorf("files_integrity_failed: got %d want 0", rec.FilesIntegrityFailed)
	}
	if rec.FilesHashMismatch != 0 {
		t.Errorf("files_hash_mismatch: got %d want 0", rec.FilesHashMismatch)
	}
	if rec.FilesMissing != 0 {
		t.Errorf("files_missing: got %d want 0", rec.FilesMissing)
	}
	if rec.FilesSizeMismatch != 0 {
		t.Errorf("files_size_mismatch: got %d want 0", rec.FilesSizeMismatch)
	}
	if rec.FilesUnreadable != 0 {
		t.Errorf("files_unreadable: got %d want 0", rec.FilesUnreadable)
	}

	// results.ndjson exists alongside summary.json with one line per file
	// checked. The verify pipeline always writes results.ndjson (even
	// when empty); for a 3-file backup we expect exactly 3 lines.
	resultsPath := filepath.Join(filepath.Dir(summaryPath), "results.ndjson")
	lineCount := countNDJSONLines(t, resultsPath)
	if lineCount != tinyFileCount {
		t.Errorf("results.ndjson line count: got %d want %d", lineCount, tinyFileCount)
	}
}

// TestE2E_VerifyIntact_MissingFile covers AC-10 (missing file branch):
// backup, delete one file from the namespaced dest, verify; exit 1 +
// files_missing=1.
func TestE2E_VerifyIntact_MissingFile(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	usb, runID := runBackupForVerify(t, "verify-missing")

	// Pick the first manifest entry and delete that file from the dest.
	// Reading the manifest (rather than e.g. picking a known fixture
	// filename) keeps the test honest about what verify actually rehashes:
	// the test fails if the manifest does not list the file we deleted.
	entryPath := firstManifestPath(t, usb, runID)
	destFile := namespacedDestPath(t, usb, entryPath)
	if err := os.Remove(destFile); err != nil {
		t.Fatalf("remove dest file %s: %v", destFile, err)
	}

	exitCode, _, _ := RunVerify(t, usb)
	if exitCode != 1 {
		t.Errorf("verify exit code: got %d want 1 (integrity_failed)", exitCode)
	}

	summaryPath := AssertVerifySummaryExists(t, usb, runID)
	rec := readSummaryRecord(t, summaryPath)

	if rec.ExitStatus != "integrity_failed" {
		t.Errorf("exit_status: got %q want %q", rec.ExitStatus, "integrity_failed")
	}
	if rec.FilesMissing != 1 {
		t.Errorf("files_missing: got %d want 1", rec.FilesMissing)
	}
	if rec.FilesHashMismatch != 0 {
		t.Errorf("files_hash_mismatch: got %d want 0", rec.FilesHashMismatch)
	}
}

// TestE2E_VerifyIntact_HashMismatch covers AC-10 (hash mismatch branch):
// backup, mutate one dest file's bytes keeping size constant, verify;
// exit 1 + files_hash_mismatch=1. Keeping the size constant ensures we
// trip the SHA256 compare rather than the size check (which would
// classify as files_size_mismatch).
func TestE2E_VerifyIntact_HashMismatch(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	usb, runID := runBackupForVerify(t, "verify-mismatch")

	entryPath := firstManifestPath(t, usb, runID)
	destFile := namespacedDestPath(t, usb, entryPath)

	data, err := os.ReadFile(destFile)
	if err != nil {
		t.Fatalf("read dest file %s: %v", destFile, err)
	}
	if len(data) == 0 {
		t.Fatalf("dest file %s is empty; nothing to flip", destFile)
	}
	// Flip the first byte. Same size + different bytes guarantees the
	// SHA256 differs (collision risk is cryptographic-negligible).
	mutated := make([]byte, len(data))
	copy(mutated, data)
	mutated[0] ^= 0xff
	if err := os.WriteFile(destFile, mutated, 0o600); err != nil {
		t.Fatalf("write mutated %s: %v", destFile, err)
	}

	exitCode, _, _ := RunVerify(t, usb)
	if exitCode != 1 {
		t.Errorf("verify exit code: got %d want 1 (integrity_failed)", exitCode)
	}

	summaryPath := AssertVerifySummaryExists(t, usb, runID)
	rec := readSummaryRecord(t, summaryPath)

	if rec.ExitStatus != "integrity_failed" {
		t.Errorf("exit_status: got %q want %q", rec.ExitStatus, "integrity_failed")
	}
	if rec.FilesHashMismatch != 1 {
		t.Errorf("files_hash_mismatch: got %d want 1", rec.FilesHashMismatch)
	}
	if rec.FilesSizeMismatch != 0 {
		t.Errorf("files_size_mismatch: got %d want 0 (same-size flip)",
			rec.FilesSizeMismatch)
	}
	if rec.FilesMissing != 0 {
		t.Errorf("files_missing: got %d want 0", rec.FilesMissing)
	}
}

// TestE2E_VerifyIntact_LatestRun confirms the "no run-id" code path picks
// the latest run by lexical sort. We back up twice into the same USB
// under two profile names (the tiny fixture is copied into each test's
// fresh source dir, so the second backup does not race the first for
// source files), then run verify with no positional and assert the
// for_run_id in the summary matches the lexically-greatest runID on
// disk.
//
// We deliberately compare against the lexically-largest runID rather
// than the second-written one. RunID format is YYYY-MM-DDTHHMMZ-<hex4>
// at minute granularity; two back-to-back backups in the same minute
// land with the same timestamp prefix and only the random hex4 suffix
// differs, so "written second" and "lexically larger" are NOT
// guaranteed to match. The resolver picks lexically-largest (matching
// the runner's pruneOldRunDirs convention); the test must check the
// same property the resolver actually implements.
func TestE2E_VerifyIntact_LatestRun(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Bootstrap with the first backup.
	usb, firstRunID := runBackupForVerify(t, "verify-latest-a")

	// Second backup under a different profile against a separate source
	// tree. Re-using the existing usb (no second mount) keeps the test
	// within the e2e-fast budget; SeedSource gives us a fresh tempdir
	// so the second backup is logically independent of the first.
	source2 := SeedSource(t, "tiny")
	SeedProfile(t, usb, "verify-latest-b", source2, []string{"*"}, nil)
	exitCode, stdout, stderr := RunBackup(t, "verify-latest-b", usb)
	if exitCode != 0 {
		t.Fatalf("second backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}
	secondRunID := lastFinishedRunID(t, usb)
	if secondRunID == "" {
		t.Fatalf("second backup did not produce a finished line in runs.ndjson")
	}
	if secondRunID == firstRunID {
		t.Fatalf("second runID matches first (%s); the runner should have minted a new one", firstRunID)
	}

	// "Latest" is the lexically-largest runID present on disk. With
	// minute-granularity timestamps, the order in which the runner
	// finished is independent of the lexical order; the verify resolver
	// uses lexical sort, so the test asserts against that.
	expectedLatest := firstRunID
	if secondRunID > expectedLatest {
		expectedLatest = secondRunID
	}
	other := firstRunID
	if expectedLatest == firstRunID {
		other = secondRunID
	}

	// Verify with no run-id positional. Should pick expectedLatest.
	exitCode, vstdout, vstderr := RunVerify(t, usb)
	if exitCode != 0 {
		t.Fatalf("verify exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, vstdout, vstderr)
	}

	// Assert the summary landed under the latest run's verifications/
	// subdir. Reading the for_run_id from inside the summary defends
	// against the resolver writing the file to the right dir but
	// recording the wrong run-id in the body (would be a silent bug
	// downstream tooling would never notice).
	summaryPath := AssertVerifySummaryExists(t, usb, expectedLatest)
	rec := readSummaryRecord(t, summaryPath)
	if rec.ForRunID != expectedLatest {
		t.Errorf("for_run_id: got %q want %q (latest by lexical sort; first=%s second=%s)",
			rec.ForRunID, expectedLatest, firstRunID, secondRunID)
	}

	// And confirm the OTHER run was NOT verified (no summary.json
	// landed inside its verifications/ subdir). We tolerate an empty or
	// absent verifications/ directory; the substantive check is that no
	// summary.json appears.
	otherVerDir := filepath.Join(usb, ".flashbackup", "runs", other, "verifications")
	if entries, err := os.ReadDir(otherVerDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() {
				continue
			}
			sumPath := filepath.Join(otherVerDir, e.Name(), "summary.json")
			if _, err := os.Stat(sumPath); err == nil {
				t.Errorf("other run %s unexpectedly has a summary.json at %s; verify should have picked the lexically-largest run %s",
					other, sumPath, expectedLatest)
			}
		}
	}
}

// readSummaryRecord reads + parses a summary.json into the LOCAL
// summaryRecord shape. Using a typed unmarshal (not map[string]any) means
// a producer-side rename surfaces here as a zero-valued field rather
// than a silent type assertion miss.
func readSummaryRecord(t *testing.T, path string) summaryRecord {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read summary %s: %v", path, err)
	}
	var rec summaryRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("unmarshal summary %s: %v", path, err)
	}
	return rec
}

// firstManifestPath gunzips the per-run manifest.ndjson.gz, parses the
// first entry, and returns its Path field (forward-slash, the value the
// runner wrote). Used by the mutation / deletion tests to pick a real
// target rather than guessing a fixture filename.
//
// Reading the manifest also serves as an indirect assertion that the
// backup actually wrote one; a malformed/empty manifest would surface
// here as a parse error.
func firstManifestPath(t *testing.T, usb, runID string) string {
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
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatalf("manifest %s has no entries", manifestGz)
	}
	var e state.ManifestEntry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatalf("unmarshal first manifest entry: %v\nline: %s", err, lines[0])
	}
	if e.Path == "" {
		t.Fatalf("first manifest entry has empty Path: %+v", e)
	}
	return e.Path
}

// namespacedDestPath returns the absolute on-disk path of a manifest
// entry by joining the USB root with paths.Namespaced(host, user,
// entryPath). Mirrors what rehash.Rehash computes; if a future
// refactor changes the namespacing recipe, this helper must change too.
//
// Host comes from os.Hostname (preflight's source) and user from
// `whoami` (mirroring backup_happy_test.go's pattern, which avoids the
// os.Getenv("USER") fallback that could differ from user.Current()).
func namespacedDestPath(t *testing.T, usb, entryPath string) string {
	t.Helper()
	hostname, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	//nolint:gosec // bounded: /usr/bin/whoami absolute path, no args
	uname, err := exec.Command("/usr/bin/whoami").Output()
	if err != nil {
		t.Fatalf("whoami: %v", err)
	}
	username := strings.TrimSpace(string(uname))
	return paths.Namespaced(usb, hostname, username, filepath.FromSlash(entryPath))
}

// countNDJSONLines returns the number of non-empty lines in an NDJSON
// file. Used by the happy-path test to confirm results.ndjson has one
// record per file (rather than re-running the per-record schema check
// the AssertVerifySummaryExists path already exercises).
func countNDJSONLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	count := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line != "" {
			count++
		}
	}
	return count
}

// lastFinishedRunID returns the run_id of the LAST "finished" line in
// runs.ndjson. Mirrors AssertRunsNDJSONHasFinishedLine but returns the
// id without failing the test on absence (the latest-run test needs to
// chain two backups and inspect only the trailing one).
func lastFinishedRunID(t *testing.T, usb string) string {
	t.Helper()
	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	data, err := os.ReadFile(runsPath)
	if err != nil {
		t.Fatalf("read runs.ndjson: %v", err)
	}
	var lastID string
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var rec struct {
			Event string `json:"event"`
			RunID string `json:"run_id"`
		}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("parse runs.ndjson line %q: %v", line, err)
		}
		if rec.Event == "finished" && rec.RunID != "" {
			lastID = rec.RunID
		}
	}
	return lastID
}
