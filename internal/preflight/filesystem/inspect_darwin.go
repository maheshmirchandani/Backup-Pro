//go:build darwin

package filesystem

import (
	"context"
	"fmt"
	"syscall"
)

// macOS <sys/mount.h> mount flag bit values. Stable kernel ABI; mirrored
// here as literal constants because Go's stdlib syscall package on darwin
// does not export these (only the linux build does). Verified against
// macOS 14/15 sources. Mirrors `golang.org/x/sys/unix` MNT_* values.
const (
	mntRDOnly uint32 = 0x00000001 // MNT_RDONLY
	mntNoExec uint32 = 0x00000004 // MNT_NOEXEC
	mntNoSUID uint32 = 0x00000008 // MNT_NOSUID
	mntNoDev  uint32 = 0x00000010 // MNT_NODEV
)

// Inspect returns Info for the given mountpoint via statfs(2). On error,
// returns a wrapped fs error. The context is checked once at entry; the
// statfs syscall itself is non-cancellable but completes in microseconds.
func Inspect(ctx context.Context, mountpoint string) (*Info, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("inspect filesystem %q: %w", mountpoint, err)
	}
	var buf syscall.Statfs_t
	if err := syscall.Statfs(mountpoint, &buf); err != nil {
		return nil, fmt.Errorf("statfs %q: %w", mountpoint, err)
	}
	raw := fstypenameToString(buf.Fstypename)
	return &Info{
		Type:       parseType(raw),
		TypeRaw:    raw,
		Mountpoint: mountpoint,
		Flags: MountFlags{
			ReadOnly: buf.Flags&mntRDOnly != 0,
			NoExec:   buf.Flags&mntNoExec != 0,
			NoSUID:   buf.Flags&mntNoSUID != 0,
			NoDev:    buf.Flags&mntNoDev != 0,
		},
	}, nil
}

// fstypenameToString converts the macOS Statfs_t.Fstypename byte array
// (declared as [16]int8 in the darwin syscall package) into a Go string,
// reading until the first NUL terminator.
func fstypenameToString(arr [16]int8) string {
	b := make([]byte, 0, len(arr))
	for _, c := range arr {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b)
}
