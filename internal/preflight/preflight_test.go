package preflight

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// setupDest mounts a fresh APFS DMG, seeds it with a valid version.json
// (so the FAIL-CLOSED gate 8 passes), and returns the mountpoint + captured
// VersionFile. Mount + skip plumbing lives in internal/testutil now.
func setupDest(t *testing.T) (string, state.VersionFile) {
	t.Helper()
	dest := testutil.MountTempVolume(t, "APFS")
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
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

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
	testutil.RequireMacOS(t)
	_, err := Preflight(context.Background(), Options{
		DestRoot:     "/nonexistent/never/will-exist-flashbackup-test",
		SkipCodesign: true,
	})
	if err == nil {
		t.Fatal("expected error on nonexistent DestRoot")
	}
}

func TestPreflight_NoVersionFile_FailsClosed(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	// Mount a clean volume but skip the version.json init step.
	// Gates 1-7 should pass; gate 8 must fail.
	dest := testutil.MountTempVolume(t, "APFS")
	pc, err := Preflight(context.Background(), Options{DestRoot: dest, SkipCodesign: true})
	if err == nil {
		if pc != nil {
			_ = pc.Release()
		}
		t.Fatal("expected gate 8 fail-closed when version.json missing")
	}
	// Grep-for-failure-source: every gate wraps with "gate N". A future
	// refactor that drops the gate name in the wrap would silently make
	// triage harder; this assertion pins the convention.
	if !strings.Contains(err.Error(), "gate 8") {
		t.Errorf("expected error to mention 'gate 8'; got %v", err)
	}
	// The lock must have been released by the rollback path.
	lockPath := filepath.Join(dest, ".flashbackup", "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock file should be cleaned up after gate 8 failure: stat err=%v", statErr)
	}
}

func TestPreflight_VerifyVolumeUnchanged_CancelledContext(t *testing.T) {
	pc := &PreflightContext{}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := pc.VerifyVolumeUnchanged(ctx); err == nil {
		t.Fatal("expected cancelled ctx error from VerifyVolumeUnchanged")
	}
}

func TestPreflight_VerifyVolumeUnchanged_NilReceiver(t *testing.T) {
	var pc *PreflightContext
	if err := pc.VerifyVolumeUnchanged(context.Background()); err == nil {
		t.Fatal("expected nil-receiver error from VerifyVolumeUnchanged")
	}
}

func TestPreflight_VerifyVolumeUnchanged_NilUUID(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, _ := setupDest(t)
	ctx := context.Background()
	pc, err := Preflight(ctx, Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	t.Cleanup(func() { _ = pc.Release() })
	// Simulate a baseline that was never captured (or was lost). The
	// volume_uuid.Verify nil-baseline guard fires, and the composition
	// must wrap it with the "uuid" qualifier so triage can locate it.
	pc.VolumeUUID = nil
	err = pc.VerifyVolumeUnchanged(ctx)
	if err == nil {
		t.Fatal("expected error when VolumeUUID baseline is nil")
	}
	if !strings.Contains(err.Error(), "uuid") {
		t.Errorf("expected error to mention 'uuid'; got %v", err)
	}
}

func TestPreflight_VerifyVolumeUnchanged_NilSymlinkBaseline(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, _ := setupDest(t)
	ctx := context.Background()
	pc, err := Preflight(ctx, Options{DestRoot: dest, SkipCodesign: true})
	if err != nil {
		t.Fatalf("Preflight: %v", err)
	}
	t.Cleanup(func() { _ = pc.Release() })
	// Simulate a missing symlink baseline. The symlink.Verify nil-baseline
	// guard fires, and the composition must wrap it with the
	// "symlink baseline" qualifier.
	pc.SymlinkBaseline = nil
	err = pc.VerifyVolumeUnchanged(ctx)
	if err == nil {
		t.Fatal("expected error when SymlinkBaseline is nil")
	}
	if !strings.Contains(err.Error(), "symlink baseline") {
		t.Errorf("expected error to mention 'symlink baseline'; got %v", err)
	}
}

func TestPreflight_ReleaseRemovesLock(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

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
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

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
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

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
