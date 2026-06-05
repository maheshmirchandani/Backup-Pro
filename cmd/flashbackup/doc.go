// Package main is the flashbackup CLI entry point. It parses the global
// argv vector, dispatches a subcommand (init, backup, verify, status,
// profiles, help), wires SIGINT/SIGTERM handling with second-signal
// escalation, and serves --version / --help directly.
//
// Subcommand handlers are implemented in Tasks 35-41; Task 34 lands ONLY
// the dispatcher and version/help paths. Each subcommand is currently a
// stub that prints a "not implemented" notice and returns exit code 2 so
// scripts that probe the binary surface get a clear "wrong feature" signal
// rather than silent success.
//
// Exit code table (binary contract, scripts may rely on these):
//
//	0   success, or --version / --help
//	1   subcommand-level failure (runner.Run, verify.Verify, etc. returned
//	    a non-recoverable error)
//	2   usage error (no subcommand, unknown subcommand, bad flag) OR a
//	    preflight failure that aborted before the run state machine started
//	130 second-signal-within-5s force exit (Ctrl-C convention from sh(1):
//	    128 + signo for SIGINT)
//
// Version string contract (--version output, parseable by support tooling):
//
//	flashbackup vX.Y.Z-suffix (rsync R.S.T, commit <sha>, built YYYY-MM-DD)
//	<blank line>
//	<GPLv3 warranty disclaimer paragraph, see versionWarrantyText below>
//
// Version fields are linker-injected via -X on package-level vars
// (Version, RsyncVersion, CommitSHA, BuildEpoch). Defaults are
// "0.1.0-core" / "3.4.1" / "(unset)" / "0" so a dev `go run` or `go build`
// without ldflags still produces a parseable line. BuildEpoch is a UNIX
// timestamp string formatted to YYYY-MM-DD in UTC; "0" renders as
// "(unset)". The Makefile `LDFLAGS_RELEASE` injects the real values from
// `git rev-parse --short HEAD` and `$SOURCE_DATE_EPOCH`.
//
// Signal handling contract (matches design spec section 6):
//
//   - First SIGINT or SIGTERM cancels the run context. Each phase honours
//     ctx.Err() at its own designated cancellation point (T0 clean abort,
//     T1 SIGTERM-rsync-with-5s-grace, etc.).
//   - Second SIGINT or SIGTERM within 5 seconds of the first forces an
//     immediate process exit with code 130. The user pressed Ctrl-C twice
//     because the first cancel did not appear to stick; we honour that
//     intent over a possibly-still-flushing graceful abort.
//
// Testability: the entry point is a thin wrapper around
// run(ctx, argv, stdout, stderr) int. main() builds the signal-wrapped
// ctx, captures os.Args / stdout / stderr, and forwards to run(). Tests
// exercise run() directly with bytes.Buffer args; no os/exec round trips
// in the unit suite.
package main
