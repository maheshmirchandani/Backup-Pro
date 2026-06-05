package main

// backup.go implements the `flashbackup backup <profile-name> <USB-path>`
// subcommand (Task 36, AC-3). It is the cmd-side seam between the operator's
// argv and the runner orchestrator:
//
//   1. Parse argv: positional[0]=profile name, positional[1]=USB mountpoint,
//      optional --move flag.
//   2. Resolve the USB path to an absolute, symlink-free mountpoint (matches
//      init.go's resolve discipline; differences would mean a profile saved
//      via one path could not be loaded via another).
//   3. Open the profile store at <mountpoint>/.flashbackup/profiles.json and
//      Get(<profile-name>). The store creates parent dirs on open; a missing
//      profiles.json yields a "profile <name> not found" error, which is the
//      desired exit-2 surface for the "operator typed wrong name" case.
//   4. Build runner.RunOptions with a plain.PlainRenderer that targets stdout
//      and picks isTTY via the stat helper (no x/term dep; matches what the
//      runner's own renderer-wiring tests expect for a writer that is not a
//      terminal).
//   5. Invoke runner.Run(ctx, opts). The runner returns (*RunResult, error);
//      we translate ExitStatus to the process exit code per the table in
//      doc.go (binary contract: scripts may rely on these).
//
// The --move flag in v0.1 is a deliberate refusal-gate: Task 37 wires the
// DELETE confirmation modal (the design spec's interactive prompt) and only
// then will move mode produce a usable run. Until then, --move prints a
// pointer at Task 37 and exits 2 so a scripted probe of `flashbackup backup
// X Y --move` fails fast with an actionable message rather than running a
// move with no confirmation.
//
// AC-3 (design spec): `flashbackup backup <profile> /Volumes/USB` on an
// initialized APFS USB with a profile pointing at a tiny source dir produces
// a manifest, an events.ndjson, a runs.ndjson two-line record, and exits 0
// with ExitStatus=ok. Covered by TestBackup_HappyPath_Copy in backup_test.go.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/plain"
	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
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
// summary block; stderr receives usage errors and runner.Run error wraps.
//
// ctx is the signal-aware ctx from main; runner.Run installs its own
// signal.NotifyContext layer on top so SIGINT/SIGTERM is observed at every
// phase boundary (the cmd-level ctx is the outer layer, the runner's is the
// inner one; the runner's inner cancel is released via defer regardless of
// return path).
func runBackup(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	// Local FlagSet so we don't pollute flag.CommandLine. ContinueOnError so
	// a bad flag prints our usage block on stderr rather than calling os.Exit
	// inside the flag package (which would bypass cmd-level cleanup).
	fs := flag.NewFlagSet("backup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	moveMode := fs.Bool("move", false,
		"move mode (delete source files after verified copy); "+
			"NOT YET SUPPORTED in v0.1 (Task 37 wires the DELETE confirm)")
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

	// --move refusal gate. Print a Task-37 pointer so the operator (or a
	// scripted probe) knows where to look for the eventual implementation
	// instead of treating the refusal as a real failure. Exit 2 (usage)
	// rather than 1 (runtime) because the input is structurally invalid for
	// the current binary; the operator can fix it by re-issuing without
	// --move.
	if *moveMode {
		fmt.Fprintln(stderr,
			"flashbackup backup: move mode not yet supported; Task 37 wires the DELETE confirm")
		return backupExitCodeUsage
	}

	// Resolve the USB path to an absolute, symlink-free mountpoint. Same
	// EvalSymlinks discipline as init.go: a missing path fails here with a
	// clear error rather than producing a half-formed run dir later.
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup backup: resolve %q: %v\n", usbPath, err)
		return backupExitCodeUsage
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup backup: %q: %v\n", abs, err)
		return backupExitCodeUsage
	}
	mountpoint := resolved
	mpInfo, err := os.Stat(mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup backup: stat %q: %v\n", mountpoint, err)
		return backupExitCodeUsage
	}
	if !mpInfo.IsDir() {
		fmt.Fprintf(stderr, "flashbackup backup: %q is not a directory\n", mountpoint)
		return backupExitCodeUsage
	}

	// Open the profile store. NewStore creates the parent dir
	// (<mountpoint>/.flashbackup) with mode 0o700 if missing; an
	// uninitialized USB therefore yields a "profile not found" error from
	// Get rather than a confusing parent-dir-create failure.
	storePath := filepath.Join(mountpoint, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup backup: open profile store: %v\n", err)
		return backupExitCodeRuntime
	}
	profile, err := store.Get(profileName)
	if err != nil {
		// Get's error already names the profile; we add the binary prefix.
		// Exit 2 because this is operator-fixable (wrong name) not a runtime
		// failure of an otherwise-valid run.
		fmt.Fprintf(stderr, "flashbackup backup: %v\n", err)
		return backupExitCodeUsage
	}

	// Build the runner options. ModeCopy because --move is refused above;
	// when Task 37 lands the move-confirm UX it will replace the refusal
	// gate and pass ModeMove here.
	opts := types.RunOptions{
		Profile:    profile,
		DestRoot:   mountpoint,
		Mode:       types.ModeCopy,
		UIRenderer: plain.NewPlainRenderer(stdout, isTTYWriter(stdout)),
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
