package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// ----------------------------------------------------------------------------
// Unit tests (no DMG; exercise argv parsing + flag refusal paths)
// ----------------------------------------------------------------------------

// TestBackup_MissingProfileArg: bare `flashbackup backup` (no positionals)
// must exit 2 + print "missing <profile-name>" + show usage. Catches the
// most common operator typo (forgot both positional args).
func TestBackup_MissingProfileArg(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "backup"})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "missing <profile-name>") {
		t.Errorf("stderr should explain missing profile arg, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestBackup_MissingDestArg: profile name supplied but no USB path. Must
// exit 2 with "missing <USB-path>". Distinct error message from the
// missing-profile case so the operator can tell which arg they forgot.
func TestBackup_MissingDestArg(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "my-profile"})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if !strings.Contains(stderr, "missing <USB-path>") {
		t.Errorf("stderr should mention missing USB-path, got %q", stderr)
	}
}

// TestBackup_TooManyArgs: extra positional args after profile + USB are
// rejected. Guards against operators accidentally appending stray args
// after the path.
func TestBackup_TooManyArgs(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "p", "/Volumes/A", "extra"})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if !strings.Contains(stderr, "unexpected extra arguments") {
		t.Errorf("stderr should explain extra args, got %q", stderr)
	}
}

// TestBackup_HelpFlag: `flashbackup backup --help` must exit 0 (deliberate
// user request for help; not an error) and print the usage block to stderr.
// A non-zero exit would make scripted help-probes look like failures.
func TestBackup_HelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			code, _, stderr := runCapture(t, []string{"flashbackup", "backup", flag})
			if code != 0 {
				t.Errorf("exit code: got %d, want 0", code)
			}
			if !strings.Contains(stderr, "Usage: flashbackup backup") {
				t.Errorf("stderr should include usage line, got %q", stderr)
			}
		})
	}
}

// TestBackup_UnknownFlag: an unrecognised flag must be rejected with exit 2.
// flag.Parse handles the error message itself; we only assert the exit code
// and that stderr is not empty.
func TestBackup_UnknownFlag(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "--nope", "p", "/Volumes/A"})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if stderr == "" {
		t.Errorf("stderr should not be empty on unknown flag")
	}
}

// TestBackup_MoveModeBadMountpoint: --move with a USB path that does not
// resolve must fail at the mountpoint gate (exit 2) BEFORE the DELETE
// prompt is shown. Order matters: prompting for a destructive confirmation
// against a path that does not exist would be operator-hostile. The
// confirmation is a Task 37 gate (AC-7/AC-8); the mountpoint check is a
// Task 36 gate. This test pins that the Task 36 gate fires first.
func TestBackup_MoveModeBadMountpoint(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-mountpoint")
	code, stdout, stderr := runCaptureStdin(t,
		[]string{"flashbackup", "backup", "--move", "p", bogus},
		"DELETE\n", // even with a valid token, the mountpoint check refuses first
	)
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if !strings.Contains(stderr, "no-such-mountpoint") {
		t.Errorf("stderr should name the bad path, got %q", stderr)
	}
	// The DELETE prompt must NOT have been printed; stdout should be empty.
	if stdout != "" {
		t.Errorf("stdout should be empty (prompt fires AFTER mountpoint gate), got %q", stdout)
	}
}

// TestBackup_NonexistentMountpoint: a USB path that does not resolve must
// exit 2 + name the path. Friend-mode equivalent of "USB not plugged in".
func TestBackup_NonexistentMountpoint(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-mountpoint")
	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "any-profile", bogus})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if !strings.Contains(stderr, "no-such-mountpoint") {
		t.Errorf("stderr should name the bad path, got %q", stderr)
	}
}

// TestBackup_PathIsRegularFile: passing a regular file as <USB-path> must
// exit 2 with a "not a directory" message. Mirrors the init.go gate.
func TestBackup_PathIsRegularFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "regular.file")
	if err := os.WriteFile(filePath, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "p", filePath})
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, backupExitCodeUsage)
	}
	if !strings.Contains(stderr, "not a directory") {
		t.Errorf("stderr should mention not-a-directory, got %q", stderr)
	}
}

// TestBackup_ArgvParsing: table-driven coverage of the --move flag position
// and the positional ordering. Each row stops at the same stage (post-flag-
// parse, pre-store-open) by using an unresolvable USB path or refused
// --move flag so we only assert the exit code. The "before path" and
// "after path" rows confirm the Go flag package's standard "flags must
// precede positionals" behaviour, which we rely on for predictable UX.
func TestBackup_ArgvParsing(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-mountpoint")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"profile + path only", []string{"backup", "p", bogus}, backupExitCodeUsage},
		{"move before positionals", []string{"backup", "--move", "p", bogus}, backupExitCodeUsage},
		{"single-dash move", []string{"backup", "-move", "p", bogus}, backupExitCodeUsage},
		{"move after path becomes extra arg", []string{"backup", "p", bogus, "--move"}, backupExitCodeUsage},
		{"missing positionals", []string{"backup"}, backupExitCodeUsage},
		{"missing dest", []string{"backup", "p"}, backupExitCodeUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := append([]string{"flashbackup"}, tc.args...)
			code, _, _ := runCapture(t, argv)
			if code != tc.want {
				t.Errorf("exit code: got %d, want %d", code, tc.want)
			}
		})
	}
}

// TestBackupExitCode_Table covers the ExitStatus -> process exit-code
// translator. Each row matches one arm of the doc.go contract. Pure unit
// test, no I/O. A future ExitStatus constant addition should surface here
// as a missing row.
func TestBackupExitCode_Table(t *testing.T) {
	cases := []struct {
		name   string
		result *types.RunResult
		want   int
	}{
		{"ok", &types.RunResult{ExitStatus: types.ExitStatusOK}, backupExitCodeOK},
		{"partial", &types.RunResult{ExitStatus: types.ExitStatusPartial}, backupExitCodeRuntime},
		{"copy_only_aborted_delete",
			&types.RunResult{ExitStatus: types.ExitStatusCopyOnlyAbortedDelete},
			backupExitCodeRuntime},
		{"crashed_resumed",
			&types.RunResult{ExitStatus: types.ExitStatusCrashedResumed},
			backupExitCodeRuntime},
		{"preflight_failed",
			&types.RunResult{ExitStatus: types.ExitStatusPreflightFailed},
			backupExitCodeUsage},
		{"empty status defaults to runtime", &types.RunResult{ExitStatus: ""}, backupExitCodeRuntime},
		{"unknown future status defaults to runtime",
			&types.RunResult{ExitStatus: "some_future_status"},
			backupExitCodeRuntime},
		{"nil result defaults to runtime", nil, backupExitCodeRuntime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := backupExitCode(tc.result)
			if got != tc.want {
				t.Errorf("backupExitCode = %d; want %d", got, tc.want)
			}
		})
	}
}

// TestIsTTYWriter covers the stat-based TTY detection. A bytes.Buffer is
// never a terminal; a regular *os.File (test fixture) lacks the
// ModeCharDevice bit so is also not a terminal; io.Discard is not an
// *os.File so the type-assertion guards us. The real-PTY branch is not
// tested because attaching a PTY would require pty.Open and is overkill
// for a single boolean.
func TestIsTTYWriter(t *testing.T) {
	if isTTYWriter(&bytes.Buffer{}) {
		t.Errorf("bytes.Buffer reported as TTY")
	}
	tmp := filepath.Join(t.TempDir(), "regular")
	if err := os.WriteFile(tmp, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(tmp)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if isTTYWriter(f) {
		t.Errorf("regular file reported as TTY")
	}
	if isTTYWriter(io.Discard) {
		t.Errorf("io.Discard reported as TTY")
	}
}

// ----------------------------------------------------------------------------
// E2E tests (mount a DMG, init, seed a profile, run backup)
//
// Reuse testutil.MountTempVolume + clearImmutableForTestInit (init_test.go,
// same package). The system-rsync override is wired via the
// FLASHBACKUP_RSYNC_PATH_FOR_TEST env var (see internal/runner/runner.go);
// when unset the embedded placeholder rsync is used and the run exits
// ExitStatusPartial (no real copy happens), which the placeholder-mode
// assertions in TestBackup_HappyPath_Copy cover.
// ----------------------------------------------------------------------------

// systemGNURsyncPath returns the path to a GNU rsync binary suitable for
// substitution, or "" if none is available. Apple's openrsync at
// /usr/bin/rsync claims rsync-2.6.9 compatibility but lacks --from0 /
// --xattrs and so cannot replace the embedded GNU rsync 3.x; only the
// Homebrew paths are considered.
func systemGNURsyncPath() string {
	candidates := []string{"/opt/homebrew/bin/rsync", "/usr/local/bin/rsync"}
	for _, p := range candidates {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		out, err := exec.Command(p, "--version").Output()
		if err != nil {
			continue
		}
		if strings.Contains(string(out), "openrsync") {
			continue
		}
		return p
	}
	return ""
}

// seedProfile writes a single-profile profiles.json directly to the
// canonical store path (<dest>/.flashbackup/profiles.json). Uses the raw
// write path (not profiles.Store.Upsert) so the test does not depend on
// Upsert's import surface staying frozen across future task amendments.
func seedProfile(t *testing.T, dest, name, source string) {
	t.Helper()
	doc := map[string]any{
		"v": 1,
		"profiles": []map[string]any{
			{
				"v":      1,
				"name":   name,
				"source": source,
			},
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal profiles.json: %v", err)
	}
	dotDir := filepath.Join(dest, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatalf("mkdir .flashbackup: %v", err)
	}
	profilesPath := filepath.Join(dotDir, "profiles.json")
	if err := os.WriteFile(profilesPath, data, 0o600); err != nil {
		t.Fatalf("write profiles.json: %v", err)
	}
}

// seedBackupSourceTree creates 3 small files at src; matches the runner's
// seedSourceTree (same content shape) for symmetry with the runner-side
// happy-path test. Returns the rel paths so callers can assert the dest
// namespace dir contents in the real-rsync branch.
func seedBackupSourceTree(t *testing.T, src string) []string {
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

// TestBackup_MoveMode_DeclinedWithEmptyStdin: --move with no DELETE input
// piped in must fail at the prompt with io.ErrUnexpectedEOF; cmd maps
// that to exit 1 (runtime). Distinct from declined-via-wrong-token (exit
// 2): EOF means "scripted invocation forgot to pipe a confirmation",
// which is a runtime failure of the script, not an operator typo.
//
// This is an E2E test because it needs a real initialized USB volume so
// the mountpoint check passes and the runBackup flow reaches the prompt.
func TestBackup_MoveMode_DeclinedWithEmptyStdin(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
	defer clearImmutableRsync(dest)

	src := t.TempDir()
	seedBackupSourceTree(t, src)
	seedProfile(t, dest, "movetest", src)

	// Empty stdin: scanner hits EOF before any line; promptDeleteConfirm
	// returns io.ErrUnexpectedEOF; cmd translates that to exit 1.
	code, stdout, stderr := runCaptureStdin(t,
		[]string{"flashbackup", "backup", "--move", "movetest", dest},
		"",
	)
	if code != backupExitCodeRuntime {
		t.Errorf("exit code: got %d, want %d (runtime; EOF on stdin)\nstdout=%s\nstderr=%s",
			code, backupExitCodeRuntime, stdout, stderr)
	}
	if !strings.Contains(stderr, "move confirmation failed") {
		t.Errorf("stderr should mention move confirmation failure, got %q", stderr)
	}
	// The runner must NOT have been invoked: no runs.ndjson lines yet.
	runsPath := filepath.Join(dest, ".flashbackup", "runs.ndjson")
	if info, err := os.Stat(runsPath); err == nil && info.Size() > 0 {
		t.Errorf("runs.ndjson should be empty/absent (runner not invoked), but has size %d", info.Size())
	}
}

// TestBackup_MoveMode_DeclinedWithWrongToken: --move with stdin="delete\n"
// must exit 2 (operator-fixable; just re-type). Asserts the runner is
// NOT invoked (no runs.ndjson entries) so the source tree is untouched.
func TestBackup_MoveMode_DeclinedWithWrongToken(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
	defer clearImmutableRsync(dest)

	src := t.TempDir()
	rels := seedBackupSourceTree(t, src)
	seedProfile(t, dest, "movetest", src)

	code, _, stderr := runCaptureStdin(t,
		[]string{"flashbackup", "backup", "--move", "movetest", dest},
		"delete\n", // lowercase: declined
	)
	if code != backupExitCodeUsage {
		t.Errorf("exit code: got %d, want %d (usage; declined)\nstderr=%s",
			code, backupExitCodeUsage, stderr)
	}
	if !strings.Contains(stderr, "aborted by operator") {
		t.Errorf("stderr should mention operator abort, got %q", stderr)
	}

	// Source files must remain on disk (runner not invoked; nothing
	// could have unlinked them).
	for _, rel := range rels {
		srcFile := filepath.Join(src, filepath.FromSlash(rel))
		if _, err := os.Stat(srcFile); err != nil {
			t.Errorf("source file %q should still exist after decline: %v", srcFile, err)
		}
	}

	// runs.ndjson should also be empty/absent: the runner never ran.
	runsPath := filepath.Join(dest, ".flashbackup", "runs.ndjson")
	if info, err := os.Stat(runsPath); err == nil && info.Size() > 0 {
		t.Errorf("runs.ndjson should be empty/absent after decline, but has size %d", info.Size())
	}
}

// TestBackup_MoveMode_AcceptedInvokesRunner: --move with stdin="DELETE\n"
// must pass the cmd-level gate and invoke runner.Run with ModeMove. The
// runner's own move-mode behaviour (atomic-gate, deletion-log, etc.) is
// owned by the runner package tests; here we only assert that the gate
// opened: runs.ndjson has 2 lines (started + finished) and the Mode in
// the runner-side options was ModeMove (validated indirectly via the
// deletion-log presence in the per-run dir when real rsync is wired).
//
// With placeholder rsync: T2 classifies all files as failed, T3 (delete)
// is skipped by the atomic gate, exit_status=copy_only_aborted_delete
// (exit 1). With real rsync: T1 copies, T2 verifies, T3 unlinks, exit 0.
// Both branches confirm runner.Run was invoked with ModeMove.
func TestBackup_MoveMode_AcceptedInvokesRunner(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
	defer clearImmutableRsync(dest)

	src := t.TempDir()
	rels := seedBackupSourceTree(t, src)
	seedProfile(t, dest, "movetest", src)

	gnuRsync := systemGNURsyncPath()
	wantRealMove := gnuRsync != ""
	if wantRealMove {
		t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)
	}

	code, _, stderr := runCaptureStdin(t,
		[]string{"flashbackup", "backup", "--move", "movetest", dest},
		"DELETE\n",
	)

	// runs.ndjson must have 2 lines regardless of rsync flavour: the
	// runner was invoked. (Decline branch above asserts ZERO lines.)
	runsPath := filepath.Join(dest, ".flashbackup", "runs.ndjson")
	runs := readBackupNDJSON(t, runsPath)
	if len(runs) != 2 {
		t.Fatalf("runs.ndjson lines = %d; want 2 (started + finished)\nstderr=%s",
			len(runs), stderr)
	}

	runID, ok := runs[1]["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("finished line missing run_id: %v", runs[1])
	}
	runDir := filepath.Join(dest, ".flashbackup", "runs", runID)

	if wantRealMove {
		// Real rsync: exit 0, source files unlinked, deletion-log exists.
		if code != backupExitCodeOK {
			t.Errorf("exit code: got %d, want %d (real rsync, move accepted)\nstderr=%s",
				code, backupExitCodeOK, stderr)
		}
		for _, rel := range rels {
			srcFile := filepath.Join(src, filepath.FromSlash(rel))
			if _, err := os.Stat(srcFile); err == nil {
				t.Errorf("source file %q should have been unlinked by move mode", srcFile)
			}
		}
		deletionLog := filepath.Join(runDir, "deletion-log.ndjson")
		if _, err := os.Stat(deletionLog); err != nil {
			t.Errorf("deletion-log.ndjson missing at %s: %v", deletionLog, err)
		}
	} else {
		// Placeholder rsync: exit 1 (atomic gate fired; deletion skipped).
		// The exit status is copy_only_aborted_delete or partial; either
		// way the cmd-level translator returns runtime. The key signal
		// is that runner.Run WAS invoked (runs.ndjson has 2 lines).
		if code != backupExitCodeRuntime {
			t.Errorf("exit code: got %d, want %d (placeholder rsync, move accepted but no files copied)\nstderr=%s",
				code, backupExitCodeRuntime, stderr)
		}
		// Source files still on disk (T3 skipped by atomic gate).
		for _, rel := range rels {
			srcFile := filepath.Join(src, filepath.FromSlash(rel))
			if _, err := os.Stat(srcFile); err != nil {
				t.Errorf("source file %q must remain when atomic gate fires: %v", srcFile, err)
			}
		}
	}
}

// TestBackup_NonexistentProfile: init then `backup with-name-that-does-not-
// exist`. Must exit 2 with a "not found" message from the profiles store.
// This is the most operator-likely error after a fresh init (forgot to
// create the profile first).
func TestBackup_NonexistentProfile(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
	defer clearImmutableRsync(dest)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "backup", "does-not-exist", dest})
	if code != backupExitCodeUsage {
		t.Fatalf("backup exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, backupExitCodeUsage, stdout, stderr)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr should mention 'not found', got %q", stderr)
	}
	if !strings.Contains(stderr, "does-not-exist") {
		t.Errorf("stderr should name the missing profile, got %q", stderr)
	}
}

// TestBackup_HappyPath_Copy covers AC-3 (design spec): init + seeded
// profile + backup against a tiny source tree produces a manifest.gz,
// events.ndjson with phase events for T0..T4, and a 2-line runs.ndjson.
//
// When a real GNU rsync is available the env-var override is set and the
// run completes ExitStatus=ok (exit 0). When only the embedded placeholder
// is on the box the test asserts the structural artifacts are present and
// the status is partial (exit 1; the placeholder rsync exits 0 without
// copying so T2 classifies all files as failed). Either way the cmd-side
// plumbing is exercised end-to-end.
func TestBackup_HappyPath_Copy(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
	defer clearImmutableRsync(dest)

	src := t.TempDir()
	rels := seedBackupSourceTree(t, src)
	seedProfile(t, dest, "my-test", src)

	// If a real GNU rsync is on the box, point the runner at it via the
	// env-var seam so we can assert ExitStatusOK + presence of dest files.
	// Without a real rsync the test runs against the embedded placeholder
	// and asserts the structural surface (manifest, runs.ndjson) only.
	gnuRsync := systemGNURsyncPath()
	wantOK := gnuRsync != ""
	if wantOK {
		t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)
	}

	code, _, stderr := runCapture(t, []string{"flashbackup", "backup", "my-test", dest})

	// Exit code depends on whether real rsync is wired in. With real rsync:
	// exit 0 (ExitStatusOK). With the placeholder: exit 1 (ExitStatusPartial
	// because dest files are missing and T2 classifies them as failed).
	if wantOK && code != backupExitCodeOK {
		t.Errorf("backup exit code: got %d, want %d (with real rsync)\nstderr=%s",
			code, backupExitCodeOK, stderr)
	}
	if !wantOK && code != backupExitCodeRuntime {
		t.Errorf("backup exit code: got %d, want %d (placeholder rsync)\nstderr=%s",
			code, backupExitCodeRuntime, stderr)
	}

	// runs.ndjson must have exactly 2 lines (started + finished) regardless
	// of the rsync flavour; the runner always emits both lines whether the
	// run ends ok or partial.
	dotDir := filepath.Join(dest, ".flashbackup")
	runsPath := filepath.Join(dotDir, "runs.ndjson")
	runs := readBackupNDJSON(t, runsPath)
	if len(runs) != 2 {
		t.Fatalf("runs.ndjson lines = %d; want 2 (started + finished)\nstderr=%s",
			len(runs), stderr)
	}
	if runs[0]["event"] != "started" || runs[1]["event"] != "finished" {
		t.Errorf("runs.ndjson event order wrong: %v", runs)
	}

	// Locate the per-run dir via the finished line's run_id. Run-id format
	// is "YYYY-MM-DDThhmmZ-NNNN" per t5_finalize.
	runID, ok := runs[1]["run_id"].(string)
	if !ok || runID == "" {
		t.Fatalf("finished line missing run_id: %v", runs[1])
	}

	// manifest.ndjson.gz must exist at the canonical per-run path.
	manifestGz := filepath.Join(dotDir, "runs", runID, "manifest.ndjson.gz")
	if _, err := os.Stat(manifestGz); err != nil {
		t.Errorf("manifest.ndjson.gz missing at %s: %v", manifestGz, err)
	}

	// events.ndjson must contain phase_started + phase_completed for every
	// T-phase (T0, T0+, T1, T2, T3, T4). We check the set; per-event
	// shapes are owned by the runner package's per-phase tests.
	eventsPath := filepath.Join(dotDir, "runs", runID, "events.ndjson")
	events := readBackupNDJSON(t, eventsPath)
	startedPhases := map[string]bool{}
	completedPhases := map[string]bool{}
	for _, ev := range events {
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
			t.Errorf("events.ndjson missing phase_started for %s", phase)
		}
		if !completedPhases[phase] {
			t.Errorf("events.ndjson missing phase_completed for %s", phase)
		}
	}

	// Real-rsync branch only: dest files exist in the namespaced subdir.
	// The placeholder rsync does no copy so this assertion would fail
	// against the structural-surface test row.
	//
	// paths.Prefix replaces dots in hostname/username with hyphens (macOS
	// hostnames like "macbook.local" need to be filesystem-friendly), so
	// the dest namespace dir is NOT a naive hostname+"-"+username concat;
	// we use paths.Prefix to stay in sync with what the runner computed
	// (single source of truth per invariant #15).
	if wantOK {
		hostname, _ := os.Hostname()
		uname, _ := exec.Command("/usr/bin/whoami").Output()
		username := strings.TrimSpace(string(uname))
		nsDir := paths.Prefix(hostname, username)
		for _, rel := range rels {
			destFile := filepath.Join(dest, nsDir, filepath.FromSlash(rel))
			if _, err := os.Stat(destFile); err != nil {
				t.Errorf("dest file %q missing after copy: %v", destFile, err)
			}
		}
	}
}

// ----------------------------------------------------------------------------
// Local helpers
// ----------------------------------------------------------------------------

// readBackupNDJSON reads an NDJSON file and returns each non-empty line as
// a map[string]any. Mirrors readNDJSON in internal/runner/t0_preflight_test.go;
// duplicated here because cmd/flashbackup cannot import test-only helpers
// from internal/runner without a build-tag dance.
func readBackupNDJSON(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse line %q: %v", line, err)
		}
		out = append(out, m)
	}
	return out
}

// clearImmutableRsync walks <dest>/.flashbackup/bin/<sha>/rsync and clears
// the chflags uchg bit on every extracted rsync binary. Without this the
// hdiutil detach in t.Cleanup would fail when the volume holds an
// immutable file. Idempotent: if no rsync was extracted (init refused), it
// quietly returns. Mirrors the cleanup in init_test.go.
func clearImmutableRsync(dest string) {
	binDir := filepath.Join(dest, ".flashbackup", "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		_ = clearImmutableForTestInit(filepath.Join(binDir, e.Name(), "rsync"))
	}
}
