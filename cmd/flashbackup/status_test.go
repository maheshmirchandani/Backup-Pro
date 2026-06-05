package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// ----------------------------------------------------------------------------
// Unit tests (no DMG; exercise argv parsing + error paths)
//
// These cover the cmd-side seam: argv parsing, --json flag, USB-path
// resolution. The disk-read helpers (readLockStatus, countRetainedRuns,
// readLastRun, readLastVerify) are covered by the e2e suite below because
// they exercise the actual on-disk layout the runner produces.
// ----------------------------------------------------------------------------

// TestStatus_MissingDestArg: bare `flashbackup status` (no positionals) must
// exit 2 with a usage block on stderr. Matches the init / verify subcommand
// convention for the most common operator typo.
func TestStatus_MissingDestArg(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status"})
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, statusExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "missing <USB-path>") {
		t.Errorf("stderr should explain missing arg, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestStatus_HelpFlag: `flashbackup status --help` must exit 0 (deliberate
// help request) and print the usage block to stderr.
func TestStatus_HelpFlag(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", "--help"})
	if code != statusExitCodeOK {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on --help, got %q", stdout)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should contain usage block, got %q", stderr)
	}
	if !strings.Contains(stderr, "--json") {
		t.Errorf("stderr should mention --json flag, got %q", stderr)
	}
}

// TestStatus_UnknownFlag: an unknown flag must exit 2 with the usage block.
// The flag.FlagSet writes its own per-flag error line; we only assert on
// the exit code + the usage anchor.
func TestStatus_UnknownFlag(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "status", "--jsonn", "/tmp"})
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, statusExitCodeUsage)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestStatus_TooManyArgs: more than one positional after the flag is an
// operator-fixable usage error (likely a missing --flag value mistake).
func TestStatus_TooManyArgs(t *testing.T) {
	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "status", "/tmp/a", "/tmp/b"})
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, statusExitCodeUsage)
	}
	if !strings.Contains(stderr, "unexpected extra arguments") {
		t.Errorf("stderr should mention extra args, got %q", stderr)
	}
}

// TestStatus_NonexistentPath: a USB path that does not exist on disk must
// exit 2 from the EvalSymlinks step. Matches init / backup / verify.
func TestStatus_NonexistentPath(t *testing.T) {
	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "status", "/nonexistent/never/will-exist-status-test"})
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, statusExitCodeUsage)
	}
	if !strings.Contains(stderr, "flashbackup status:") {
		t.Errorf("stderr should be prefixed with 'flashbackup status:', got %q", stderr)
	}
}

// TestStatus_PathIsFile: USB path that resolves to a regular file (not a
// directory) must exit 2. Catches the operator who passed a single file
// path by mistake.
func TestStatus_PathIsFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, []string{"flashbackup", "status", tmpFile})
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, statusExitCodeUsage)
	}
	if !strings.Contains(stderr, "not a directory") {
		t.Errorf("stderr should mention 'not a directory', got %q", stderr)
	}
}

// TestRunStatus_DirectCall_NoStdin: exercise runStatus directly with a nil
// stdin to confirm the stdin parameter is accepted but not consumed. Matches
// the verify subcommand's handler-signature regression guard.
func TestRunStatus_DirectCall_NoStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runStatus(context.Background(), []string{}, nil, &stdout, &stderr)
	if code != statusExitCodeUsage {
		t.Errorf("exit code: got %d, want %d (missing USB path)", code, statusExitCodeUsage)
	}
}

// TestHumanizeBytes covers the four regimes plus the negative-defensive arm.
// The bare-bytes case asserts a concrete numeric format; the kilo/mega/giga
// cases assert the one-decimal-place SI rendering. A future format tweak
// (e.g. binary IEC units) must update both this test and the helper.
func TestHumanizeBytes(t *testing.T) {
	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0 B"},
		{"negative defensive", -5, "0 B"},
		{"sub-kilo", 999, "999 B"},
		{"exact kilo", 1_000, "1.0 KB"},
		{"mid-kilo", 1_500, "1.5 KB"},
		{"sub-mega", 999_999, "1000.0 KB"},
		{"exact mega", 1_000_000, "1.0 MB"},
		{"mid-mega", 982_000_000, "982.0 MB"},
		{"exact giga", 1_000_000_000, "1.0 GB"},
		{"big giga", 487_000_000_000, "487.0 GB"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := humanizeBytes(tc.in)
			if got != tc.want {
				t.Errorf("humanizeBytes(%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestReadLockStatus covers both branches (file present -> held, file
// absent -> free). The held case writes a real (non-flock'd) lock file at
// the canonical path so the helper's stat-only contract is exercised
// without depending on the live lock package.
func TestReadLockStatus(t *testing.T) {
	tmpDir := t.TempDir()
	lockPath := filepath.Join(tmpDir, lockBasename)
	if got := readLockStatus(lockPath); got != "free" {
		t.Errorf("absent lock: got %q, want %q", got, "free")
	}
	if err := os.WriteFile(lockPath, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := readLockStatus(lockPath); got != "held" {
		t.Errorf("present lock: got %q, want %q", got, "held")
	}
}

// TestCountRetainedRuns plants a mix of canonical-pattern dirs, non-canonical
// dirs, and a stray regular file under runs/. Only the canonical-pattern
// dirs must be counted.
func TestCountRetainedRuns(t *testing.T) {
	tmpDir := t.TempDir()
	runsDir := filepath.Join(tmpDir, "runs")
	canonical := []string{
		"2026-06-04T1000Z-aaaa",
		"2026-06-04T1100Z-bbbb",
		"2026-06-04T1200Z-cccc",
	}
	for _, name := range canonical {
		if err := os.MkdirAll(filepath.Join(runsDir, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Non-canonical dir: should NOT be counted.
	if err := os.MkdirAll(filepath.Join(runsDir, "stray-dir"), 0o700); err != nil {
		t.Fatal(err)
	}
	// Stray file with a canonical-looking name: should NOT be counted
	// (not a dir).
	if err := os.WriteFile(filepath.Join(runsDir, "2026-06-04T1300Z-dddd"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	if got := countRetainedRuns(tmpDir); got != len(canonical) {
		t.Errorf("countRetainedRuns: got %d, want %d", got, len(canonical))
	}
}

// TestCountRetainedRuns_MissingDir: a runs/ that has not been created yet
// (fresh USB) returns 0 rather than an error.
func TestCountRetainedRuns_MissingDir(t *testing.T) {
	if got := countRetainedRuns(t.TempDir()); got != 0 {
		t.Errorf("missing runs dir: got %d, want 0", got)
	}
}

// TestReadLastRun_AbsentFile: a missing runs.ndjson returns nil so the
// caller can drop last_run from the JSON output via omitempty.
func TestReadLastRun_AbsentFile(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "runs.ndjson")
	if got := readLastRun(missing); got != nil {
		t.Errorf("absent runs.ndjson: got %+v, want nil", got)
	}
}

// TestReadLastRun_OnlyStarted: a runs.ndjson with only a started line (the
// run crashed before AppendFinished) returns nil; invariant #10 says
// crashed runs are observable by absence of a finished line. The next
// observer (verify, status) treats them as not-yet-completed.
func TestReadLastRun_OnlyStarted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.ndjson")
	store, err := state.NewNDJSONRunLogStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.AppendStarted(context.Background(), state.StartedRun{
		V:                  1,
		RunID:              "2026-06-04T1200Z-aaaa",
		StartedAt:          time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
		Mode:               "copy",
		SourceRoot:         "/src",
		DestRoot:           "/dst",
		FlashbackupVersion: "0.1.0-core",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := readLastRun(path); got != nil {
		t.Errorf("only-started runs.ndjson: got %+v, want nil", got)
	}
}

// TestReadLastRun_PicksLastFinished: a runs.ndjson with multiple finished
// lines must return the LAST one (the most-recently-written, which matches
// the chronological latest because runs.ndjson is append-only).
func TestReadLastRun_PicksLastFinished(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "runs.ndjson")
	store, err := state.NewNDJSONRunLogStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	if err := store.AppendFinished(ctx, state.FinishedRun{
		V:              1,
		RunID:          "2026-06-04T1000Z-aaaa",
		StartedAt:      time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC),
		FinishedAt:     time.Date(2026, 6, 4, 10, 5, 0, 0, time.UTC),
		Mode:           "copy",
		Profile:        "older",
		ExitStatus:     "ok",
		FilesTotal:     10,
		FilesSucceeded: 10,
		BytesTotal:     1024,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendFinished(ctx, state.FinishedRun{
		V:              1,
		RunID:          "2026-06-04T1200Z-bbbb",
		StartedAt:      time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
		FinishedAt:     time.Date(2026, 6, 4, 12, 30, 0, 0, time.UTC),
		Mode:           "move",
		Profile:        "newer",
		ExitStatus:     "ok",
		FilesTotal:     20,
		FilesSucceeded: 20,
		BytesTotal:     2048,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}

	got := readLastRun(path)
	if got == nil {
		t.Fatal("readLastRun returned nil; want the newer entry")
	}
	if got.RunID != "2026-06-04T1200Z-bbbb" {
		t.Errorf("RunID: got %q, want the newer entry", got.RunID)
	}
	if got.Profile != "newer" {
		t.Errorf("Profile: got %q, want %q", got.Profile, "newer")
	}
	if got.Mode != "move" {
		t.Errorf("Mode: got %q, want %q", got.Mode, "move")
	}
}

// TestReadLastVerify_Absent: a missing runs/ dir returns nil. Matches the
// fresh-USB case.
func TestReadLastVerify_Absent(t *testing.T) {
	if got := readLastVerify(filepath.Join(t.TempDir(), "runs")); got != nil {
		t.Errorf("missing runs dir: got %+v, want nil", got)
	}
}

// TestReadLastVerify_PicksNewest plants two verifications across two runs
// and asserts the helper picks the one with the most recent VerifiedAt.
// The tie-break by lexical VerifyID is not exercised here (one of the two
// is strictly newer) so a future tie-break-behaviour change requires its
// own test.
func TestReadLastVerify_PicksNewest(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")

	plantSummary := func(t *testing.T, runID, verifyID string, verifiedAt time.Time, exitStatus string) {
		t.Helper()
		verDir := filepath.Join(runsDir, runID, "verifications", verifyID)
		if err := os.MkdirAll(verDir, 0o700); err != nil {
			t.Fatal(err)
		}
		summary := map[string]any{
			"v":                      1,
			"verify_id":              verifyID,
			"for_run_id":             runID,
			"verified_at":            verifiedAt.UTC().Format(time.RFC3339Nano),
			"exit_status":            exitStatus,
			"files_verified":         42,
			"files_integrity_failed": 0,
			"files_hash_mismatch":    0,
		}
		data, err := json.MarshalIndent(summary, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(verDir, "summary.json"), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	plantSummary(t,
		"2026-06-04T1000Z-aaaa", "2026-06-04T1015Z-1111",
		time.Date(2026, 6, 4, 10, 15, 0, 0, time.UTC), "ok")
	plantSummary(t,
		"2026-06-04T1100Z-bbbb", "2026-06-04T1130Z-2222",
		time.Date(2026, 6, 4, 11, 30, 0, 0, time.UTC), "ok")

	got := readLastVerify(runsDir)
	if got == nil {
		t.Fatal("readLastVerify returned nil; want the newer entry")
	}
	if got.VerifyID != "2026-06-04T1130Z-2222" {
		t.Errorf("VerifyID: got %q, want the newer entry", got.VerifyID)
	}
	if got.ForRunID != "2026-06-04T1100Z-bbbb" {
		t.Errorf("ForRunID: got %q, want %q", got.ForRunID, "2026-06-04T1100Z-bbbb")
	}
}

// TestReadLastVerify_SkipsCorrupt: a corrupt summary.json (not parseable as
// JSON) is silently skipped; readLastVerify still returns the parseable
// entry. This is the per-verify failure-isolation contract.
func TestReadLastVerify_SkipsCorrupt(t *testing.T) {
	dir := t.TempDir()
	runsDir := filepath.Join(dir, "runs")

	corruptDir := filepath.Join(runsDir, "2026-06-04T1000Z-aaaa", "verifications", "2026-06-04T1015Z-1111")
	if err := os.MkdirAll(corruptDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(corruptDir, "summary.json"), []byte("not json"), 0o600); err != nil {
		t.Fatal(err)
	}

	validDir := filepath.Join(runsDir, "2026-06-04T1100Z-bbbb", "verifications", "2026-06-04T1130Z-2222")
	if err := os.MkdirAll(validDir, 0o700); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"v":                      1,
		"verify_id":              "2026-06-04T1130Z-2222",
		"for_run_id":             "2026-06-04T1100Z-bbbb",
		"verified_at":            time.Date(2026, 6, 4, 11, 30, 0, 0, time.UTC).UTC().Format(time.RFC3339Nano),
		"exit_status":            "ok",
		"files_verified":         7,
		"files_integrity_failed": 0,
		"files_hash_mismatch":    0,
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(validDir, "summary.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	got := readLastVerify(runsDir)
	if got == nil {
		t.Fatal("readLastVerify returned nil despite a valid summary present")
	}
	if got.VerifyID != "2026-06-04T1130Z-2222" {
		t.Errorf("VerifyID: got %q, want the valid entry", got.VerifyID)
	}
}

// TestEmitStatusJSON_LockedSchema asserts the marshaled JSON contains every
// top-level key from the locked schema (API Contracts, lines 401-437) plus
// the nested keys of last_run + last_verify when they are populated. This
// is the contract test for the --json schema; a future schema-breaking
// rename trips here before it ships.
func TestEmitStatusJSON_LockedSchema(t *testing.T) {
	rec := &statusRecord{
		V:                  1,
		FlashbackupVersion: "0.1.0-core",
		RsyncVersion:       "3.4.1",
		USBPath:            "/Volumes/FLASHBKP",
		USBVolumeUUID:      "ABCD-EF01-2345-6789-ABCDEF012345",
		USBFilesystem:      "APFS",
		USBBytesFree:       132_000_000_000,
		USBBytesTotal:      487_000_000_000,
		NamespacePrefix:    "macbook-mahesh",
		LockStatus:         "free",
		RetainedRuns:       1,
		RetentionLimit:     10,
		LastRun: &statusLastRun{
			RunID:          "2026-06-03T1430Z-a7f2",
			StartedAt:      "2026-06-03T14:30:00Z",
			FinishedAt:     "2026-06-03T14:48:24Z",
			Mode:           "copy",
			Profile:        "my-docs",
			ExitStatus:     "ok",
			FilesTotal:     1234,
			FilesSucceeded: 1234,
			FilesFailed:    0,
			BytesTotal:     982_000_000,
		},
		LastVerify: &statusLastVerify{
			VerifyID:             "2026-06-04T0900Z-c9f4",
			VerifiedAt:           "2026-06-04T09:00:00Z",
			ForRunID:             "2026-06-03T1430Z-a7f2",
			ExitStatus:           "ok",
			FilesVerified:        1234,
			FilesIntegrityFailed: 0,
			FilesHashMismatch:    0,
		},
	}
	var buf bytes.Buffer
	if err := emitStatusJSON(&buf, rec); err != nil {
		t.Fatalf("emitStatusJSON: %v", err)
	}

	var parsed map[string]any
	if err := json.Unmarshal(buf.Bytes(), &parsed); err != nil {
		t.Fatalf("unmarshal output: %v", err)
	}

	// Every top-level key from the locked schema must be present.
	wantTopKeys := []string{
		"v", "flashbackup_version", "rsync_version",
		"usb_path", "usb_volume_uuid", "usb_filesystem",
		"usb_bytes_free", "usb_bytes_total",
		"namespace_prefix", "lock_status",
		"last_run", "last_verify",
		"retained_runs", "retention_limit",
	}
	for _, k := range wantTopKeys {
		if _, ok := parsed[k]; !ok {
			t.Errorf("top-level key %q missing from --json output", k)
		}
	}

	// last_run sub-keys.
	lr, ok := parsed["last_run"].(map[string]any)
	if !ok {
		t.Fatalf("last_run not a map: %T", parsed["last_run"])
	}
	wantLastRunKeys := []string{
		"run_id", "started_at", "finished_at", "mode", "profile",
		"exit_status", "files_total", "files_succeeded", "files_failed",
		"bytes_total",
	}
	for _, k := range wantLastRunKeys {
		if _, ok := lr[k]; !ok {
			t.Errorf("last_run key %q missing", k)
		}
	}

	// last_verify sub-keys.
	lv, ok := parsed["last_verify"].(map[string]any)
	if !ok {
		t.Fatalf("last_verify not a map: %T", parsed["last_verify"])
	}
	wantLastVerifyKeys := []string{
		"verify_id", "verified_at", "for_run_id", "exit_status",
		"files_verified", "files_integrity_failed", "files_hash_mismatch",
	}
	for _, k := range wantLastVerifyKeys {
		if _, ok := lv[k]; !ok {
			t.Errorf("last_verify key %q missing", k)
		}
	}

	// v field is an integer schema version, not a string.
	if v, _ := parsed["v"].(float64); v != float64(statusSchemaVersion) {
		t.Errorf("v: got %v, want %d", parsed["v"], statusSchemaVersion)
	}
}

// TestEmitStatusJSON_OmitsAbsentSubObjects asserts that a fresh-USB record
// (no LastRun, no LastVerify) does NOT emit the keys; omitempty is doing
// its job. A consumer that sees an empty last_run would have to special-
// case the empty shape vs the absent shape; the schema example only
// documents the populated case.
func TestEmitStatusJSON_OmitsAbsentSubObjects(t *testing.T) {
	rec := &statusRecord{
		V:                  1,
		FlashbackupVersion: "0.1.0-core",
		RsyncVersion:       "3.4.1",
		USBPath:            "/Volumes/FLASHBKP",
		USBVolumeUUID:      "ABCD",
		USBFilesystem:      "APFS",
		NamespacePrefix:    "macbook-mahesh",
		LockStatus:         "free",
		RetainedRuns:       0,
		RetentionLimit:     10,
	}
	var buf bytes.Buffer
	if err := emitStatusJSON(&buf, rec); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "last_run") {
		t.Errorf("--json should omit last_run when absent; got %s", out)
	}
	if strings.Contains(out, "last_verify") {
		t.Errorf("--json should omit last_verify when absent; got %s", out)
	}
}

// TestEmitStatusPlain_PopulatedSurface asserts the tabular renderer
// surfaces every field label so a future relabel trips the test. We do
// NOT assert on exact byte layout; the label-anchor check is the contract.
func TestEmitStatusPlain_PopulatedSurface(t *testing.T) {
	rec := &statusRecord{
		V:                  1,
		FlashbackupVersion: "0.1.0-core",
		RsyncVersion:       "3.4.1",
		USBPath:            "/Volumes/FLASHBKP",
		USBVolumeUUID:      "ABCD-EF01",
		USBFilesystem:      "APFS",
		USBBytesFree:       132_000_000_000,
		USBBytesTotal:      487_000_000_000,
		NamespacePrefix:    "macbook-mahesh",
		LockStatus:         "free",
		RetainedRuns:       1,
		RetentionLimit:     10,
		LastRun: &statusLastRun{
			RunID:          "2026-06-03T1430Z-a7f2",
			StartedAt:      "2026-06-03T14:30:00Z",
			FinishedAt:     "2026-06-03T14:48:24Z",
			Mode:           "copy",
			Profile:        "my-docs",
			ExitStatus:     "ok",
			FilesTotal:     1234,
			FilesSucceeded: 1234,
			BytesTotal:     982_000_000,
		},
		LastVerify: &statusLastVerify{
			VerifyID:      "2026-06-04T0900Z-c9f4",
			VerifiedAt:    "2026-06-04T09:00:00Z",
			ForRunID:      "2026-06-03T1430Z-a7f2",
			ExitStatus:    "ok",
			FilesVerified: 1234,
		},
	}
	var buf bytes.Buffer
	if err := emitStatusPlain(&buf, rec); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	wantSubstrings := []string{
		"USB path:", "USB volume UUID:", "USB filesystem:",
		"Free / total:", "Namespace prefix:", "Lock status:",
		"FlashBackup version:", "rsync version:", "Retained runs:",
		"Last run:", "RunID:", "Started:", "Finished:", "Mode:",
		"Profile:", "Exit status:", "Files:", "Bytes:",
		"Last verify:", "VerifyID:", "For RunID:", "Verified at:",
		"Files verified:", "Integrity fails:", "Hash mismatches:",
		"/Volumes/FLASHBKP", "ABCD-EF01", "APFS",
		"macbook-mahesh", "free", "0.1.0-core", "3.4.1",
		"1 / 10",
		"2026-06-03T1430Z-a7f2", "my-docs", "ok",
		"982.0 MB", "132.0 GB", "487.0 GB",
		"2026-06-04T0900Z-c9f4",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(out, want) {
			t.Errorf("plain output missing substring %q\nfull output:\n%s", want, out)
		}
	}
}

// TestEmitStatusPlain_NoneYet covers the fresh-USB renderer arm: both
// "Last run: (none yet)" and "Last verify: (none yet)" anchors must
// appear so the operator does not mistake an empty section for a render
// bug.
func TestEmitStatusPlain_NoneYet(t *testing.T) {
	rec := &statusRecord{
		V:                  1,
		FlashbackupVersion: "0.1.0-core",
		RsyncVersion:       "3.4.1",
		USBPath:            "/Volumes/FLASHBKP",
		USBVolumeUUID:      "ABCD",
		USBFilesystem:      "APFS",
		NamespacePrefix:    "macbook-mahesh",
		LockStatus:         "free",
		RetainedRuns:       0,
		RetentionLimit:     10,
	}
	var buf bytes.Buffer
	if err := emitStatusPlain(&buf, rec); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if !strings.Contains(out, "Last run: (none yet)") {
		t.Errorf("plain output missing 'Last run: (none yet)'\nfull output:\n%s", out)
	}
	if !strings.Contains(out, "Last verify: (none yet)") {
		t.Errorf("plain output missing 'Last verify: (none yet)'\nfull output:\n%s", out)
	}
}

// ----------------------------------------------------------------------------
// E2E tests (mount a DMG, init, then call status)
//
// These cover the real disk-read pipeline: drives.Query against a mounted
// APFS volume, live readLockStatus against the runner's lock path, and
// the populated runs.ndjson / verifications/ trees that backup + verify
// produce.
// ----------------------------------------------------------------------------

// statusInitUSB initializes a USB at the given dest path via the init
// subcommand. Mirrors verifyCmdInitUSB; kept as a separate helper so a
// future change to the init seam touches one place per subcommand test
// file.
func statusInitUSB(t *testing.T, dest string) {
	t.Helper()
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
}

// TestStatus_HappyPath_FreshInit: init a USB; status must report no
// last_run, no last_verify, retained_runs=0. The lock is free at this
// point (init does not hold it on return).
func TestStatus_HappyPath_FreshInit(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	statusInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", "--json", dest})
	if code != statusExitCodeOK {
		t.Fatalf("status exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, statusExitCodeOK, stdout, stderr)
	}

	var rec map[string]any
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("parse json: %v\nstdout=%s", err, stdout)
	}
	if got, _ := rec["lock_status"].(string); got != "free" {
		t.Errorf("lock_status: got %q, want %q", got, "free")
	}
	if got, _ := rec["retained_runs"].(float64); got != 0 {
		t.Errorf("retained_runs: got %v, want 0", got)
	}
	if got, _ := rec["retention_limit"].(float64); got != 10 {
		t.Errorf("retention_limit: got %v, want 10", got)
	}
	if _, ok := rec["last_run"]; ok {
		t.Errorf("last_run should be omitted on fresh init; got %v", rec["last_run"])
	}
	if _, ok := rec["last_verify"]; ok {
		t.Errorf("last_verify should be omitted on fresh init; got %v", rec["last_verify"])
	}
	if fs, _ := rec["usb_filesystem"].(string); !strings.Contains(strings.ToLower(fs), "apfs") {
		t.Errorf("usb_filesystem: got %q, want a value containing 'apfs'", fs)
	}
	if uuid, _ := rec["usb_volume_uuid"].(string); uuid == "" {
		t.Errorf("usb_volume_uuid should be populated by drives.Query")
	}
}

// TestStatus_LockHeld: plant a lock file (no real backup running) and
// confirm status reports lock_status="held". The helper uses a plain stat,
// not Acquire, so a synthetic lock file is sufficient evidence.
func TestStatus_LockHeld(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	statusInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	lockPath := filepath.Join(dest, ".flashbackup", lockBasename)
	if err := os.WriteFile(lockPath, []byte("{}"), 0o600); err != nil {
		t.Fatalf("plant lock file: %v", err)
	}

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", "--json", dest})
	if code != statusExitCodeOK {
		t.Fatalf("status exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, statusExitCodeOK, stdout, stderr)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if got, _ := rec["lock_status"].(string); got != "held" {
		t.Errorf("lock_status: got %q, want %q", got, "held")
	}
}

// TestStatus_AfterBackup: init + plant a runs.ndjson finished line (the
// runner's two-line append output) and confirm status surfaces last_run
// with the right RunID + counters.
//
// We don't actually run a full backup here because that requires either a
// real rsync (gated by systemGNURsyncPath) or the placeholder which fails
// in interesting ways for status purposes. The runs.ndjson surface IS the
// contract status reads; planting it directly exercises the same read
// path as a real run.
func TestStatus_AfterBackup(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	statusInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	runID := "2026-06-04T1500Z-aaaa"
	runsPath := filepath.Join(dest, ".flashbackup", "runs.ndjson")
	store, err := state.NewNDJSONRunLogStore(runsPath)
	if err != nil {
		t.Fatalf("NewNDJSONRunLogStore: %v", err)
	}
	ctx := context.Background()
	startedAt := time.Date(2026, 6, 4, 15, 0, 0, 0, time.UTC)
	finishedAt := time.Date(2026, 6, 4, 15, 10, 0, 0, time.UTC)
	if err := store.AppendStarted(ctx, state.StartedRun{
		V:                  1,
		RunID:              runID,
		StartedAt:          startedAt,
		Mode:               "copy",
		Profile:            "test-profile",
		SourceRoot:         "/src",
		DestRoot:           dest,
		FlashbackupVersion: "0.1.0-core",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendFinished(ctx, state.FinishedRun{
		V:                  1,
		RunID:              runID,
		StartedAt:          startedAt,
		FinishedAt:         finishedAt,
		Mode:               "copy",
		Profile:            "test-profile",
		SourceRoot:         "/src",
		DestRoot:           dest,
		FilesTotal:         100,
		FilesSucceeded:     100,
		FilesFailed:        0,
		BytesTotal:         123456789,
		ExitStatus:         "ok",
		FlashbackupVersion: "0.1.0-core",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	// Plant the per-run dir so retained_runs reflects the new run.
	if err := os.MkdirAll(filepath.Join(dest, ".flashbackup", "runs", runID), 0o700); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", "--json", dest})
	if code != statusExitCodeOK {
		t.Fatalf("status exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, statusExitCodeOK, stdout, stderr)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	if got, _ := rec["retained_runs"].(float64); got != 1 {
		t.Errorf("retained_runs: got %v, want 1", got)
	}
	lr, ok := rec["last_run"].(map[string]any)
	if !ok {
		t.Fatalf("last_run missing or not a map: %T", rec["last_run"])
	}
	if got, _ := lr["run_id"].(string); got != runID {
		t.Errorf("last_run.run_id: got %q, want %q", got, runID)
	}
	if got, _ := lr["mode"].(string); got != "copy" {
		t.Errorf("last_run.mode: got %q, want %q", got, "copy")
	}
	if got, _ := lr["profile"].(string); got != "test-profile" {
		t.Errorf("last_run.profile: got %q, want %q", got, "test-profile")
	}
	if got, _ := lr["exit_status"].(string); got != "ok" {
		t.Errorf("last_run.exit_status: got %q, want %q", got, "ok")
	}
	if got, _ := lr["files_total"].(float64); got != 100 {
		t.Errorf("last_run.files_total: got %v, want 100", got)
	}
	if got, _ := lr["files_succeeded"].(float64); got != 100 {
		t.Errorf("last_run.files_succeeded: got %v, want 100", got)
	}
	if got, _ := lr["bytes_total"].(float64); got != 123456789 {
		t.Errorf("last_run.bytes_total: got %v, want 123456789", got)
	}
}

// TestStatus_AfterVerify: init + plant a finished runs.ndjson + plant a
// verify summary.json under the canonical path. Status must surface
// last_verify with the right counters.
func TestStatus_AfterVerify(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	statusInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	runID := "2026-06-04T1500Z-aaaa"
	verifyID := "2026-06-04T1530Z-bbbb"
	verifiedAt := time.Date(2026, 6, 4, 15, 30, 0, 0, time.UTC)

	verifyDir := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications", verifyID)
	if err := os.MkdirAll(verifyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	summary := map[string]any{
		"v":                      1,
		"verify_id":              verifyID,
		"for_run_id":             runID,
		"verified_at":            verifiedAt.UTC().Format(time.RFC3339Nano),
		"exit_status":            "ok",
		"files_verified":         77,
		"files_integrity_failed": 0,
		"files_hash_mismatch":    0,
	}
	data, _ := json.MarshalIndent(summary, "", "  ")
	if err := os.WriteFile(filepath.Join(verifyDir, "summary.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", "--json", dest})
	if code != statusExitCodeOK {
		t.Fatalf("status exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, statusExitCodeOK, stdout, stderr)
	}
	var rec map[string]any
	if err := json.Unmarshal([]byte(stdout), &rec); err != nil {
		t.Fatalf("parse json: %v", err)
	}
	lv, ok := rec["last_verify"].(map[string]any)
	if !ok {
		t.Fatalf("last_verify missing or not a map: %T", rec["last_verify"])
	}
	if got, _ := lv["verify_id"].(string); got != verifyID {
		t.Errorf("last_verify.verify_id: got %q, want %q", got, verifyID)
	}
	if got, _ := lv["for_run_id"].(string); got != runID {
		t.Errorf("last_verify.for_run_id: got %q, want %q", got, runID)
	}
	if got, _ := lv["exit_status"].(string); got != "ok" {
		t.Errorf("last_verify.exit_status: got %q, want %q", got, "ok")
	}
	if got, _ := lv["files_verified"].(float64); got != 77 {
		t.Errorf("last_verify.files_verified: got %v, want 77", got)
	}
}

// TestStatus_PlainTextDefault: without --json, the output is the tabular
// summary on stdout. Asserts the field-label anchors so a regression in
// the renderer trips here.
func TestStatus_PlainTextDefault(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	statusInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "status", dest})
	if code != statusExitCodeOK {
		t.Fatalf("status exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, statusExitCodeOK, stdout, stderr)
	}
	wantSubstrings := []string{
		"USB path:", "USB filesystem:", "Free / total:",
		"Namespace prefix:", "Lock status:", "FlashBackup version:",
		"rsync version:", "Retained runs:",
		"Last run: (none yet)", "Last verify: (none yet)",
	}
	for _, want := range wantSubstrings {
		if !strings.Contains(stdout, want) {
			t.Errorf("plain status missing substring %q\nfull stdout:\n%s", want, stdout)
		}
	}
}
