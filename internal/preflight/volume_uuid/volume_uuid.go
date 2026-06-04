package volume_uuid

import (
	"context"
	"errors"
	"fmt"

	"github.com/maheshmirchandani/Backup-Pro/internal/drives"
)

// ErrVolumeUUIDChanged means the live UUID at <mountpoint> differs from
// the captured UUID, suggesting whole-volume substitution (e.g., USB
// unplugged and a different drive mounted at the same path).
var ErrVolumeUUIDChanged = errors.New("volume UUID changed since T0 baseline")

// Captured records the VolumeUUID + mountpoint at T0. Stored in
// PreflightContext for invariant #30 cross-checks at every phase boundary.
type Captured struct {
	Mountpoint string
	UUID       string // canonical macOS VolumeUUID (e.g. "ABCDEF01-2345-...")
}

// VolumeUUIDChangedError wraps ErrVolumeUUIDChanged with diagnostics.
// Use errors.Is(err, ErrVolumeUUIDChanged) for sentinel checks and
// errors.As(err, &v) where v is *VolumeUUIDChangedError to extract
// Mountpoint/Expected/Got for surfacing in runner-level event logs.
type VolumeUUIDChangedError struct {
	Mountpoint string
	Expected   string
	Got        string
}

func (e *VolumeUUIDChangedError) Error() string {
	return fmt.Sprintf("volume UUID at %q changed: expected %q, got %q: %s",
		e.Mountpoint, e.Expected, e.Got, ErrVolumeUUIDChanged.Error())
}

func (e *VolumeUUIDChangedError) Unwrap() error { return ErrVolumeUUIDChanged }

// Capture queries diskutil for the mountpoint's VolumeUUID. Returns a
// Captured snapshot suitable for storing in PreflightContext.
//
// Returns an error if ctx is cancelled, the diskutil shellout fails, or
// diskutil reports an empty VolumeUUID for the mountpoint (which would
// defeat the point of an identity baseline).
func Capture(ctx context.Context, mountpoint string) (*Captured, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("capture volume uuid: %w", err)
	}
	vol, err := drives.Query(ctx, mountpoint)
	if err != nil {
		return nil, fmt.Errorf("capture volume uuid for %q: %w", mountpoint, err)
	}
	if vol.VolumeUUID == "" {
		return nil, fmt.Errorf("capture volume uuid for %q: empty UUID", mountpoint)
	}
	return &Captured{
		Mountpoint: mountpoint,
		UUID:       vol.VolumeUUID,
	}, nil
}

// Verify re-queries the live VolumeUUID at the captured mountpoint and
// compares to baseline.UUID. Returns nil if unchanged. Returns a
// *VolumeUUIDChangedError (which wraps ErrVolumeUUIDChanged via Unwrap)
// when the live UUID differs from the baseline.
//
// Callers should treat any non-nil return as "abort the current phase":
// either the underlying diskutil query failed (USB ejected, permissions
// flip) or the volume identity actually changed. Both are unsafe to
// continue past per invariant #30.
func Verify(ctx context.Context, baseline *Captured) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify volume uuid: %w", err)
	}
	if baseline == nil {
		return fmt.Errorf("verify volume uuid: nil baseline")
	}
	vol, err := drives.Query(ctx, baseline.Mountpoint)
	if err != nil {
		return fmt.Errorf("verify volume uuid for %q: %w", baseline.Mountpoint, err)
	}
	if vol.VolumeUUID != baseline.UUID {
		return &VolumeUUIDChangedError{
			Mountpoint: baseline.Mountpoint,
			Expected:   baseline.UUID,
			Got:        vol.VolumeUUID,
		}
	}
	return nil
}
