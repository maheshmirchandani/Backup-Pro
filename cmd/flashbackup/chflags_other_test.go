//go:build !darwin

package main

// clearImmutableForTestInit is a no-op on non-darwin platforms; the
// rsync extract's chflags step is also a no-op there. Kept symmetric
// with the darwin file so the test code can call it unconditionally.
func clearImmutableForTestInit(_ string) error {
	return nil
}
