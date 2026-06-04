// Package symlink implements the destination-path symlink-refusal preflight
// gate AND captures a (device, inode) baseline per path component for
// invariant #30 verification at every phase boundary.
//
// Invariants enforced:
//   - Symlink-in-dest-path refusal: no component of destPath may be a
//     symlink. Defends against an attacker pre-creating <USB>/Documents
//     as a symlink to /etc.
//   - Component identity baseline for #30 cross-checks: (dev, ino) per
//     component captured at T0. Verify() at phase boundaries detects
//     mid-run remount, symlink swap, or file replacement.
//
// Usage in Task 20 (preflight integration):
//
//	baseline, err := symlink.WalkAndBaseline(ctx, destPath)
//	// store baseline in PreflightContext for later phases
//	defer func() {
//	    if err := symlink.Verify(ctx, destPath, baseline); err != nil {
//	        // abort the phase: dest path changed mid-run
//	    }
//	}()
package symlink
