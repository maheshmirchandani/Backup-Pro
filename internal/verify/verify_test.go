package verify

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/load"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/rehash"
)

// Test layout:
//
//   - Unit tests (no mount required): input validation, RunID resolution
//     edge cases, summary write round-trip, helper-level coverage. These
//     run on every `go test ./...`.
//
//   - End-to-end tests (FLASHBACKUP_E2E=1 + APFS DMG via hdiutil): exercise
//     the full Verify state machine including preflight. The tests in this
//     file build a fixture USB by manually planting a manifest + version.json
//     + namespaced dest files on the mounted DMG. That avoids the heavyweight
//     runner.Run end-to-end harness (which needs system rsync) and keeps the
//     verify tests focused on the verify pipeline.

// ----------------------------------------------------------------------------
// Mount + DMG helpers
// ----------------------------------------------------------------------------
//
// Shared mount + skip helpers (RequireMacOS / RequireDiskutil / RequireE2E /
// RequireHdiutil / MountTempVolume) live in internal/testutil. The verify-
// specific test scaffolding (plantRun, plantedRun, recordingRenderer, etc.)
// stays local.

// setupDest mounts a fresh APFS DMG and seeds it with a valid version.json.
// The hostname/username helpers are also exposed so tests can write
// namespaced dest files to the same prefix verify will look at.
func setupDest(t *testing.T) (destRoot, hostname, username string) {
	t.Helper()
	dest := testutil.MountTempVolume(t, "APFS")
	dotDir := filepath.Join(dest, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatalf("mkdir dot dir: %v", err)
	}
	versionPath := filepath.Join(dotDir, "version.json")
	if _, err := state.InitVersionFile(versionPath, "test-version", false); err != nil {
		t.Fatalf("InitVersionFile: %v", err)
	}
	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	u := os.Getenv("USER")
	if u == "" {
		t.Fatal("USER env var empty; cannot derive namespace prefix")
	}
	return dest, host, u
}

// ----------------------------------------------------------------------------
// Fixture-USB helpers
// ----------------------------------------------------------------------------

// plantedRun is the inputs used to seed a per-run fixture on the USB:
// the run-dir, the manifest with HMAC-signed entries, and the namespaced
// dest files those entries point at. Returned by plantRun so tests can
// re-derive paths for tamper / mutate / delete operations.
type plantedRun struct {
	runID        string
	runDir       string
	manifestPath string
	files        map[string][]byte // relPath -> payload (canonical bytes)
}

// plantRun seeds one run on the USB: writes manifest.ndjson.gz + dest
// files. The runID format matches the canonical runIDPattern so verify's
// resolver picks it up. host/user must match the values setupDest returned
// so paths.Namespaced derives the right dest path.
func plantRun(t *testing.T, destRoot, runID, host, user string, files map[string][]byte) *plantedRun {
	t.Helper()

	// 1. Make the per-run dir under .flashbackup/runs/<runID>/.
	dotDir := filepath.Join(destRoot, ".flashbackup")
	runDir := filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}

	// 2. Read the HMAC key from version.json so AppendEntry signs entries
	// with the same key Load.Load will verify against.
	versionPath := filepath.Join(dotDir, "version.json")
	vf, err := state.ReadVersionFile(versionPath)
	if err != nil {
		t.Fatalf("ReadVersionFile: %v", err)
	}
	hmacKey, err := hex.DecodeString(vf.HMACKey)
	if err != nil {
		t.Fatalf("decode hmac key: %v", err)
	}

	// 3. Open the manifest store at the canonical path and append entries.
	manifestBase := filepath.Join(runDir, "manifest.ndjson")
	store, err := state.NewNDJSONManifestStore(manifestBase, hmacKey)
	if err != nil {
		t.Fatalf("NewNDJSONManifestStore: %v", err)
	}
	ctx := context.Background()
	for rel, payload := range files {
		e := state.ManifestEntry{
			V:            1,
			Path:         rel,
			Size:         int64(len(payload)),
			MtimeNS:      1700000000000000000,
			SHA256Source: sha256Hex(payload),
			CopiedAt:     time.Date(2026, 6, 4, 12, 0, 0, 0, time.UTC),
			Status:       state.StatusVerified,
		}
		if err := store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("AppendEntry %q: %v", rel, err)
		}
	}
	if err := store.Gzip(ctx); err != nil {
		t.Fatalf("Gzip: %v", err)
	}

	// 4. Write the dest files at the namespaced path so rehash finds them.
	for rel, payload := range files {
		full := paths.Namespaced(destRoot, host, user, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir dest %q: %v", full, err)
		}
		if err := os.WriteFile(full, payload, 0o600); err != nil {
			t.Fatalf("write dest %q: %v", full, err)
		}
	}

	return &plantedRun{
		runID:        runID,
		runDir:       runDir,
		manifestPath: manifestBase + ".gz",
		files:        files,
	}
}

// canonicalRunID returns a unique runID for tests (matches the runIDPattern
// regex used by the resolver). seed lets multi-run tests produce distinct
// IDs that also sort chronologically by seed ascending.
func canonicalRunID(seed int) string {
	// Use a year+month+day+offset shape so seed=N produces IDs that
	// lexically sort exactly with seed.
	t := time.Date(2026, 1, 1, 0, seed, 0, 0, time.UTC)
	return t.Format("2006-01-02T1504Z") + "-aaaa"
}

// sha256Hex returns the hex-encoded sha256 of b. Duplicated from the
// rehash test helpers to keep this test file self-contained; the verify
// package deliberately does not depend on the internal/hash streaming
// helper since the test only needs a one-shot digest.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// ----------------------------------------------------------------------------
// Renderers used by tests
// ----------------------------------------------------------------------------

type recordingRenderer struct {
	mu     sync.Mutex
	events []types.UIEvent
}

func (r *recordingRenderer) OnEvent(_ context.Context, ev types.UIEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingRenderer) snapshot() []types.UIEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.UIEvent, len(r.events))
	copy(out, r.events)
	return out
}

// ----------------------------------------------------------------------------
// Manifest-tamper helpers
// ----------------------------------------------------------------------------

// tamperManifestEntry rewrites the SHA256Source field of the first entry
// in the manifest's gzipped NDJSON, keeping the persisted HMAC intact so
// Load surfaces it as IntegrityError (AC-19 path). Returns silently if the
// manifest contains zero entries.
func tamperManifestEntry(t *testing.T, manifestPath string) {
	t.Helper()
	raw := gunzipFile(t, manifestPath)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("manifest empty; nothing to tamper")
	}
	var e state.ManifestEntry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e.SHA256Source = "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"
	rewritten, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = rewritten
	out := bytes.Join(lines, []byte("\n"))
	out = append(out, '\n')
	writeGzipFile(t, manifestPath, out)
}

func gunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
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

// ----------------------------------------------------------------------------
// Pure unit tests (no mount required)
// ----------------------------------------------------------------------------

func TestVerify_EmptyDestRoot(t *testing.T) {
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot: "",
	})
	if err == nil {
		t.Fatal("expected error for empty DestRoot")
	}
	if !strings.Contains(err.Error(), "DestRoot") {
		t.Errorf("error: got %q want mentions DestRoot", err.Error())
	}
	if res == nil {
		t.Fatal("expected non-nil result even on validation failure")
	}
	if res.ExitStatus != ExitStatusPreflightFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusPreflightFailed)
	}
}

func TestVerify_AllAndRunIDMutuallyExclusive(t *testing.T) {
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot: "/tmp/anything",
		All:      true,
		RunID:    "2026-06-04T1200Z-aaaa",
	})
	if err == nil {
		t.Fatal("expected error for All=true with non-empty RunID")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Errorf("error: got %q want mentions 'mutually exclusive'", err.Error())
	}
	if res.ExitStatus != ExitStatusPreflightFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusPreflightFailed)
	}
}

func TestNewVerifyID_MatchesCanonicalFormat(t *testing.T) {
	at := time.Date(2026, 6, 4, 9, 5, 0, 0, time.UTC)
	id := newVerifyID(at)
	if !runIDPattern.MatchString(id) {
		t.Errorf("newVerifyID = %q; want match canonical pattern", id)
	}
	if !strings.HasPrefix(id, "2026-06-04T0905Z-") {
		t.Errorf("newVerifyID = %q; want prefix 2026-06-04T0905Z-", id)
	}
}

func TestNewVerifyID_TwoCallsDiffer(t *testing.T) {
	// Same timestamp; different hex suffix. Guards against a regression
	// that drops the random nonce.
	at := time.Date(2026, 6, 4, 9, 5, 0, 0, time.UTC)
	a := newVerifyID(at)
	b := newVerifyID(at)
	// Probabilistic; on a 65k space the odds of collision are ~1/65k per
	// pair. Treat a collision as a likely regression rather than bad
	// luck. Re-roll once to be safe.
	if a == b {
		b = newVerifyID(at)
	}
	if a == b {
		t.Errorf("two VerifyIDs collided: %q", a)
	}
}

func TestClassifyExitStatus(t *testing.T) {
	tests := []struct {
		name string
		rr   *rehash.Result
		lr   *load.LoadResult
		want string
	}{
		{"clean", &rehash.Result{}, &load.LoadResult{}, ExitStatusOK},
		{"nil-rehash-result", nil, &load.LoadResult{}, ExitStatusIntegrityFailed},
		{"nil-load-result", &rehash.Result{}, nil, ExitStatusIntegrityFailed},
		{"integrity error", &rehash.Result{}, &load.LoadResult{
			IntegrityErrors: []load.IntegrityError{{}},
		}, ExitStatusIntegrityFailed},
		{"schema error", &rehash.Result{}, &load.LoadResult{
			SchemaErrors: []load.SchemaError{{}},
		}, ExitStatusIntegrityFailed},
		{"hash mismatch", &rehash.Result{FilesHashMismatch: 1}, &load.LoadResult{}, ExitStatusIntegrityFailed},
		{"size mismatch", &rehash.Result{FilesSizeMismatch: 1}, &load.LoadResult{}, ExitStatusIntegrityFailed},
		{"missing", &rehash.Result{FilesMissing: 1}, &load.LoadResult{}, ExitStatusIntegrityFailed},
		{"unreadable", &rehash.Result{FilesUnreadable: 1}, &load.LoadResult{}, ExitStatusIntegrityFailed},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := classifyExitStatus(tc.rr, tc.lr)
			if got != tc.want {
				t.Errorf("classifyExitStatus: got %q want %q", got, tc.want)
			}
		})
	}
}

// TestWriteResultsNDJSON_RoundTrip locks the per-file results.ndjson
// schema added per Task 32 review (2026-06-05): writes a mixed-status
// PerFile slice (verified + hash_mismatch + missing + unreadable),
// reads back NDJSON line-by-line, asserts every field round-trips
// including expected vs actual sha256, expected vs actual size, and
// the error string for the unreadable entry.
func TestWriteResultsNDJSON_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	perFile := []rehash.FileResult{
		{
			Entry:        sampleManifestEntry("a.txt", 100, "aaaa"),
			Status:       rehash.StatusVerified,
			ActualSize:   100,
			ActualSHA256: "aaaa",
		},
		{
			Entry:        sampleManifestEntry("b.txt", 200, "bbbb"),
			Status:       rehash.StatusHashMismatch,
			ActualSize:   200,
			ActualSHA256: "ffff",
		},
		{
			Entry:      sampleManifestEntry("c.txt", 300, "cccc"),
			Status:     rehash.StatusMissing,
			ActualSize: -1,
		},
		{
			Entry:      sampleManifestEntry("d.txt", 400, "dddd"),
			Status:     rehash.StatusUnreadable,
			ActualSize: -1,
			Err:        errors.New("permission denied"),
		},
	}
	if err := writeResultsNDJSON(dir, perFile); err != nil {
		t.Fatalf("writeResultsNDJSON: %v", err)
	}

	resultsPath := filepath.Join(dir, "results.ndjson")
	data, err := os.ReadFile(resultsPath)
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) != len(perFile) {
		t.Fatalf("lines: got %d want %d", len(lines), len(perFile))
	}

	var got [4]resultsRecord
	for i, line := range lines {
		if err := json.Unmarshal(line, &got[i]); err != nil {
			t.Fatalf("unmarshal line %d: %v", i, err)
		}
	}

	// Verified: actual matches expected; no error.
	if got[0].Path != "a.txt" || got[0].Status != "verified" ||
		got[0].Sha256Expected != "aaaa" || got[0].Sha256Actual != "aaaa" ||
		got[0].SizeExpected != 100 || got[0].SizeActual != 100 || got[0].Err != "" {
		t.Errorf("verified record drift: %+v", got[0])
	}
	// Hash mismatch: actual sha differs from expected.
	if got[1].Path != "b.txt" || got[1].Status != "hash_mismatch" ||
		got[1].Sha256Expected != "bbbb" || got[1].Sha256Actual != "ffff" {
		t.Errorf("hash_mismatch record drift: %+v", got[1])
	}
	// Missing: ActualSize -1 stays as -1 (NOT omitted by omitempty; omitempty
	// drops zero but -1 is a sentinel and should survive).
	if got[2].Path != "c.txt" || got[2].Status != "missing" || got[2].SizeActual != -1 {
		t.Errorf("missing record drift: %+v", got[2])
	}
	// Unreadable: error string round-trips.
	if got[3].Path != "d.txt" || got[3].Status != "unreadable" ||
		got[3].Err != "permission denied" {
		t.Errorf("unreadable record drift: %+v", got[3])
	}

	// File mode 0o644 (no secrets; support tooling readable).
	stat, err := os.Stat(resultsPath)
	if err != nil {
		t.Fatalf("stat results: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o644 {
		t.Errorf("mode: got %o want 0o644", got)
	}
}

// TestWriteResultsNDJSON_EmptyPerFile asserts that an empty PerFile slice
// produces an empty file (zero entries verified is a valid outcome for an
// empty profile / empty manifest).
func TestWriteResultsNDJSON_EmptyPerFile(t *testing.T) {
	dir := t.TempDir()
	if err := writeResultsNDJSON(dir, nil); err != nil {
		t.Fatalf("writeResultsNDJSON(nil): %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "results.ndjson"))
	if err != nil {
		t.Fatalf("read results: %v", err)
	}
	if len(data) != 0 {
		t.Errorf("results.ndjson should be empty, got %d bytes: %q", len(data), data)
	}
}

func sampleManifestEntry(path string, size int64, sha string) state.ManifestEntry {
	return state.ManifestEntry{
		V:            1,
		Path:         path,
		Size:         size,
		MtimeNS:      1700000000000000000,
		SHA256Source: sha,
		Status:       state.StatusVerified,
	}
}

func TestWriteSummary_RoundTrip(t *testing.T) {
	// Round-trip: write a known VerifyResult; gunzip the file; parse it
	// back; assert every field survived. Guards the on-disk schema lock.
	dir := t.TempDir()
	runID := canonicalRunID(1)
	verifyID := "2026-06-04T1200Z-bbbb"

	res := &VerifyResult{
		RunID:                runID,
		FilesChecked:         10,
		FilesVerified:        7,
		FilesHashMismatch:    1,
		FilesIntegrityFailed: 1,
		FilesMissing:         1,
		FilesSizeMismatch:    0,
		FilesUnreadable:      0,
		FilesExtraInDest:     3,
		DurationSeconds:      42,
		BytesRead:            1234567890,
		ExitStatus:           ExitStatusIntegrityFailed,
	}
	if err := writeSummary(dir, runID, verifyID, res); err != nil {
		t.Fatalf("writeSummary: %v", err)
	}

	summaryPath := filepath.Join(dir, "runs", runID, "verifications", verifyID, "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var got summaryRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.V != summarySchemaVersion {
		t.Errorf("V: got %d want %d", got.V, summarySchemaVersion)
	}
	if got.VerifyID != verifyID {
		t.Errorf("VerifyID: got %q want %q", got.VerifyID, verifyID)
	}
	if got.ForRunID != runID {
		t.Errorf("ForRunID: got %q want %q", got.ForRunID, runID)
	}
	if got.FilesChecked != res.FilesChecked {
		t.Errorf("FilesChecked: got %d want %d", got.FilesChecked, res.FilesChecked)
	}
	if got.FilesIntegrityFailed != res.FilesIntegrityFailed {
		t.Errorf("FilesIntegrityFailed: got %d want %d",
			got.FilesIntegrityFailed, res.FilesIntegrityFailed)
	}
	if got.FilesExtraInDest != res.FilesExtraInDest {
		t.Errorf("FilesExtraInDest: got %d want %d",
			got.FilesExtraInDest, res.FilesExtraInDest)
	}
	if got.BytesRead != res.BytesRead {
		t.Errorf("BytesRead: got %d want %d", got.BytesRead, res.BytesRead)
	}
	if got.ExitStatus != res.ExitStatus {
		t.Errorf("ExitStatus: got %q want %q", got.ExitStatus, res.ExitStatus)
	}
	if got.VerifiedAt.IsZero() {
		t.Error("VerifiedAt is zero; want non-zero timestamp")
	}

	// File mode is 0o644 (no secrets in the verify summary).
	stat, err := os.Stat(summaryPath)
	if err != nil {
		t.Fatalf("stat summary: %v", err)
	}
	if got := stat.Mode().Perm(); got != 0o644 {
		t.Errorf("summary mode: got %o want 0644", got)
	}
}

func TestResolveRunIDs_RejectsMalformedRunID(t *testing.T) {
	// A typo should surface as a typed error, not a silent skip that
	// "verifies" nothing.
	dotDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dotDir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := resolveRunIDs(dotDir, VerifyOptions{RunID: "not-a-run-id"})
	if err == nil {
		t.Fatal("expected error for malformed RunID")
	}
	if !strings.Contains(err.Error(), "canonical pattern") {
		t.Errorf("error: got %q want mentions 'canonical pattern'", err.Error())
	}
}

func TestResolveRunIDs_RejectsMissingRunID(t *testing.T) {
	dotDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dotDir, "runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := resolveRunIDs(dotDir, VerifyOptions{RunID: "2026-06-04T1200Z-aaaa"})
	if err == nil {
		t.Fatal("expected error for missing RunID dir")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestResolveRunIDs_NoRunsOnUSB(t *testing.T) {
	dotDir := t.TempDir()
	// Do NOT create runs/ dir.
	_, err := resolveRunIDs(dotDir, VerifyOptions{})
	if err == nil {
		t.Fatal("expected error when no runs are present")
	}
	if !strings.Contains(err.Error(), "no runs") {
		t.Errorf("error: got %q want mentions 'no runs'", err.Error())
	}
}

func TestResolveRunIDs_LatestPicksNewest(t *testing.T) {
	dotDir := t.TempDir()
	runsDir := filepath.Join(dotDir, "runs")
	// Make three canonical run dirs; the lexically-latest is the newest.
	ids := []string{canonicalRunID(1), canonicalRunID(2), canonicalRunID(3)}
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(runsDir, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveRunIDs(dotDir, VerifyOptions{})
	if err != nil {
		t.Fatalf("resolveRunIDs: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("want 1 RunID for latest, got %d", len(got))
	}
	if got[0] != ids[2] {
		t.Errorf("latest: got %q want %q", got[0], ids[2])
	}
}

func TestResolveRunIDs_AllReturnsSortedAscending(t *testing.T) {
	dotDir := t.TempDir()
	runsDir := filepath.Join(dotDir, "runs")
	// Make three canonical run dirs in arbitrary creation order.
	ids := []string{canonicalRunID(3), canonicalRunID(1), canonicalRunID(2)}
	for _, id := range ids {
		if err := os.MkdirAll(filepath.Join(runsDir, id), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	got, err := resolveRunIDs(dotDir, VerifyOptions{All: true})
	if err != nil {
		t.Fatalf("resolveRunIDs: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("want 3 RunIDs, got %d", len(got))
	}
	want := []string{canonicalRunID(1), canonicalRunID(2), canonicalRunID(3)}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d]: got %q want %q", i, got[i], w)
		}
	}
}

func TestResolveRunIDs_SkipsNonCanonicalDirs(t *testing.T) {
	// Defense against arbitrary user content under .flashbackup/runs/:
	// only canonical-RunID-named dirs are eligible.
	dotDir := t.TempDir()
	runsDir := filepath.Join(dotDir, "runs")
	if err := os.MkdirAll(filepath.Join(runsDir, canonicalRunID(1)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runsDir, "notes"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runsDir, "readme.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := resolveRunIDs(dotDir, VerifyOptions{All: true})
	if err != nil {
		t.Fatalf("resolveRunIDs: %v", err)
	}
	if len(got) != 1 || got[0] != canonicalRunID(1) {
		t.Errorf("got %v want [%q]", got, canonicalRunID(1))
	}
}

func TestAggregatedRunID(t *testing.T) {
	tests := []struct {
		name string
		opts VerifyOptions
		ids  []string
		want string
	}{
		{"all-true", VerifyOptions{All: true}, []string{"a", "b"}, allRunIDSentinel},
		{"single", VerifyOptions{}, []string{"abc"}, "abc"},
		{"empty-runs", VerifyOptions{}, nil, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := aggregatedRunID(tc.opts, tc.ids)
			if got != tc.want {
				t.Errorf("got %q want %q", got, tc.want)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// End-to-end tests (mounted DMG)
// ----------------------------------------------------------------------------

func TestVerify_HappyPath(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(1)
	plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt":       []byte("alpha content"),
		"sub/b.txt":   []byte("bravo content longer"),
		"deep/c.json": []byte(`{"k":"v"}`),
	})

	rend := &recordingRenderer{}
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		UIRenderer:   rend,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.ExitStatus != ExitStatusOK {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusOK)
	}
	if res.FilesChecked != 3 {
		t.Errorf("FilesChecked: got %d want 3", res.FilesChecked)
	}
	if res.FilesVerified != 3 {
		t.Errorf("FilesVerified: got %d want 3", res.FilesVerified)
	}
	if res.FilesIntegrityFailed != 0 {
		t.Errorf("FilesIntegrityFailed: got %d want 0", res.FilesIntegrityFailed)
	}
	if res.BytesRead == 0 {
		t.Error("BytesRead: got 0; want > 0")
	}
	if res.RunID != runID {
		t.Errorf("RunID: got %q want %q", res.RunID, runID)
	}

	// summary.json lands at the canonical path.
	verifyDirs, _ := os.ReadDir(filepath.Join(dest, ".flashbackup", "runs", runID, "verifications"))
	if len(verifyDirs) != 1 {
		t.Fatalf("expected 1 verifications subdir, got %d", len(verifyDirs))
	}
	summaryPath := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications",
		verifyDirs[0].Name(), "summary.json")
	if _, err := os.Stat(summaryPath); err != nil {
		t.Errorf("summary.json missing at %s: %v", summaryPath, err)
	}
}

func TestVerify_TamperedManifest(t *testing.T) {
	// AC-19: an HMAC-failed manifest line must surface as
	// FilesIntegrityFailed and force ExitStatus=integrity_failed.
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(2)
	pr := plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("alpha"),
		"b.txt": []byte("bravo"),
	})

	// Tamper the manifest in-place. The dest files are still intact, so a
	// run that didn't check the HMAC would report ExitStatus=ok.
	tamperManifestEntry(t, pr.manifestPath)

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.ExitStatus != ExitStatusIntegrityFailed {
		t.Errorf("ExitStatus: got %q want %q (AC-19 violated)",
			res.ExitStatus, ExitStatusIntegrityFailed)
	}
	if res.FilesIntegrityFailed != 1 {
		t.Errorf("FilesIntegrityFailed: got %d want 1", res.FilesIntegrityFailed)
	}
}

func TestVerify_HashMismatch(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(3)
	pr := plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("original"),
	})

	// Mutate the dest file's bytes (same length so size check passes; the
	// hash compare must catch the drift).
	destPath := paths.Namespaced(dest, host, user, "a.txt")
	if err := os.WriteFile(destPath, []byte("MUTATED!"), 0o600); err != nil {
		t.Fatalf("write mutated: %v", err)
	}
	_ = pr // unused outside fixture

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.ExitStatus != ExitStatusIntegrityFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusIntegrityFailed)
	}
	if res.FilesHashMismatch != 1 {
		t.Errorf("FilesHashMismatch: got %d want 1", res.FilesHashMismatch)
	}
}

func TestVerify_MissingDestFile(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(4)
	pr := plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("alpha"),
		"b.txt": []byte("bravo"),
	})

	// Delete one dest file post-fixture.
	destPath := paths.Namespaced(dest, host, user, "b.txt")
	if err := os.Remove(destPath); err != nil {
		t.Fatalf("rm dest: %v", err)
	}
	_ = pr

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.ExitStatus != ExitStatusIntegrityFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusIntegrityFailed)
	}
	if res.FilesMissing != 1 {
		t.Errorf("FilesMissing: got %d want 1", res.FilesMissing)
	}
}

func TestVerify_RunIDSpecified(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	// Plant two runs.
	r1 := canonicalRunID(1)
	r2 := canonicalRunID(2)
	plantRun(t, dest, r1, host, user, map[string][]byte{"a.txt": []byte("a-content")})
	plantRun(t, dest, r2, host, user, map[string][]byte{
		"b.txt": []byte("b-content"),
		"c.txt": []byte("c-content"),
	})

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		RunID:        r1,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.RunID != r1 {
		t.Errorf("RunID: got %q want %q", res.RunID, r1)
	}
	// Only r1's one file got verified; r2's two files were not touched.
	if res.FilesChecked != 1 {
		t.Errorf("FilesChecked: got %d want 1", res.FilesChecked)
	}
}

func TestVerify_RunIDLatest(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	r1 := canonicalRunID(1)
	r2 := canonicalRunID(2)
	plantRun(t, dest, r1, host, user, map[string][]byte{"a.txt": []byte("a-content")})
	plantRun(t, dest, r2, host, user, map[string][]byte{
		"b.txt": []byte("b-content"),
		"c.txt": []byte("c-content"),
	})

	// RunID="" should resolve to the latest (r2 by lexical sort).
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.RunID != r2 {
		t.Errorf("RunID: got %q want %q", res.RunID, r2)
	}
	if res.FilesChecked != 2 {
		t.Errorf("FilesChecked: got %d want 2 (r2 has 2 files)", res.FilesChecked)
	}
}

func TestVerify_RunIDInvalid(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, _, _ := setupDest(t)
	// RunID matches the canonical pattern but the dir does not exist.
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		RunID:        canonicalRunID(99),
		SkipCodesign: true,
	})
	if err == nil {
		t.Fatal("expected error for nonexistent RunID")
	}
	if res.ExitStatus != ExitStatusPreflightFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusPreflightFailed)
	}
}

func TestVerify_All(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	// Three runs; one tampered.
	r1 := canonicalRunID(1)
	r2 := canonicalRunID(2)
	r3 := canonicalRunID(3)
	plantRun(t, dest, r1, host, user, map[string][]byte{"a.txt": []byte("a")})
	pr2 := plantRun(t, dest, r2, host, user, map[string][]byte{"b.txt": []byte("b")})
	plantRun(t, dest, r3, host, user, map[string][]byte{"c.txt": []byte("c"), "d.txt": []byte("d")})

	// Tamper r2's manifest. Aggregate ExitStatus must reflect this.
	tamperManifestEntry(t, pr2.manifestPath)

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		All:          true,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.RunID != allRunIDSentinel {
		t.Errorf("RunID: got %q want %q", res.RunID, allRunIDSentinel)
	}
	// Aggregate counters: r1(1) + r2(0 verified, 1 integrity fail) + r3(2) = 3 verified, 1 integrity failed.
	if res.FilesChecked != 3 {
		// r2's tampered entry never makes it to rehash (load drops it),
		// so FilesChecked sees the 1 file from r1 and 2 files from r3.
		t.Errorf("FilesChecked: got %d want 3", res.FilesChecked)
	}
	if res.FilesVerified != 3 {
		t.Errorf("FilesVerified: got %d want 3", res.FilesVerified)
	}
	if res.FilesIntegrityFailed != 1 {
		t.Errorf("FilesIntegrityFailed: got %d want 1", res.FilesIntegrityFailed)
	}
	if res.ExitStatus != ExitStatusIntegrityFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusIntegrityFailed)
	}

	// Each run has its own summary.json under its verifications/ subdir.
	for _, runID := range []string{r1, r2, r3} {
		verifyDir := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications")
		ents, err := os.ReadDir(verifyDir)
		if err != nil {
			t.Errorf("read verify dir for %s: %v", runID, err)
			continue
		}
		if len(ents) != 1 {
			t.Errorf("run %s: want 1 verifications subdir, got %d", runID, len(ents))
		}
	}
}

func TestVerify_CheckExtras(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(5)
	plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("alpha"),
		"b.txt": []byte("bravo"),
	})

	// Write an extra file at the namespaced dest that is NOT in the manifest.
	extraPath := paths.Namespaced(dest, host, user, "extra.txt")
	if err := os.WriteFile(extraPath, []byte("not in manifest"), 0o600); err != nil {
		t.Fatalf("write extra: %v", err)
	}

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		CheckExtras:  true,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.FilesExtraInDest != 1 {
		t.Errorf("FilesExtraInDest: got %d want 1", res.FilesExtraInDest)
	}
	// Extras alone do NOT fail the verify (per spec section 5).
	if res.ExitStatus != ExitStatusOK {
		t.Errorf("ExitStatus: got %q want %q (extras-only must not fail)",
			res.ExitStatus, ExitStatusOK)
	}
}

func TestVerify_CheckExtrasOff(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(6)
	plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("alpha"),
	})

	// Plant an extra dest file but DO NOT request CheckExtras: the
	// extras count must stay at 0 (no walk performed).
	extraPath := paths.Namespaced(dest, host, user, "untracked.txt")
	if err := os.WriteFile(extraPath, []byte("hi"), 0o600); err != nil {
		t.Fatalf("write extra: %v", err)
	}

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.FilesExtraInDest != 0 {
		t.Errorf("FilesExtraInDest: got %d want 0 (CheckExtras=false)", res.FilesExtraInDest)
	}
}

func TestVerify_PreflightFailure(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	// Mount a fresh DMG but do NOT seed version.json so preflight fails
	// at gate 8 (fail-closed missing version file).
	dest := testutil.MountTempVolume(t, "APFS")

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err == nil {
		t.Fatal("expected preflight failure")
	}
	if res.ExitStatus != ExitStatusPreflightFailed {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusPreflightFailed)
	}
}

func TestVerify_SummaryJSONContents(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(7)
	plantRun(t, dest, runID, host, user, map[string][]byte{
		"a.txt": []byte("hello"),
		"b.txt": []byte("world"),
	})

	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Find the summary.json under the canonical path.
	verifyDir := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications")
	ents, err := os.ReadDir(verifyDir)
	if err != nil {
		t.Fatalf("read verify dir: %v", err)
	}
	if len(ents) != 1 {
		t.Fatalf("want 1 verifications subdir, got %d", len(ents))
	}
	summaryPath := filepath.Join(verifyDir, ents[0].Name(), "summary.json")
	data, err := os.ReadFile(summaryPath)
	if err != nil {
		t.Fatalf("read summary: %v", err)
	}
	var got summaryRecord
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.V != summarySchemaVersion {
		t.Errorf("V: got %d want %d", got.V, summarySchemaVersion)
	}
	if got.ForRunID != runID {
		t.Errorf("ForRunID: got %q want %q", got.ForRunID, runID)
	}
	if got.FilesChecked != res.FilesChecked {
		t.Errorf("FilesChecked: got %d want %d", got.FilesChecked, res.FilesChecked)
	}
	if got.FilesVerified != res.FilesVerified {
		t.Errorf("FilesVerified: got %d want %d", got.FilesVerified, res.FilesVerified)
	}
	if got.ExitStatus != res.ExitStatus {
		t.Errorf("ExitStatus: got %q want %q", got.ExitStatus, res.ExitStatus)
	}
}

func TestVerify_RendererSummaryEvent(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(8)
	plantRun(t, dest, runID, host, user, map[string][]byte{"a.txt": []byte("hi")})

	rend := &recordingRenderer{}
	if _, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		UIRenderer:   rend,
		SkipCodesign: true,
	}); err != nil {
		t.Fatalf("Verify: %v", err)
	}

	// Final event must be UIEvtSummary with Status=ok.
	events := rend.snapshot()
	if len(events) == 0 {
		t.Fatal("renderer saw no events")
	}
	last := events[len(events)-1]
	if last.Kind != types.UIEvtSummary {
		t.Errorf("last event Kind: got %s want %s", last.Kind, types.UIEvtSummary)
	}
	if last.Status != ExitStatusOK {
		t.Errorf("last event Status: got %q want %q", last.Status, ExitStatusOK)
	}
}

func TestVerify_NilRenderer(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest, host, user := setupDest(t)
	runID := canonicalRunID(9)
	plantRun(t, dest, runID, host, user, map[string][]byte{"a.txt": []byte("hi")})

	// Nil renderer must not panic.
	res, err := Verify(context.Background(), VerifyOptions{
		DestRoot:     dest,
		UIRenderer:   nil,
		SkipCodesign: true,
	})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if res.ExitStatus != ExitStatusOK {
		t.Errorf("ExitStatus: got %q want %q", res.ExitStatus, ExitStatusOK)
	}
}
