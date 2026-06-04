//go:build !darwin

package filesystem

import (
	"context"
	"fmt"
	"runtime"
)

// Inspect is a non-darwin stub. FlashBackup is macOS-only at runtime; this
// stub exists so the public API surface compiles on Linux dev/CI hosts and
// pure-Go tests (e.g. Validate) can still run there.
func Inspect(_ context.Context, mountpoint string) (*Info, error) {
	return nil, fmt.Errorf("inspect filesystem %q: unsupported platform (GOOS=%s)", mountpoint, runtime.GOOS)
}
