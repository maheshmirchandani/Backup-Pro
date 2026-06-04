package preflight

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/user"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/codesign"
	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/filesystem"
	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/lock"
	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/symlink"
	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/volume_uuid"
	"github.com/maheshmirchandani/Backup-Pro/internal/rsync"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Options configure a Preflight() invocation.
type Options struct {
	// DestRoot is the absolute path to the USB mountpoint that will be the
	// backup destination (e.g. "/Volumes/FLASHBKP"). Required.
	DestRoot string

	// SkipCodesign forces dev-mode behavior (logs a warning, does not abort)
	// when codesign returns ErrInvalidSignature. Used in tests; do NOT set
	// in production. Release builds should never see this true.
	SkipCodesign bool
}

// PreflightContext is the populated output of a successful Preflight() call.
// Stored by the runner; passed to every phase. Release the LockHandle when
// done (typically via defer).
type PreflightContext struct {
	LockHandle      *lock.LockHandle
	SymlinkBaseline *symlink.Baseline
	VolumeUUID      *volume_uuid.Captured
	Filesystem      *filesystem.Info
	DestRoot        string
	DotDir          string // <DestRoot>/.flashbackup
	Hostname        string
	Username        string
	VersionFile     state.VersionFile // loaded HMAC key for manifest
	RsyncPath       string            // absolute path to extracted rsync binary
}

// Preflight runs all preflight gates in order. See package doc.go for the
// canonical 9-gate sequence. Order matters: codesign first (verify our own
// binary before trusting anything), then read-only inspect (no state changes),
// then lock (acquire exclusivity), then mutating ops (version, rsync extract).
//
// On any gate failure, Preflight cleans up any state it created (e.g.,
// releases the lock if acquired before a later gate fails) and returns the
// wrapped error.
func Preflight(ctx context.Context, opts Options) (*PreflightContext, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("preflight: %w", err)
	}
	if opts.DestRoot == "" {
		return nil, fmt.Errorf("preflight: DestRoot is empty")
	}

	pc := &PreflightContext{DestRoot: opts.DestRoot}
	var success bool
	defer func() {
		if success {
			return
		}
		// Roll back any partial state we created.
		if pc.LockHandle != nil {
			_ = pc.LockHandle.Release()
			pc.LockHandle = nil
		}
	}()

	// Gate 1: codesign self-verify.
	// Dev builds return ErrDevBuild by design; that is not a failure.
	// Tests may set SkipCodesign to bypass ErrInvalidSignature (which would
	// otherwise abort because test binaries are unsigned but compiled as
	// release; in practice they will hit ErrDevBuild on this codepath).
	if err := codesign.VerifySelf(ctx); err != nil {
		switch {
		case errors.Is(err, codesign.ErrDevBuild):
			// Expected in dev/test; not a failure.
		case opts.SkipCodesign:
			// Test escape hatch; treat as warning (no logger plumbed yet).
		default:
			return nil, fmt.Errorf("preflight gate 1 (codesign): %w", err)
		}
	}

	// Gate 2: resolve DestRoot to absolute + ensure it exists as a directory.
	abs, err := filepath.Abs(opts.DestRoot)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 2 (resolve DestRoot): %w", err)
	}
	pc.DestRoot = abs
	info, statErr := os.Stat(abs)
	if statErr != nil {
		return nil, fmt.Errorf("preflight gate 2 (DestRoot stat): %w", statErr)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("preflight gate 2 (DestRoot stat): %q is not a directory", abs)
	}

	// Gate 3: filesystem (APFS/HFS+; refuse noexec).
	fsInfo, err := filesystem.Inspect(ctx, pc.DestRoot)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 3 (filesystem inspect): %w", err)
	}
	if err := filesystem.Validate(fsInfo); err != nil {
		return nil, fmt.Errorf("preflight gate 3 (filesystem validate): %w", err)
	}
	pc.Filesystem = fsInfo

	// Gate 4: symlink baseline (refuse symlinks in path; record dev/ino).
	baseline, err := symlink.WalkAndBaseline(ctx, pc.DestRoot)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 4 (symlink): %w", err)
	}
	pc.SymlinkBaseline = baseline

	// Gate 5: volume UUID capture (invariant #30).
	captured, err := volume_uuid.Capture(ctx, pc.DestRoot)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 5 (volume_uuid): %w", err)
	}
	pc.VolumeUUID = captured

	// Gate 6: ensure DotDir exists with mode 0700.
	pc.DotDir = filepath.Join(pc.DestRoot, ".flashbackup")
	if err := os.MkdirAll(pc.DotDir, 0o700); err != nil {
		return nil, fmt.Errorf("preflight gate 6 (mkdir DotDir): %w", err)
	}

	// Gate 7: lock (with captured volume UUID for cross-checks).
	lockPath := filepath.Join(pc.DotDir, "lock")
	lh, err := lock.Acquire(ctx, lockPath, captured.UUID)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 7 (lock): %w", err)
	}
	pc.LockHandle = lh

	// Gate 8: version file (FAIL-CLOSED per invariant #11).
	// If absent or corrupt, the caller must run `flashbackup init` first.
	versionPath := filepath.Join(pc.DotDir, "version.json")
	vf, err := state.ReadVersionFile(versionPath)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 8 (version.json): %w", err)
	}
	pc.VersionFile = vf

	// Gate 9: rsync extraction (SHA256-verified).
	rsyncPath, err := rsync.EnsureExtracted(ctx, pc.DotDir)
	if err != nil {
		return nil, fmt.Errorf("preflight gate 9 (rsync extract): %w", err)
	}
	pc.RsyncPath = rsyncPath

	// Capture hostname + username for the manifest namespace prefix.
	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("preflight: get hostname: %w", err)
	}
	pc.Hostname = host
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("preflight: get current user: %w", err)
	}
	pc.Username = u.Username

	success = true
	return pc, nil
}

// VerifyVolumeUnchanged re-checks that the destination volume + path
// components have not changed since T0. Called by the runner at every phase
// boundary. Returns nil if all checks pass; returns a wrapped error otherwise.
//
// Composes:
//   - volume_uuid.Verify (catches whole-volume substitution)
//   - symlink.Verify (catches in-place dir/symlink swap)
func (pc *PreflightContext) VerifyVolumeUnchanged(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("verify volume unchanged: %w", err)
	}
	if pc == nil {
		return fmt.Errorf("verify volume unchanged: nil PreflightContext")
	}
	if err := volume_uuid.Verify(ctx, pc.VolumeUUID); err != nil {
		return fmt.Errorf("verify volume unchanged (uuid): %w", err)
	}
	if err := symlink.Verify(ctx, pc.DestRoot, pc.SymlinkBaseline); err != nil {
		return fmt.Errorf("verify volume unchanged (symlink baseline): %w", err)
	}
	return nil
}

// Release releases the lock and any other resources held by the context.
// Safe to call multiple times; second and subsequent calls are no-ops.
// Typical pattern: `defer pctx.Release()` immediately after a successful
// Preflight.
func (pc *PreflightContext) Release() error {
	if pc == nil || pc.LockHandle == nil {
		return nil
	}
	err := pc.LockHandle.Release()
	pc.LockHandle = nil
	return err
}
