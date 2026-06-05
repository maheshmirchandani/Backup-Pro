//go:build darwin

package testutil

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

// Canonical absolute paths to the macOS system tools we shell out to. Kept
// as constants (not exec.LookPath) because a sandboxed CI environment may
// have a $PATH that excludes /usr/sbin even when the binary is present at
// its conventional location; an absolute path bypasses the PATH lookup.
const (
	diskutilPath = "/usr/sbin/diskutil"
	hdiutilPath  = "/usr/bin/hdiutil"
)

// RequireE2E skips the test unless FLASHBACKUP_E2E=1 is in the environment.
// This is the gate the Makefile's e2e-fast / e2e-safety targets set; lets
// `go test ./...` stay fast and hermetic while still allowing CI + local
// runs to exercise the mounted-DMG path.
func RequireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("FLASHBACKUP_E2E") != "1" {
		t.Skip("requires FLASHBACKUP_E2E=1 (mounts a DMG via hdiutil)")
	}
}

// RequireMacOS skips the test on non-darwin platforms. The hdiutil + APFS
// preflight pipeline is macOS-only by design (PRD invariant: FlashBackup
// is macOS-first), so any test that touches that pipeline guards with
// this helper.
func RequireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("test is macOS-only; runtime.GOOS=%s", runtime.GOOS)
	}
}

// RequireDiskutil skips when /usr/sbin/diskutil is absent. volume_uuid.Capture
// (preflight gate 5) shells out to diskutil; without it that gate cannot
// succeed and the test would fail for the wrong reason.
func RequireDiskutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(diskutilPath); err != nil {
		t.Skipf("%s not available: %v", diskutilPath, err)
	}
}

// RequireHdiutil skips when /usr/bin/hdiutil is absent. We need it to create
// a temporary APFS / HFS+ / ExFAT volume; arbitrary tempdirs under
// /var/folders are not queryable by diskutil and would not exercise the
// filesystem-detection code path.
func RequireHdiutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(hdiutilPath); err != nil {
		t.Skipf("%s not available: %v", hdiutilPath, err)
	}
}

// MountTempVolume creates a 10 MB DMG with the requested filesystem
// (-fs argument: "APFS", "HFS+", "ExFAT", "MS-DOS", etc.), attaches it
// under /Volumes, and returns the mountpoint. Cleanup detaches the DMG.
// Skips the test on hdiutil failure (sandboxed CI, fs unsupported, etc.).
//
// The volume name embeds time.Now().UnixNano so parallel test runs (across
// packages and goroutines) do not collide on the /Volumes/<name> path.
//
// Callers must have already passed RequireMacOS and RequireHdiutil; this
// helper calls RequireHdiutil internally as belt-and-suspenders.
func MountTempVolume(t *testing.T, fsType string) string {
	t.Helper()
	RequireHdiutil(t)

	volname := fmt.Sprintf("Flashbackup%d", time.Now().UnixNano())
	dmgPath := filepath.Join(t.TempDir(), volname+".dmg")

	out, err := exec.Command(hdiutilPath, "create",
		"-size", "10m",
		"-fs", fsType,
		"-volname", volname,
		"-ov",
		"-attach",
		dmgPath,
	).CombinedOutput()
	if err != nil {
		t.Skipf("hdiutil create -fs %s failed (likely sandbox-restricted or unsupported): %v\n%s",
			fsType, err, out)
	}
	mountpoint := "/Volumes/" + volname

	// Sanity-check that the mount actually appeared. hdiutil sometimes
	// returns success while leaving the volume unmounted on sandboxed runs.
	if _, statErr := os.Stat(mountpoint); statErr != nil {
		_ = exec.Command(hdiutilPath, "detach", "-force", mountpoint).Run()
		t.Skipf("hdiutil attach -fs %s succeeded but mountpoint %q is absent: %v",
			fsType, mountpoint, statErr)
	}

	t.Cleanup(func() {
		// detach is best-effort: failure here would surface as a leaked
		// /Volumes entry, not a test correctness issue.
		_ = exec.Command(hdiutilPath, "detach", "-force", mountpoint).Run()
	})

	return mountpoint
}
