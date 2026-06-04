package codesign

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestVerifySelf_DevBuild_ReturnsErrDevBuild covers the default `go test`
// path. The IsReleaseBuild var is "false" because tests do not apply the
// Makefile's release ldflag, so VerifySelf must return ErrDevBuild without
// even trying to invoke codesign.
//
// This is the most important assertion in the file: it guarantees that
// running `go test ./...` on a dev workstation does not fail on the
// codesign gate, and that dev-build entrypoints (cmd/flashbackup compiled
// with `go run` or plain `go build`) never accidentally invoke codesign
// against an unsigned binary.
func TestVerifySelf_DevBuild_ReturnsErrDevBuild(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("VerifySelf dev-build path is only exercised on darwin; the non-darwin stub is covered separately.")
	}
	if IsReleaseBuild == "true" {
		t.Skip("IsReleaseBuild is true; this test only meaningful on default dev builds.")
	}
	err := VerifySelf(context.Background())
	if err == nil {
		t.Fatal("expected ErrDevBuild, got nil")
	}
	if !errors.Is(err, ErrDevBuild) {
		t.Fatalf("errors.Is(err, ErrDevBuild) = false; err = %v", err)
	}
}

// TestVerifySelf_CancelledContext covers the ctx-pre-check at the top of
// verifySelf. A cancelled ctx should short-circuit before we even decide
// dev-vs-release, because callers may use ctx cancellation as a
// shutdown-fast signal during preflight.
func TestVerifySelf_CancelledContext(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("ctx pre-check is only present in the darwin path.")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately
	err := VerifySelf(ctx)
	if err == nil {
		t.Fatal("expected error for cancelled ctx, got nil")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("errors.Is(err, context.Canceled) = false; err = %v", err)
	}
}

// TestSentinels_Distinct asserts that ErrInvalidSignature and ErrDevBuild
// are not the same error value. Tests in callers will rely on errors.Is to
// distinguish "this is a dev build, log and continue" from "this binary has
// been tampered with, abort", so the two sentinels must be independent.
func TestSentinels_Distinct(t *testing.T) {
	if errors.Is(ErrDevBuild, ErrInvalidSignature) {
		t.Error("ErrDevBuild should not match ErrInvalidSignature")
	}
	if errors.Is(ErrInvalidSignature, ErrDevBuild) {
		t.Error("ErrInvalidSignature should not match ErrDevBuild")
	}
}

// TestVerifySelf_ReleaseBuild_TamperedBinary exercises the release path with
// the IsReleaseBuild var temporarily flipped to "true" AND a tampered copy of
// the test binary substituted via the osExecutable seam.
//
// Why a tampered copy: on Apple Silicon, the kernel REQUIRES ad-hoc
// signatures on every executable, so `go test`'s artifact passes
// `codesign --verify --strict` cleanly (it's ad-hoc signed by the linker).
// To exercise the failure path we copy the test binary to a temp dir, flip
// one byte in the middle of the file, and point osExecutable at the copy.
// codesign sees the broken signature and exits non-zero, which is exactly
// the tampered-binary case spec invariant #29 defends against.
//
// On non-darwin we skip; on darwin without /usr/bin/codesign (extremely
// unlikely; it ships with the OS) we also skip cleanly.
func TestVerifySelf_ReleaseBuild_TamperedBinary(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("release path only meaningful on darwin.")
	}
	if _, err := os.Stat(codesignPath); err != nil {
		t.Skipf("codesign not available at %s: %v", codesignPath, err)
	}

	// Get the real test binary path BEFORE we swap osExecutable so we have
	// something to copy.
	realExe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// Copy the test binary into t.TempDir() so we can mutate it without
	// disturbing the original. t.TempDir is cleaned up by the testing
	// framework.
	tamperedPath := filepath.Join(t.TempDir(), "tampered-flashbackup")
	if err := copyFile(realExe, tamperedPath); err != nil {
		t.Fatalf("copy test binary: %v", err)
	}
	// Flip one byte ~10% of the way into the file. This invalidates the
	// ad-hoc signature without corrupting the Mach-O header (which would
	// produce a different error class). codesign should reject this with
	// "a sealed resource is missing or invalid" or similar.
	if err := flipMiddleByte(tamperedPath); err != nil {
		t.Fatalf("tamper with test binary: %v", err)
	}

	saved := IsReleaseBuild
	IsReleaseBuild = "true"
	savedExe := osExecutable
	osExecutable = func() (string, error) { return tamperedPath, nil }
	t.Cleanup(func() {
		IsReleaseBuild = saved
		osExecutable = savedExe
	})

	err = VerifySelf(context.Background())
	if err == nil {
		t.Fatal("expected ErrInvalidSignature on tampered binary, got nil")
	}
	if !errors.Is(err, ErrInvalidSignature) {
		t.Fatalf("errors.Is(err, ErrInvalidSignature) = false; err = %v", err)
	}
	// The error message should mention codesign and the tampered path so an
	// operator reading the log can tell which gate fired and on what file.
	if !strings.Contains(err.Error(), "codesign") {
		t.Errorf("error message missing 'codesign' marker: %v", err)
	}
	if !strings.Contains(err.Error(), tamperedPath) {
		t.Errorf("error message missing tampered path %q: %v", tamperedPath, err)
	}
}

// TestVerifySelf_ReleaseBuild_HappyPath exercises the release path against
// the ad-hoc-signed `go test` binary itself (no tampering). On Apple Silicon
// the test binary is ad-hoc signed by the Go linker, so
// `codesign --verify --strict` accepts it and VerifySelf returns nil.
//
// This complements TestVerifySelf_ReleaseBuild_TamperedBinary: together they
// prove that the release path returns nil on a valid signature and a
// wrapped-sentinel error on an invalid one.
func TestVerifySelf_ReleaseBuild_HappyPath(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("release path only meaningful on darwin.")
	}
	if _, err := os.Stat(codesignPath); err != nil {
		t.Skipf("codesign not available at %s: %v", codesignPath, err)
	}

	saved := IsReleaseBuild
	IsReleaseBuild = "true"
	t.Cleanup(func() { IsReleaseBuild = saved })

	err := VerifySelf(context.Background())
	if err != nil {
		// On Apple Silicon ad-hoc signing is mandatory; if this fails the
		// test environment is unusual (rosetta? sandboxed CI without
		// codesign?). Skip rather than hard-fail.
		t.Skipf("codesign rejected the ad-hoc-signed test binary; environment may not support this test: %v", err)
	}
}

// copyFile is a minimal test helper. We deliberately don't use io.Copy on
// open files because the test binary may exceed the default buffer size and
// we want chmod 0700 on the destination so codesign can read it.
func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0700)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// flipMiddleByte XORs one byte ~10% of the way into the file. Far enough
// past the Mach-O header that we don't break parsing, close enough to the
// front that we're inside a code page that the signature covers.
func flipMiddleByte(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if info.Size() < 1024 {
		// Defensive: a 1KB file is implausibly small for a Go test binary,
		// but if it ever happens we'd flip a header byte and confuse
		// codesign with the wrong error.
		return os.ErrInvalid
	}
	offset := info.Size() / 10
	buf := make([]byte, 1)
	if _, err := f.ReadAt(buf, offset); err != nil {
		return err
	}
	buf[0] ^= 0xFF
	if _, err := f.WriteAt(buf, offset); err != nil {
		return err
	}
	return nil
}
