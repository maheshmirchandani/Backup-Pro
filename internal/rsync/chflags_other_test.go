//go:build !darwin

package rsync

// clearAllFlagsForTest is a no-op on non-darwin platforms (no chflags
// equivalent and the production code's applyImmutableFlag is also a
// no-op there).
func clearAllFlagsForTest(_ string) error {
	return nil
}
