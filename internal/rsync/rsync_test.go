package rsync

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// clearImmutableForTest attempts to clear the macOS uchg flag so that the
// test can rewrite or remove a file the production code may have made
// immutable. On non-darwin or when the flag was never set, this is a no-op.
// The test itself does not assert anything about chflags; that is covered
// implicitly by the happy-path test on darwin.
func clearImmutableForTest(t *testing.T, path string) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return
	}
	// Use the same syscall path as production (chflags(2)) with flags=0
	// to clear all flags. Errors are ignored: the test will fail later
	// at os.Remove if the flag is actually still set.
	_ = clearAllFlagsForTest(path)
}

// registerImmutableCleanup ensures the uchg flag is cleared on the extracted
// rsync binary at test teardown. Without this, t.TempDir()'s RemoveAll fails
// on darwin with EPERM because the production code set chflags uchg. Safe
// to call before the file actually exists; the cleanup re-stats at run
// time and skips missing paths.
func registerImmutableCleanup(t *testing.T, extractRoot string) {
	t.Helper()
	t.Cleanup(func() {
		// Walk to find any rsync file under <extractRoot>/bin/<sha>/rsync
		// and clear flags. We don't fail the test if cleanup itself errors,
		// since TempDir's own cleanup will surface the real problem.
		binDir := filepath.Join(extractRoot, "bin")
		entries, err := os.ReadDir(binDir)
		if err != nil {
			return
		}
		for _, e := range entries {
			candidate := filepath.Join(binDir, e.Name(), "rsync")
			if _, err := os.Stat(candidate); err == nil {
				_ = clearAllFlagsForTest(candidate)
			}
		}
	})
}

func TestEmbeddedSHA256_Stable(t *testing.T) {
	h1 := EmbeddedSHA256()
	h2 := EmbeddedSHA256()
	if h1 != h2 {
		t.Fatalf("EmbeddedSHA256 not stable: %s vs %s", h1, h2)
	}
	if len(h1) != 64 {
		t.Errorf("EmbeddedSHA256 length = %d, want 64 (hex sha256)", len(h1))
	}
	// Sanity: only [0-9a-f].
	for _, r := range h1 {
		isHex := (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f')
		if !isHex {
			t.Errorf("EmbeddedSHA256 contains non-hex rune %q", r)
			break
		}
	}
}

func TestEnsureExtracted_HappyPath(t *testing.T) {
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	path, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	// Path must live under <dir>/bin/<sha256>/rsync.
	wantPrefix := filepath.Join(dir, "bin", EmbeddedSHA256())
	if !strings.HasPrefix(path, wantPrefix) {
		t.Errorf("extracted path = %s, want prefix %s", path, wantPrefix)
	}
	if filepath.Base(path) != "rsync" {
		t.Errorf("extracted basename = %s, want rsync", filepath.Base(path))
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat extracted: %v", err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Errorf("extracted mode = %o, want 0500", info.Mode().Perm())
	}
	if info.Size() == 0 {
		t.Error("extracted file is empty")
	}
	if int(info.Size()) != len(embeddedRsync) {
		t.Errorf("extracted size = %d, want %d", info.Size(), len(embeddedRsync))
	}

	// No leftover .tmp sibling.
	if _, err := os.Stat(path + ".tmp"); err == nil {
		t.Error("unexpected .tmp leftover next to extracted binary")
	}
}

func TestEnsureExtracted_Idempotent(t *testing.T) {
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	path1, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	path2, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if path1 != path2 {
		t.Errorf("paths differ across idempotent calls: %s vs %s", path1, path2)
	}
}

func TestEnsureExtracted_ReExtractsOnTamper(t *testing.T) {
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	path, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// The production code may have set chflags uchg. Clear it so the test
	// can simulate a tampered or missing binary. On non-darwin this is a
	// no-op; on darwin we ignore the error and rely on os.Remove to surface
	// a real problem.
	clearImmutableForTest(t, path)
	if err := os.Remove(path); err != nil {
		t.Fatalf("remove for tamper: %v", err)
	}
	path2, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("second after tamper: %v", err)
	}
	if path2 != path {
		t.Errorf("path changed unexpectedly: %s -> %s", path, path2)
	}
	if _, err := os.Stat(path2); err != nil {
		t.Errorf("expected file to exist after re-extract: %v", err)
	}
}

func TestEnsureExtracted_ReExtractsOnSHA256Mismatch(t *testing.T) {
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	path, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	// Overwrite with garbage so the on-disk SHA256 no longer matches the
	// embedded one. Clear immutable + restore writable mode 0600 first so
	// the test can rewrite.
	clearImmutableForTest(t, path)
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatalf("chmod tamper: %v", err)
	}
	if err := os.WriteFile(path, []byte("tampered"), 0o600); err != nil {
		t.Fatalf("write tamper: %v", err)
	}

	// EnsureExtracted should detect the mismatch and re-extract.
	path2, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("re-extract: %v", err)
	}
	if path2 != path {
		t.Errorf("path changed: %s -> %s", path, path2)
	}
	info, err := os.Stat(path2)
	if err != nil {
		t.Fatalf("stat post-reextract: %v", err)
	}
	if info.Mode().Perm() != 0o500 {
		t.Errorf("mode after re-extract = %o, want 0500", info.Mode().Perm())
	}
	if int(info.Size()) != len(embeddedRsync) {
		t.Errorf("size after re-extract = %d, want %d", info.Size(), len(embeddedRsync))
	}
}

func TestEnsureExtracted_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := EnsureExtracted(ctx, dir)
	if err == nil {
		t.Fatal("expected error on cancelled ctx, got nil")
	}
}

func TestEnsureExtracted_PathStructure(t *testing.T) {
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	path, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	rel, err := filepath.Rel(dir, path)
	if err != nil {
		t.Fatalf("rel: %v", err)
	}
	// Expected: bin/<64-hex>/rsync
	parts := strings.Split(rel, string(filepath.Separator))
	if len(parts) != 3 {
		t.Fatalf("relative path parts = %v, want 3 (bin/<sha>/rsync)", parts)
	}
	if parts[0] != "bin" {
		t.Errorf("parts[0] = %q, want \"bin\"", parts[0])
	}
	if len(parts[1]) != 64 {
		t.Errorf("parts[1] len = %d, want 64 (sha256 hex)", len(parts[1]))
	}
	if parts[2] != "rsync" {
		t.Errorf("parts[2] = %q, want \"rsync\"", parts[2])
	}
}
