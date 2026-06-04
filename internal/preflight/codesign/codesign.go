package codesign

import (
	"context"
	"errors"
)

// ErrInvalidSignature means codesign --verify --strict rejected the running
// binary. Wrapped so that errors.Is(err, ErrInvalidSignature) returns true.
var ErrInvalidSignature = errors.New("binary failed codesign verification")

// ErrDevBuild means the running binary was built without the release ldflag
// override (IsReleaseBuild != "true") and has no expected Apple Developer ID
// signature. Callers in dev/test contexts may treat this as a warning rather
// than a fatal error; release entrypoints must NOT see this (the Makefile
// `build` target always sets IsReleaseBuild=true).
var ErrDevBuild = errors.New("dev build; codesign verification skipped")

// VerifySelf invokes `/usr/bin/codesign --verify --strict <self-path>` against
// the currently-running executable. Behavior depends on build mode:
//
//   - Release build (IsReleaseBuild == "true", set via -ldflags by the
//     Makefile's `build` target): runs codesign for real. On non-zero exit,
//     returns an error that wraps ErrInvalidSignature with the codesign
//     stderr/stdout output attached for diagnostics. On success, returns nil.
//
//   - Dev build (IsReleaseBuild != "true"; the default in `go run`,
//     `go test`, `go build` without the Makefile): returns ErrDevBuild
//     immediately without exec'ing codesign. Dev binaries are unsigned by
//     construction; running codesign against them would always fail.
//
//   - Non-darwin: returns a clear "only supported on darwin" error in all
//     build modes (the file is a stub on linux/windows so the package still
//     compiles cross-platform for dev tooling).
//
// The ctx is propagated to exec.CommandContext so that timeouts and
// cancellation propagate to the codesign subprocess.
func VerifySelf(ctx context.Context) error {
	return verifySelf(ctx)
}
