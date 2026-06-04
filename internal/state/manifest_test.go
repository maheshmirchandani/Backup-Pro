package state

import (
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func TestManifestEntry_JSONShape(t *testing.T) {
	e := ManifestEntry{
		V:            1,
		Path:         "Documents/foo.pdf",
		Size:         12345,
		MtimeNS:      1718000000000000000,
		SHA256Source: "abc123",
		CopiedAt:     time.Date(2026, 6, 3, 14, 30, 15, 0, time.UTC),
		Status:       StatusVerified,
		HMAC:         "deadbeef",
	}
	got, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"path":"Documents/foo.pdf","size":12345,"mtime_ns":1718000000000000000,"sha256_source":"abc123","copied_at":"2026-06-03T14:30:15Z","status":"verified","hmac":"deadbeef"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestNDJSONManifestStore_AppendThenGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.ndjson")
	store, err := NewNDJSONManifestStore(path, []byte("test-key"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ctx := context.Background()
	e := ManifestEntry{
		V: 1, Path: "foo.txt", Size: 5, MtimeNS: 100,
		SHA256Source: "deadbeef", CopiedAt: time.Unix(0, 0).UTC(),
		Status: StatusVerified,
	}
	if err := store.AppendEntry(ctx, e); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Gzip(ctx); err != nil {
		t.Fatalf("gzip: %v", err)
	}

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("uncompressed manifest still present: err=%v", err)
	}
	if _, err := os.Stat(path + ".tmp.gz"); !os.IsNotExist(err) {
		t.Errorf(".tmp.gz still present after Gzip: err=%v", err)
	}
	gzPath := path + ".gz"
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open gz: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	if !strings.Contains(string(data), `"path":"foo.txt"`) {
		t.Errorf("manifest missing entry; got %s", string(data))
	}
	if !strings.Contains(string(data), `"hmac":"`) {
		t.Errorf("manifest missing hmac; got %s", string(data))
	}
}

func TestNDJSONManifestStore_GzipIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.ndjson")
	store, _ := NewNDJSONManifestStore(path, []byte("k"))
	ctx := context.Background()
	if err := store.Gzip(ctx); err != nil {
		t.Fatalf("first gzip: %v", err)
	}
	if err := store.Gzip(ctx); err != nil {
		t.Fatalf("second gzip should be no-op: %v", err)
	}
}

func TestNDJSONManifestStore_AppendAfterGzip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.ndjson")
	store, _ := NewNDJSONManifestStore(path, []byte("k"))
	ctx := context.Background()
	_ = store.Gzip(ctx)
	err := store.AppendEntry(ctx, ManifestEntry{V: 1, Path: "x"})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected closed error, got %v", err)
	}
}

func TestNDJSONManifestStore_AppendCancelledContext(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "manifest.ndjson")
	store, _ := NewNDJSONManifestStore(path, []byte("k"))
	t.Cleanup(func() { _ = store.Gzip(context.Background()) })
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := store.AppendEntry(ctx, ManifestEntry{V: 1, Path: "x"})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

// newTestStore returns a ndjsonManifestStore initialized enough to call
// computeHMACLocked. Doesn't open any file; used only for HMAC unit tests.
func newTestStore(key []byte) *ndjsonManifestStore {
	return &ndjsonManifestStore{
		hmacKey: key,
		mac:     hmac.New(sha256.New, key),
	}
}

func TestVerifyHMAC_TamperedRejected(t *testing.T) {
	key := []byte("test-key")
	e := ManifestEntry{
		V: 1, Path: "foo.txt", Size: 5, MtimeNS: 100,
		SHA256Source: "abc", CopiedAt: time.Unix(0, 0).UTC(),
		Status: StatusVerified,
	}
	store := newTestStore(key)
	e.HMAC = store.computeHMACLocked(e)

	if !VerifyHMAC(e, key) {
		t.Fatal("verify failed on clean entry")
	}

	tampered := e
	tampered.SHA256Source = "deadbeef"
	if VerifyHMAC(tampered, key) {
		t.Error("verify passed on tampered entry (sha256_source field)")
	}

	tampered = e
	tampered.Size = 999
	if VerifyHMAC(tampered, key) {
		t.Error("verify passed on tampered entry (size field)")
	}

	if VerifyHMAC(e, []byte("other-key")) {
		t.Error("verify passed with wrong key")
	}
}

// TestHMAC_PipeSeparatorForgeryRejected is the critical adversarial test from
// the Plan 1 multi-hat round. Under the original fmt.Sprintf("%d|%s|%d|...")
// canonical encoding, entry A (Path="a|1", Size=0) and entry B (Path="a",
// Size=1) would produce the same canonical string and therefore the same HMAC.
// Length-prefixed encoding makes them distinct. This test locks the fix.
func TestHMAC_PipeSeparatorForgeryRejected(t *testing.T) {
	key := []byte("test-key")
	store := newTestStore(key)

	a := ManifestEntry{
		V: 1, Path: "a|1", Size: 0, MtimeNS: 0,
		SHA256Source: "x", CopiedAt: time.Unix(0, 0).UTC(),
		Status: StatusVerified,
	}
	b := ManifestEntry{
		V: 1, Path: "a", Size: 1, MtimeNS: 0,
		SHA256Source: "x", CopiedAt: time.Unix(0, 0).UTC(),
		Status: StatusVerified,
	}

	macA := store.computeHMACLocked(a)
	macB := store.computeHMACLocked(b)
	if macA == macB {
		t.Fatalf("HMAC collision on pipe-separator forgery: a=%s b=%s", macA, macB)
	}
}

func TestVerifyHMAC_Property(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		key := []byte("rapid-test-key")
		store := newTestStore(key)

		path := rapid.StringN(0, 200, 256).Draw(t, "path")
		size := rapid.Int64Range(0, 1<<40).Draw(t, "size")
		mtime := rapid.Int64Range(0, 1<<60).Draw(t, "mtime_ns")
		sha := rapid.StringN(0, 64, 64).Draw(t, "sha256")

		e := ManifestEntry{
			V: 1, Path: path, Size: size, MtimeNS: mtime,
			SHA256Source: sha, CopiedAt: time.Unix(0, 0).UTC(),
			Status: StatusVerified,
		}
		e.HMAC = store.computeHMACLocked(e)
		if !VerifyHMAC(e, key) {
			t.Fatalf("clean entry failed verify: %+v", e)
		}
	})
}
