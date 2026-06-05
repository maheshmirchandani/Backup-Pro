package load_test

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/load"
)

// ----------------------------------------------------------------------------
// Fixture helpers
// ----------------------------------------------------------------------------

// fixture wires a per-test temp dir with a valid version.json + a manifest
// store ready for AppendEntry. Returns the temp dir, the manifest path that
// callers will pass to Load (the .gz path that Gzip() rename-promotes), the
// version-file path, and the opened store.
type fixture struct {
	dir         string
	manifestGz  string
	versionPath string
	store       state.ManifestStore
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	dir := t.TempDir()
	versionPath := filepath.Join(dir, "version.json")
	if _, err := state.InitVersionFile(versionPath, "0.1.0-test", false); err != nil {
		t.Fatalf("init version file: %v", err)
	}
	vf, err := state.ReadVersionFile(versionPath)
	if err != nil {
		t.Fatalf("read version file: %v", err)
	}
	hmacKey := decodeHex(t, vf.HMACKey)

	manifestBase := filepath.Join(dir, "manifest.ndjson")
	store, err := state.NewNDJSONManifestStore(manifestBase, hmacKey)
	if err != nil {
		t.Fatalf("new manifest store: %v", err)
	}
	return &fixture{
		dir:         dir,
		manifestGz:  manifestBase + ".gz",
		versionPath: versionPath,
		store:       store,
	}
}

func decodeHex(t *testing.T, s string) []byte {
	t.Helper()
	out := make([]byte, len(s)/2)
	for i := 0; i < len(out); i++ {
		var b byte
		_, err := fmt.Sscanf(s[i*2:i*2+2], "%02x", &b)
		if err != nil {
			t.Fatalf("decode hex byte at %d: %v", i, err)
		}
		out[i] = b
	}
	return out
}

func sampleEntry(path string, size int64, sha string) state.ManifestEntry {
	return state.ManifestEntry{
		V:            1,
		Path:         path,
		Size:         size,
		MtimeNS:      1700000000000000000,
		SHA256Source: sha,
		CopiedAt:     time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC),
		Status:       state.StatusVerified,
	}
}

// gunzipFile returns the raw NDJSON bytes after gunzipping path.
func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path) //nolint:gosec // test fixture path
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	return data
}

// writeGzipFile writes data as a gzip-compressed file at path with 0o600 perms.
func writeGzipFile(t *testing.T, path string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// finalizeStore calls store.Gzip(ctx) and asserts success. After this returns,
// the .tmp.gz has been renamed to .gz on disk.
func finalizeStore(t *testing.T, store state.ManifestStore) {
	t.Helper()
	if err := store.Gzip(context.Background()); err != nil {
		t.Fatalf("finalize store: %v", err)
	}
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

func TestLoad_HappyPath(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	entries := []state.ManifestEntry{
		sampleEntry("a.txt", 10, "aaaa"),
		sampleEntry("b.txt", 20, "bbbb"),
		sampleEntry("c.txt", 30, "cccc"),
	}
	for _, e := range entries {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	finalizeStore(t, fx.store)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(res.Entries), 3; got != want {
		t.Errorf("Entries: got %d want %d", got, want)
	}
	if len(res.IntegrityErrors) != 0 {
		t.Errorf("IntegrityErrors: got %d want 0", len(res.IntegrityErrors))
	}
	if len(res.SchemaErrors) != 0 {
		t.Errorf("SchemaErrors: got %d want 0", len(res.SchemaErrors))
	}
	if res.SchemaVersion != state.CurrentSchemaVersion {
		t.Errorf("SchemaVersion: got %d want %d", res.SchemaVersion, state.CurrentSchemaVersion)
	}
	if res.EntriesScanned != 3 {
		t.Errorf("EntriesScanned: got %d want 3", res.EntriesScanned)
	}
	for i, e := range res.Entries {
		if e.Path != entries[i].Path {
			t.Errorf("Entries[%d].Path: got %q want %q", i, e.Path, entries[i].Path)
		}
		if e.HMAC == "" {
			t.Errorf("Entries[%d].HMAC: empty", i)
		}
	}
}

func TestLoad_TamperedEntry(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	entries := []state.ManifestEntry{
		sampleEntry("a.txt", 10, "aaaaaaaa"),
		sampleEntry("b.txt", 20, "bbbbbbbb"),
		sampleEntry("c.txt", 30, "cccccccc"),
	}
	for _, e := range entries {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	finalizeStore(t, fx.store)

	// Tamper: rewrite the middle entry's sha256_source byte, keeping the
	// persisted HMAC intact so verify catches the mismatch.
	raw := gunzipFile(t, fx.manifestGz)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines after gunzip, got %d", len(lines))
	}
	var middle state.ManifestEntry
	if err := json.Unmarshal(lines[1], &middle); err != nil {
		t.Fatalf("unmarshal middle: %v", err)
	}
	middle.SHA256Source = "ffffffff" // changed; HMAC stays at the original
	rewritten, err := json.Marshal(middle)
	if err != nil {
		t.Fatalf("marshal middle: %v", err)
	}
	lines[1] = rewritten
	repacked := bytes.Join(lines, []byte("\n"))
	repacked = append(repacked, '\n')
	writeGzipFile(t, fx.manifestGz, repacked)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(res.IntegrityErrors); got != 1 {
		t.Fatalf("IntegrityErrors: got %d want 1", got)
	}
	if res.IntegrityErrors[0].LineNumber != 2 {
		t.Errorf("LineNumber: got %d want 2", res.IntegrityErrors[0].LineNumber)
	}
	if res.IntegrityErrors[0].Entry.Path != "b.txt" {
		t.Errorf("Entry.Path: got %q want b.txt", res.IntegrityErrors[0].Entry.Path)
	}
	if !strings.Contains(res.IntegrityErrors[0].Reason, "hmac mismatch") {
		t.Errorf("Reason: got %q want contains 'hmac mismatch'", res.IntegrityErrors[0].Reason)
	}
	if got := len(res.Entries); got != 2 {
		t.Errorf("Entries: got %d want 2", got)
	}
	if res.EntriesScanned != 3 {
		t.Errorf("EntriesScanned: got %d want 3", res.EntriesScanned)
	}
}

func TestLoad_BadJSONLine(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	entries := []state.ManifestEntry{
		sampleEntry("a.txt", 10, "aaaa"),
		sampleEntry("b.txt", 20, "bbbb"),
	}
	for _, e := range entries {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	finalizeStore(t, fx.store)

	// Inject a malformed line in the middle.
	raw := gunzipFile(t, fx.manifestGz)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	injected := [][]byte{lines[0], []byte("{this is not valid json"), lines[1]}
	repacked := bytes.Join(injected, []byte("\n"))
	repacked = append(repacked, '\n')
	writeGzipFile(t, fx.manifestGz, repacked)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(res.SchemaErrors); got != 1 {
		t.Fatalf("SchemaErrors: got %d want 1", got)
	}
	if res.SchemaErrors[0].LineNumber != 2 {
		t.Errorf("LineNumber: got %d want 2", res.SchemaErrors[0].LineNumber)
	}
	if !strings.Contains(res.SchemaErrors[0].Reason, "invalid json") {
		t.Errorf("Reason: got %q want contains 'invalid json'", res.SchemaErrors[0].Reason)
	}
	if got := len(res.Entries); got != 2 {
		t.Errorf("Entries: got %d want 2", got)
	}
	if res.EntriesScanned != 3 {
		t.Errorf("EntriesScanned: got %d want 3", res.EntriesScanned)
	}
}

func TestLoad_WrongSchemaVersion(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// Synthetic: write a single line with V=99 directly into a gzipped file.
	// Bypasses the store (which would always use V=1).
	rogue := state.ManifestEntry{
		V:            99,
		Path:         "rogue.txt",
		Size:         1,
		MtimeNS:      1,
		SHA256Source: "x",
		CopiedAt:     time.Unix(0, 0).UTC(),
		Status:       state.StatusVerified,
		HMAC:         "0",
	}
	raw, err := json.Marshal(rogue)
	if err != nil {
		t.Fatalf("marshal rogue: %v", err)
	}
	raw = append(raw, '\n')
	writeGzipFile(t, fx.manifestGz, raw)

	_, err = load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected pipeline error for unsupported per-entry V")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error: got %q want contains 'schema_version'", err.Error())
	}
}

// TestLoad_WrongSchemaVersionMidStream locks the abort-mid-loop behavior:
// a V=99 line appearing AFTER several legitimate V=1 entries must still
// fail the whole load with a pipeline error (not be quietly collected
// into SchemaErrors). Task 30 review (2026-06-04) flagged that the
// single-entry test alone did not lock this; without this regression
// guard a future implementation could special-case "first-entry-only"
// and silently accept rogue lines after a clean prefix.
func TestLoad_WrongSchemaVersionMidStream(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// Seed 3 legitimate V=1 entries via the store.
	entries := []state.ManifestEntry{
		sampleEntry("a.txt", 10, "aaaa"),
		sampleEntry("b.txt", 20, "bbbb"),
		sampleEntry("c.txt", 30, "cccc"),
	}
	for _, e := range entries {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	finalizeStore(t, fx.store)

	// Inject a V=99 line AFTER the legitimate prefix.
	raw := gunzipFile(t, fx.manifestGz)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 3 {
		t.Fatalf("expected 3 seed lines, got %d", len(lines))
	}
	rogue := state.ManifestEntry{
		V:            99,
		Path:         "rogue.txt",
		Size:         1,
		MtimeNS:      1,
		SHA256Source: "x",
		CopiedAt:     time.Unix(0, 0).UTC(),
		Status:       state.StatusVerified,
		HMAC:         "0",
	}
	rogueBytes, err := json.Marshal(rogue)
	if err != nil {
		t.Fatalf("marshal rogue: %v", err)
	}
	injected := append(append([][]byte{}, lines...), rogueBytes)
	repacked := bytes.Join(injected, []byte("\n"))
	repacked = append(repacked, '\n')
	writeGzipFile(t, fx.manifestGz, repacked)

	_, err = load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected pipeline error for mid-stream V=99 line")
	}
	if !strings.Contains(err.Error(), "schema_version") {
		t.Errorf("error: got %q want contains 'schema_version'", err.Error())
	}
	// The error MUST mention a line number greater than 1 so support
	// tooling can point an operator at the offending line. Anchoring on
	// "line 4" specifically would over-fit; the principle is "not the
	// first line" so any of "line 2", "line 3", "line 4" is acceptable
	// (the implementation reports line 4 today; the test tolerates
	// future cadence changes).
	if !strings.Contains(err.Error(), "line ") {
		t.Errorf("error: got %q want contains 'line <N>'", err.Error())
	}
	for _, badPrefix := range []string{"line 0", "line 1\""} {
		if strings.Contains(err.Error(), badPrefix) {
			t.Errorf("error reports line %q; mid-stream abort must point past the legitimate prefix", badPrefix)
		}
	}
}

// TestLoad_EntriesScannedInvariant locks the LoadResult arithmetic
// invariant documented in doc.go and at load.go's package-comment:
// EntriesScanned == len(Entries) + len(IntegrityErrors) + len(SchemaErrors).
// Task 30 review (2026-06-04) flagged that no single test asserted the
// equality directly. Exercises the mixed-outcome case (verified +
// tampered + bad JSON in one load) so a future code path that
// increments EntriesScanned without appending to one of the three
// slices is caught immediately.
func TestLoad_EntriesScannedInvariant(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// Seed: 4 legitimate entries. We will tamper one and inject a bad
	// JSON line, leaving 3 verified + 1 tampered + 1 bad json = 5
	// scanned.
	entries := []state.ManifestEntry{
		sampleEntry("a.txt", 10, "aaaa"),
		sampleEntry("b.txt", 20, "bbbb"),
		sampleEntry("c.txt", 30, "cccc"),
		sampleEntry("d.txt", 40, "dddd"),
	}
	for _, e := range entries {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	finalizeStore(t, fx.store)

	raw := gunzipFile(t, fx.manifestGz)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 4 {
		t.Fatalf("expected 4 seed lines, got %d", len(lines))
	}
	// Tamper line 2: replace one byte in the JSON body so HMAC fails.
	tampered := bytes.Replace(lines[1], []byte(`"path":"b.txt"`), []byte(`"path":"X.txt"`), 1)
	// Inject a malformed JSON line in the middle.
	repackedLines := [][]byte{lines[0], tampered, lines[2], []byte("{this is not valid json"), lines[3]}
	repacked := bytes.Join(repackedLines, []byte("\n"))
	repacked = append(repacked, '\n')
	writeGzipFile(t, fx.manifestGz, repacked)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(res.Entries), 3; got != want {
		t.Errorf("Entries: got %d want %d", got, want)
	}
	if got, want := len(res.IntegrityErrors), 1; got != want {
		t.Errorf("IntegrityErrors: got %d want %d", got, want)
	}
	if got, want := len(res.SchemaErrors), 1; got != want {
		t.Errorf("SchemaErrors: got %d want %d", got, want)
	}
	sum := len(res.Entries) + len(res.IntegrityErrors) + len(res.SchemaErrors)
	if res.EntriesScanned != sum {
		t.Errorf("invariant violation: EntriesScanned (%d) != sum of slices (%d) "+
			"(Entries=%d, IntegrityErrors=%d, SchemaErrors=%d)",
			res.EntriesScanned, sum, len(res.Entries), len(res.IntegrityErrors), len(res.SchemaErrors))
	}
}

func TestLoad_MissingManifestFile(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// version.json exists; manifest does not.
	_, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    filepath.Join(fx.dir, "nonexistent.ndjson.gz"),
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected error for missing manifest")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
	if !strings.Contains(err.Error(), "open manifest") {
		t.Errorf("expected error to mention 'open manifest', got %q", err.Error())
	}
}

func TestLoad_MissingVersionFile(t *testing.T) {
	dir := t.TempDir()
	// Create a valid (but unused) manifest.gz so the manifest path itself is
	// not the failure mode.
	manifestPath := filepath.Join(dir, "manifest.ndjson.gz")
	writeGzipFile(t, manifestPath, []byte{})

	_, err := load.Load(context.Background(), load.LoadOptions{
		ManifestPath:    manifestPath,
		VersionFilePath: filepath.Join(dir, "version.json"),
	})
	if err == nil {
		t.Fatal("expected fail-closed error for missing version.json")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
	if !strings.Contains(err.Error(), "version file") {
		t.Errorf("expected error to mention 'version file', got %q", err.Error())
	}
}

func TestLoad_CorruptVersionFile(t *testing.T) {
	dir := t.TempDir()
	versionPath := filepath.Join(dir, "version.json")
	if err := os.WriteFile(versionPath, []byte("{not valid json"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	manifestPath := filepath.Join(dir, "manifest.ndjson.gz")
	writeGzipFile(t, manifestPath, []byte{})

	_, err := load.Load(context.Background(), load.LoadOptions{
		ManifestPath:    manifestPath,
		VersionFilePath: versionPath,
	})
	if err == nil {
		t.Fatal("expected fail-closed error for corrupt version.json")
	}
	// ReadVersionFile's parse-error message suggests --reset-keys; surface
	// that to the operator via the wrapped chain.
	if !strings.Contains(err.Error(), "--reset-keys") {
		t.Errorf("expected wrapped fail-closed error mentioning --reset-keys, got %q", err.Error())
	}
}

func TestLoad_EmptyManifest(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// Finalize an empty store: no entries appended, just the gzip trailer.
	finalizeStore(t, fx.store)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(res.Entries) != 0 {
		t.Errorf("Entries: got %d want 0", len(res.Entries))
	}
	if len(res.IntegrityErrors) != 0 {
		t.Errorf("IntegrityErrors: got %d want 0", len(res.IntegrityErrors))
	}
	if len(res.SchemaErrors) != 0 {
		t.Errorf("SchemaErrors: got %d want 0", len(res.SchemaErrors))
	}
	if res.EntriesScanned != 0 {
		t.Errorf("EntriesScanned: got %d want 0", res.EntriesScanned)
	}
}

func TestLoad_CancelledMidStream(t *testing.T) {
	fx := newFixture(t)
	ctx := context.Background()

	// 1024 entries gives 4 ctx-check intervals to land cancellation on.
	const n = 1024
	for i := 0; i < n; i++ {
		if err := fx.store.AppendEntry(ctx, sampleEntry(
			fmt.Sprintf("f%d.txt", i), int64(i), fmt.Sprintf("%08x", i),
		)); err != nil {
			t.Fatalf("append %d: %v", i, err)
		}
	}
	finalizeStore(t, fx.store)

	cctx, cancel := context.WithCancel(ctx)
	cancel() // cancel immediately so the first interval check trips

	_, err := load.Load(cctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestLoad_HMACCanonicalEncoding(t *testing.T) {
	// Round-trip a manifest with edge-case field values (paths with pipe
	// chars, special UTF-8, large size). If the loader's HMAC canonical
	// encoding differs from the writer's, these would surface as
	// IntegrityErrors. The point: confirm Load reuses state.VerifyHMAC
	// (which uses state.writeCanonical) and does not silently re-tokenize.
	fx := newFixture(t)
	ctx := context.Background()

	edgeCases := []state.ManifestEntry{
		sampleEntry("a|1", 0, "x"), // pipe in path (forgery vector)
		sampleEntry("a", 1, "x"),   // pipe-collision twin
		sampleEntry("documents/résumé.pdf", 1234567890, strings.Repeat("a", 64)),
		sampleEntry("", 0, ""), // empty fields
		sampleEntry(strings.Repeat("x", 1000), 1<<40, strings.Repeat("f", 64)), // very long path + big size
	}
	for _, e := range edgeCases {
		if err := fx.store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("append %q: %v", e.Path, err)
		}
	}
	finalizeStore(t, fx.store)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got, want := len(res.Entries), len(edgeCases); got != want {
		t.Errorf("Entries: got %d want %d", got, want)
	}
	if len(res.IntegrityErrors) != 0 {
		t.Errorf("unexpected IntegrityErrors (canonical encoding drift?): %+v", res.IntegrityErrors)
	}
}

func TestLoad_PipeSeparatorForgeryRejected(t *testing.T) {
	// Regression for invariant #33 / TestHMAC_PipeSeparatorForgeryRejected.
	// Under a delimiter-based canonical encoding, entry A (Path="a|1", Size=0)
	// and entry B (Path="a", Size=1) would produce the same HMAC. Forge B by
	// reusing A's HMAC; Load must surface this as IntegrityError, not accept.
	fx := newFixture(t)
	ctx := context.Background()

	a := sampleEntry("a|1", 0, "x")
	if err := fx.store.AppendEntry(ctx, a); err != nil {
		t.Fatalf("append a: %v", err)
	}
	finalizeStore(t, fx.store)

	// Read back A's HMAC from disk.
	raw := gunzipFile(t, fx.manifestGz)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) != 1 {
		t.Fatalf("expected 1 line, got %d", len(lines))
	}
	var written state.ManifestEntry
	if err := json.Unmarshal(lines[0], &written); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	aHMAC := written.HMAC
	if aHMAC == "" {
		t.Fatal("a.HMAC empty after write")
	}

	// Forge entry B with A's HMAC. Same MtimeNS / CopiedAt / Status / sha as
	// a, only Path + Size differ.
	b := sampleEntry("a", 1, "x")
	b.HMAC = aHMAC

	forged, err := json.Marshal(b)
	if err != nil {
		t.Fatalf("marshal b: %v", err)
	}
	forged = append(forged, '\n')
	writeGzipFile(t, fx.manifestGz, forged)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(res.IntegrityErrors); got != 1 {
		t.Fatalf("IntegrityErrors: got %d want 1 (forgery accepted!)", got)
	}
	if len(res.Entries) != 0 {
		t.Errorf("Entries: got %d want 0", len(res.Entries))
	}
}

func TestLoad_MissingHMACField(t *testing.T) {
	// A valid-JSON entry without an HMAC field is structurally malformed
	// (the writer always sets HMAC). Surface as SchemaError, not silently
	// accept or hash-match against the empty string.
	fx := newFixture(t)
	ctx := context.Background()

	e := sampleEntry("a.txt", 1, "x")
	// Deliberately do not set e.HMAC.
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	raw = append(raw, '\n')
	writeGzipFile(t, fx.manifestGz, raw)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(res.SchemaErrors); got != 1 {
		t.Fatalf("SchemaErrors: got %d want 1", got)
	}
	if !strings.Contains(res.SchemaErrors[0].Reason, "missing hmac") {
		t.Errorf("Reason: got %q want contains 'missing hmac'", res.SchemaErrors[0].Reason)
	}
}

func TestLoad_EmptyManifestPath(t *testing.T) {
	_, err := load.Load(context.Background(), load.LoadOptions{
		ManifestPath:    "",
		VersionFilePath: "/tmp/version.json",
	})
	if err == nil {
		t.Fatal("expected error for empty ManifestPath")
	}
	if !strings.Contains(err.Error(), "ManifestPath") {
		t.Errorf("error: got %q want mentions ManifestPath", err.Error())
	}
}

func TestLoad_EmptyVersionFilePath(t *testing.T) {
	_, err := load.Load(context.Background(), load.LoadOptions{
		ManifestPath:    "/tmp/manifest.gz",
		VersionFilePath: "",
	})
	if err == nil {
		t.Fatal("expected error for empty VersionFilePath")
	}
	if !strings.Contains(err.Error(), "VersionFilePath") {
		t.Errorf("error: got %q want mentions VersionFilePath", err.Error())
	}
}

func TestLoad_CancelledAtEntry(t *testing.T) {
	fx := newFixture(t)
	finalizeStore(t, fx.store)

	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := load.Load(cctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected cancellation error at entry")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestLoad_NotGzipFile(t *testing.T) {
	// Pass a plain (non-gzip) file as the manifest. gzip.NewReader must
	// surface the magic-number mismatch as a pipeline error.
	fx := newFixture(t)
	plainPath := filepath.Join(fx.dir, "plain.ndjson")
	if err := os.WriteFile(plainPath, []byte(`{"v":1,"path":"x"}`+"\n"), 0o600); err != nil {
		t.Fatalf("write plain: %v", err)
	}

	_, err := load.Load(context.Background(), load.LoadOptions{
		ManifestPath:    plainPath,
		VersionFilePath: fx.versionPath,
	})
	if err == nil {
		t.Fatal("expected gzip reader error for non-gzip file")
	}
	if !strings.Contains(err.Error(), "gzip") {
		t.Errorf("error: got %q want mentions 'gzip'", err.Error())
	}
}

func TestLoad_EmptyLineInStream(t *testing.T) {
	// Empty (zero-byte) lines surface as SchemaError, not silently skipped.
	// Guards against future writer drift that lands stray '\n' lines.
	fx := newFixture(t)
	ctx := context.Background()

	if err := fx.store.AppendEntry(ctx, sampleEntry("a.txt", 1, "x")); err != nil {
		t.Fatalf("append: %v", err)
	}
	finalizeStore(t, fx.store)

	raw := gunzipFile(t, fx.manifestGz)
	// Inject a blank line before the real entry.
	repacked := append([]byte("\n"), raw...)
	writeGzipFile(t, fx.manifestGz, repacked)

	res, err := load.Load(ctx, load.LoadOptions{
		ManifestPath:    fx.manifestGz,
		VersionFilePath: fx.versionPath,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got := len(res.SchemaErrors); got != 1 {
		t.Fatalf("SchemaErrors: got %d want 1", got)
	}
	if res.SchemaErrors[0].Reason != "empty line" {
		t.Errorf("Reason: got %q want 'empty line'", res.SchemaErrors[0].Reason)
	}
	if len(res.Entries) != 1 {
		t.Errorf("Entries: got %d want 1", len(res.Entries))
	}
}
