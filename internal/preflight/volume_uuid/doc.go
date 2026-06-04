// Package volume_uuid captures a USB volume's VolumeUUID at T0 (preflight)
// and re-verifies it at every phase boundary per spec invariant #30.
// Defends against whole-volume substitution (e.g., USB unplugged mid-run
// and a different drive plugged in at the same /Volumes/<name> path).
//
// Complementary to internal/preflight/symlink, NOT redundant:
//   - symlink captures (device, inode) baseline per path component; catches
//     in-place file/dir replacement and symlink swap.
//   - volume_uuid captures the macOS VolumeUUID; catches whole-volume
//     substitution (different USB stick at the same mount path).
//
// Task 20 (preflight integrate) stores Captured in PreflightContext.VolumeUUID
// and the runner calls Verify at every phase boundary, aborting on mismatch.
package volume_uuid
