//go:build darwin

package rsync

import "syscall"

// ufImmutable mirrors UF_IMMUTABLE from <sys/stat.h> on macOS. A file with
// this flag set cannot be unlinked, renamed, or have its content modified
// until the flag is cleared (e.g. `chflags nouchg <path>`). The owner can
// set or clear it; no root required.
const ufImmutable = 0x00000002

// applyImmutableFlag attempts to mark the path with the macOS uchg (user
// immutable) flag. Best-effort: failure is returned to the caller but the
// caller may safely ignore it. Common failure modes:
//   - the underlying filesystem does not support chflags (e.g. some tmpfs)
//   - sandbox/SIP restrictions on certain system locations
//
// Per Hacker hat amendment 2026-06-03: combined with chmod 0500 and the
// SHA256-keyed extraction path, this is the best macOS-only mitigation
// available without fexecve.
func applyImmutableFlag(path string) error {
	return syscall.Chflags(path, ufImmutable)
}
