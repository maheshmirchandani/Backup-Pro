package symlink

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrSymlinkInPath means at least one component in the dest path is a symlink.
var ErrSymlinkInPath = errors.New("symlink in destination path")

// ErrComponentChanged means a path component's identity changed since T0.
var ErrComponentChanged = errors.New("path component changed since baseline")

// ComponentInfo captures the (device, inode) identity of one path component
// at T0. Used by Verify to detect mid-run swaps.
type ComponentInfo struct {
	Path  string // absolute path of this component
	Dev   uint64 // st_dev
	Ino   uint64 // st_ino
	IsDir bool
}

// Baseline is the captured identity of every component in the destination
// path walked at T0. Stored in PreflightContext for later VerifyVolumeUnchanged
// calls.
type Baseline struct {
	Components []ComponentInfo // ordered from root toward dest
}

// SymlinkError wraps ErrSymlinkInPath with the offending component for
// diagnostics.
type SymlinkError struct {
	Component string
}

func (e *SymlinkError) Error() string {
	return fmt.Sprintf("destination path component %q is a symlink (refusing for safety): %s", e.Component, ErrSymlinkInPath.Error())
}

func (e *SymlinkError) Unwrap() error { return ErrSymlinkInPath }

// ComponentChangedError wraps ErrComponentChanged with the offending component
// and a human-readable reason for diagnostics.
type ComponentChangedError struct {
	Component string
	Reason    string
}

func (e *ComponentChangedError) Error() string {
	return fmt.Sprintf("path component %q changed (%s): %s", e.Component, e.Reason, ErrComponentChanged.Error())
}

func (e *ComponentChangedError) Unwrap() error { return ErrComponentChanged }

// WalkAndBaseline walks every component of destPath (which must be absolute),
// using lstat at each level. Refuses if any component is a symlink. Returns
// a Baseline that records (dev, ino) for every component, for later Verify
// invocations at phase boundaries.
//
// destPath must be an absolute path; relative paths are rejected.
func WalkAndBaseline(ctx context.Context, destPath string) (*Baseline, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("walk and baseline: %w", err)
	}
	if !filepath.IsAbs(destPath) {
		return nil, fmt.Errorf("walk and baseline: destPath %q must be absolute", destPath)
	}
	components := splitComponents(destPath)
	base := &Baseline{Components: make([]ComponentInfo, 0, len(components))}
	for _, comp := range components {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("walk and baseline: %w", err)
		}
		info, err := os.Lstat(comp)
		if err != nil {
			return nil, fmt.Errorf("lstat %q: %w", comp, err)
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return nil, &SymlinkError{Component: comp}
		}
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			return nil, fmt.Errorf("lstat %q: cannot extract syscall.Stat_t", comp)
		}
		base.Components = append(base.Components, ComponentInfo{
			Path:  comp,
			Dev:   uint64(stat.Dev),
			Ino:   uint64(stat.Ino),
			IsDir: info.IsDir(),
		})
	}
	return base, nil
}

// Verify re-walks destPath with the same lstat-per-component logic and
// compares (dev, ino) against the captured baseline. Returns nil if every
// component still has the same identity. Returns an error if any component
// has changed (mount/remount, symlink swap, file replaced).
func Verify(ctx context.Context, destPath string, baseline *Baseline) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify symlink baseline: %w", err)
	}
	if baseline == nil {
		return fmt.Errorf("verify symlink baseline: nil baseline")
	}
	current, err := WalkAndBaseline(ctx, destPath)
	if err != nil {
		return fmt.Errorf("verify: rewalk failed: %w", err)
	}
	if len(current.Components) != len(baseline.Components) {
		return &ComponentChangedError{
			Component: destPath,
			Reason:    fmt.Sprintf("component count changed: %d -> %d", len(baseline.Components), len(current.Components)),
		}
	}
	for i, want := range baseline.Components {
		got := current.Components[i]
		if got.Dev != want.Dev || got.Ino != want.Ino {
			return &ComponentChangedError{
				Component: got.Path,
				Reason:    fmt.Sprintf("(dev,ino) was (%d,%d), now (%d,%d)", want.Dev, want.Ino, got.Dev, got.Ino),
			}
		}
	}
	return nil
}

// splitComponents returns ["/", "/Volumes", "/Volumes/USB", ...] for an
// absolute path. Always includes the root "/" as the first element.
func splitComponents(path string) []string {
	path = filepath.Clean(path)
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	out := []string{"/"}
	cur := ""
	for _, p := range parts {
		if p == "" {
			continue
		}
		cur = cur + "/" + p
		out = append(out, cur)
	}
	return out
}
