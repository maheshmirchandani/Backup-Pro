//go:build !darwin

package testutil

import (
	"os"
	"runtime"
	"testing"
)

// RequireE2E mirrors the darwin variant for cross-platform builds. On
// non-darwin platforms the FLASHBACKUP_E2E gate is honoured the same way,
// but the test will almost certainly skip earlier via RequireMacOS.
func RequireE2E(t *testing.T) {
	t.Helper()
	if os.Getenv("FLASHBACKUP_E2E") != "1" {
		t.Skip("requires FLASHBACKUP_E2E=1 (mounts a DMG via hdiutil)")
	}
}

// RequireMacOS skips immediately on non-darwin platforms. This is the
// guard that should fire first in any test that uses the mount helpers;
// if it does and the rest of the helpers are unreached, the panic
// fallback below never triggers.
func RequireMacOS(t *testing.T) {
	t.Helper()
	t.Skipf("test is macOS-only; runtime.GOOS=%s", runtime.GOOS)
}

// RequireDiskutil is unreachable on non-darwin because RequireMacOS
// short-circuits first. Defined as a no-op skip for symmetry with the
// darwin variant so cross-platform builds compile.
func RequireDiskutil(t *testing.T) {
	t.Helper()
	t.Skipf("diskutil only present on darwin; runtime.GOOS=%s", runtime.GOOS)
}

// RequireHdiutil mirrors RequireDiskutil: a defensive skip rather than a
// panic so a misordered test (RequireMacOS missing) still degrades to a
// skip rather than crashing the run.
func RequireHdiutil(t *testing.T) {
	t.Helper()
	t.Skipf("hdiutil only present on darwin; runtime.GOOS=%s", runtime.GOOS)
}

// MountTempVolume panics if reached. Any caller that gets past
// RequireMacOS (and so reaches this stub) has skipped the guard and
// would silently no-op on non-darwin; panicking loudly surfaces the
// mis-ordered Skip call.
func MountTempVolume(t *testing.T, fsType string) string {
	t.Helper()
	panic("testutil.MountTempVolume called on non-darwin; missing RequireMacOS guard?")
}
