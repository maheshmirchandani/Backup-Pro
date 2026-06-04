//go:build !darwin

package rsync

// applyImmutableFlag is a no-op on non-darwin platforms. The product is
// macOS-only at runtime; this stub exists solely so that dev/CI hosts on
// linux can still build and run the unit tests for extraction logic.
func applyImmutableFlag(_ string) error {
	return nil
}
