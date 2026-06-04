package drives

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"howett.net/plist"
)

// diskutilPath is the absolute path to the macOS diskutil binary.
// Absolute path is required per CISO discipline: no $PATH lookup.
const diskutilPath = "/usr/sbin/diskutil"

// volumesRoot is the conventional macOS mount-root for non-system volumes.
const volumesRoot = "/Volumes"

// Volume describes one mounted macOS volume captured from `diskutil info -plist`.
// VolumeUUID is the stable identifier across remounts (Task 19 uses it for
// per-phase USB identity verification, invariant #30); MountPoint is NOT stable.
type Volume struct {
	Name           string // VolumeName (user-visible label)
	MountPoint     string // e.g. /Volumes/FLASHBKP or /
	DiskID         string // e.g. disk4s1 (DeviceIdentifier from diskutil)
	FilesystemType string // e.g. "APFS", "HFS+", "ExFAT", "MS-DOS"
	VolumeUUID     string // stable identifier across remounts (invariant #30)
	BytesTotal     int64  // Size from diskutil
	BytesFree      int64  // FreeSpace from diskutil; 0 on snapshot-mounted roots
	IsRemovable    bool   // Removable=Yes (matches the bus-level removable bit)
	IsInternal     bool   // Internal=Yes (Apple-internal bus, e.g. NVMe on Apple Silicon)
}

// rawVolumeInfo maps a subset of the `diskutil info -plist` output. Field
// names match the plist keys exactly; howett.net/plist uses `plist:` tags.
// Only fields used by Volume are decoded; unknown keys are ignored.
type rawVolumeInfo struct {
	VolumeName       string `plist:"VolumeName"`
	DeviceIdentifier string `plist:"DeviceIdentifier"`
	MountPoint       string `plist:"MountPoint"`
	FilesystemName   string `plist:"FilesystemName"`
	FilesystemType   string `plist:"FilesystemType"`
	VolumeUUID       string `plist:"VolumeUUID"`
	Size             int64  `plist:"Size"`
	FreeSpace        int64  `plist:"FreeSpace"`
	Removable        bool   `plist:"Removable"`
	Internal         bool   `plist:"Internal"`
}

// EnumerateVolumes returns all currently-mounted volumes discovered under
// /Volumes/. For each candidate it shells out to `diskutil info -plist` and
// parses the result.
//
// If includeRoot is false (default for most callers, e.g. `flashbackup status`
// listing candidate USB destinations), the boot volume (MountPoint=="/") is
// elided from the result. Pass true to include it.
//
// Volumes whose diskutil query fails (e.g. race with eject, permission
// quirk on a special mount) are skipped silently; this matches the
// best-effort contract of an observer package. The runner-level event log
// is the right place for "tried and failed" surfacing, not this read path.
//
// The returned slice is in /Volumes/ ReadDir order (typically lexical) and
// is never nil; an empty result returns []Volume{}-equivalent (Go zero len).
func EnumerateVolumes(ctx context.Context, includeRoot bool) ([]Volume, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("enumerate volumes: %w", err)
	}

	entries, err := os.ReadDir(volumesRoot)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", volumesRoot, err)
	}

	vols := make([]Volume, 0, len(entries)+1)

	// Optionally probe the root volume directly. `/Volumes/` does not
	// contain "/" as an entry, so includeRoot requires a separate query.
	if includeRoot {
		if vol, qErr := Query(ctx, "/"); qErr == nil {
			vols = append(vols, *vol)
		}
		// If the root query fails we continue silently; per the contract
		// above, EnumerateVolumes is best-effort over per-volume failures.
	}

	for _, e := range entries {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("enumerate volumes: %w", err)
		}
		mount := filepath.Join(volumesRoot, e.Name())
		vol, qErr := Query(ctx, mount)
		if qErr != nil {
			// Race with unmount, permission error on a special mount, etc.
			// Best-effort: skip this candidate.
			continue
		}
		// /Volumes/ may contain a symlink alias to "/" (some macOS configs
		// expose the boot volume as /Volumes/Macintosh HD). Honor includeRoot
		// for that case too: if we already added "/" above, don't double-emit.
		if vol.MountPoint == "/" {
			if !includeRoot {
				continue
			}
			// includeRoot=true: we already added "/" via the explicit probe.
			continue
		}
		vols = append(vols, *vol)
	}
	return vols, nil
}

// Query runs `diskutil info -plist <mountpoint>` and returns the decoded
// Volume. The mountpoint is passed as an argv-separate argument (no shell),
// so the usual shell-metacharacter concerns do not apply.
//
// Consumers (current): internal/preflight/volume_uuid uses Query to capture
// the destination USB's VolumeUUID at T0 (invariant #30) and to re-verify it
// at every phase boundary. EnumerateVolumes uses Query as its per-mountpoint
// probe. The function is safe to call concurrently.
//
// The returned *Volume is never nil on success. On error the *Volume is nil
// and the error chain includes the underlying cause (exec error, plist parse
// error, or ctx error from CommandContext).
func Query(ctx context.Context, mountpoint string) (*Volume, error) {
	cmd := exec.CommandContext(ctx, diskutilPath, "info", "-plist", mountpoint)
	// Force LC_ALL=C for locale-independent diskutil output. The XML plist
	// uses ASCII keys today, but error strings on non-English locales differ
	// and could confuse future parsers; mirror the lock package precedent.
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("diskutil info %q: %w", mountpoint, err)
	}
	var info rawVolumeInfo
	if _, err := plist.Unmarshal(out, &info); err != nil {
		return nil, fmt.Errorf("parse plist for %q: %w", mountpoint, err)
	}
	vol := volumeFromRaw(&info)
	return &vol, nil
}

// volumeFromRaw projects a rawVolumeInfo into the public Volume shape.
// Prefers FilesystemName (e.g. "APFS") over FilesystemType (e.g. "apfs")
// for user-facing display; falls back to FilesystemType when name is empty.
func volumeFromRaw(info *rawVolumeInfo) Volume {
	return Volume{
		Name:           info.VolumeName,
		MountPoint:     info.MountPoint,
		DiskID:         info.DeviceIdentifier,
		FilesystemType: firstNonEmpty(info.FilesystemName, info.FilesystemType),
		VolumeUUID:     info.VolumeUUID,
		BytesTotal:     info.Size,
		BytesFree:      info.FreeSpace,
		IsRemovable:    info.Removable,
		IsInternal:     info.Internal,
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
