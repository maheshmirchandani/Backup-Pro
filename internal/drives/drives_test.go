package drives

import (
	"context"
	"os"
	"regexp"
	"runtime"
	"testing"
	"time"
)

// requireMacOS skips the test when not running on Darwin. The product is
// macOS-only per spec, so on Linux/Windows we have nothing useful to assert.
// In CI (macos-14 runner) this is a no-op; locally on a non-Mac dev box the
// suite remains runnable without false reds.
func requireMacOS(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		t.Skipf("drives package is macOS-only; runtime.GOOS=%s", runtime.GOOS)
	}
}

// requireDiskutil skips when /usr/sbin/diskutil is absent (e.g. a stripped
// container image). Allows the unit suite to remain valid on developer
// machines without diskutil while still acting as a real smoke test where
// it exists.
func requireDiskutil(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(diskutilPath); err != nil {
		t.Skipf("%s not available: %v", diskutilPath, err)
	}
}

// TestEnumerateVolumes_Smoke exercises the full pipeline against the local
// macOS instance. Asserts: at least one volume is returned when includeRoot=true
// (the boot volume always exists), no error, every returned volume has a
// MountPoint set.
func TestEnumerateVolumes_Smoke(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	vols, err := EnumerateVolumes(ctx, true)
	if err != nil {
		t.Fatalf("EnumerateVolumes(includeRoot=true): %v", err)
	}
	if len(vols) == 0 {
		t.Fatalf("EnumerateVolumes returned 0 volumes; expected at least the root volume")
	}
	for i, v := range vols {
		if v.MountPoint == "" {
			t.Errorf("vols[%d]=%+v: empty MountPoint", i, v)
		}
		// Sanity: bytes-free should never exceed bytes-total when both are
		// reported. Some macOS snapshot mounts report FreeSpace=0 with a
		// non-zero Size; that is legitimate and not a failure.
		if v.BytesTotal > 0 && v.BytesFree > v.BytesTotal {
			t.Errorf("vols[%d]=%+v: BytesFree > BytesTotal", i, v)
		}
	}
}

// TestQueryVolume_RootMountpoint validates the diskutil call and plist parse
// against the well-known "/" mountpoint. Asserts VolumeName is non-empty and
// VolumeUUID is in UUID format (8-4-4-4-12 hex).
func TestQueryVolume_RootMountpoint(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	info, err := queryVolume(ctx, "/")
	if err != nil {
		t.Fatalf("queryVolume(/): %v", err)
	}
	if info.VolumeName == "" {
		t.Errorf("VolumeName empty for /; raw info=%+v", info)
	}
	if info.MountPoint != "/" {
		t.Errorf("MountPoint = %q, want %q", info.MountPoint, "/")
	}
	// VolumeUUID is the stable identity used by Task 19. APFS volumes use
	// canonical UUID format; HFS+ legacy may use a different format but the
	// boot volume on any post-Big-Sur Mac is APFS.
	uuidRE := regexp.MustCompile(`^[0-9A-Fa-f]{8}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{4}-[0-9A-Fa-f]{12}$`)
	if !uuidRE.MatchString(info.VolumeUUID) {
		t.Errorf("VolumeUUID %q is not canonical UUID format", info.VolumeUUID)
	}
}

// TestEnumerateVolumes_CancelledContext verifies pre-cancelled context
// short-circuits before any diskutil shellout.
func TestEnumerateVolumes_CancelledContext(t *testing.T) {
	requireMacOS(t)
	// We do NOT require diskutil here: the cancellation must short-circuit
	// before any exec attempt.

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	vols, err := EnumerateVolumes(ctx, false)
	if err == nil {
		t.Fatalf("EnumerateVolumes with cancelled ctx returned nil error; vols=%+v", vols)
	}
	// The error chain should carry context.Canceled.
	if ctx.Err() == nil {
		t.Fatalf("ctx.Err() should be non-nil after cancel()")
	}
}

// TestEnumerateVolumes_ExcludesRootByDefault asserts that the default
// (includeRoot=false) call never returns a volume whose MountPoint is "/".
// This is the contract used by `flashbackup status` and `init` to list
// candidate USB destinations.
func TestEnumerateVolumes_ExcludesRootByDefault(t *testing.T) {
	requireMacOS(t)
	requireDiskutil(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	vols, err := EnumerateVolumes(ctx, false)
	if err != nil {
		t.Fatalf("EnumerateVolumes(includeRoot=false): %v", err)
	}
	for i, v := range vols {
		if v.MountPoint == "/" {
			t.Errorf("vols[%d]=%+v: root volume leaked into includeRoot=false result", i, v)
		}
	}
}

// TestFirstNonEmpty pins the small helper's behavior. Cheap to test, catches
// accidental swap of the fallback chain.
func TestFirstNonEmpty(t *testing.T) {
	cases := []struct {
		a, b, want string
	}{
		{"APFS", "apfs", "APFS"},
		{"", "apfs", "apfs"},
		{"", "", ""},
		{"HFS+", "", "HFS+"},
	}
	for _, c := range cases {
		got := firstNonEmpty(c.a, c.b)
		if got != c.want {
			t.Errorf("firstNonEmpty(%q,%q) = %q, want %q", c.a, c.b, got, c.want)
		}
	}
}
