package e2e

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
)

// Per-test-process caches: build each binary at most once and reuse the
// resulting path for every subsequent invocation. Without this, every
// e2e test row would pay the ~1 to 2 second `go build` cost; with it,
// the build runs once and per-test cost drops to pure exec.
var (
	binaryPath      string
	binaryBuildOnce sync.Once
	binaryBuildErr  error

	faultinjectPath      string
	faultinjectBuildOnce sync.Once
	faultinjectBuildErr  error
)

// BuildBinary returns the absolute path to a built `flashbackup` binary,
// building it on first call and caching the path for the rest of the
// test process. Failures abort the calling test via t.Fatalf; tests that
// want to handle a build error themselves can call buildBinaryE() (not
// exported).
//
// The build runs WITHOUT the release ldflags (no -X main.IsReleaseBuild,
// no -trimpath, no Version stamping) because e2e tests do not care about
// the release-only codesign self-verify gate. Tests that exercise that
// gate must build with `make build` separately.
func BuildBinary(t *testing.T) string {
	t.Helper()
	binaryBuildOnce.Do(func() {
		binaryPath, binaryBuildErr = buildBinaryAtPath("flashbackup-e2e-binary-", nil)
	})
	if binaryBuildErr != nil {
		t.Fatalf("flashbackup binary build failed: %v", binaryBuildErr)
	}
	return binaryPath
}

// BuildFaultinjectBinary returns the absolute path to a `flashbackup`
// binary built with `-tags faultinject`. Cached separately from the
// release-shape binary so tests that mix both don't clobber the cache.
// Used by safety tests (Tasks 48 to 51b in the plan).
func BuildFaultinjectBinary(t *testing.T) string {
	t.Helper()
	faultinjectBuildOnce.Do(func() {
		faultinjectPath, faultinjectBuildErr = buildBinaryAtPath(
			"flashbackup-e2e-faultinject-", []string{"-tags", "faultinject"})
	})
	if faultinjectBuildErr != nil {
		t.Fatalf("flashbackup-faultinject binary build failed: %v", faultinjectBuildErr)
	}
	return faultinjectPath
}

// buildBinaryAtPath builds cmd/flashbackup into a fresh tempdir and
// returns the absolute path to the produced binary. The tempdir is
// intentionally NOT registered with t.Cleanup; the cached binary path
// must outlive the test that triggered the build (so other tests in the
// same process can reuse it). The OS will reclaim the tempdir on
// process exit.
//
// Extra `go build` args (e.g. `-tags faultinject`) are inserted between
// the `build` verb and the `-o` flag.
func buildBinaryAtPath(prefix string, extraArgs []string) (string, error) {
	root, err := findRepoRoot()
	if err != nil {
		return "", fmt.Errorf("find repo root: %w", err)
	}
	tmpdir, err := os.MkdirTemp("", prefix)
	if err != nil {
		return "", fmt.Errorf("mkdir tmpdir: %w", err)
	}
	binPath := filepath.Join(tmpdir, "flashbackup")

	args := []string{"build"}
	args = append(args, extraArgs...)
	args = append(args, "-o", binPath, "./cmd/flashbackup")

	//nolint:gosec // bounded: building our own cmd from $PATH go in a test context
	cmd := exec.Command("go", args...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("go build: %w\n%s", err, out)
	}
	return binPath, nil
}

// findRepoRoot walks up from the current working directory looking for
// a go.mod file. Returns the directory containing it. The Go test
// process starts with cwd = test package dir, so the walk is at most
// a handful of levels.
//
// Falls back to GOMOD env (set by `go test`) if the directory walk
// somehow misses; that env carries the absolute path to go.mod which
// we strip to its containing dir.
func findRepoRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	// Final fallback: $GOMOD points to the go.mod path; trim filename.
	if gomod := os.Getenv("GOMOD"); gomod != "" && gomod != os.DevNull {
		return filepath.Dir(gomod), nil
	}
	return "", fmt.Errorf("no go.mod found from cwd=%q upward", cwd)
}
