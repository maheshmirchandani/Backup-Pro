//go:build darwin

package lock

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
)

var ioregUUIDRE = regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)

// lookupHostUUID reads the system-wide IOPlatformUUID via ioreg. This is the
// stable identifier macOS itself uses internally; survives renames, network
// changes, and user account changes. Falls back to os.Hostname() if ioreg
// fails or its output cannot be parsed.
func lookupHostUUID() string {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return fallbackHostname()
	}
	m := ioregUUIDRE.FindSubmatch(out)
	if len(m) < 2 {
		return fallbackHostname()
	}
	return strings.TrimSpace(string(m[1]))
}

func fallbackHostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown-host"
	}
	return h
}
