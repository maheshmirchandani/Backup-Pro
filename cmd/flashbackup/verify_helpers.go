package main

// verify_helpers.go holds the ExitStatus -> process exit code translator
// for the `verify` subcommand. Split from verify.go for the same reason
// backup_helpers.go is split from backup.go: file-length hygiene. The
// translator is a pure function with no I/O side effects.

import (
	"github.com/maheshmirchandani/Backup-Pro/internal/verify"
)

// verifyExitCode translates a verify.VerifyResult into the process exit
// code. Centralized so the table stays close to the doc.go contract;
// adding a future ExitStatus only requires editing one switch arm.
//
// Mapping (matches master plan Task 38 brief):
//
//	verify.ExitStatusOK              -> 0
//	verify.ExitStatusIntegrityFailed -> 1  (AC-19 tamper, AC-10 mismatches)
//	verify.ExitStatusPreflightFailed -> 2  (preflight gate refused)
//	nil result OR empty ExitStatus   -> 1  (defensive: pipeline returned an
//	                                        error before populating result)
//
// A non-nil verifyErr with ExitStatusOK is not possible by contract
// (Verify only sets ExitStatusOK after a clean run); the switch arm
// returns the ExitStatusOK exit code rather than adding a defensive
// override that would mask a real bug in the pipeline.
func verifyExitCode(result *verify.VerifyResult) int {
	if result == nil {
		return verifyExitCodeRuntime
	}
	switch result.ExitStatus {
	case verify.ExitStatusOK:
		return verifyExitCodeOK
	case verify.ExitStatusPreflightFailed:
		return verifyExitCodeUsage
	case verify.ExitStatusIntegrityFailed:
		return verifyExitCodeRuntime
	default:
		// Empty string (Verify never populated it) or a future ExitStatus
		// constant we have not learned to translate yet. Default to
		// runtime failure so a scripted probe sees the "something broke"
		// signal rather than a confusing exit 0 / 2 collision.
		return verifyExitCodeRuntime
	}
}
