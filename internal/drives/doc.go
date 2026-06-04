// Package drives enumerates mounted macOS volumes via /usr/sbin/diskutil.
//
// Invariants:
//   - Absolute binary path is used (`/usr/sbin/diskutil`); no $PATH lookup
//     per CISO discipline (no PATH-injection risk).
//   - Public API takes ctx as the first parameter and honors cancellation.
//   - Errors are wrapped with `fmt.Errorf("<verb> <noun>: %w", err)` to
//     preserve the chain for runner-level event surfaces.
//   - The package is a read-only observer of the system. It never executes
//     mount, unmount, format, or write operations. It only shells out to
//     `diskutil info -plist <mountpoint>` and parses the resulting plist.
//   - Volume identity for future re-verification (Task 19 / invariant #30)
//     is the VolumeUUID field. MountPoint is unstable across remounts and
//     must NOT be used as identity.
//
// Scope (v0.1, Task 10):
//   - Enumerate entries under /Volumes/ and emit one Volume per entry.
//   - Skip the root volume by default; opt-in via includeRoot=true.
//   - macOS-only; no portability layer (the whole product is macOS-only).
//
// Consumers (current and planned): `flashbackup status` (Task 39) for the
// USB capacity surface, and `internal/preflight/volume_uuid` (Task 19) for
// per-phase USB identity re-verification.
package drives
