//go:build darwin

package codesign

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// IsReleaseBuild is overridden at link time in release builds via:
//
//	go build -ldflags "-X github.com/maheshmirchandani/Backup-Pro/internal/preflight/codesign.IsReleaseBuild=true" ...
//
// (see the Makefile `build` target). When this value is not exactly "true",
// VerifySelf treats the binary as a dev/test build and returns ErrDevBuild
// without invoking codesign. Type is string (not bool) because -X ldflags
// only support string vars.
var IsReleaseBuild = "false"

// codesignPath is the absolute path to the system codesign binary. We use the
// absolute path (no PATH lookup) to defend against an attacker who has
// modified PATH to point at a malicious codesign shim. /usr/bin/codesign is a
// macOS system binary on all supported macOS versions (10.6+).
const codesignPath = "/usr/bin/codesign"

// execCommandContext is the exec.CommandContext entry point, exposed as a
// package-private var so tests can stub it. Defaults to the real
// exec.CommandContext.
var execCommandContext = exec.CommandContext

// osExecutable returns the running binary's absolute path. Exposed as a var
// so tests can stub a deterministic path. Defaults to os.Executable.
var osExecutable = os.Executable

func verifySelf(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codesign verify: %w", err)
	}
	if IsReleaseBuild != "true" {
		return ErrDevBuild
	}
	exePath, err := osExecutable()
	if err != nil {
		return fmt.Errorf("codesign verify: get executable path: %w", err)
	}
	cmd := execCommandContext(ctx, codesignPath, "--verify", "--strict", exePath)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Wrap the sentinel so errors.Is(err, ErrInvalidSignature) works.
		// Include the raw codesign output for diagnostics (stripped of
		// trailing whitespace). The original exec error is intentionally
		// embedded in the message rather than wrapped because the caller
		// cares about the sentinel, not the *exec.ExitError type.
		return fmt.Errorf("%w: codesign --verify --strict %q exited: %v; output: %s",
			ErrInvalidSignature,
			exePath,
			err,
			strings.TrimSpace(string(out)),
		)
	}
	return nil
}
