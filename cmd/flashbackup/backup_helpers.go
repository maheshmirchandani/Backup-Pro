package main

// backup_helpers.go holds the small helpers that backup.go would otherwise
// push over the 200-line file budget: the ExitStatus -> process exit-code
// translator (backupExitCode), the stdlib-only TTY detector (isTTYWriter),
// and the USB-path + profile resolver (resolveBackupTargets). The first
// two are pure; the third does Stat / EvalSymlinks / profile-store I/O,
// but the I/O is one tight block of error returns and belongs next to the
// other backup-subcommand plumbing.

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// resolveBackupTargets resolves the operator-supplied USB path to an
// absolute, symlink-free mountpoint and loads the named profile from
// <mountpoint>/.flashbackup/profiles.json. On success it returns the
// mountpoint, the loaded profile, and (0, nil). On failure it returns a
// non-zero exit code (matching the cmd-level contract) and an error
// pre-formatted with the "flashbackup backup:" prefix; backup.go just
// surfaces err to stderr and returns the code.
//
// Exit-code rationale:
//   - backupExitCodeUsage (2) for any operator-fixable path failure
//     (bad path, missing profile, not-a-directory) since these are
//     "you typed the wrong thing" failures.
//   - backupExitCodeRuntime (1) for an inability to open the profile
//     store itself (would indicate a filesystem-level issue on the
//     mounted USB).
func resolveBackupTargets(usbPath, profileName string) (
	mountpoint string, profile profiles.Profile, code int, err error,
) {
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		return "", profiles.Profile{}, backupExitCodeUsage,
			fmt.Errorf("flashbackup backup: resolve %q: %w", usbPath, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", profiles.Profile{}, backupExitCodeUsage,
			fmt.Errorf("flashbackup backup: %q: %w", abs, err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return "", profiles.Profile{}, backupExitCodeUsage,
			fmt.Errorf("flashbackup backup: stat %q: %w", resolved, err)
	}
	if !info.IsDir() {
		return "", profiles.Profile{}, backupExitCodeUsage,
			fmt.Errorf("flashbackup backup: %q is not a directory", resolved)
	}
	storePath := filepath.Join(resolved, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		return "", profiles.Profile{}, backupExitCodeRuntime,
			fmt.Errorf("flashbackup backup: open profile store: %w", err)
	}
	p, err := store.Get(profileName)
	if err != nil {
		// store.Get's error already names the missing profile; exit 2
		// because this is operator-fixable (wrong name) rather than a
		// runtime failure of an otherwise-valid run.
		return "", profiles.Profile{}, backupExitCodeUsage,
			fmt.Errorf("flashbackup backup: %w", err)
	}
	return resolved, p, 0, nil
}

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
