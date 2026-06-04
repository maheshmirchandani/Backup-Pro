package filesystem

import (
	"errors"
	"fmt"
)

// FilesystemType is a small enum of filesystems we care about for the
// FlashBackup destination volume.
type FilesystemType string

const (
	TypeAPFS    FilesystemType = "apfs"
	TypeHFSPlus FilesystemType = "hfs" // HFS+ (Mac OS Extended)
	TypeExFAT   FilesystemType = "exfat"
	TypeMSDOS   FilesystemType = "msdos" // FAT32
	TypeUnknown FilesystemType = "unknown"
)

// Info describes a mountpoint's filesystem properties relevant to preflight.
type Info struct {
	Type       FilesystemType
	TypeRaw    string // raw type string as reported by statfs (e.g. "apfs", "hfs")
	Mountpoint string
	Flags      MountFlags
}

// MountFlags captures the mount-time flags that affect FlashBackup operation.
type MountFlags struct {
	ReadOnly bool
	NoExec   bool // refuse this: embedded rsync needs exec on the mountpoint
	NoSUID   bool // OK; we don't suid
	NoDev    bool // OK; we don't create devices
}

// ErrFilesystemUnsupported means the filesystem type isn't APFS or HFS+.
var ErrFilesystemUnsupported = errors.New("filesystem unsupported")

// ErrFilesystemNoExec means the mountpoint is mounted with the noexec flag.
var ErrFilesystemNoExec = errors.New("filesystem mounted noexec")

// UnsupportedError wraps ErrFilesystemUnsupported with the offending Info so
// callers can render a precise diagnostic.
type UnsupportedError struct {
	Info *Info
}

func (e *UnsupportedError) Error() string {
	raw := "unknown"
	mp := ""
	if e.Info != nil {
		if e.Info.TypeRaw != "" {
			raw = e.Info.TypeRaw
		}
		mp = e.Info.Mountpoint
	}
	return fmt.Sprintf(
		"filesystem at %s is %s; FlashBackup requires APFS or HFS+.\n"+
			"Reformat with:\n"+
			"    diskutil eraseDisk APFS FLASHBKP /dev/diskN\n"+
			"(replace /dev/diskN with the device from `diskutil list`; ALL DATA WILL BE LOST)",
		mp, raw,
	)
}

func (e *UnsupportedError) Unwrap() error { return ErrFilesystemUnsupported }

// Validate returns nil if the filesystem is acceptable for FlashBackup
// (APFS or HFS+, executable). Otherwise returns an error: an *UnsupportedError
// for type rejections, or an error wrapping ErrFilesystemNoExec for noexec.
func Validate(info *Info) error {
	if info == nil {
		return fmt.Errorf("validate filesystem: nil info")
	}
	switch info.Type {
	case TypeAPFS, TypeHFSPlus:
		// accepted
	default:
		return &UnsupportedError{Info: info}
	}
	if info.Flags.NoExec {
		return fmt.Errorf(
			"mountpoint %s is mounted with noexec; FlashBackup needs to extract and exec rsync from this volume. "+
				"Remount without noexec: sudo mount -uw -o exec %s: %w",
			info.Mountpoint, info.Mountpoint, ErrFilesystemNoExec,
		)
	}
	return nil
}

// parseType maps a lowercase statfs type name to a FilesystemType.
func parseType(raw string) FilesystemType {
	switch raw {
	case "apfs":
		return TypeAPFS
	case "hfs":
		return TypeHFSPlus
	case "exfat":
		return TypeExFAT
	case "msdos":
		return TypeMSDOS
	default:
		return TypeUnknown
	}
}
