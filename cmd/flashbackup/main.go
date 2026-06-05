package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"
)

// Linker-injected build identity. Defaults are dev-time values so a plain
// `go run` or `go build` (without ldflags) still produces a parseable
// --version line. The Makefile's release target injects real values via
// `-X main.<name>=...` (the symbol path is "main." regardless of the import
// path because Go's linker rewrites all package-main vars under that
// prefix; the Makefile comment explains the verified-empirically rationale).
//
// Reassigned via -X (not const) so the linker can override without rewriting
// source; this is the same pattern used by codesign.IsReleaseBuild and
// runner.flashbackupVersion. See doc.go for the version-string contract.
var (
	Version      = "0.1.0-core"
	RsyncVersion = "3.4.1"
	CommitSHA    = "(unset)"
	BuildEpoch   = "0"
)

// versionWarrantyText is the GPLv3 warranty disclaimer required by section
// 11 of the GPLv3 license for interactive programs. The OSS multi-hat review
// flagged its absence as a license-compliance gap; --version is the
// canonical surface for it (the binary has no other interactive banner in
// v0.1). Kept as a string literal (not embedded via go:embed) so the disclaimer
// travels with the binary even on a stripped build with no embedded assets.
const versionWarrantyText = `This program is free software: you can redistribute it and/or modify it
under the terms of the GNU General Public License v3 as published by the
Free Software Foundation. This program comes with ABSOLUTELY NO WARRANTY.
See LICENSE for details.`

// subcommandHandler is the canonical signature for every subcommand
// implementation. The dispatcher passes ctx (signal-aware), the trailing
// argv (with the program name and subcommand name dropped), and the three
// stdio streams. A handler returns the process exit code. stdin is passed
// to every handler even when the subcommand has no interactive prompts
// today; the symmetry simplifies the dispatcher and matches the io.Reader
// / io.Writer pattern used by os/exec.Cmd.
//
// This signature was lifted out of an ad-hoc per-arm switch by the Task 35
// review A2 plan amendment so the dispatcher stays a single table lookup
// instead of growing one switch arm per subcommand.
type subcommandHandler func(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int

// subcommandList drives both the help screen and the dispatcher. Order is
// significant: it controls the printed help and the order in which a future
// shell-completion generator emits names. "help" appears last because it is
// a meta-command, not a workflow step.
//
// The handler field is nil when the subcommand has not yet been wired; the
// dispatcher falls through to dispatchStub which prints the not-implemented
// notice referencing the named task. Each task lands by setting its handler
// field; this file is the single place to edit when wiring a new subcommand.
var subcommandList = []struct {
	name    string
	task    string
	desc    string
	handler subcommandHandler
}{
	{"init", "Task 35", "initialize a USB volume for FlashBackup", runInit},
	{"backup", "Task 36 (+ Task 37 move-confirm)", "run a backup using a profile", runBackup},
	{"verify", "Task 38", "verify the integrity of a prior run", runVerify},
	{"status", "Task 39", "show recent run history and current state", nil},
	{"profiles", "Task 40", "list, create, edit, or delete backup profiles", nil},
	{"help", "Task 41", "show help for the binary or a subcommand", nil},
}

// main is a thin wrapper over run() that wires the real os.Args, stdout,
// stderr, and a signal-aware ctx. All actual logic lives in run() so the
// test suite can call run() directly with bytes.Buffer args (no fork/exec).
func main() {
	ctx, cancel := installSignalHandlers(context.Background(), os.Stderr)
	defer cancel()

	code := run(ctx, os.Args, os.Stdin, os.Stdout, os.Stderr)
	os.Exit(code)
}

// run is the testable entry point. It returns the process exit code. argv
// matches os.Args (argv[0] is the program name, argv[1:] are arguments).
// stdin is the source of interactive input (today: the move-mode `DELETE`
// confirmation line in `flashbackup backup --move`); stdout receives
// --version / --help and any subcommand happy-path output; stderr receives
// usage errors and the not-implemented stubs.
//
// ctx is the signal-aware context from installSignalHandlers (in real main)
// or context.Background (in tests). Subcommand stubs do not consult ctx
// today; future task implementations will accept ctx as their first
// argument.
func run(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(argv) < 2 {
		printUsage(stderr)
		return 2
	}

	arg := argv[1]
	switch arg {
	case "--version", "-v":
		printVersion(stdout)
		return 0
	case "--help", "-h":
		printUsage(stdout)
		return 0
	}

	for _, sc := range subcommandList {
		if sc.name != arg {
			continue
		}
		// argv[2:] drops the program name and subcommand so each handler
		// sees only its own positional args + flags. A nil handler means
		// the task has not landed yet; the stub prints a "not implemented"
		// notice naming the task number.
		if sc.handler != nil {
			return sc.handler(ctx, argv[2:], stdin, stdout, stderr)
		}
		return dispatchStub(ctx, sc.name, sc.task, stderr)
	}

	fmt.Fprintf(stderr, "flashbackup: unknown subcommand %q\n\n", arg)
	printUsage(stderr)
	return 2
}

// printUsage writes the short-form usage block. Kept under 24 lines so it
// fits in a typical terminal without scrolling. The subcommand list is
// generated from subcommandList so adding a new subcommand only requires
// editing one place.
func printUsage(w io.Writer) {
	fmt.Fprintln(w, "flashbackup - portable macOS backup utility")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flashbackup <subcommand> [args...]")
	fmt.Fprintln(w, "  flashbackup --version")
	fmt.Fprintln(w, "  flashbackup --help")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Subcommands:")
	for _, sc := range subcommandList {
		fmt.Fprintf(w, "  %-9s %s\n", sc.name, sc.desc)
	}
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "See https://github.com/maheshmirchandani/Backup-Pro for documentation.")
}

// printVersion writes the version line plus the GPLv3 warranty disclaimer.
// Format: "flashbackup vX.Y.Z (rsync R.S.T, commit <sha>, built YYYY-MM-DD)".
// The blank line + disclaimer follows on the next line so a `--version |
// head -1` keeps working for the common case of "just give me the version".
func printVersion(w io.Writer) {
	fmt.Fprintf(w, "flashbackup v%s (rsync %s, commit %s, built %s)\n",
		Version, RsyncVersion, CommitSHA, formatBuildDate(BuildEpoch))
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, versionWarrantyText)
}

// formatBuildDate parses a UNIX-timestamp string and returns the matching
// UTC date in YYYY-MM-DD form. Returns "(unset)" for "0", empty string, or
// any unparseable value; the operator should never see a half-formed date.
//
// Why UTC: a reproducible-build epoch is timezone-agnostic by convention
// (SOURCE_DATE_EPOCH is documented as seconds since the UNIX epoch in UTC).
// Rendering in the local timezone would make two binaries built from the
// same source on machines in different cities show different "built" dates,
// which defeats the point.
func formatBuildDate(epoch string) string {
	if epoch == "" || epoch == "0" {
		return "(unset)"
	}
	secs, err := strconv.ParseInt(epoch, 10, 64)
	if err != nil || secs <= 0 {
		return "(unset)"
	}
	return time.Unix(secs, 0).UTC().Format("2006-01-02")
}

// dispatchStub is the placeholder body for every Task 35-41 subcommand. It
// prints a not-implemented notice that names the responsible task so a
// scripted probe can fail fast with an actionable signal. Exit code 2
// (usage error, not 1) so a script that runs `flashbackup backup ... ||
// recover` does not treat the stub as a failed real run.
func dispatchStub(ctx context.Context, name, task string, errSink io.Writer) int {
	// Discard ctx for now; subcommand implementations will consume it as
	// their first argument. The signature keeps the intent visible to the
	// next task author.
	_ = ctx
	fmt.Fprintf(errSink, "flashbackup: subcommand %q not implemented yet (%s)\n", name, task)
	return 2
}
