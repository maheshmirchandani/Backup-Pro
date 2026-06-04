// Package filesystem implements the filesystem-type and mount-flag preflight
// gate per spec invariant #4 (require APFS or HFS+; refuse exFAT with a
// reformat recipe). Also rejects noexec mountpoints since the embedded rsync
// is extracted to and executed from the destination volume.
//
// Detection uses statfs(2) on macOS to read the filesystem type name and
// mount flags. Recognised types are APFS, HFS+ ("hfs"), exFAT, and msdos
// (FAT32); everything else is reported as Unknown and rejected.
//
// Mount flags inspected:
//   - MNT_RDONLY: surfaced; not refused here (callers may need write checks).
//   - MNT_NOEXEC: refused with a remount recipe (rsync needs exec).
//   - MNT_NOSUID / MNT_NODEV: surfaced; harmless for FlashBackup.
//
// macOS only at runtime. On non-darwin builds the package compiles but
// Inspect returns an "unsupported platform" error so the public API exists
// uniformly for cross-platform CI/dev hosts; Validate is pure Go and runs
// on any platform.
package filesystem
