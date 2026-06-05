package main

// backup.go implements the `flashbackup backup <profile-name> <USB-path>`
// subcommand (Task 36, AC-3; Task 37 adds AC-7/AC-8). It is the cmd-side
// seam between the operator's argv and the runner orchestrator:
//
//  1. Parse argv: positionals + optional --move flag.
//  2. Resolve the USB path (abs + EvalSymlinks; matches init.go).
//  3. Open the profile store at <mountpoint>/.flashbackup/profiles.json
//     and Get(<profile-name>). A missing profiles.json yields a
//     "profile <name> not found" error (exit 2).
//  4. Build runner.RunOptions with a plain.PlainRenderer on stdout.
//  5. If --move: run promptDeleteConfirm (see backup_prompt.go). The
//     upfront cmd-side gate is the design spec's "Type DELETE" friction
//     (section 4); the runner's atomic-gate inside T3 is preserved.
//  6. Invoke runner.Run(ctx, opts); translate ExitStatus -> process exit
//     code per the table in doc.go.
//
// AC-3 covered by TestBackup_HappyPath_Copy. AC-7 / AC-8 covered by
// TestPromptDeleteConfirm_* (backup_prompt_test.go) and
// TestBackup_MoveMode_* (backup_test.go).

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/maheshmirchandani/Backup-Pro/internal/plain"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// backupExitCodeOK / backupExitCodeRuntime / backupExitCodeUsage mirror the
// binary's exit-code contract in doc.go. Declared here as named constants so
// the ExitStatus translator reads as a table rather than a wall of literals.
const (
	backupExitCodeOK      = 0
	backupExitCodeRuntime = 1
	backupExitCodeUsage   = 2
)

// runBackup is the testable entry point for the `backup` subcommand. argv is
// the trailing args after "backup" (so argv[0] is the profile name or a
// flag, NOT the subcommand name). stdout receives runner UI events and the
// summary block; stderr receives usage errors and runner.Run error wraps;
// stdin is the source of the move-mode `DELETE` confirmation line (Task 37).
// Tests pass a bytes.Buffer for stdin; main passes os.Stdin.
//
// ctx is the signal-aware ctx from main; runner.Run installs its own
// signal.NotifyContext layer on top so SIGINT/SIGTERM is observed at every
// phase boundary (the cmd-level ctx is the outer layer, the runner's is the
// inner one; the runner's inner cancel is released via defer regardless of
// return path).
func runBackup(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Local FlagSet so we don't pollute flag.CommandLine. ContinueOnError so
	// a bad flag prints our usage block on stderr rather than calling os.Exit
	// inside the flag package (which would bypass cmd-level cleanup).
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	moveMode := fs.Bool("move", false,
		"move mode (delete source files after verified copy); "+
			"requires typing literal DELETE at the upfront prompt")
	// --inject is registered behind the faultinject build tag (see
	// inject_faultinject.go); on a release build, registerInjectFlag is
	// a no-op and any --inject occurrence trips the standard flag-package
	// "flag provided but not defined" rejection.
	injects := registerInjectFlag(fs)
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: flashbackup backup <profile-name> <USB-path> [--move]")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Runs a backup using a saved profile.")
		fmt.Fprintln(stderr, "  <profile-name>  name of a profile stored on the USB")
		fmt.Fprintln(stderr, "                  (<USB-path>/.flashbackup/profiles.json)")
		fmt.Fprintln(stderr, "  <USB-path>      mountpoint of an initialized FlashBackup USB")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return backupExitCodeOK
		}
		return backupExitCodeUsage
	}
	rest := fs.Args()
	if len(rest) < 1 {
		fmt.Fprintln(stderr, "flashbackup backup: missing <profile-name> argument")
		fs.Usage()
		return backupExitCodeUsage
	}
	if len(rest) < 2 {
		fmt.Fprintln(stderr, "flashbackup backup: missing <USB-path> argument")
		fs.Usage()
		return backupExitCodeUsage
	}
	if len(rest) > 2 {
		fmt.Fprintf(stderr, "flashbackup backup: unexpected extra arguments: %v\n", rest[2:])
		fs.Usage()
		return backupExitCodeUsage
	}
	profileName := rest[0]
	usbPath := rest[1]

	// Resolve USB path + load profile. Helper returns the prefixed
	// error verbatim (matching the inlined formatting this block used to
	// own) plus the right exit code for each failure mode; see
	// backup_helpers.go for the exit-code rationale table.
	mountpoint, profile, resolveCode, resolveErr := resolveBackupTargets(usbPath, profileName)
	if resolveErr != nil {
		fmt.Fprintln(stderr, resolveErr.Error())
		return resolveCode
	}

	// Build the runner options. Mode defaults to ModeCopy; the --move
	// confirmation gate below upgrades to ModeMove only after the operator
	// types the literal "DELETE" token (Task 37, AC-7 + AC-8). Renderer is
	// built once and reused for the prompt + runner UI events so the
	// confirmation appears on the same writer as the subsequent run output.
	opts := types.RunOptions{
		Profile:    profile,
		DestRoot:   mountpoint,
		Mode:       types.ModeCopy,
		UIRenderer: plain.NewPlainRenderer(stdout, isTTYWriter(stdout)),
	}

	// --move confirmation gate. Routed through the shared renderer so the
	// warning + prompt land on stdout (the runner's UIEvent surface) while
	// the read happens on stdin. Decline is exit 2 (operator-fixable: just
	// re-run without --move or with the right intent); read failure is
	// exit 1 (runtime: stdin closed mid-read or a non-EOF read error).
	if *moveMode {
		if err := promptDeleteConfirm(ctx, opts.UIRenderer, stdin); err != nil {
			if errors.Is(err, errDeleteAborted) {
				fmt.Fprintln(stderr,
					"flashbackup backup: move mode aborted by operator (DELETE not typed)")
				return backupExitCodeUsage
			}
			fmt.Fprintf(stderr,
				"flashbackup backup: move confirmation failed: %v\n", err)
			return backupExitCodeRuntime
		}
		opts.Mode = types.ModeMove
	}

	// Install any --inject specs (no-op on release builds; faults are
	// parsed + activated on the faultinject build). Done AFTER the move-
	// mode confirmation gate so a declined --move with --inject still
	// exits cleanly without arming faults in the global active list.
	if err := activateInjects(stderr, injects); err != nil {
		fmt.Fprintf(stderr, "flashbackup backup: %v\n", err)
		return backupExitCodeUsage
	}

	// Invoke the runner. The orchestrator owns all in-run signal handling,
	// audit logging, and ExitStatus resolution; cmd only translates the
	// final status into a process exit code.
	result, runErr := runner.Run(ctx, opts)

	// Surface runner errors to stderr. The runner has already written the
	// rich error trail to events.ndjson + runs.ndjson; the stderr line is
	// the operator-visible signal. Format follows design spec section 6
	// (the renderer's UIEvtSummary block already covers the "where" line;
	// we just emit the bare error here so it lines up under that block).
	if runErr != nil {
		fmt.Fprintf(stderr, "flashbackup backup: %v\n", runErr)
	}

	return backupExitCode(result)
}

// backupExitCode (process exit code translator) and isTTYWriter (stdlib-only
// TTY detector) live in backup_helpers.go. The split is purely a file-length
// hygiene concern; semantically both helpers belong to this subcommand.
