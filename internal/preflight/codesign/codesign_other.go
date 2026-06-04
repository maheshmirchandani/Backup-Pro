//go:build !darwin

package codesign

import (
	"context"
	"fmt"
)

// verifySelf on non-darwin platforms is a stub that returns a clear error.
// The package still compiles cross-platform so dev tooling (e.g. running
// `go vet ./...` on a linux CI runner) doesn't break, but the function
// itself refuses to run because there is no codesign equivalent outside
// macOS in this product's threat model.
func verifySelf(_ context.Context) error {
	return fmt.Errorf("codesign verify: only supported on darwin")
}
