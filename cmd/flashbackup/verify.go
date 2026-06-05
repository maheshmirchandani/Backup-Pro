package main

// verify.go implements the `flashbackup verify [--all | <run-id>]
// [--check-extras] <USB-path>` subcommand (Task 38, AC-9 / AC-10 / AC-19).
// It is the cmd-side seam between the operator's argv and the verify
// pipeline (internal/verify.Verify):
//
//  1. Parse argv: --all and --check-extras flags, plus 1 or 2 positionals
//     depending on whether an explicit run-id was passed.
//  2. Resolve the USB path (abs + EvalSymlinks; matches init.go + backup.go).
//  3. Build verify.VerifyOptions with a plain.PlainRenderer on stdout.
//  4. Invoke verify.Verify(ctx, opts); translate ExitStatus -> exit code per
//     the table in doc.go (0 / 1 / 2).
//
// Exit code table for verify (matches doc.go, master plan Task 38 brief):
//
//	verify.ExitStatusOK              -> 0
//	verify.ExitStatusIntegrityFailed -> 1  (includes AC-19 tamper detection)
//	verify.ExitStatusPreflightFailed -> 2
//	empty / unknown                  -> 1  (defensive)
//
// AC-9: re-hash the last manifest and report per-file outcome. Covered by
//       TestVerify_HappyPath.
// AC-10: counter accuracy under missing/mismatched/unreadable files. Covered
//        by TestVerify_HashMismatch (in internal/verify) + the e2e cases
//        below.
// AC-19: manifest tamper rejection. Covered by TestVerify_TamperedManifest.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/plain"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify"
)

// verifyExitCode* mirror the binary's exit-code contract in doc.go.
// Declared here as named constants so the ExitStatus translator reads as
// a table rather than a wall of literals.
const (
	verifyExitCodeOK      = 0
	verifyExitCodeRuntime = 1
	verifyExitCodeUsage   = 2
)

// runVerify is the testable entry point for the `verify` subcommand. argv
// is the trailing args after "verify" (so argv[0] is a positional or a
// flag, NOT the subcommand name). stdout receives the UIEvtSummary line
// from the renderer; stderr receives usage errors and any wrapped error
// from verify.Verify.
//
// stdin is accepted for handler-signature symmetry; verify has no
// interactive prompts today. ctx is the signal-aware ctx from main;
// verify.Verify installs its own signal.NotifyContext layer on top so
// SIGINT / SIGTERM is observed at every per-file boundary.
func runVerify(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin // accepted for handler-signature symmetry; verify has no prompts

	// Local FlagSet so we don't pollute flag.CommandLine. ContinueOnError so
	// a bad flag prints our usage block on stderr rather than calling
	// os.Exit inside the flag package (which would bypass cmd-level cleanup).
	fs := flag.NewFlagSet("verify", flag.ContinueOnError)
	fs.SetOutput(stderr)
	allMode := fs.Bool("all", false,
		"verify every run on the USB (mutually exclusive with <run-id>)")
	checkExtras := fs.Bool("check-extras", false,
		"additionally count files in dest that are NOT in any manifest "+
			"(informational; never an integrity error)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: flashbackup verify [--all | <run-id>] [--check-extras] <USB-path>")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Re-hashes manifest entries and confirms dest files still match.")
		fmt.Fprintln(stderr, "  <run-id>    optional: verify a specific run (canonical")
		fmt.Fprintln(stderr, "              YYYY-MM-DDTHHMMZ-XXXX format). Omitted: verify")
		fmt.Fprintln(stderr, "              the latest run.")
		fmt.Fprintln(stderr, "  <USB-path>  mountpoint of an initialized FlashBackup USB")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return verifyExitCodeOK
		}
		return verifyExitCodeUsage
	}

	// Positional layout depends on --all:
	//   --all:        one positional (USB-path).
	//   no --all:     one or two positionals; if two, the first is a run-id.
	// We reject the impossible combinations (zero positionals; --all with
	// two positionals; too many positionals) here so verify.Verify never
	// sees an ambiguous opts.RunID + opts.All shape.
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "flashbackup verify: missing <USB-path> argument")
		fs.Usage()
		return verifyExitCodeUsage
	}

	var runID, usbPath string
	switch {
	case *allMode && len(rest) == 1:
		usbPath = rest[0]
	case *allMode && len(rest) > 1:
		fmt.Fprintln(stderr,
			"flashbackup verify: --all is mutually exclusive with a positional <run-id>")
		fs.Usage()
		return verifyExitCodeUsage
	case len(rest) == 1:
		// Single positional, no --all: that's the USB path; verify the
		// latest run.
		usbPath = rest[0]
	case len(rest) == 2:
		// Two positionals, no --all: <run-id> <USB-path>.
		runID = rest[0]
		usbPath = rest[1]
	default:
		fmt.Fprintf(stderr,
			"flashbackup verify: unexpected extra arguments: %v\n", rest[2:])
		fs.Usage()
		return verifyExitCodeUsage
	}

	// Resolve the USB path to an absolute, symlink-free mountpoint. Same
	// EvalSymlinks discipline as init.go + backup.go: a missing path fails
	// here with a clear error rather than propagating into preflight as a
	// confusing "version.json missing" failure.
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup verify: resolve %q: %v\n", usbPath, err)
		return verifyExitCodeUsage
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup verify: %q: %v\n", abs, err)
		return verifyExitCodeUsage
	}
	mountpoint := resolved
	mpInfo, err := os.Stat(mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup verify: stat %q: %v\n", mountpoint, err)
		return verifyExitCodeUsage
	}
	if !mpInfo.IsDir() {
		fmt.Fprintf(stderr, "flashbackup verify: %q is not a directory\n", mountpoint)
		return verifyExitCodeUsage
	}

	// Build the verify options. Renderer goes on stdout so the operator
	// sees the per-file progress + UIEvtSummary line in the same stream as
	// any other subcommand output. SkipCodesign stays false (release
	// default); the test seam is verify-internal and the cmd CLI
	// deliberately does not expose it.
	opts := verify.VerifyOptions{
		RunID:       runID,
		All:         *allMode,
		CheckExtras: *checkExtras,
		DestRoot:    mountpoint,
		UIRenderer:  plain.NewPlainRenderer(stdout, isTTYWriter(stdout)),
	}

	// Invoke the verify pipeline. The orchestrator owns its own signal
	// handling, manifest load + HMAC check, rehash loop, and ExitStatus
	// resolution. cmd only translates the final status into a process
	// exit code.
	result, verifyErr := verify.Verify(ctx, opts)

	// Surface verify errors to stderr. The pipeline has already written
	// per-file results.ndjson + summary.json to disk; the stderr line is
	// the operator-visible signal. Format follows the backup subcommand's
	// pattern (single line under the renderer's UIEvtSummary block).
	if verifyErr != nil {
		fmt.Fprintf(stderr, "flashbackup verify: %v\n", verifyErr)
	}

	return verifyExitCode(result)
}

// verifyExitCode (process exit code translator) lives in verify_helpers.go.
// The split is purely a file-length hygiene concern matching backup.go +
// backup_helpers.go.
