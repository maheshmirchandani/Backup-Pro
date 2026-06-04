package preflight

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

const (
	diskutilPath = "/usr/sbin/diskutil"
	hdiutilPath  = "/usr/bin/hdiutil"
)

func requireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("preflight is macOS-only; runtime.GOOS=%s", runtime.GOOS)
	}
}

// requireDiskutil skips when /usr/sbin/diskutil is absent. volume_uuid.Capture
// shells out to diskutil; without it gate 5 cannot succeed.
func requireDiskutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(diskutilPath); err != nil {
		t.Skipf("%s not available: %v", diskutilPath, err)
	}
}

// requireHdiutil skips when /usr/bin/hdiutil is absent. We need it to create
// a temporary APFS volume so that gate 5 (volume_uuid.Capture) succeeds;
// arbitrary tempdirs under /var/folders are not queryable by diskutil.
func requireHdiutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(hdiutilPath); err != nil {
		t.Skipf("%s not available: %v", hdiutilPath, err)
	}
}

// mountTempVolume creates a small APFS DMG, attaches it under /Volumes, and
// returns the mountpoint. Cleanup detaches the DMG and removes the image file.
// Skips the test on attach failure (sandbox restrictions, low disk, etc.).
func mountTempVolume(t *testing.T) string {
	t.Helper()
	requireHdiutil(t)

	// Unique volume name so parallel test runs do not collide.
	volname := fmt.Sprintf("FlashbackupPreflight%d", time.Now().UnixNano())
	dmgPath := filepath.Join(t.TempDir(), volname+".dmg")

	// 10MB is enough to hold .flashbackup/* and the placeholder rsync.
	cmd := exec.Command(hdiutilPath, "create",
		"-size", "10m",
		"-fs", "APFS",
		"-volname", volname,
		"-ov",
		"-attach",
		dmgPath,
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Skipf("hdiutil create failed (likely sandbox-restricted environment): %v\n%s", err, out)
	}
	mountpoint := "/Volumes/" + volname

	// Sanity-check that the mount actually appeared. hdiutil sometimes
	// returns success while leaving the volume unmounted on sandboxed runs.
	if _, statErr := os.Stat(mountpoint); statErr != nil {
		_ = exec.Command(hdiutilPath, "detach", "-force", mountpoint).Run()
		t.Skipf("hdiutil attach succeeded but mountpoint %q is absent: %v", mountpoint, statErr)
	}

	t.Cleanup(func() {
		// detach is best-effort: failure here would surface as a leaked
		// /Volumes entry, not a test correctness issue.
		_ = exec.Command(hdiutilPath, "detach", "-force", mountpoint).Run()
	})

	return mountpoint
}

// setupDest mounts a fresh APFS DMG, seeds it with a valid version.json
// (so the FAIL-CLOSED gate 8 passes), and returns the mountpoint + captured
// VersionFile.
func setupDest(t *testing.T) (string, state.VersionFile) {
	t.Helper()
	dest := mountTempVolume(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	versionPath := filepath.Join(dotDir, "version.json")
	vf, err := state.InitVersionFile(versionPath, "test-version", false)
	if err != nil {
		t.Fatalf("InitVersionFile: %v", err)
	}
	return dest, vf
}

func TestPreflight_HappyPath(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	dest, _ := setupDest(t)
	ctx := context.Background()
	pc, err := Preflight(ctx, Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	t.Cleanup(func() { _ = pc.Release() })

	if pc.DestRoot == "" {
		t.Error("DestRoot empty")
	}
	if pc.DotDir == "" {
		t.Error("DotDir empty")
	}
	if pc.LockHandle == nil {
		t.Error("LockHandle nil")
	}
	if pc.VolumeUUID == nil || pc.VolumeUUID.UUID == "" {
		t.Error("VolumeUUID not captured")
	}
	if pc.SymlinkBaseline == nil || len(pc.SymlinkBaseline.Components) == 0 {
		t.Error("SymlinkBaseline empty")
	}
	if pc.Filesystem == nil {
		t.Error("Filesystem nil")
	}
	if pc.VersionFile.SchemaVersion != 1 {
		t.Errorf("SchemaVersion = %d, want 1", pc.VersionFile.SchemaVersion)
	}
	if pc.RsyncPath == "" {
		t.Error("RsyncPath empty")
	}
	if _, statErr := os.Stat(pc.RsyncPath); statErr != nil {
		t.Errorf("RsyncPath %q does not exist: %v", pc.RsyncPath, statErr)
	}
	if pc.Hostname == "" {
		t.Error("Hostname empty")
	}
	if pc.Username == "" {
		t.Error("Username empty")
	}
}

func TestPreflight_EmptyDestRoot(t *testing.T) {
	_, err := Preflight(context.Background(), Options{DestRoot: ""})
	if err == nil {
		t.Fatal("expected error on empty DestRoot")
	}
}

func TestPreflight_DestRootNotExist(t *testing.T) {
	requireMacOS(t)
	_, err := Preflight(context.Background(), Options{
		DestRoot:     "/nonexistent/never/will-exist-flashbackup-test",
		SkipCodesign: true,
	})
	if err == nil {
		t.Fatal("expected error on nonexistent DestRoot")
	}
}

func TestPreflight_NoVersionFile_FailsClosed(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	// Mount a clean volume but skip the version.json init step.
	// Gates 1-7 should pass; gate 8 must fail.
	dest := mountTempVolume(t)
	pc, err := Preflight(context.Background(), Options{DestRoot: dest, SkipCodesign: true})
	if err == nil {
		if pc != nil {
			_ = pc.Release()
		}
		t.Fatal("expected gate 8 fail-closed when version.json missing")
	}
	// The lock must have been released by the rollback path.
	lockPath := filepath.Join(dest, ".flashbackup", "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock file should be cleaned up after gate 8 failure: stat err=%v", statErr)
	}
}

func TestPreflight_ReleaseRemovesLock(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	dest, _ := setupDest(t)
	pc, err := Preflight(context.Background(), Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	lockPath := filepath.Join(pc.DotDir, "lock")
	if _, statErr := os.Stat(lockPath); statErr != nil {
		t.Errorf("lock file should exist while held: %v", statErr)
	}
	if err := pc.Release(); err != nil {
		t.Errorf("Release: %v", err)
	}
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock file should be gone after Release: %v", statErr)
	}
}

func TestPreflight_DoubleReleaseOK(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	dest, _ := setupDest(t)
	pc, err := Preflight(context.Background(), Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	if err := pc.Release(); err != nil {
		t.Errorf("first Release: %v", err)
	}
	if err := pc.Release(); err != nil {
		t.Errorf("second Release should be no-op: %v", err)
	}
}

func TestPreflight_VerifyVolumeUnchanged_HappyPath(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	dest, _ := setupDest(t)
	ctx := context.Background()
	pc, err := Preflight(ctx, Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	t.Cleanup(func() { _ = pc.Release() })
	if err := pc.VerifyVolumeUnchanged(ctx); err != nil {
		t.Errorf("VerifyVolumeUnchanged on unchanged dest: %v", err)
	}
}

func TestPreflight_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Preflight(ctx, Options{DestRoot: "/tmp"})
	if err == nil {
		t.Fatal("expected cancelled ctx error")
	}
}
