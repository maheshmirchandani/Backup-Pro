//go:build darwin

package main

import "syscall"

// clearImmutableForTestInit clears every chflags bit on path. The init
// subcommand's step-5 extract calls rsync.EnsureExtracted, which on
// darwin sets the uchg (user immutable) flag on the extracted binary.
// Without clearing it, t.TempDir's RemoveAll trips on EPERM and the
// test process leaks DMG-backing tmpfiles in /tmp.
//
// Mirrors internal/rsync.clearAllFlagsForTest; duplicated here because
// the helper is test-only and re-exporting it would surface an
// implementation detail in the public API.
func clearImmutableForTestInit(path string) error {
	return syscall.Chflags(path, 0)
}
