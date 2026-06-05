package main

// help.go implements the `flashbackup help [<subcommand>]` subcommand
// (Task 41). The handler is a thin dispatcher over the subcommandHelpTexts
// constants table in helptext.go:
//
//   flashbackup help              -> prints the empty-key (top-level) entry
//   flashbackup help <subcommand> -> prints the named entry
//   flashbackup help <unknown>    -> exit 2 + "unknown subcommand"
//   flashbackup help ""           -> exit 2 (empty key is reserved for top-level
//                                            and is not a valid argv value)
//
// Design choice: rather than forking the named subcommand with ["--help"]
// argv (which would re-enter that subcommand's flag-parser and require each
// handler to be a faithful proxy for its own help text), runHelp reads
// directly from the constants table. This keeps the help screen output
// byte-identical regardless of which entry path the operator used and
// removes any implicit coupling between the help subcommand and the side
// effects of partial argv parsing in another handler.
//
// The existing init / backup / verify / status / profiles handlers continue
// to print their own fs.Usage block on --help. Future tasks may refactor
// those blocks to also pull from subcommandHelpTexts; today they stay as
// a fallback so a per-subcommand --help still works even if a future edit
// to helptext.go inadvertently shadows or empties an entry.

import (
	"context"
	"fmt"
	"io"
)

// helpExitCode* mirror the binary's exit-code contract in doc.go. Declared
// as named constants so the runHelp dispatcher reads as a table rather than
// a wall of literals; matches the convention used by init / backup / verify
// / status / profiles.
const (
	helpExitCodeOK    = 0
	helpExitCodeUsage = 2
)

// runHelp is the testable entry point for the `help` subcommand. argv is
// the trailing args after "help" (so argv[0] is the optional subcommand
// name, NOT the verb "help"). stdout receives the help body on a
// successful lookup; stderr receives the error message on an unknown or
// empty name. ctx and stdin are accepted for handler-signature symmetry;
// help has no interactive prompts and does no I/O that could block on a
// signal.
//
// Exit codes:
//
//	0   help text printed to stdout
//	2   too many arguments, empty name, or unknown subcommand
//
// The dispatcher in main.go does not pass --help / -h through to runHelp;
// those reach the top-level run() switch and are served by printUsage
// (which itself now pulls from subcommandHelpTexts[""]). A literal
// "flashbackup help --help" arrives here as argv = ["--help"], which is
// not a known subcommand name and falls through to the "unknown" arm.
// That is the intended behaviour: --help is a binary-level flag, not a
// subcommand of "help". A scripted probe that wants the help-subcommand
// help should pass `flashbackup help help` (self-reference, by design).
func runHelp(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = ctx   // accepted for handler-signature symmetry; help has no ctx-aware work
	_ = stdin // accepted for handler-signature symmetry; help has no prompts

	// No args: top-level help. Sourced from the empty-key entry so the
	// top-level usage screen and `flashbackup --help` agree byte for byte.
	if len(argv) == 0 {
		fmt.Fprint(stdout, subcommandHelpTexts[""])
		return helpExitCodeOK
	}

	// Reject any shape other than exactly one positional. The contract is
	// "one optional subcommand name", and tolerating extras would mask the
	// operator's intent (e.g. `flashbackup help init backup` is ambiguous;
	// reject loudly so the operator picks one).
	if len(argv) > 1 {
		fmt.Fprintf(stderr,
			"flashbackup help: unexpected extra arguments: %v\n", argv[1:])
		return helpExitCodeUsage
	}

	name := argv[0]

	// The empty-string key is the top-level usage entry; it must never be
	// reachable via argv because the surface for that is `flashbackup
	// help` with no positional. An explicit `flashbackup help ""` is a
	// usage error, not a covert route to the top-level screen.
	if name == "" {
		fmt.Fprintln(stderr,
			"flashbackup help: <subcommand> must not be empty")
		return helpExitCodeUsage
	}

	// Look up by name. The comma-ok form distinguishes "no entry" (unknown
	// subcommand) from "empty entry" (which should never happen for a
	// non-empty key; the table guarantees every known name has a non-empty
	// body, and TestHelp_HelpTextHasUsageLine catches a regression).
	text, ok := subcommandHelpTexts[name]
	if !ok {
		fmt.Fprintf(stderr,
			"flashbackup help: unknown subcommand %q\n", name)
		return helpExitCodeUsage
	}

	fmt.Fprint(stdout, text)
	return helpExitCodeOK
}
