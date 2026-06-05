package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestFixtureTreeSHA256_TinyMatchesManifest is a smoke test for the
// SHA256-of-tree recipe. We compute the hash of the committed
// test/fixtures/tiny tree and compare it to the line recorded in
// test/fixtures/tiny/MANIFEST.txt. A mismatch means either:
//
//   - a fixture file was edited without re-running regen-manifest.sh
//     (test acts as a tripwire so the SHA-of-tree never silently
//     drifts from the documented value), or
//   - the Go SHA256-of-tree implementation drifted from the shell one
//     in test/fixtures/regen-manifest.sh.
//
// Test is hermetic; needs no DMG / no e2e gate.
func TestFixtureTreeSHA256_TinyMatchesManifest(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	tinyDir := filepath.Join(root, "test", "fixtures", "tiny")
	got := FixtureTreeSHA256(t, tinyDir)

	manifest, err := os.ReadFile(filepath.Join(tinyDir, "MANIFEST.txt"))
	if err != nil {
		t.Fatalf("read tiny MANIFEST.txt: %v", err)
	}
	want := extractManifestSHA(t, string(manifest))
	if got != want {
		t.Errorf("tiny SHA256-of-tree mismatch\n got:  %s\n want: %s\n(re-run test/fixtures/regen-manifest.sh after any fixture edit)",
			got, want)
	}
}

// TestFixtureTreeSHA256_RealisticMatchesManifest is the same tripwire
// against the realistic fixture. Kept as a separate test so a single
// fixture drift identifies which tree changed.
func TestFixtureTreeSHA256_RealisticMatchesManifest(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	dir := filepath.Join(root, "test", "fixtures", "realistic")
	got := FixtureTreeSHA256(t, dir)

	manifest, err := os.ReadFile(filepath.Join(dir, "MANIFEST.txt"))
	if err != nil {
		t.Fatalf("read realistic MANIFEST.txt: %v", err)
	}
	want := extractManifestSHA(t, string(manifest))
	if got != want {
		t.Errorf("realistic SHA256-of-tree mismatch\n got:  %s\n want: %s\n(re-run test/fixtures/regen-manifest.sh after any fixture edit)",
			got, want)
	}
}

// TestFixtureTreeSHA256_Framing pins the recipe by hand: build a
// 2-file tempdir with known contents, hash both ways, confirm match.
// Guards against silent reordering / framing changes in the helper.
func TestFixtureTreeSHA256_Framing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a"), []byte("AAA"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b"), []byte("BBB"), 0o600); err != nil {
		t.Fatal(err)
	}

	got := FixtureTreeSHA256(t, dir)

	// Hand-roll the same recipe: sorted paths, each followed by NL, then
	// file bytes, then NL.
	h := sha256.New()
	h.Write([]byte("a\nAAA\n"))
	h.Write([]byte("b\nBBB\n"))
	want := hex.EncodeToString(h.Sum(nil))

	if got != want {
		t.Errorf("framing mismatch\n got:  %s\n want: %s", got, want)
	}
}

// TestSeedSource_TinyHermetic verifies that SeedSource copies the tiny
// fixture verbatim (MANIFEST.txt excluded). Hermetic; no DMG needed.
// On non-darwin the test still runs because copyDir is platform-neutral
// and the tiny fixture has no pathological members.
func TestSeedSource_TinyHermetic(t *testing.T) {
	src := SeedSource(t, "tiny")

	// Must NOT have copied MANIFEST.txt.
	if _, err := os.Stat(filepath.Join(src, "MANIFEST.txt")); err == nil {
		t.Errorf("MANIFEST.txt was copied; it should be skipped")
	}

	for _, name := range []string{"a.txt", "b.md", "c.json"} {
		if _, err := os.Stat(filepath.Join(src, name)); err != nil {
			t.Errorf("expected file %s in seeded source: %v", name, err)
		}
	}
}

// TestSeedSource_RealisticHermetic verifies the multi-subdir copy.
func TestSeedSource_RealisticHermetic(t *testing.T) {
	src := SeedSource(t, "realistic")
	for _, rel := range []string{
		"docs/notes.md",
		"docs/quarterly.md",
		"docs/changelog.txt",
		"code/main.txt",
		"code/util.txt",
		"code/util_test.txt",
		"photos/img-001.jpg",
		"photos/img-002.jpg",
		"photos/img-003.jpg",
	} {
		if _, err := os.Stat(filepath.Join(src, rel)); err != nil {
			t.Errorf("expected %s in seeded source: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(src, "MANIFEST.txt")); err == nil {
		t.Errorf("MANIFEST.txt was copied; it should be skipped")
	}
}

// TestFindRepoRoot smoke-tests the go.mod walker.
func TestFindRepoRoot(t *testing.T) {
	root, err := findRepoRoot()
	if err != nil {
		t.Fatalf("findRepoRoot: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Errorf("repo root %s has no go.mod: %v", root, err)
	}
	// Sanity: the test/e2e dir should sit under the discovered root.
	if _, err := os.Stat(filepath.Join(root, "test", "e2e")); err != nil {
		t.Errorf("repo root %s missing test/e2e: %v", root, err)
	}
}

// TestSeedSource_PathologicalRequiresDarwin only materializes the
// pathological fixture on darwin (it uses bash + chflags-aware files).
// On non-darwin the test is skipped because the script depends on
// macOS-specific behaviour for the sparse-file path.
func TestSeedSource_PathologicalRequiresDarwin(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skipf("pathological fixture is macOS-first; runtime.GOOS=%s", runtime.GOOS)
	}
	src := SeedSource(t, "pathological")
	for _, expected := range []string{"plain.txt", "immutable-target.txt", "sparse.bin"} {
		if _, err := os.Stat(filepath.Join(src, expected)); err != nil {
			t.Errorf("expected %s in pathological source: %v", expected, err)
		}
	}
}

// extractManifestSHA pulls the "SHA256-of-tree: <hex>" line from a
// MANIFEST.txt body. Anchored to a known prefix to avoid false
// positives on any other hex-looking substring.
func extractManifestSHA(t *testing.T, body string) string {
	t.Helper()
	const prefix = "SHA256-of-tree: "
	for _, line := range strings.Split(body, "\n") {
		if strings.HasPrefix(line, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(line, prefix))
		}
	}
	t.Fatalf("no %q line in MANIFEST", prefix)
	return ""
}
