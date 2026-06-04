//go:build !darwin

package lock

import "os"

// lookupHostUUID returns the OS hostname as a best-effort machine identifier
// on non-darwin platforms (primarily Linux CI). Production FlashBackup runs
// only on macOS where the darwin build is used instead.
func lookupHostUUID() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown-host"
	}
	return h
}
