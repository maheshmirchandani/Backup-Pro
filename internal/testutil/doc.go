// Package testutil holds test-only helpers shared across FlashBackup
// internal packages. The package boundary exists to centralize the
// hdiutil-mount + macOS-skip dance that six test files previously
// duplicated (preflight, runner x3, verify, cmd/flashbackup).
//
// Production code MUST NOT import this package. The helpers call t.Skip,
// t.Fatal, and t.Cleanup; calling them from non-test code would either
// panic (nil t) or wire test-only behavior into the production binary.
//
// The package is split into darwin / non-darwin files via build tags. On
// non-darwin platforms the mount helpers panic if called past the
// RequireMacOS guard (defence in depth: a mis-ordered Skip call in a new
// test should fail loudly rather than silently no-op the mount).
//
// API surface (intentionally tiny):
//
//	RequireE2E(t)             - skip unless FLASHBACKUP_E2E=1
//	RequireMacOS(t)           - skip unless runtime.GOOS=="darwin"
//	RequireDiskutil(t)        - skip if /usr/sbin/diskutil missing
//	RequireHdiutil(t)         - skip if /usr/bin/hdiutil missing
//	MountTempVolume(t, fs)    - create + attach a 10 MB DMG; auto-detach
//
// MountTempVolume returns the mountpoint (e.g. "/Volumes/Flashbackup1234")
// and registers a t.Cleanup that detaches the DMG. The fs argument is
// passed through to `hdiutil create -fs <fs>`; callers pass "APFS",
// "HFS+", "ExFAT", or any other tag hdiutil accepts. The 10 MB size is
// hard-coded because every existing call site uses it; if a future test
// needs a different size, extend the API rather than parametrizing here.
package testutil
