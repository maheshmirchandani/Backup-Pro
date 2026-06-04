//go:build darwin

package rsync

import "syscall"

// clearAllFlagsForTest clears every chflags bit on the given path. Used by
// tests to undo the production code's chflags uchg before removing or
// rewriting a file.
func clearAllFlagsForTest(path string) error {
	return syscall.Chflags(path, 0)
}
