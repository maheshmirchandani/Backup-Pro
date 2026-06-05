package main

// backup_helpers.go holds the two small helpers that backup.go would
// otherwise push over the 200-line file budget: the ExitStatus -> process
// exit-code translator (backupExitCode) and the stdlib-only TTY detector
// (isTTYWriter). Both are pure functions with no I/O side effects, so they
// belong here rather than alongside the argv-parse / runner-invoke pipeline
// in backup.go.

import (
	"io"
	"os"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// backupExitCode translates a runner.RunResult into the process exit code.
// Centralized so the table stays close to the doc.go contract; a future
// ExitStatus addition only requires editing one switch arm.
//
// Mapping table (matches the master plan Task 36 brief):
//
//	ExitStatusOK                       -> 0
//	ExitStatusPartial                  -> 1
//	ExitStatusCopyOnlyAbortedDelete    -> 1  (atomic gate fired in move mode)
//	ExitStatusPreflightFailed          -> 2  (T0 refused; nothing written)
//	ExitStatusCrashedResumed           -> 1  (orphan recovery completed; not
//	                                          a clean current run)
//	nil result OR empty ExitStatus     -> 1  (defensive: runner returned an
//	                                          error before populating result)
//
// We do not translate ExitStatusOK + non-nil runErr to non-zero here because
// the runner.Run contract is that ExitStatusOK is only set after T4 finalize
// succeeded; a successful finalize is a successful run by definition.
func backupExitCode(result *types.RunResult) int {
	if result == nil {
		return backupExitCodeRuntime
	}
	switch result.ExitStatus {
	case types.ExitStatusOK:
		return backupExitCodeOK
	case types.ExitStatusPreflightFailed:
		return backupExitCodeUsage
	case types.ExitStatusPartial,
		types.ExitStatusCopyOnlyAbortedDelete,
		types.ExitStatusCrashedResumed:
		return backupExitCodeRuntime
	default:
		// Empty string (runner never populated it) or a future ExitStatus
		// constant we have not learned to translate yet. Default to runtime
		// failure so a scripted probe sees the "something broke" signal
		// rather than a confusing exit 0 / 2 collision.
		return backupExitCodeRuntime
	}
}

// isTTYWriter reports whether w is a real terminal device. Used to pick the
// plain renderer's TTY mode (rate-limited overwriting progress) vs pipe mode
// (one line per event, progress dropped).
//
// Implementation: w must be an *os.File AND its mode must include
// os.ModeCharDevice. Avoids the golang.org/x/term dependency (we already
// have stdlib-only access to the same signal via FileMode bits), and a
// bytes.Buffer / pipe / file passed in tests naturally returns false (the
// type-assertion fails for the Buffer, and the mode bit is absent for a
// regular file).
//
// Confirmed on darwin (the only v0.1 supported platform) that os.Stdout's
// FileMode carries ModeCharDevice when run from a terminal and lacks it
// when stdout is piped or redirected.
func isTTYWriter(w io.Writer) bool {
	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return (info.Mode() & os.ModeCharDevice) != 0
}
