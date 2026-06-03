// Package paths computes the namespace prefix used to distinguish multiple
// machines/users sharing the same USB destination. Invariants #5 + #15.
package paths

import (
	"fmt"
	"path/filepath"
	"strings"
)

// Prefix returns the namespace directory name "<safe-hostname>-<safe-username>".
// macOS hostnames may contain dots ("macbook.local"); replaced with hyphens for
// filesystem-friendliness.
func Prefix(hostname, username string) string {
	safeHost := strings.ReplaceAll(hostname, ".", "-")
	safeUser := strings.ReplaceAll(username, ".", "-")
	return safeHost + "-" + safeUser
}

// Namespaced returns the full destination path:
//
//	<destRoot>/<Prefix(hostname,username)>/<srcRelative>
func Namespaced(destRoot, hostname, username, srcRelative string) string {
	return filepath.Join(destRoot, Prefix(hostname, username), srcRelative)
}

// SourceFromNamespaced strips the destination root and namespace prefix,
// returning the source-relative path. Returns an error if the destPath does
// not have the expected prefix.
func SourceFromNamespaced(destPath, destRoot, hostname, username string) (string, error) {
	prefix := filepath.Join(destRoot, Prefix(hostname, username))
	rel, err := filepath.Rel(prefix, destPath)
	if err != nil {
		return "", fmt.Errorf("path %q is not under namespace %q: %w", destPath, prefix, err)
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("path %q escapes namespace %q", destPath, prefix)
	}
	return rel, nil
}
