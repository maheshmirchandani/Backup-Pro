package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// runCapture exercises run() with a fresh stdout / stderr buffer pair so
// each test gets isolated assertions. Returns (code, stdout, stderr) so the
// table-driven cases below can do all three assertions inline.
//
// Stdin is an empty bytes.Buffer; cases that need to exercise the move-mode
// DELETE prompt (Task 37) use runCaptureStdin instead so they can preload
// a "DELETE\n" or other token line.
func runCapture(t *testing.T, argv []string) (int, string, string) {
	t.Helper()
	return runCaptureStdin(t, argv, "")
}

// runCaptureStdin is the stdin-aware variant of runCapture. The stdin
// argument is preloaded into a bytes.Buffer so the scanner inside
// runBackup's promptDeleteConfirm reads it as a single non-blocking line
// (or sees EOF if stdin is the empty string).
func runCaptureStdin(t *testing.T, argv []string, stdin string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	in := bytes.NewBufferString(stdin)
	code := run(context.Background(), argv, in, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestRun_NoSubcommand: argv has only the program name. The dispatcher
// should print usage to stderr and return exit 2 so a wrapper script that
// invokes `flashbackup` with no args sees a clear "missing subcommand"
// signal, not a silent zero exit that suggests success.
func TestRun_NoSubcommand(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup"})
	if code != 2 {
		t.Errorf("exit code: got %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty for usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should contain usage block, got %q", stderr)
	}
	if !strings.Contains(stderr, "Subcommands:") {
		t.Errorf("stderr should list subcommands, got %q", stderr)
	}
}

// TestRun_VersionFlag: --version writes the version line + the GPLv3
// warranty disclaimer to stdout. Asserts on the literal prefix
// "flashbackup v" plus the warranty paragraph leader so a future tweak to
// the version string (e.g. adding a pre-release suffix) does not require
// editing this test. Returns exit 0.
func TestRun_VersionFlag(t *testing.T) {
	cases := []string{"--version", "-v"}
	for _, flag := range cases {
		t.Run(flag, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, []string{"flashbackup", flag})
			if code != 0 {
				t.Errorf("exit code: got %d, want 0", code)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty on --version, got %q", stderr)
			}
			if !strings.HasPrefix(stdout, "flashbackup v") {
				t.Errorf("stdout should start with %q, got %q", "flashbackup v", stdout)
			}
			if !strings.Contains(stdout, "rsync ") {
				t.Errorf("stdout should mention rsync version, got %q", stdout)
			}
			if !strings.Contains(stdout, "commit ") {
				t.Errorf("stdout should mention commit, got %q", stdout)
			}
			if !strings.Contains(stdout, "built ") {
				t.Errorf("stdout should mention build date, got %q", stdout)
			}
			if !strings.Contains(stdout, "ABSOLUTELY NO WARRANTY") {
				t.Errorf("stdout should contain GPLv3 warranty disclaimer, got %q", stdout)
			}
			if !strings.Contains(stdout, "GNU General Public License") {
				t.Errorf("stdout should reference GPLv3 by name, got %q", stdout)
			}
		})
	}
}

// TestRun_HelpFlag: --help and -h both print usage to stdout and return 0.
// Same usage body as the no-arg path, but on a different stream and with a
// different exit code; this distinguishes a deliberate `--help` request
// from a typo'd invocation.
func TestRun_HelpFlag(t *testing.T) {
	cases := []string{"--help", "-h"}
	for _, flag := range cases {
		t.Run(flag, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, []string{"flashbackup", flag})
			if code != 0 {
				t.Errorf("exit code: got %d, want 0", code)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty on --help, got %q", stderr)
			}
			if !strings.Contains(stdout, "Usage:") {
				t.Errorf("stdout should contain usage block, got %q", stdout)
			}
			if !strings.Contains(stdout, "Subcommands:") {
				t.Errorf("stdout should list subcommands, got %q", stdout)
			}
		})
	}
}

// TestRun_UnknownSubcommand: a bare-string subcommand not in the dispatch
// table is rejected with the offending token quoted on stderr plus the
// usage block, exit 2. The quoted-token assertion guards against a future
// regression where the error message drops the user-supplied value (which
// is the only signal that lets the operator notice they typed `bakcup`
// instead of `backup`).
func TestRun_UnknownSubcommand(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "xyzzy"})
	if code != 2 {
		t.Errorf("exit code: got %d, want 2", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty for unknown subcommand, got %q", stdout)
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("stderr should mention 'unknown subcommand', got %q", stderr)
	}
	if !strings.Contains(stderr, "\"xyzzy\"") {
		t.Errorf("stderr should quote the offending token, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestRun_KnownStubSubcommand: every known subcommand from subcommandList
// that has NOT yet been replaced returns the not-implemented stub (exit 2,
// message names the task). This is the contract that Tasks 35-41 REPLACE
// in turn; this test deliberately asserts on the stub message so each
// future task gets a failing test that points the implementer at the
// right replacement site.
//
// Subcommands already replaced (real implementation in place) are listed
// in replacedSubcommands and skipped here; their own *_test.go files
// own the real-behaviour assertions. Update both that set and the
// dispatcher when a task lands.
func TestRun_KnownStubSubcommand(t *testing.T) {
	replacedSubcommands := map[string]bool{
		"init":   true, // Task 35; see init_test.go
		"backup": true, // Task 36; see backup_test.go
		"verify": true, // Task 38; see verify_test.go
	}
	for _, sc := range subcommandList {
		if replacedSubcommands[sc.name] {
			continue
		}
		t.Run(sc.name, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, []string{"flashbackup", sc.name})
			if code != 2 {
				t.Errorf("exit code: got %d, want 2", code)
			}
			if stdout != "" {
				t.Errorf("stdout should be empty for stub, got %q", stdout)
			}
			if !strings.Contains(stderr, "not implemented yet") {
				t.Errorf("stderr should mention not implemented, got %q", stderr)
			}
			if !strings.Contains(stderr, sc.task) {
				t.Errorf("stderr should name the task (%s), got %q", sc.task, stderr)
			}
			if !strings.Contains(stderr, "\""+sc.name+"\"") {
				t.Errorf("stderr should quote subcommand name, got %q", stderr)
			}
		})
	}
}

// TestFormatBuildDate covers the three regimes of the helper: unset
// ("0" or empty), unparseable (non-numeric), and a real epoch that should
// render in UTC. The fixed epoch is 2026-06-04 00:00:00 UTC (1780531200);
// verified independently via `date -u -r 1780531200 +%Y-%m-%d` so a human
// reading the test can reproduce the conversion without trusting the code
// under test.
func TestFormatBuildDate(t *testing.T) {
	cases := []struct {
		name  string
		epoch string
		want  string
	}{
		{"zero string", "0", "(unset)"},
		{"empty string", "", "(unset)"},
		{"negative", "-1", "(unset)"},
		{"garbage", "not-a-number", "(unset)"},
		{"real epoch 2026-06-04 UTC", "1780531200", "2026-06-04"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatBuildDate(tc.epoch)
			if got != tc.want {
				t.Errorf("formatBuildDate(%q) = %q, want %q", tc.epoch, got, tc.want)
			}
		})
	}
}

// TestInstallSignalHandlers_FirstSignalCancelsCtx delivers a single SIGINT
// to the current process and asserts the returned ctx becomes Done. This
// validates the FIRST-signal contract (graceful cancellation) without
// crossing into second-signal os.Exit territory (which would kill the
// test binary). cancel() is invoked via defer twice to prove the
// idempotent double-cancel path does not panic.
//
// Sending SIGINT to os.Getpid() exercises the same syscall path real
// users hit with Ctrl-C, so this test catches regressions in signal-wire
// integration that a fake-channel test would miss.
func TestInstallSignalHandlers_FirstSignalCancelsCtx(t *testing.T) {
	ctx, cancel := installSignalHandlers(context.Background(), io.Discard)
	defer cancel()
	defer cancel() // intentional double-cancel: cancelOnce must absorb it.

	proc, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatalf("find self process: %v", err)
	}
	if err := proc.Signal(syscall.SIGINT); err != nil {
		t.Fatalf("send SIGINT: %v", err)
	}

	select {
	case <-ctx.Done():
		// success
	case <-time.After(2 * time.Second):
		t.Fatal("ctx did not become Done within 2s of SIGINT")
	}
}

// TestInstallSignalHandlers_CancelWithoutSignal ensures the cancel func
// can be invoked when no signal has arrived (the common shutdown path of
// a clean run). The goroutine must exit, and the cancel must be safe to
// call twice. Validated indirectly by the test simply not hanging.
func TestInstallSignalHandlers_CancelWithoutSignal(t *testing.T) {
	_, cancel := installSignalHandlers(context.Background(), io.Discard)
	cancel()
	cancel() // second call should be a no-op via cancelOnce.
}

// TestPrintVersion_DefaultValues asserts the literal default version line so
// a future careless edit to the Version / RsyncVersion / CommitSHA /
// BuildEpoch defaults trips the test instead of silently shifting the
// dev-build version output. Release builds override these via ldflags and
// are covered by the make verify-release integration gate.
func TestPrintVersion_DefaultValues(t *testing.T) {
	var buf bytes.Buffer
	printVersion(&buf)
	out := buf.String()
	wantPrefix := "flashbackup v0.1.0-core (rsync 3.4.1, commit (unset), built (unset))"
	if !strings.HasPrefix(out, wantPrefix) {
		t.Errorf("version line: got %q, want prefix %q", out, wantPrefix)
	}
}
