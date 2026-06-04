package volume_uuid

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"
)

const diskutilPath = "/usr/sbin/diskutil"

// requireMacOS skips on non-Darwin: the product is macOS-only per spec.
func requireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("volume_uuid is macOS-only; runtime.GOOS=%s", runtime.GOOS)
	}
}

// requireDiskutil skips when /usr/sbin/diskutil is absent (stripped images).
// drives.Query shells out to diskutil, so without it Capture/Verify cannot
// produce a real UUID. Tests that only assert pure-Go behavior (nil baseline,
// pre-cancelled ctx short-circuit) do NOT call this.
func requireDiskutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(diskutilPath); err != nil {
		t.Skipf("%s not available: %v", diskutilPath, err)
	}
}

// TestCapture_RootVolume exercises the happy path against "/", which always
// exists on macOS and has a stable VolumeUUID.
func TestCapture_RootVolume(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	captured, err := Capture(ctx, "/")
	if err != nil {
		t.Fatalf("Capture(/): %v", err)
	}
	if captured.UUID == "" {
		t.Error("expected non-empty UUID for root volume")
	}
	if captured.Mountpoint != "/" {
		t.Errorf("Mountpoint = %q, want %q", captured.Mountpoint, "/")
	}
}

// TestVerify_SameVolume captures the root volume and immediately re-verifies.
// Should succeed (no time for the UUID to actually change).
func TestVerify_SameVolume(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	captured, err := Capture(ctx, "/")
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if err := Verify(ctx, captured); err != nil {
		t.Errorf("Verify same volume should succeed: %v", err)
	}
}

// TestVerify_DetectsChangedUUID injects a fake baseline UUID and asserts
// Verify returns ErrVolumeUUIDChanged AND a *VolumeUUIDChangedError with
// the correct diagnostic fields populated.
func TestVerify_DetectsChangedUUID(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	captured := &Captured{
		Mountpoint: "/",
		UUID:       "00000000-0000-0000-0000-000000000000",
	}
	err := Verify(ctx, captured)
	if err == nil {
		t.Fatal("expected error on UUID mismatch")
	}
	if !errors.Is(err, ErrVolumeUUIDChanged) {
		t.Errorf("expected error chain to contain ErrVolumeUUIDChanged, got %v", err)
	}
	var changed *VolumeUUIDChangedError
	if !errors.As(err, &changed) {
		t.Fatalf("expected *VolumeUUIDChangedError type, got %T", err)
	}
	if changed.Mountpoint != "/" {
		t.Errorf("Mountpoint = %q, want %q", changed.Mountpoint, "/")
	}
	if changed.Expected != captured.UUID {
		t.Errorf("Expected = %q, want %q", changed.Expected, captured.UUID)
	}
	if changed.Got == "" || changed.Got == captured.UUID {
		t.Errorf("Got = %q; want a real UUID different from baseline", changed.Got)
	}
}

// TestCapture_CancelledContext verifies pre-cancelled ctx short-circuits
// before any diskutil shellout AND that the returned error chain carries
// context.Canceled (so the runner can distinguish ctx-cancel from a real
// USB identity flip).
func TestCapture_CancelledContext(t *testing.T) {
	requireMacOS(t)
	// No requireDiskutil: cancellation must short-circuit before exec.

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Capture(ctx, "/")
	if err == nil {
		t.Fatal("expected cancelled ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error chain to contain context.Canceled, got %v", err)
	}
}

// TestVerify_NilBaseline pins the nil-baseline guard. A nil baseline would
// be a programmer error (Task 20 wiring bug), and the right reaction is a
// clear error, not a nil-pointer panic.
func TestVerify_NilBaseline(t *testing.T) {
	err := Verify(context.Background(), nil)
	if err == nil {
		t.Fatal("expected error on nil baseline")
	}
}

// TestVerify_CancelledContext verifies pre-cancelled ctx short-circuits
// Verify before the diskutil shellout. Symmetric with Capture's ctx guard.
func TestVerify_CancelledContext(t *testing.T) {
	requireMacOS(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := Verify(ctx, &Captured{Mountpoint: "/", UUID: "irrelevant"})
	if err == nil {
		t.Fatal("expected cancelled ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected error chain to contain context.Canceled, got %v", err)
	}
}
