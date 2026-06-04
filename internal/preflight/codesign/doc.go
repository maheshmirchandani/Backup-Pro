// Package codesign implements the per-launch binary self-verification gate
// (spec invariant #29). On every launch of `flashbackup`, the running binary
// shells out to `/usr/bin/codesign --verify --strict <self-path>` to confirm
// the on-disk image has not been tampered with since Apple notarization.
//
// This defends against:
//   - A local attacker swapping the `flashbackup` binary on USB after install.
//   - Bit-rot or accidental corruption flipping bytes in the binary or its
//     embedded resources (signed rsync SHA, embedded version, etc.).
//   - Distribution-mirror tampering between download and run.
//
// Failure semantics:
//   - On release builds (ldflag IsReleaseBuild=true): codesign exit non-zero
//     returns ErrInvalidSignature, wrapped with the codesign stderr output.
//     The caller (cmd/flashbackup main) MUST abort the run.
//   - On dev builds (default ldflag, IsReleaseBuild=false): VerifySelf returns
//     ErrDevBuild without invoking codesign at all. Dev binaries from `go run`,
//     `go test`, and `go build` (without the release Makefile target) are
//     unsigned by construction; running real codesign would always fail and
//     mask development.
//
// Why ldflags, not build tags: a build-tag split (`//go:build release` vs
// `//go:build !release`) would mean `go test` couldn't exercise the release
// path at all, and we'd lose the ability to flip a release binary into "dev
// mode" for triage. Using a string variable set at link time via
// `-X .../codesign.IsReleaseBuild=true` keeps both paths in the same compiled
// binary and makes the dev-vs-release decision a single, auditable string
// comparison. The Makefile's `build` target supplies the ldflag.
//
// Usage in Task 20 (preflight integration):
//
//	if err := codesign.VerifySelf(ctx); err != nil {
//	    if errors.Is(err, codesign.ErrDevBuild) {
//	        // dev/test launch; log and continue
//	    } else {
//	        return fmt.Errorf("preflight: codesign self-verify: %w", err)
//	    }
//	}
//
// Platform: darwin only. Non-darwin builds return a clear "unsupported
// platform" error.
package codesign
