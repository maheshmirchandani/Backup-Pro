// Package preflight composes the T0 preflight gates for the FlashBackup
// runner. Each gate lives in its own subpackage (lock, filesystem, symlink,
// codesign, volume_uuid); this package orders and orchestrates them.
//
// Call order (must not be reordered):
//
//  1. codesign self-verify (verify own binary before trusting embedded constants)
//  2. resolve DestRoot to absolute + stat
//  3. filesystem.Inspect + Validate (refuse exFAT/msdos/noexec)
//  4. symlink.WalkAndBaseline (refuse symlinks in dest path; record baseline)
//  5. volume_uuid.Capture (record UUID for invariant #30)
//  6. ensure DotDir = <DestRoot>/.flashbackup exists (mode 0700)
//  7. lock.Acquire (with captured volume_uuid)
//  8. state.ReadVersionFile (fail-closed; init is a separate path)
//  9. rsync.EnsureExtracted (verify SHA256 of embedded binary)
//
// On any gate failure, partial state is rolled back (e.g., lock released).
//
// Returned PreflightContext is stored by the runner. VerifyVolumeUnchanged()
// is called at every phase boundary to detect mid-run volume substitution
// (combines volume_uuid.Verify + symlink.Verify).
//
// Release() releases the lock and any held resources. Idempotent.
package preflight
