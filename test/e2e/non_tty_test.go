package e2e

import (
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// non_tty_test.go covers AC-15 (design spec): a backup invocation whose
// stdout is NOT a TTY (pipe, file redirect, captured by the test harness)
// emits per-event lines only. The TTY-mode progress overwrites
// (UIEvtProgress lines using a leading \r) MUST NOT appear on a non-TTY
// stream because:
//
//  1. They burn pipe / log-file bytes for no operator benefit (the audit
//     trail at <USB>/.flashbackup/runs/<id>/events.ndjson is the durable
//     record).
//  2. A \r mid-stream in a piped log file mangles `tail -f` and `grep`
//     output downstream.
//
// The non-TTY contract per internal/plain/renderer.go:
//
//   - writeProgress: drops outright when r.isTTY == false (no output).
//   - writeFileStarted / writeFileCompleted: per-file lines DO emit (the
//     non-TTY mode is the audit-friendly verbose mode for log capture).
//   - writePhaseStarted / writePhaseCompleted: phase-boundary lines always
//     emit ("=> T0 preflight starting", "OK T0 preflight", etc.).
//   - writeSummary: always emits the three-line "Run complete." block.
//
// How the test observes non-TTY mode: the shared runCLI helper in
// helpers.go pipes the subprocess stdout into a bytes.Buffer rather than
// inheriting the test runner's terminal. Inside the subprocess,
// cmd/flashbackup/backup.go calls plain.NewPlainRenderer(stdout,
// isTTYWriter(stdout)). isTTYWriter (backup_helpers.go) returns false
// for a piped stdout because the *os.File the subprocess sees does not
// carry os.ModeCharDevice in its FileMode bits. So the production code
// path under test is exactly the AC-15 path; no test-only seam is in
// play.
//
// Tagged into the e2e-fast Makefile gate via the "NonTTY" run-name
// pattern (Makefile line 100: `-run "Init|BackupHappy|VerifyIntact|
// LockContention|NonTTY"`).

// TestE2E_NonTTY_BackupSuppressesProgress is the AC-15 core assertion:
// a backup invocation with stdout captured to a bytes.Buffer (not a TTY)
// completes exit 0 and the captured stdout
//
//   - DOES NOT contain any '\r' character (the only producer of '\r' in
//     the plain renderer is writeProgress; non-TTY mode short-circuits
//     before any \r is written).
//   - DOES contain at least one phase-completion line ("OK T0 preflight"
//     in particular, because T0 always runs and always completes ok on
//     the happy path).
//   - DOES contain the summary block ("Run complete.", "exit status:",
//     "details: see").
//
// We reuse the happy-path scaffolding (real GNU rsync discovery, tiny
// fixture, namespaced dest) so the assertions test the production code
// path end-to-end rather than a renderer-unit shape.
func TestE2E_NonTTY_BackupSuppressesProgress(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Same rsync requirement as backup_happy_test.go: without a real GNU
	// rsync the embedded placeholder shell stub never copies bytes, so T1
	// transfers nothing and the renderer never emits the per-file lines
	// that would otherwise prove the non-TTY "verbose" branch fired. Skip
	// rather than pretend.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "test-non-tty-suppress", source, []string{"*"}, nil)

	exitCode, stdout, stderr := RunBackup(t, "test-non-tty-suppress", usb)
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// AC-15 primary assertion: no \r anywhere on stdout. The only emitter
	// of \r in the plain renderer is writeProgress (renderer.go line 233:
	// "\r%3d%% (...)"). writeProgress's first action in non-TTY mode is
	// `if !r.isTTY { return nil }`, so a single \r byte in captured
	// stdout means non-TTY mode is broken (isTTYWriter mis-detected the
	// pipe, the renderer constructor ignored the flag, or a future code
	// path added a new \r-emitting branch without checking r.isTTY).
	if strings.ContainsRune(stdout, '\r') {
		t.Errorf("non-TTY stdout contains \\r (progress overwrites must be suppressed); first 200 chars: %q",
			truncateForLog(stdout, 200))
	}

	// AC-15 secondary assertion: phase-completion lines DO appear. We
	// look for "OK T0 preflight" because:
	//   - T0 (preflight) is the first phase and ALWAYS runs (no
	//     conditional skip path on the happy flow).
	//   - The label "T0 preflight" is the human-readable label produced
	//     by renderer.go phaseLabel() for Phase "T0".
	//   - The "OK " prefix is the writePhaseCompleted line shape for
	//     Status "ok" (renderer.go line 150).
	// Together they pin one concrete phase-boundary line so a future
	// renderer line-shape change surfaces here rather than silently.
	if !strings.Contains(stdout, "OK T0 preflight") {
		t.Errorf("non-TTY stdout missing the T0 phase-completion line \"OK T0 preflight\"; got: %q",
			truncateForLog(stdout, 500))
	}

	// AC-15 summary assertion: the three-line "Run complete." block per
	// spec section 6 (what / where / next-step). We check three substrings
	// independently rather than the exact block so a future cosmetic
	// reword (e.g., "Backup complete." or "Run finished.") doesn't break
	// the test on a non-substantive change; the substrings together still
	// pin the contract that external tooling greps for ("exit status:"
	// classifies the run, "details: see" points the operator at the
	// audit artifact).
	for _, want := range []string{"Run complete.", "exit status: ok", "details: see"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("non-TTY stdout missing summary substring %q\nfull stdout: %s",
				want, stdout)
		}
	}
}

// TestE2E_NonTTY_PipeFriendlyOutput is the second non-TTY assertion: every
// line of captured stdout terminates with '\n', not '\r\n' (which would
// be the renderer producing Windows-style line endings; not our contract)
// and not a dangling line without a trailing newline (which would mean a
// progress line leaked through or the summary block lost its tail \n).
// Each non-empty line also starts with one of the recognized event
// prefixes so a grep / awk pipeline against the captured log produces
// structured records.
//
// Recognized line-start tokens (matching renderer.go):
//
//	"=> "    phase_started ("=> T0 preflight starting")
//	"OK "    phase_completed ok ("OK T0 preflight")
//	"-- "    phase_completed skipped ("-- T0 preflight skipped")
//	"!! "    phase_completed aborted ("!! T0 preflight aborted: ...")
//	"** "    phase_completed unknown status ("** T0 preflight foo")
//	"?? "    unknown event Kind (PS3 fail-open passthrough)
//	"   "    indented per-file line ("   start /path", "   OK /path",
//	         "   !! /path") -- non-TTY-only because the per-file branch
//	         is suppressed under TTY.
//	"Run complete."     summary block header
//	"  exit status:"    summary block exit-status line
//	"  finished at:"    summary block timestamp line
//	"  details: see"    summary block artifact-pointer line
//
// An empty line is allowed (the summary block emits "\n\nRun complete." so
// there is one blank line before the summary header). Any other prefix
// fails the assertion as either an unknown leak or a regression in line
// shaping.
func TestE2E_NonTTY_PipeFriendlyOutput(t *testing.T) {
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
	SeedProfile(t, usb, "test-non-tty-pipe", source, []string{"*"}, nil)

	exitCode, stdout, stderr := RunBackup(t, "test-non-tty-pipe", usb)
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// No \r\n anywhere. A literal \r\n appearing in the stream would mean
	// the renderer emitted a Windows-style line ending; not our contract.
	// This is technically subsumed by the no-\r check below but called
	// out separately so a future regression that emits \r\n but not bare
	// \r produces a clearer failure message.
	if strings.Contains(stdout, "\r\n") {
		t.Errorf("non-TTY stdout contains \\r\\n line endings (LF-only is the contract); first 200 chars: %q",
			truncateForLog(stdout, 200))
	}

	// No bare \r anywhere. The renderer's only \r emitter is the TTY-mode
	// progress branch (renderer.go line 233); a bare \r here means
	// non-TTY mode is broken (overlaps with TestE2E_NonTTY_Backup
	// SuppressesProgress; kept separate so the failure surfaces in the
	// pipe-friendly test too if only one of the two runs).
	if strings.ContainsRune(stdout, '\r') {
		t.Errorf("non-TTY stdout contains bare \\r (no progress overwrites on a pipe); first 200 chars: %q",
			truncateForLog(stdout, 200))
	}

	// The captured stream must end with a newline. A missing final \n
	// would mean the summary block's tail line ("details: see ...\n")
	// got truncated or a dangling progress line leaked through without
	// the inProgressLine cleanup flushing it. We trim ONE trailing
	// newline (renderer block ends with "...\n", which is correct) and
	// reject anything else.
	if !strings.HasSuffix(stdout, "\n") {
		t.Errorf("non-TTY stdout does not end with \\n (dangling line; first 500 chars and tail): %q ... %q",
			truncateForLog(stdout, 500), tailForLog(stdout, 200))
	}

	// Each non-empty line carries a recognized prefix. Empty lines are
	// allowed (the summary block emits a leading blank line). We list
	// the prefixes here rather than build a regex so a future producer
	// shape addition forces an explicit update to this list rather than
	// drifting silently.
	recognizedPrefixes := []string{
		"=> ", "OK ", "-- ", "!! ", "** ", "?? ",
		"   ",
		"Run complete.",
		"  exit status:",
		"  finished at:",
		"  details: see",
	}
	lines := strings.Split(stdout, "\n")
	for i, line := range lines {
		if line == "" {
			continue
		}
		matched := false
		for _, prefix := range recognizedPrefixes {
			if strings.HasPrefix(line, prefix) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("non-TTY stdout line %d does not start with a recognized event prefix: %q\n(recognized: %v)\nfull stdout: %s",
				i, line, recognizedPrefixes, stdout)
		}
	}
}

// truncateForLog returns s truncated to at most n bytes with an ellipsis
// appended if truncation occurred. Used to keep test failure logs
// readable when stdout is large (multi-KB rsync output on the realistic
// fixture, even though this test uses tiny).
func truncateForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

// tailForLog returns the last n bytes of s with an ellipsis prepended if
// truncation occurred. Paired with truncateForLog for the "head + tail"
// shape used in the missing-trailing-newline failure mode.
func tailForLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "(truncated)..." + s[len(s)-n:]
}
