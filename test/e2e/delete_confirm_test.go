package e2e

// delete_confirm_test.go covers AC-7 + AC-8 of the design spec: the
// pre-T0 cmd-side "Type DELETE" confirmation gate (Task 37 / Task 52).
//
// Spec contract (design spec section 4 + section 6 AC-7/AC-8, refined
// 2026-06-05 per Task 37 review A2):
//
//   - The DELETE prompt is a PRE-T0 cmd-side gate. It fires before any
//     runner phase, before lock acquisition, before runs.ndjson is
//     written. Operator declines means the runner is never invoked.
//
//   - Decline path (AC-7): typing anything other than the literal
//     "DELETE" token (case-sensitive byte equality, no trim, no fold)
//     aborts. cmd writes "flashbackup backup: move mode aborted by
//     operator (DELETE not typed)" to stderr and exits with code 2
//     (backupExitCodeUsage). Source files stay on disk; no runs.ndjson
//     record exists; no per-run directory under
//     <USB>/.flashbackup/runs/ is created.
//
//   - Accept path (AC-8): typing "DELETE\n" exactly proceeds with
//     opts.Mode = ModeMove. The runner executes T0..T4, copies the
//     source tree to the namespaced dest, and (on success) unlinks the
//     source files. Exit code 0; runs.ndjson "finished" line with
//     exit_status=ok; manifest.ndjson.gz exists at the per-run path.
//
// These are end-to-end assertions (real init + real APFS DMG + real
// flashbackup binary + real bufio.Scanner-on-stdin path) rather than
// unit tests. The cmd-side unit coverage in
// cmd/flashbackup/backup_prompt_test.go already pins the per-input
// table-driven decline matrix and the renderer hand-off; this file
// proves the same contract holds through the cached-binary subprocess
// pipeline that exit-codes and on-disk artifacts depend on.
//
// Plain build (no faultinject tag): neither AC-7 nor AC-8 needs a
// fault hook. The friction itself (`bufio.Scanner.Scan` against
// "DELETE") is the production code path under test. Tagged into the
// e2e-fast Makefile gate via the "DeleteConfirm" run-name pattern
// (Makefile line 100 amended to include DeleteConfirm alongside the
// Init / BackupHappy / VerifyIntact / LockContention / NonTTY set).
// The master plan classifies Task 52 as e2e-fast (PR-gating per PS2);
// the gate move from e2e-safety to e2e-fast restores that intent.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_DeleteConfirm_AcceptedProceedsWithMoveMode is the AC-8
// end-to-end assertion: `--move` with stdin "DELETE\n" runs the full
// runner pipeline in ModeMove. The successful flow leaves the manifest
// at the per-run path, populates runs.ndjson with exit_status=ok, and
// (because this is move mode) unlinks the source files.
//
// Skips cleanly without FLASHBACKUP_E2E=1, without macOS / hdiutil /
// diskutil, or without a real GNU rsync (the embedded extract is a
// placeholder shell stub at this stage of the plan that exits 0 without
// copying bytes; without a real rsync the AC-8 contract that "T2
// verifies cleanly and T3 unlinks the source" cannot hold).
func TestE2E_DeleteConfirm_AcceptedProceedsWithMoveMode(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB.
	usb := SetupUSB(t, 64)

	// Seed source: copy the tiny fixture tree (a.txt, b.md, c.json) into
	// a fresh tempdir. The AC-8 assertion checks each tiny-fixture file
	// is unlinked from the source after the run; we couple to the
	// fixture's three-file layout intentionally so the post-run check
	// is precise.
	source := SeedSource(t, "tiny")

	// Seed profile with includes=["*"] so every tiny-fixture file is in
	// scope.
	SeedProfile(t, usb, "delete-confirm-accept", source, []string{"*"}, nil)

	// Sanity: source files exist BEFORE the run. If the fixture seed
	// drifted (a future MANIFEST.txt update added or removed a file),
	// surface that here rather than via an indirect post-run assertion.
	tinyFiles := []string{"a.txt", "b.md", "c.json"}
	for _, name := range tinyFiles {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("pre-run sanity: source %s missing: %v", name, err)
		}
	}

	// Run backup with --move and the exact-case DELETE token. The
	// prompt fires before T0; the scanner accepts the line; opts.Mode
	// upgrades to ModeMove; the runner executes T0..T4. RunBackupStdin
	// places extraArgs ("--move") BEFORE the positional <profile> <usb>
	// args so the flag parser sees it as a flag, not as a positional.
	exitCode, stdout, stderr := RunBackupStdin(t,
		"DELETE\n", "delete-confirm-accept", usb, "--move")

	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0 (accept path -> runner success)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// runs.ndjson finished line carries exit_status=ok; the helper
	// returns the run_id of the last finished line which we use to
	// locate the per-run manifest.
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}

	// manifest.ndjson.gz exists at the per-run path. This is the
	// canonical "the run completed cleanly" forensic surface.
	AssertManifestExists(t, usb, runID)

	// Move semantics: source files are unlinked after T2 verified them
	// and the atomic gate opened. The fixture has three files; each
	// must be gone from the source dir post-run. ENOENT is the success
	// signal; any other stat error (EPERM, ELOOP) is treated as a
	// failure with a clear message.
	for _, name := range tinyFiles {
		p := filepath.Join(source, name)
		if _, err := os.Stat(p); err == nil {
			t.Errorf("source/%s still present after move-mode run; AC-8 says T3 unlinks the source on success",
				name)
		} else if !os.IsNotExist(err) {
			t.Errorf("source/%s stat error (expected ENOENT): %v", name, err)
		}
	}
}

// TestE2E_DeleteConfirm_DeclinedAborts is the AC-7 end-to-end
// assertion: `--move` with stdin "delete\n" (lowercase) declines the
// confirmation. The runner is NOT invoked; the process exits 2 with
// the stderr signal "aborted by operator (DELETE not typed)"; no
// per-run directory is created under <USB>/.flashbackup/runs/; the
// source tree is untouched.
//
// This test does NOT require a real GNU rsync: the decline path
// short-circuits before any rsync invocation. We keep the SetupUSB /
// SeedSource scaffolding so the prompt fires against a real
// initialized USB (matching the production code path that resolves
// the mountpoint + profile before the prompt). No
// FLASHBACKUP_RSYNC_PATH_FOR_TEST seam is set; the env-less path is
// adequate.
func TestE2E_DeleteConfirm_DeclinedAborts(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Mount + init the USB. The init step still requires hdiutil (DMG
	// mount) and the extracted rsync stub on the volume, but the
	// decline path never invokes that stub so a missing real rsync is
	// fine here.
	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "delete-confirm-decline", source, []string{"*"}, nil)

	tinyFiles := []string{"a.txt", "b.md", "c.json"}
	for _, name := range tinyFiles {
		if _, err := os.Stat(filepath.Join(source, name)); err != nil {
			t.Fatalf("pre-run sanity: source %s missing: %v", name, err)
		}
	}

	// Run backup with --move and lowercase "delete\n". The scanner
	// reads one line; promptDeleteConfirm sees a non-match against the
	// "DELETE" token; cmd writes the abort line and returns
	// backupExitCodeUsage (2).
	exitCode, stdout, stderr := RunBackupStdin(t,
		"delete\n", "delete-confirm-decline", usb, "--move")

	// Exit code 2 = backupExitCodeUsage, the operator-fixable signal
	// (per cmd/flashbackup/backup.go const declarations and AC-7 in
	// the design spec).
	if exitCode != 2 {
		t.Fatalf("backup exit code: got %d want 2 (decline -> usage)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// stderr carries the AC-7 verbatim signal. The full line is
	// "flashbackup backup: move mode aborted by operator (DELETE not
	// typed)" (cmd/flashbackup/backup.go:137); we substring-check the
	// salient phrase so a future cosmetic reword of the prefix does
	// not break the test.
	if !strings.Contains(stderr, "aborted by operator (DELETE not typed)") {
		t.Errorf("stderr missing AC-7 abort signal\n got stderr: %q\n want substring: %q",
			stderr, "aborted by operator (DELETE not typed)")
	}

	// Runner was NOT invoked: no per-run directory exists under
	// <USB>/.flashbackup/runs/. The runs/ parent dir may or may not
	// exist depending on init layout (currently init creates it
	// empty); we check there are zero entries inside it OR it does
	// not exist at all. Either is a valid "runner never ran" signal.
	runsDir := filepath.Join(usb, ".flashbackup", "runs")
	entries, err := os.ReadDir(runsDir)
	if err == nil {
		if len(entries) != 0 {
			names := make([]string, 0, len(entries))
			for _, e := range entries {
				names = append(names, e.Name())
			}
			t.Errorf("runner created per-run directory under %s on decline path; AC-7 says no T0 occurs\nentries: %v",
				runsDir, names)
		}
	} else if !os.IsNotExist(err) {
		t.Errorf("read %s: %v (expected either ENOENT or an empty dir)", runsDir, err)
	}

	// runs.ndjson must be absent OR zero-byte (the runner never wrote a
	// started line). A non-empty runs.ndjson would mean the runner DID
	// fire which contradicts AC-7's "no T0 / T1 / T3 occurs."
	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	if info, err := os.Stat(runsPath); err == nil && info.Size() > 0 {
		t.Errorf("runs.ndjson has size %d on decline path; AC-7 says runner not invoked (file should be absent or zero-byte)",
			info.Size())
	}

	// Source files unchanged: every fixture file still on disk. AC-7
	// is a pre-T0 gate so the unlinks never had a chance to fire, but
	// the assertion is the operator-visible ground truth: nothing in
	// the source tree was touched.
	for _, name := range tinyFiles {
		p := filepath.Join(source, name)
		if _, err := os.Stat(p); err != nil {
			t.Errorf("source/%s missing after decline path; AC-7 says runner never ran so source must be untouched: %v",
				name, err)
		}
	}
}
