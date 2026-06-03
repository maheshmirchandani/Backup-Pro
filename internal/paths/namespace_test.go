package paths

import (
	"path/filepath"
	"testing"
)

func TestNamespaced_HappyPath(t *testing.T) {
	got := Namespaced("/Volumes/USB", "macbook", "alice", "Documents/foo.pdf")
	want := filepath.Join("/Volumes/USB", "macbook-alice", "Documents/foo.pdf")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestSourceFromNamespaced_RoundTrip(t *testing.T) {
	dest := Namespaced("/Volumes/USB", "macbook", "alice", "Documents/foo.pdf")
	got, err := SourceFromNamespaced(dest, "/Volumes/USB", "macbook", "alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := "Documents/foo.pdf"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestPrefix_StripsSpecialChars(t *testing.T) {
	// Hostnames can contain dots (e.g. "macbook.local"); usernames are usually safe.
	got := Prefix("macbook.local", "alice")
	want := "macbook-local-alice"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
