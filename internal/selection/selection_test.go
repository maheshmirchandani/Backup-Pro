package selection

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"golang.org/x/text/unicode/norm"
)

// writeFile creates path with the given contents and 0o600 perms (gosec G306
// pin). Parent dirs are created with 0o700.
func writeFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdirall %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("writefile %q: %v", path, err)
	}
}

// writeFileBytes writes raw bytes (used by the NFC duplicate test where the
// path itself is a raw byte sequence, not a Go-source string).
func writeFileBytes(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdirall %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, contents, 0o600); err != nil {
		t.Fatalf("writefile %q: %v", path, err)
	}
}

// candidatePaths returns the RelativePath of every Candidate, sorted (the
// walker already sorts; this lets the test compare directly via reflect or
// the typical equal-slices idiom).
func candidatePaths(cands []Candidate) []string {
	out := make([]string, 0, len(cands))
	for _, c := range cands {
		out = append(out, c.RelativePath)
	}
	return out
}

func TestWalk_HappyPath(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "b.md"), "beta")
	writeFile(t, filepath.Join(dir, "c", "d.pdf"), "delta")

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"a.txt", "b.md", "c/d.pdf"}
	if !equalSlices(got, want) {
		t.Errorf("Candidates = %v, want %v", got, want)
	}

	for _, c := range res.Candidates {
		if c.Size == 0 {
			t.Errorf("%q: Size = 0; want non-zero", c.RelativePath)
		}
		if c.MtimeNS == 0 {
			t.Errorf("%q: MtimeNS = 0; want non-zero", c.RelativePath)
		}
		if c.AbsolutePath == "" {
			t.Errorf("%q: AbsolutePath empty", c.RelativePath)
		}
	}
}

func TestWalk_IncludesFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "b.md"), "beta")
	writeFile(t, filepath.Join(dir, "c.pdf"), "gamma")

	res, err := Walk(context.Background(), Options{
		SourceRoot: dir,
		Includes:   []string{"*.pdf", "*.md"},
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"b.md", "c.pdf"}
	if !equalSlices(got, want) {
		t.Errorf("Candidates = %v, want %v", got, want)
	}
	// a.txt is filtered by Includes, NOT by Excludes, so per the contract
	// it does not appear in Skipped (Skipped tracks Excludes hits only).
	for _, s := range res.Skipped {
		if s == "a.txt" {
			t.Errorf("a.txt unexpectedly in Skipped (Includes-filtered files are not Skipped)")
		}
	}
}

func TestWalk_ExcludesFilter(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "alpha")
	writeFile(t, filepath.Join(dir, "b.tmp"), "junk")
	writeFile(t, filepath.Join(dir, ".DS_Store"), "macos-junk")
	writeFile(t, filepath.Join(dir, "c.md"), "real")

	res, err := Walk(context.Background(), Options{
		SourceRoot: dir,
		Excludes:   []string{"*.tmp", ".DS_Store"},
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"a.txt", "c.md"}
	if !equalSlices(got, want) {
		t.Errorf("Candidates = %v, want %v", got, want)
	}

	wantSkipped := []string{".DS_Store", "b.tmp"}
	if !equalSlices(res.Skipped, wantSkipped) {
		t.Errorf("Skipped = %v, want %v", res.Skipped, wantSkipped)
	}
}

func TestWalk_IncludesAndExcludes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "keep.md"), "1")
	writeFile(t, filepath.Join(dir, "keep.txt"), "2")
	writeFile(t, filepath.Join(dir, "draft-skip.md"), "3")
	writeFile(t, filepath.Join(dir, "ignore.pdf"), "4")

	res, err := Walk(context.Background(), Options{
		SourceRoot: dir,
		Includes:   []string{"*.md", "*.txt"},
		Excludes:   []string{"draft-*"},
	})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"keep.md", "keep.txt"}
	if !equalSlices(got, want) {
		t.Errorf("Candidates = %v, want %v", got, want)
	}
	// draft-skip.md matched Excludes -> Skipped.
	if !contains(res.Skipped, "draft-skip.md") {
		t.Errorf("Skipped missing draft-skip.md: %v", res.Skipped)
	}
}

// TestWalk_NFCDuplicateDetection writes two files whose filenames differ only
// in Unicode normalization form (one NFC, one NFD). On a default APFS volume
// macOS preserves both forms at the inode level (normalization-preserving),
// so both files coexist on disk. The walker must detect the collision: both
// raw paths land in CollidingPaths, neither in Candidates. Per invariant #32.
func TestWalk_NFCDuplicateDetection(t *testing.T) {
	dir := t.TempDir()

	// "café.txt" in NFC: 'c', 'a', 'f', 0xc3 0xa9 ('é' precomposed), '.', ...
	nfcName := "café.txt"
	// "café.txt" in NFD: 'c', 'a', 'f', 'e' + 0xcc 0x81 (combining acute)
	nfdName := "café.txt"

	// Sanity: they normalize to the same NFC string.
	if norm.NFC.String(nfcName) != norm.NFC.String(nfdName) {
		t.Fatalf("test setup bug: NFC(nfc)=%q NFC(nfd)=%q", norm.NFC.String(nfcName), norm.NFC.String(nfdName))
	}

	nfcPath := filepath.Join(dir, nfcName)
	nfdPath := filepath.Join(dir, nfdName)
	writeFileBytes(t, nfcPath, []byte("nfc-content"))

	// APFS on most macOS volumes is normalization-preserving but NOT
	// normalization-insensitive for the user-data role; both files coexist.
	// If the underlying filesystem DOES collapse them (e.g., a case-folded
	// HFS+ volume mounted under /tmp on some macOS setups), the test
	// becomes a no-op rather than a false fail.
	if err := os.WriteFile(nfdPath, []byte("nfd-content"), 0o600); err != nil {
		t.Skipf("filesystem collapsed NFC/NFD twins (likely case-folded volume): %v", err)
	}

	// Verify both physical entries exist before walking.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) < 2 {
		t.Skipf("filesystem collapsed NFC/NFD twins to a single entry; got %d entries", len(entries))
	}

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if len(res.Candidates) != 0 {
		t.Errorf("expected 0 Candidates (both forms collided), got %d: %v",
			len(res.Candidates), candidatePaths(res.Candidates))
	}

	if len(res.CollidingPaths) != 2 {
		t.Errorf("expected 2 CollidingPaths, got %d: %v", len(res.CollidingPaths), res.CollidingPaths)
	}

	// Both raw names should appear in CollidingPaths.
	if !contains(res.CollidingPaths, nfcName) {
		t.Errorf("CollidingPaths missing NFC raw form %q: %v", nfcName, res.CollidingPaths)
	}
	if !contains(res.CollidingPaths, nfdName) {
		t.Errorf("CollidingPaths missing NFD raw form %q: %v", nfdName, res.CollidingPaths)
	}
}

func TestWalk_SymlinksNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "real.txt")
	writeFile(t, target, "real-data")

	link := filepath.Join(dir, "link.txt")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unsupported on this filesystem: %v", err)
	}

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"link.txt", "real.txt"}
	if !equalSlices(got, want) {
		t.Fatalf("Candidates = %v, want %v", got, want)
	}

	for _, c := range res.Candidates {
		if c.RelativePath != "link.txt" {
			continue
		}
		if os.FileMode(c.Mode)&os.ModeSymlink == 0 {
			t.Errorf("link.txt Mode = %v; want ModeSymlink bit set", os.FileMode(c.Mode))
		}
	}
}

func TestWalk_CancelledContext(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.txt"), "x")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := Walk(ctx, Options{SourceRoot: dir})
	if err == nil {
		t.Fatalf("Walk with cancelled ctx returned nil error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error chain does not contain context.Canceled: %v", err)
	}
}

func TestWalk_SourceRootNotExist(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "does-not-exist", "really")
	_, err := Walk(context.Background(), Options{SourceRoot: bogus})
	if err == nil {
		t.Fatalf("Walk on missing root returned nil error")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q does not mention the bogus path", err.Error())
	}
}

func TestWalk_SkipsDirectoryEntries(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "sub", "deeper"), 0o700); err != nil {
		t.Fatalf("mkdirall: %v", err)
	}
	writeFile(t, filepath.Join(dir, "root.txt"), "r")
	writeFile(t, filepath.Join(dir, "sub", "mid.txt"), "m")
	writeFile(t, filepath.Join(dir, "sub", "deeper", "leaf.txt"), "l")

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := []string{"root.txt", "sub/deeper/leaf.txt", "sub/mid.txt"}
	if !equalSlices(got, want) {
		t.Errorf("Candidates = %v, want %v (no bare directory entries)", got, want)
	}

	// Spot-check: no Candidate ends in a known directory name.
	for _, c := range res.Candidates {
		if strings.HasSuffix(c.RelativePath, "/sub") || strings.HasSuffix(c.RelativePath, "/deeper") {
			t.Errorf("Candidate %q is a directory; directories must not appear", c.RelativePath)
		}
	}
}

func TestWalk_SortedDeterministicOrder(t *testing.T) {
	dir := t.TempDir()
	// Create in a non-sorted order; the walker still returns sorted.
	for _, name := range []string{"zeta.txt", "alpha.txt", "mike.txt", "bravo.txt"} {
		writeFile(t, filepath.Join(dir, name), "x")
	}

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	got := candidatePaths(res.Candidates)
	want := make([]string, len(got))
	copy(want, got)
	sort.Strings(want)
	if !equalSlices(got, want) {
		t.Errorf("Candidates not sorted: %v", got)
	}
}

func TestWalk_RelativePathHasForwardSlashes(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a", "b", "c.txt"), "x")

	res, err := Walk(context.Background(), Options{SourceRoot: dir})
	if err != nil {
		t.Fatalf("Walk: %v", err)
	}

	if len(res.Candidates) != 1 {
		t.Fatalf("expected 1 Candidate, got %d", len(res.Candidates))
	}
	got := res.Candidates[0].RelativePath
	want := "a/b/c.txt"
	if got != want {
		t.Errorf("RelativePath = %q, want %q (forward slashes)", got, want)
	}
	// Belt-and-braces: assert no backslashes anywhere.
	if strings.ContainsRune(got, '\\') {
		t.Errorf("RelativePath %q contains a backslash", got)
	}
}

// TestCandidateCollector_CollisionLogic exercises the NFC-collision branch
// of the collector directly. The end-to-end TestWalk_NFCDuplicateDetection
// skips when the macOS APFS Data role volume collapses NFC/NFD twins at
// write time (which it does by default on every test machine + CI runner).
// That skip means the FS-level test does not exercise the collision branch
// in coverage; this unit test does, against the same code path the walker
// uses. Per invariant #32 — keeping the test independent of FS behavior.
func TestCandidateCollector_CollisionLogic(t *testing.T) {
	t.Run("single entry accepted", func(t *testing.T) {
		c := newCandidateCollector()
		c.add("file.txt", "file.txt", Candidate{RelativePath: "file.txt"})
		if got := len(c.candidates()); got != 1 {
			t.Errorf("candidates len = %d, want 1", got)
		}
		if got := len(c.collisions()); got != 0 {
			t.Errorf("collisions len = %d, want 0", got)
		}
	})

	t.Run("two raw forms collide on same norm key", func(t *testing.T) {
		// NFC: 'cafe' + U+00E9 (precomposed) + ".txt"
		nfcRaw := string([]byte{'c', 'a', 'f', 0xc3, 0xa9, '.', 't', 'x', 't'})
		// NFD: 'cafe' + 'e' + U+0301 (combining acute) + ".txt"
		nfdRaw := string([]byte{'c', 'a', 'f', 'e', 0xcc, 0x81, '.', 't', 'x', 't'})
		if nfcRaw == nfdRaw {
			t.Fatalf("test setup bug: nfcRaw == nfdRaw")
		}
		normKey := norm.NFC.String(nfdRaw)
		if normKey != norm.NFC.String(nfcRaw) {
			t.Fatalf("test setup bug: norm forms differ between nfc and nfd raw")
		}

		c := newCandidateCollector()
		c.add(normKey, nfcRaw, Candidate{RelativePath: normKey})
		c.add(normKey, nfdRaw, Candidate{RelativePath: normKey})

		if got := len(c.candidates()); got != 0 {
			t.Errorf("candidates len = %d, want 0 (both forms in collision)", got)
		}
		coll := c.collisions()
		if len(coll) != 2 {
			t.Fatalf("collisions len = %d, want 2; got %v", len(coll), coll)
		}
		gotSet := map[string]bool{coll[0]: true, coll[1]: true}
		if !gotSet[nfcRaw] || !gotSet[nfdRaw] {
			t.Errorf("collisions = %v; want both raw forms present (nfc=% x, nfd=% x)", coll, []byte(nfcRaw), []byte(nfdRaw))
		}
	})

	t.Run("three-way collision reports all forms", func(t *testing.T) {
		c := newCandidateCollector()
		c.add("k", "raw-a", Candidate{})
		c.add("k", "raw-b", Candidate{})
		c.add("k", "raw-c", Candidate{})
		if got := len(c.candidates()); got != 0 {
			t.Errorf("candidates len = %d, want 0", got)
		}
		coll := c.collisions()
		if len(coll) != 3 {
			t.Errorf("collisions len = %d, want 3; got %v", len(coll), coll)
		}
	})

	t.Run("same raw form duplicate is idempotent", func(t *testing.T) {
		// add() called twice with the same (normKey, rawRel) pair (paranoia
		// case; shouldn't happen on a sane FS but the collector must not
		// flip a single entry into the collision set).
		c := newCandidateCollector()
		c.add("file.txt", "file.txt", Candidate{RelativePath: "file.txt"})
		c.add("file.txt", "file.txt", Candidate{RelativePath: "file.txt"})
		if got := len(c.candidates()); got != 1 {
			t.Errorf("candidates len = %d, want 1 (same raw form twice is not a collision)", got)
		}
		if got := len(c.collisions()); got != 0 {
			t.Errorf("collisions len = %d, want 0", got)
		}
	})

	t.Run("distinct norm keys do not interact", func(t *testing.T) {
		c := newCandidateCollector()
		c.add("a.txt", "a.txt", Candidate{RelativePath: "a.txt"})
		c.add("b.txt", "b.txt", Candidate{RelativePath: "b.txt"})
		if got := len(c.candidates()); got != 2 {
			t.Errorf("candidates len = %d, want 2", got)
		}
		if got := len(c.collisions()); got != 0 {
			t.Errorf("collisions len = %d, want 0", got)
		}
	})
}

// equalSlices reports whether a and b have the same length and pairwise-equal
// elements. Used in place of reflect.DeepEqual for clearer test diagnostics.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func contains(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}
