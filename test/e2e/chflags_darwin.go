//go:build darwin

package e2e

import "syscall"

// clearChflagsForTest clears every chflags bit on path. The init
// extract sets the uchg (user immutable) flag on the embedded rsync
// binary it lays down at <usb>/.flashbackup/bin/<sha>/rsync. Without
// clearing it, the t.Cleanup hdiutil-detach fails on EPERM and the
// test process leaks tmpfiles in /tmp.
//
// Mirrors cmd/flashbackup's clearImmutableForTestInit helper and
// internal/rsync.clearAllFlagsForTest. The helper is test-only and
// re-exporting it from internal/rsync would surface an implementation
// detail in the public API.
func clearChflagsForTest(path string) error {
	return syscall.Chflags(path, 0)
}
