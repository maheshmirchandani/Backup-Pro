//go:build !darwin

package e2e

// clearChflagsForTest is a no-op on non-darwin platforms because the
// rsync extract's chflags step is also a no-op there. Kept symmetric
// with the darwin file so the test code can call it unconditionally
// without a build-tag dance at the call site.
func clearChflagsForTest(_ string) error {
	return nil
}
