package e2e

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// SetupUSB mounts a fresh APFS DMG via testutil.MountTempVolume and
// runs `flashbackup init` against it so the volume has a populated
// .flashbackup/ subdir (version.json, bin/<sha>/rsync, etc.). Returns
// the absolute mountpoint path.
//
// Cleanup is layered:
//  1. testutil.MountTempVolume's t.Cleanup runs `hdiutil detach`.
//  2. This function ALSO registers a t.Cleanup that walks
//     <usb>/.flashbackup/bin/*/rsync clearing the chflags uchg bit
//     that the init extract sets; without that the detach fails
//     on an immutable file.
//
// sizeMB is accepted for forward-compat but currently ignored:
// testutil.MountTempVolume hard-codes 10 MB. Callers that need a
// different size should extend testutil first.
func SetupUSB(t *testing.T, sizeMB int) string {
	t.Helper()
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireHdiutil(t)

	_ = sizeMB // intentionally unused; testutil API does not parametrize yet.

	usb := testutil.MountTempVolume(t, "APFS")

	// Run `flashbackup init` so .flashbackup/ exists; subsequent backup /
	// verify / status calls then have a real version.json + rsync to use.
	bin := BuildBinary(t)
	cmd := exec.Command(bin, "init", usb)
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Clear immutable bits before t.Cleanup-driven detach tries to run.
		clearImmutableRsync(usb)
		t.Fatalf("flashbackup init %s failed: %v\n%s", usb, err, out)
	}

	// Register the immutable-clear BEFORE testutil's detach runs. t.Cleanup
	// is LIFO, so this fires first, leaving the volume clean for detach.
	t.Cleanup(func() { clearImmutableRsync(usb) })

	return usb
}

// SeedSource creates a fresh tempdir, copies the named fixture tree into
// it, and returns the absolute path to the copy. The tempdir is
// registered with t.TempDir so cleanup is automatic.
//
// fixtureName is one of "tiny", "realistic", or "pathological". The
// first two are copied verbatim from test/fixtures/<name>/. The
// pathological tree is materialized by running
// test/fixtures/pathological/mkfixtures.sh against the tempdir (the
// fixture members can't survive a plain git checkout; see that
// directory's MANIFEST.txt for the reasoning).
//
// The MANIFEST.txt file in each source fixture is intentionally NOT
// copied; it is documentation, not a source-tree member.
func SeedSource(t *testing.T, fixtureName string) string {
	t.Helper()
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	fixtureSrc := filepath.Join(root, "test", "fixtures", fixtureName)
	if _, err := os.Stat(fixtureSrc); err != nil {
		t.Fatalf("fixture %q not found at %s: %v", fixtureName, fixtureSrc, err)
	}
	dest := t.TempDir()

	switch fixtureName {
	case "pathological":
		script := filepath.Join(fixtureSrc, "mkfixtures.sh")
		cmd := exec.Command("/bin/bash", script, dest)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("mkfixtures.sh %s failed: %v\n%s", dest, err, out)
		}
	default:
		if err := copyDir(fixtureSrc, dest); err != nil {
			t.Fatalf("copy fixture %q: %v", fixtureName, err)
		}
	}
	return dest
}

// SeedProfile writes a profiles.json entry directly under
// <usb>/.flashbackup/profiles.json via the internal/profiles store. The
// store's validation surface (pattern allowlist, NUL-byte rejection)
// runs against the supplied includes / excludes so the e2e fixture
// can't seed a profile that the real subcommand would refuse.
//
// Uses profiles.Store.Upsert (not raw JSON write) so the seeded entry
// is byte-identical to what `flashbackup profiles new` would produce.
func SeedProfile(t *testing.T, usb, name, source string, includes, excludes []string) {
	t.Helper()
	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		t.Fatalf("profiles.NewStore at %s: %v", storePath, err)
	}
	p := profiles.Profile{
		Name:     name,
		Source:   source,
		Includes: includes,
		Excludes: excludes,
	}
	if err := store.Upsert(p); err != nil {
		t.Fatalf("profiles.Upsert %+v: %v", p, err)
	}
}

// RunBackup execs `flashbackup backup <profile> <usb> [extraArgs...]`
// against the cached binary and returns (exitCode, stdout, stderr).
// extraArgs are appended AFTER the positional args; callers that need
// the args to land before the positionals (e.g., --move precedes the
// profile name) should pass them and re-order via the RunBackupArgv
// variant.
//
// stdin is closed immediately (empty); callers that need to feed a
// confirmation token must use RunBackupStdin.
func RunBackup(t *testing.T, profile, usb string, extraArgs ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"backup", profile, usb}, extraArgs...)
	return runCLI(t, args, "")
}

// RunBackupStdin is RunBackup plus a stdin payload (e.g. "DELETE\n" for
// the --move confirmation gate). The payload is fed verbatim; callers
// must include any trailing newline themselves.
func RunBackupStdin(t *testing.T, stdin, profile, usb string, extraArgs ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"backup"}, extraArgs...)
	args = append(args, profile, usb)
	return runCLI(t, args, stdin)
}

// RunBackupFaultinject execs the faultinject-tagged binary with one or
// more --inject specs and a stdin payload. Returns (exitCode, stdout,
// stderr). Used by safety tests (Tasks 48 to 51b in the master plan) that
// need to drive the runner through specific failure modes via the DSL
// documented in internal/runner/faultinject.go.
//
// Flag layout: every --inject occurrence is placed BEFORE the
// positional <profile> <usb> args so the standard flag.Parse stop-at-
// first-positional rule does not swallow them. extraArgs (typically
// `--move`) are placed after the injects but before the positionals for
// the same reason.
//
// stdin is fed verbatim; callers including a trailing newline for the
// `DELETE` move-mode confirmation are expected to include it themselves
// (e.g. "DELETE\n"). An empty stdin closes the descriptor immediately.
func RunBackupFaultinject(t *testing.T, profile, usb string,
	extraArgs []string, injectSpecs []string, stdin string,
) (int, string, string) {
	t.Helper()
	bin := BuildFaultinjectBinary(t)
	args := []string{"backup"}
	for _, spec := range injectSpecs {
		args = append(args, "--inject="+spec)
	}
	args = append(args, extraArgs...)
	args = append(args, profile, usb)
	return runCLIWithBinary(t, bin, args, stdin)
}

// runCLIWithBinary is the binary-parameterized variant of runCLI used by
// RunBackupFaultinject so the cache lookup hits BuildFaultinjectBinary
// rather than BuildBinary. The two helpers share the rest of the exec
// shape (stdout/stderr capture, exec.ExitError unwrap).
func runCLIWithBinary(t *testing.T, bin string, args []string, stdin string) (int, string, string) {
	t.Helper()
	cmd := exec.Command(bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("exec %s %v: %v", bin, args, err)
		}
	}
	return code, stdout.String(), stderr.String()
}

// RunInit execs `flashbackup init <usb> [extraArgs...]`. Useful for
// re-init tests (--reset-keys) and for the negative-path init tests
// that need the cached binary rather than the in-process runInit.
func RunInit(t *testing.T, usb string, extraArgs ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"init"}, extraArgs...)
	args = append(args, usb)
	return runCLI(t, args, "")
}

// RunVerify execs `flashbackup verify <usb> [extraArgs...]`.
func RunVerify(t *testing.T, usb string, extraArgs ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"verify"}, extraArgs...)
	args = append(args, usb)
	return runCLI(t, args, "")
}

// RunStatus execs `flashbackup status <usb> [extraArgs...]`.
func RunStatus(t *testing.T, usb string, extraArgs ...string) (int, string, string) {
	t.Helper()
	args := append([]string{"status"}, extraArgs...)
	args = append(args, usb)
	return runCLI(t, args, "")
}

// RunProfiles execs `flashbackup profiles <action> [args...]`.
func RunProfiles(t *testing.T, action string, args ...string) (int, string, string) {
	t.Helper()
	all := append([]string{"profiles", action}, args...)
	return runCLI(t, all, "")
}

// runCLI is the shared exec shell. Captures stdout + stderr separately,
// translates *exec.ExitError into the underlying exit code, and returns
// 0 + empty strings if the binary build itself failed (which would have
// already aborted the test via BuildBinary).
func runCLI(t *testing.T, args []string, stdin string) (int, string, string) {
	t.Helper()
	bin := BuildBinary(t)
	cmd := exec.Command(bin, args...)
	if stdin != "" {
		cmd.Stdin = strings.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			code = exitErr.ExitCode()
		} else {
			t.Fatalf("exec %s %v: %v", bin, args, err)
		}
	}
	return code, stdout.String(), stderr.String()
}

// copyDir recursively copies src to dest, preserving directory layout
// but NOT preserving the MANIFEST.txt files (they're fixture
// documentation, not source-tree members). File modes are forced to
// 0o600 (regular) and 0o700 (dirs) to match the test fixture mode
// convention.
func copyDir(src, dest string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return os.MkdirAll(dest, 0o700)
		}
		// Skip MANIFEST.txt at any depth and dotfiles like .gitkeep:
		// they're meta, not source content.
		name := d.Name()
		if name == "MANIFEST.txt" || strings.HasPrefix(name, ".git") {
			return nil
		}
		target := filepath.Join(dest, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFile(path, target)
	})
}

func copyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o700); err != nil {
		return fmt.Errorf("mkdir parent %s: %w", filepath.Dir(dest), err)
	}
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open %s: %w", src, err)
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", dest, err)
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return fmt.Errorf("copy %s -> %s: %w", src, dest, err)
	}
	return out.Close()
}

// clearImmutableRsync walks <usb>/.flashbackup/bin/*/rsync and clears
// any chflags bits. The init extract sets uchg (user immutable) on the
// extracted binary; the hdiutil detach in t.Cleanup fails if the volume
// still holds an immutable file. Idempotent: missing dirs are silently
// skipped (init may have refused before the extract step).
//
// This is the same dance as cmd/flashbackup/backup_test.go's
// clearImmutableRsync helper; duplicated here because that one is in
// package main and cannot be imported.
func clearImmutableRsync(usb string) {
	binDir := filepath.Join(usb, ".flashbackup", "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		target := filepath.Join(binDir, e.Name(), "rsync")
		_ = clearChflagsForTest(target)
	}
}
