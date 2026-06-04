package rsync

import (
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/maheshmirchandani/Backup-Pro/internal/hash"
)

// embeddedRsync is the raw payload built into the flashbackup binary.
// In Plan 1 / Task 12 this is a small shell-script placeholder
// (`bin/rsync.placeholder`). Task 12a's scripts/build-rsync.sh will replace
// the embedded file with a universal2 GNU rsync 3.4.1 binary; no Go-side
// change is required at that swap since EmbeddedSHA256 recomputes from the
// new bytes.
//
//go:embed bin/rsync.placeholder
var embeddedRsync []byte

var (
	embeddedHashOnce sync.Once
	embeddedHash     string
)

// EmbeddedSHA256 returns the hex-encoded SHA256 of the embedded rsync binary.
// Computed lazily on first call and memoized. Used to:
//  1. Derive the per-version extraction subdirectory under
//     `<dotFlashbackupDir>/bin/<sha256>/rsync`, so older flashbackup versions
//     do not collide with newer ones.
//  2. Verify the on-disk file after extraction (the file is re-hashed from
//     the filesystem, not trusted by virtue of being just-written).
func EmbeddedSHA256() string {
	embeddedHashOnce.Do(func() {
		sum := sha256.Sum256(embeddedRsync)
		embeddedHash = hex.EncodeToString(sum[:])
	})
	return embeddedHash
}

// EnsureExtracted ensures the embedded rsync binary is present at a stable
// path under dotFlashbackupDir, with mode 0500 and (best-effort) the macOS
// uchg immutable flag. Returns the absolute path of the extracted binary.
//
// Idempotent: if the binary already exists at the expected path AND its
// SHA256 matches EmbeddedSHA256, the function returns immediately. If the
// file is missing, partially written, or has a different SHA256 (tampered
// or leftover from a different flashbackup version), it is re-extracted
// via tmp+rename.
//
// Cancellation: ctx is checked at entry and again during the on-disk
// SHA256 verifications (which iterate over file bytes via
// internal/hash.StreamSHA256).
func EnsureExtracted(ctx context.Context, dotFlashbackupDir string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("ensure rsync extracted: %w", err)
	}
	sum := EmbeddedSHA256()
	extractDir := filepath.Join(dotFlashbackupDir, "bin", sum)
	extractPath := filepath.Join(extractDir, "rsync")

	// Fast path: already extracted with correct content?
	if existing, err := os.Open(extractPath); err == nil {
		gotHash, _, hashErr := hash.StreamSHA256(ctx, existing)
		_ = existing.Close()
		if hashErr == nil && gotHash == sum {
			return extractPath, nil
		}
		// SHA256 mismatch (or hash error such as ctx cancel): fall through
		// to re-extract. We only return the ctx-cancel error explicitly
		// to preserve cancel semantics.
		if hashErr != nil && ctx.Err() != nil {
			return "", fmt.Errorf("ensure rsync extracted: %w", ctx.Err())
		}
	}

	if err := os.MkdirAll(extractDir, 0o700); err != nil {
		return "", fmt.Errorf("create rsync bin dir: %w", err)
	}

	tmpPath := extractPath + ".tmp"
	// Best-effort cleanup of any stale tmp from a prior crashed extract.
	_ = os.Remove(tmpPath)

	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o500)
	if err != nil {
		return "", fmt.Errorf("create rsync tmp: %w", err)
	}
	if _, err := tmpFile.Write(embeddedRsync); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write rsync tmp: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("fsync rsync tmp: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("close rsync tmp: %w", err)
	}

	// Re-verify the SHA256 of what actually landed on disk before renaming
	// into place. Catches partial writes, FS corruption, and (theoretically)
	// a race in which something rewrote the file between Write and Close.
	written, err := os.Open(tmpPath)
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("reopen rsync tmp: %w", err)
	}
	diskHash, _, err := hash.StreamSHA256(ctx, written)
	_ = written.Close()
	if err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("verify rsync tmp: %w", err)
	}
	if diskHash != sum {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rsync SHA256 mismatch after write: got %s want %s", diskHash, sum)
	}

	if err := os.Rename(tmpPath, extractPath); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("rename rsync into place: %w", err)
	}

	// Best-effort immutable flag. On darwin this is `chflags uchg` via
	// syscall.Chflags(path, UF_IMMUTABLE). On non-darwin (test/dev hosts)
	// this is a no-op. Failure is non-fatal because (a) the SHA256-keyed
	// path and 0500 mode already make casual tampering hard, and (b) some
	// filesystems (e.g. tmpfs in CI) do not support flags.
	_ = applyImmutableFlag(extractPath)

	return extractPath, nil
}
