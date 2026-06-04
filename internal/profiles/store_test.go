package profiles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// newTestStore returns a Store rooted in a tempdir, plus the file path.
func newTestStore(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "profiles.json")
	s, err := NewStore(path)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	return s, path
}

// mustUpsert fails the test if Upsert errors.
func mustUpsert(t *testing.T, s *Store, p Profile) {
	t.Helper()
	if err := s.Upsert(p); err != nil {
		t.Fatalf("Upsert(%q): %v", p.Name, err)
	}
}

func TestStore_NewAndLoad(t *testing.T) {
	s, _ := newTestStore(t)
	p := Profile{
		Name:     "docs",
		Source:   "/home/user/docs",
		Includes: []string{"*.md", "notes/*.txt"},
		Excludes: []string{"draft-*"},
	}
	mustUpsert(t, s, p)

	got, err := s.Get("docs")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Name != "docs" || got.Source != "/home/user/docs" {
		t.Errorf("Get round-trip mismatch: %+v", got)
	}
	if len(got.Includes) != 2 || got.Includes[0] != "*.md" {
		t.Errorf("Includes mismatch: %+v", got.Includes)
	}
	if got.V != SchemaVersion {
		t.Errorf("V = %d, want %d", got.V, SchemaVersion)
	}

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("List len = %d, want 1", len(list))
	}
}

func TestStore_UpsertReplacesExisting(t *testing.T) {
	s, _ := newTestStore(t)
	mustUpsert(t, s, Profile{Name: "p", Source: "/a"})
	mustUpsert(t, s, Profile{Name: "p", Source: "/b"})

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected single entry after replacement, got %d", len(list))
	}
	if list[0].Source != "/b" {
		t.Errorf("Source = %q, want /b (replacement)", list[0].Source)
	}
}

func TestStore_UpsertSortsByName(t *testing.T) {
	s, _ := newTestStore(t)
	mustUpsert(t, s, Profile{Name: "charlie", Source: "/c"})
	mustUpsert(t, s, Profile{Name: "alpha", Source: "/a"})
	mustUpsert(t, s, Profile{Name: "bravo", Source: "/b"})

	list, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	want := []string{"alpha", "bravo", "charlie"}
	if len(list) != len(want) {
		t.Fatalf("len = %d, want %d", len(list), len(want))
	}
	for i, name := range want {
		if list[i].Name != name {
			t.Errorf("list[%d].Name = %q, want %q", i, list[i].Name, name)
		}
	}
}

func TestStore_GetOnEmpty(t *testing.T) {
	s, _ := newTestStore(t)
	_, err := s.Get("nope")
	if err == nil {
		t.Fatal("expected error on Get for missing profile, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' error, got %v", err)
	}
}

func TestStore_DeleteHappy(t *testing.T) {
	s, _ := newTestStore(t)
	mustUpsert(t, s, Profile{Name: "todelete", Source: "/x"})
	if err := s.Delete("todelete"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := s.Get("todelete"); err == nil {
		t.Fatal("expected Get to error after Delete, got nil")
	}
}

func TestStore_DeleteNonexistent(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.Delete("ghost")
	if err == nil {
		t.Fatal("expected error deleting unknown profile, got nil")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("expected 'not found' in error, got %v", err)
	}
}

// Table-driven pattern rejection covers the 8 attack/syntax vectors from
// the multi-hat security review.
func TestStore_RejectsBadPatterns(t *testing.T) {
	cases := []struct {
		name        string
		pattern     string
		wantSubstr  string
		fieldExclud bool // place pattern in Excludes vs Includes
	}{
		{"Bracket", "foo[", "disallowed characters", false},
		{"Traversal", "../../*", "must not contain ..", false},
		{"LeadingSlash", "/etc/foo", "must not start with /", false},
		{"DoubleStar", "**/*.pdf", "must not contain **", false},
		{"NUL", "foo\x00bar", "NUL byte", false},
		{"Long", strings.Repeat("a", 300), "exceeds 256 chars", false},
		{"DisallowedChars", "foo!bar", "disallowed characters", false},
		{"Empty", "", "empty pattern", false},
	}
	for _, tc := range cases {
		tc := tc
		t.Run("TestStore_Rejects"+tc.name, func(t *testing.T) {
			s, _ := newTestStore(t)
			p := Profile{Name: "x", Source: "/x"}
			if tc.fieldExclud {
				p.Excludes = []string{tc.pattern}
			} else {
				p.Includes = []string{tc.pattern}
			}
			err := s.Upsert(p)
			if err == nil {
				t.Fatalf("expected rejection of pattern %q, got nil", tc.pattern)
			}
			if !strings.Contains(err.Error(), tc.wantSubstr) {
				t.Errorf("error %q does not contain %q", err.Error(), tc.wantSubstr)
			}
		})
	}
}

func TestStore_RejectsEmptyName(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.Upsert(Profile{Name: "", Source: "/x"})
	if err == nil {
		t.Fatal("expected error for empty name, got nil")
	}
	if !strings.Contains(err.Error(), "name is empty") {
		t.Errorf("error = %v, want 'name is empty'", err)
	}
}

func TestStore_RejectsEmptySource(t *testing.T) {
	s, _ := newTestStore(t)
	err := s.Upsert(Profile{Name: "p", Source: ""})
	if err == nil {
		t.Fatal("expected error for empty source, got nil")
	}
	if !strings.Contains(err.Error(), "source is empty") {
		t.Errorf("error = %v, want 'source is empty'", err)
	}
}

func TestStore_LoadRespects1MBCap(t *testing.T) {
	s, path := newTestStore(t)

	// Build a syntactically valid JSON document over 1 MB by stuffing a huge
	// pattern in a comment-style filler. Easiest: write a top-level object
	// with a "junk" string > 1 MB.
	bigField := strings.Repeat("x", 2*1024*1024) // 2 MB
	raw := []byte(`{"v":1,"junk":"` + bigField + `","profiles":[]}`)
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := s.List()
	if err == nil {
		t.Fatal("expected DoS-cap error, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Errorf("expected 'exceeds' in error, got %v", err)
	}
	if !strings.Contains(err.Error(), "1048576") {
		t.Errorf("expected '1048576' (1 MB in bytes) in error, got %v", err)
	}
}

func TestStore_StampsSchemaVersion(t *testing.T) {
	s, _ := newTestStore(t)
	// User-supplied V: 0 should be normalized to SchemaVersion on store.
	mustUpsert(t, s, Profile{V: 0, Name: "p", Source: "/x"})

	got, err := s.Get("p")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.V != SchemaVersion {
		t.Errorf("V = %d, want %d", got.V, SchemaVersion)
	}
}

func TestStore_ParentDirCreatedWith0700(t *testing.T) {
	root := t.TempDir()
	nested := filepath.Join(root, "sub", "sub2")
	path := filepath.Join(nested, "profiles.json")

	if _, err := NewStore(path); err != nil {
		t.Fatalf("NewStore: %v", err)
	}

	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("Stat nested dir: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0700 {
		t.Errorf("parent dir mode = %o, want 0700", mode)
	}
}

func TestStore_FileModeIs0644(t *testing.T) {
	s, path := newTestStore(t)
	mustUpsert(t, s, Profile{Name: "p", Source: "/x"})

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	mode := info.Mode().Perm()
	if mode != 0644 {
		t.Errorf("profiles.json mode = %o, want 0644", mode)
	}
}

// Smoke test: persisted JSON parses back into ProfilesDoc cleanly.
// Belt-and-braces against accidental schema drift.
func TestStore_OnDiskShapeIsProfilesDoc(t *testing.T) {
	s, path := newTestStore(t)
	mustUpsert(t, s, Profile{Name: "p", Source: "/x", Includes: []string{"*.txt"}})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	var doc ProfilesDoc
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if doc.V != SchemaVersion {
		t.Errorf("doc.V = %d, want %d", doc.V, SchemaVersion)
	}
	if len(doc.Profiles) != 1 || doc.Profiles[0].Name != "p" {
		t.Errorf("doc.Profiles unexpected: %+v", doc.Profiles)
	}
}
