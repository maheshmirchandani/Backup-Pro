package runner

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
	"sync"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Helpers reused: captureRenderer, readNDJSON, eventKinds, faultingEventStore,
// canonicalRunID, makeT1EventStore from sibling test files in the same package.

// makeT3Stores opens a fresh events.ndjson AND a NDJSONManifestStore under a
// tmp DotDir/runs/<runID>/. Mirrors makeT2Stores' shape but also threads the
// manifest store + path so tests can read the gzipped manifest.
func makeT3Stores(t *testing.T) (es state.EventStore, ms state.ManifestStore,
	eventsPath, manifestPath, manifestKeyHex string, hmacKey []byte) {
	t.Helper()
	dotDir := filepath.Join(t.TempDir(), ".flashbackup")
	runID := canonicalRunID
	runDir := filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	eventsPath = filepath.Join(runDir, "events.ndjson")
	store, err := state.NewNDJSONEventStore(eventsPath)
	if err != nil {
		t.Fatalf("open events store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	hmacKey = []byte("t3-test-hmac-key")
	manifestPath = filepath.Join(runDir, "manifest.ndjson")
	mstore, err := state.NewNDJSONManifestStore(manifestPath, hmacKey)
	if err != nil {
		t.Fatalf("open manifest store: %v", err)
	}
	t.Cleanup(func() { _ = mstore.Gzip(context.Background()) })

	return store, mstore, eventsPath, manifestPath, "", hmacKey
}

// seedTransferred makes N small files in src and writes byte-equal copies
// to dest (preserving the relative path). Returns the matching Candidates
// and the Signatures captured BEFORE any mutation (so the (size, mtime_ns)
// gate has something to compare against).
func seedTransferred(t *testing.T, files []seedFile) (src, dest string,
	cands []selection.Candidate, sigs map[string]types.Signature) {
	t.Helper()
	src = t.TempDir()
	dest = t.TempDir()
	sigs = make(map[string]types.Signature, len(files))

	for _, sf := range files {
		full := filepath.Join(src, filepath.FromSlash(sf.rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir src: %v", err)
		}
		if err := os.WriteFile(full, sf.content, 0o600); err != nil {
			t.Fatalf("write src %s: %v", full, err)
		}
		// Copy to dest unless seedFile says skip (used to simulate
		// rsync-failed-to-transfer).
		if !sf.skipDest {
			destFull := filepath.Join(dest, filepath.FromSlash(sf.rel))
			if err := os.MkdirAll(filepath.Dir(destFull), 0o700); err != nil {
				t.Fatalf("mkdir dest: %v", err)
			}
			body := sf.content
			if sf.destDiffers {
				body = []byte("DIFFERENT-" + string(sf.content))
			}
			if err := os.WriteFile(destFull, body, 0o600); err != nil {
				t.Fatalf("write dest %s: %v", destFull, err)
			}
		}
	}

	// Stat sources AFTER writes to capture authoritative signatures.
	for _, sf := range files {
		full := filepath.Join(src, filepath.FromSlash(sf.rel))
		st, err := os.Stat(full)
		if err != nil {
			t.Fatalf("stat src: %v", err)
		}
		cands = append(cands, selection.Candidate{
			RelativePath: sf.rel,
			AbsolutePath: full,
			Size:         st.Size(),
			MtimeNS:      st.ModTime().UnixNano(),
		})
		sigs[sf.rel] = types.Signature{Size: st.Size(), MtimeNS: st.ModTime().UnixNano()}
	}
	return src, dest, cands, sigs
}

type seedFile struct {
	rel         string
	content     []byte
	destDiffers bool // dest copy gets different bytes (forces hash_mismatch)
	skipDest    bool // do not write dest copy (forces not_transferred)
}

// readManifestGz decompresses a .tmp.gz manifest and returns one
// ManifestEntry per line. The store is finalized with Gzip() first so the
// .gz form is what we read.
func readManifestGz(t *testing.T, ms state.ManifestStore, path string) []state.ManifestEntry {
	t.Helper()
	if err := ms.Gzip(context.Background()); err != nil {
		t.Fatalf("Gzip manifest: %v", err)
	}
	gzPath := path + ".gz"
	f, err := os.Open(gzPath)
	if err != nil {
		t.Fatalf("open manifest.gz: %v", err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read gz: %v", err)
	}
	var out []state.ManifestEntry
	for _, line := range bytes.Split(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var e state.ManifestEntry
		if err := json.Unmarshal(line, &e); err != nil {
			t.Fatalf("parse manifest line %q: %v", string(line), err)
		}
		out = append(out, e)
	}
	return out
}

func TestRunT3HashCompare_HappyPath_AllVerified(t *testing.T) {
	files := []seedFile{
		{rel: "a.txt", content: []byte("alpha-bytes")},
		{rel: "b.md", content: []byte("bravo-bytes")},
		{rel: "sub/c.pdf", content: []byte("charlie-bytes")},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, eventsPath, manifestPath, _, hmacKey := makeT3Stores(t)
	rend := &captureRenderer{}

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
		UIRenderer:    rend,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T3Result")
	}
	if res.FilesTotal != len(files) || res.FilesVerified != len(files) {
		t.Errorf("FilesTotal=%d FilesVerified=%d; want %d/%d",
			res.FilesTotal, res.FilesVerified, len(files), len(files))
	}
	if res.FilesHashMismatch != 0 || res.FilesSourceMutated != 0 ||
		res.FilesSourceUnreadable != 0 || res.FilesDestUnreadable != 0 ||
		res.FilesNotTransferred != 0 {
		t.Errorf("non-verified counts != 0: %+v", res)
	}
	for _, c := range cands {
		if got := res.PerFileStatus[c.RelativePath]; got != state.StatusVerified {
			t.Errorf("PerFileStatus[%q]=%q; want verified", c.RelativePath, got)
		}
	}

	// Audit log: phase_started + N file_completed + phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started"}
	for range files {
		want = append(want, "file_completed")
	}
	want = append(want, "phase_completed")
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("event[%d] kind = %q; want %q", i, kinds[i], k)
		}
	}
	for i, ev := range events {
		if ev["phase"] != "T2" {
			t.Errorf("event[%d].phase = %v; want T2", i, ev["phase"])
		}
	}
	// Each file_completed has Details{path, status:"verified",
	// sha256_source: 16-hex-prefix}.
	for i := 0; i < len(files); i++ {
		ev := events[1+i]
		details, ok := ev["details"].(map[string]any)
		if !ok {
			t.Fatalf("event[%d].details missing: %v", 1+i, ev)
		}
		if details["status"] != "verified" {
			t.Errorf("event[%d].details.status = %v; want verified", 1+i, details["status"])
		}
		prefix, ok := details["sha256_source"].(string)
		if !ok || len(prefix) != 16 {
			t.Errorf("event[%d].details.sha256_source = %v; want 16-hex-char string", 1+i, details["sha256_source"])
		}
	}
	// phase_completed Details has duration_ms + files_total + files_verified.
	pc, _ := events[len(events)-1]["details"].(map[string]any)
	if v, ok := pc["duration_ms"].(float64); !ok || v < 0 {
		t.Errorf("phase_completed.duration_ms = %v; want non-negative", pc["duration_ms"])
	}
	if v, ok := pc["files_total"].(float64); !ok || int(v) != len(files) {
		t.Errorf("phase_completed.files_total = %v; want %d", pc["files_total"], len(files))
	}
	if v, ok := pc["files_verified"].(float64); !ok || int(v) != len(files) {
		t.Errorf("phase_completed.files_verified = %v; want %d", pc["files_verified"], len(files))
	}

	// Renderer: UIEvtPhaseStarted, N x UIEvtFileCompleted, UIEvtPhaseCompleted.
	ui := rend.seen()
	if len(ui) != 1+len(files)+1 {
		t.Fatalf("renderer events = %d; want %d", len(ui), 1+len(files)+1)
	}
	if ui[0].Kind != types.UIEvtPhaseStarted || ui[0].Phase != types.PhaseHashCompare {
		t.Errorf("ui[0] = %+v; want PhaseStarted/T2", ui[0])
	}
	for i := 0; i < len(files); i++ {
		if ui[1+i].Kind != types.UIEvtFileCompleted {
			t.Errorf("ui[%d].Kind = %v; want FileCompleted", 1+i, ui[1+i].Kind)
		}
		if ui[1+i].Phase != types.PhaseHashCompare {
			t.Errorf("ui[%d].Phase = %v; want T2", 1+i, ui[1+i].Phase)
		}
		if ui[1+i].Status != string(state.StatusVerified) {
			t.Errorf("ui[%d].Status = %q; want verified", 1+i, ui[1+i].Status)
		}
	}
	last := ui[len(ui)-1]
	if last.Kind != types.UIEvtPhaseCompleted || last.Status != "ok" || last.Phase != types.PhaseHashCompare {
		t.Errorf("ui[last] = %+v; want PhaseCompleted/ok/T2", last)
	}

	// Manifest has N entries; each entry's HMAC validates under hmacKey.
	entries := readManifestGz(t, ms, manifestPath)
	if len(entries) != len(files) {
		t.Fatalf("manifest entries = %d; want %d", len(entries), len(files))
	}
	for i, e := range entries {
		if e.V != 1 {
			t.Errorf("entry[%d].V = %d; want 1", i, e.V)
		}
		if e.Status != state.StatusVerified {
			t.Errorf("entry[%d].Status = %q; want verified", i, e.Status)
		}
		if e.SHA256Source == "" {
			t.Errorf("entry[%d].SHA256Source empty", i)
		}
		if e.HMAC == "" {
			t.Errorf("entry[%d].HMAC empty", i)
		}
		if !state.VerifyHMAC(e, hmacKey) {
			t.Errorf("entry[%d] HMAC integrity check failed: %+v", i, e)
		}
	}
}

func TestRunT3HashCompare_HashMismatch(t *testing.T) {
	files := []seedFile{
		{rel: "ok.txt", content: []byte("ok-bytes")},
		{rel: "bad.txt", content: []byte("source-bytes"), destDiffers: true},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, eventsPath, _, _, _ := makeT3Stores(t)

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res.PerFileStatus["bad.txt"] != state.StatusHashMismatch {
		t.Errorf("PerFileStatus[bad.txt] = %q; want hash_mismatch", res.PerFileStatus["bad.txt"])
	}
	if res.FilesHashMismatch != 1 {
		t.Errorf("FilesHashMismatch = %d; want 1", res.FilesHashMismatch)
	}
	if res.FilesVerified != 1 {
		t.Errorf("FilesVerified = %d; want 1", res.FilesVerified)
	}

	// Audit log: must include a hash_mismatch event for bad.txt with
	// Details{path, sha256_source_prefix, sha256_dest_prefix}.
	events := readNDJSON(t, eventsPath)
	var hm map[string]any
	for _, ev := range events {
		if ev["kind"] == "hash_mismatch" {
			hm = ev
			break
		}
	}
	if hm == nil {
		t.Fatal("expected hash_mismatch event in audit log")
	}
	if hm["path"] != "bad.txt" {
		t.Errorf("hash_mismatch.path = %v; want bad.txt", hm["path"])
	}
	det, _ := hm["details"].(map[string]any)
	if sp, _ := det["sha256_source_prefix"].(string); len(sp) != 16 {
		t.Errorf("hash_mismatch.details.sha256_source_prefix = %v; want 16-hex", det["sha256_source_prefix"])
	}
	if dp, _ := det["sha256_dest_prefix"].(string); len(dp) != 16 {
		t.Errorf("hash_mismatch.details.sha256_dest_prefix = %v; want 16-hex", det["sha256_dest_prefix"])
	}
	// file_completed for bad.txt MUST NOT be emitted (hash_mismatch is the
	// per-file event for mismatch; we lock this behavior).
	for _, ev := range events {
		if ev["kind"] == "file_completed" && ev["path"] == "bad.txt" {
			t.Error("file_completed should not be emitted alongside hash_mismatch")
		}
	}
}

func TestRunT3HashCompare_SourceMutated(t *testing.T) {
	files := []seedFile{
		{rel: "stable.txt", content: []byte("stable")},
		{rel: "mut.txt", content: []byte("source-original")},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, eventsPath, manifestPath, _, hmacKey := makeT3Stores(t)

	// Mutate the source AFTER signature capture: rewrite the file so size
	// and/or mtime_ns shift. Sleep briefly to ensure mtime_ns delta on
	// filesystems with nanosecond resolution.
	mutFull := filepath.Join(src, "mut.txt")
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(mutFull, []byte("source-MUTATED-WITH-EXTRA-BYTES"), 0o600); err != nil {
		t.Fatalf("mutate source: %v", err)
	}

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res.PerFileStatus["mut.txt"] != state.StatusSourceMutated {
		t.Errorf("PerFileStatus[mut.txt] = %q; want source_mutated", res.PerFileStatus["mut.txt"])
	}
	if res.FilesSourceMutated != 1 {
		t.Errorf("FilesSourceMutated = %d; want 1", res.FilesSourceMutated)
	}

	// Audit: source_mutated event with Details{path}.
	events := readNDJSON(t, eventsPath)
	var sm map[string]any
	for _, ev := range events {
		if ev["kind"] == "source_mutated" {
			sm = ev
			break
		}
	}
	if sm == nil {
		t.Fatal("expected source_mutated event in audit log")
	}
	if sm["path"] != "mut.txt" {
		t.Errorf("source_mutated.path = %v; want mut.txt", sm["path"])
	}

	// Manifest entry for mut.txt has SHA256Source == "" (never hashed).
	entries := readManifestGz(t, ms, manifestPath)
	var mut state.ManifestEntry
	for _, e := range entries {
		if e.Path == "mut.txt" {
			mut = e
			break
		}
	}
	if mut.Path != "mut.txt" {
		t.Fatal("manifest missing mut.txt entry")
	}
	if mut.Status != state.StatusSourceMutated {
		t.Errorf("mut.txt entry.Status = %q; want source_mutated", mut.Status)
	}
	if mut.SHA256Source != "" {
		t.Errorf("mut.txt entry.SHA256Source = %q; want empty (no hash on mutated source)", mut.SHA256Source)
	}
	if !state.VerifyHMAC(mut, hmacKey) {
		t.Errorf("mut.txt entry HMAC integrity check failed: %+v", mut)
	}
}

func TestRunT3HashCompare_NotTransferred(t *testing.T) {
	files := []seedFile{
		{rel: "exists.txt", content: []byte("exists")},
		{rel: "missing.txt", content: []byte("never-copied"), skipDest: true},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, _, _, _, _ := makeT3Stores(t)

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res.PerFileStatus["missing.txt"] != state.StatusNotTransferred {
		t.Errorf("PerFileStatus[missing.txt] = %q; want not_transferred", res.PerFileStatus["missing.txt"])
	}
	if res.FilesNotTransferred != 1 {
		t.Errorf("FilesNotTransferred = %d; want 1", res.FilesNotTransferred)
	}
	if res.FilesVerified != 1 {
		t.Errorf("FilesVerified = %d; want 1 (exists.txt)", res.FilesVerified)
	}
}

func TestRunT3HashCompare_SourceUnreadable(t *testing.T) {
	// Skip if running as root (root bypasses 0000 perms).
	if os.Geteuid() == 0 {
		t.Skip("source_unreadable test requires non-root user")
	}
	files := []seedFile{
		{rel: "ok.txt", content: []byte("ok")},
		{rel: "denied.txt", content: []byte("denied-bytes")},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, _, _, _, _ := makeT3Stores(t)

	// chmod source/denied.txt to 0000 so open() fails.
	deniedSrc := filepath.Join(src, "denied.txt")
	if err := os.Chmod(deniedSrc, 0o000); err != nil {
		t.Fatalf("chmod 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(deniedSrc, 0o600) })

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res.PerFileStatus["denied.txt"] != state.StatusSourceUnreadable {
		t.Errorf("PerFileStatus[denied.txt] = %q; want source_unreadable", res.PerFileStatus["denied.txt"])
	}
	if res.FilesSourceUnreadable != 1 {
		t.Errorf("FilesSourceUnreadable = %d; want 1", res.FilesSourceUnreadable)
	}
}

func TestRunT3HashCompare_DestUnreadable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("dest_unreadable test requires non-root user")
	}
	files := []seedFile{
		{rel: "ok.txt", content: []byte("ok")},
		{rel: "destdenied.txt", content: []byte("dest-denied")},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	es, ms, _, _, _, _ := makeT3Stores(t)

	// chmod dest/destdenied.txt to 0000 so open() fails on the dest side
	// but the source is still readable.
	deniedDest := filepath.Join(dest, "destdenied.txt")
	if err := os.Chmod(deniedDest, 0o000); err != nil {
		t.Fatalf("chmod 0000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(deniedDest, 0o600) })

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	if res.PerFileStatus["destdenied.txt"] != state.StatusDestUnreadable {
		t.Errorf("PerFileStatus[destdenied.txt] = %q; want dest_unreadable", res.PerFileStatus["destdenied.txt"])
	}
	if res.FilesDestUnreadable != 1 {
		t.Errorf("FilesDestUnreadable = %d; want 1", res.FilesDestUnreadable)
	}
}

func TestRunT3HashCompare_CancelledContextAtEntry(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("a")},
	})
	es, ms, eventsPath, _, _, _ := makeT3Stores(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunT3HashCompare(ctx, T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected cancelled-context error at entry")
	}
	if res != nil {
		t.Errorf("expected nil result, got %+v", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty events.ndjson at entry-cancel; got %q", string(data))
	}
}

// midCancelT3Store cancels a tied cancel func after Append has landed N
// file_completed (or any) events. Used to exercise mid-loop cancellation.
type midCancelT3Store struct {
	inner       state.EventStore
	cancel      context.CancelFunc
	cancelAfter int
	mu          sync.Mutex
	count       int
}

func (m *midCancelT3Store) Append(ctx context.Context, ev state.Event) error {
	if err := m.inner.Append(ctx, ev); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if ev.Kind == "file_completed" || ev.Kind == "hash_mismatch" || ev.Kind == "source_mutated" {
		m.count++
		if m.count == m.cancelAfter {
			m.cancel()
		}
	}
	return nil
}

func (m *midCancelT3Store) Checkpoint(ctx context.Context) error {
	return m.inner.Checkpoint(ctx)
}

func (m *midCancelT3Store) Close() error { return m.inner.Close() }

func TestRunT3HashCompare_CancelledMidLoop(t *testing.T) {
	// Seed enough files that mid-loop cancel has files left to skip.
	const nFiles = 16
	var files []seedFile
	for i := 0; i < nFiles; i++ {
		files = append(files, seedFile{
			rel:     fmt.Sprintf("f-%02d.txt", i),
			content: []byte(fmt.Sprintf("file-%02d", i)),
		})
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	innerES, eventsPath := makeT1EventStore(t)
	ms, err := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "manifest.ndjson"), []byte("k"))
	if err != nil {
		t.Fatalf("open manifest: %v", err)
	}
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	es := &midCancelT3Store{inner: innerES, cancel: cancel, cancelAfter: 3}

	_, err = RunT3HashCompare(ctx, T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error after mid-loop cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}
	// phase_completed must NOT be present; the phase did not complete.
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "phase_completed" {
			t.Error("phase_completed should be absent on mid-loop cancellation")
		}
	}
}

func TestRunT3HashCompare_NilEventStore(t *testing.T) {
	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot: "/tmp",
		DestRoot:   "/tmp",
		EventStore: nil,
	})
	if err == nil {
		t.Fatal("expected error on nil EventStore")
	}
	if !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected error to mention EventStore; got %v", err)
	}
}

func TestRunT3HashCompare_NilManifestStore(t *testing.T) {
	es, _, _, _, _, _ := makeT3Stores(t)
	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    "/tmp",
		DestRoot:      "/tmp",
		EventStore:    es,
		ManifestStore: nil,
	})
	if err == nil {
		t.Fatal("expected error on nil ManifestStore")
	}
	if !strings.Contains(err.Error(), "ManifestStore") {
		t.Errorf("expected error to mention ManifestStore; got %v", err)
	}
}

func TestRunT3HashCompare_AppendPhaseStartedFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("a")},
	})
	innerES, _ := makeT1EventStore(t)
	ms, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })
	sentinel := errors.New("simulated phase_started fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when phase_started Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT3HashCompare_AppendFileCompletedFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("a")},
		{rel: "b.txt", content: []byte("b")},
	})
	innerES, _ := makeT1EventStore(t)
	ms, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })
	sentinel := errors.New("simulated file_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "file_completed", err: sentinel}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when file_completed Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT3HashCompare_AppendHashMismatchFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "bad.txt", content: []byte("source"), destDiffers: true},
	})
	innerES, _ := makeT1EventStore(t)
	ms, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })
	sentinel := errors.New("simulated hash_mismatch fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "hash_mismatch", err: sentinel}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when hash_mismatch Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT3HashCompare_AppendSourceMutatedFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "mut.txt", content: []byte("original")},
	})
	// Force mutation BEFORE running.
	mutFull := filepath.Join(src, "mut.txt")
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(mutFull, []byte("MUTATED-BIGGER-CONTENT"), 0o600); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	innerES, _ := makeT1EventStore(t)
	ms, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })
	sentinel := errors.New("simulated source_mutated fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "source_mutated", err: sentinel}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when source_mutated Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT3HashCompare_AppendPhaseCompletedFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("a")},
	})
	innerES, _ := makeT1EventStore(t)
	ms, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })
	sentinel := errors.New("simulated phase_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when phase_completed Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

// failingManifestStore fails AppendEntry on the Nth call. Used to exercise
// the manifest write error branch (per-file errors do bubble up as wrapped
// errors per the contract; ManifestStore is the authoritative record).
type failingManifestStore struct {
	failOn int // 1-indexed
	seen   int
	err    error
	inner  state.ManifestStore
}

func (f *failingManifestStore) AppendEntry(ctx context.Context, e state.ManifestEntry) error {
	f.seen++
	if f.seen == f.failOn {
		return f.err
	}
	return f.inner.AppendEntry(ctx, e)
}
func (f *failingManifestStore) Gzip(ctx context.Context) error { return f.inner.Gzip(ctx) }

func TestRunT3HashCompare_ManifestAppendEntryFails(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("a")},
		{rel: "b.txt", content: []byte("b")},
	})
	es, _, _, _, _, _ := makeT3Stores(t)
	inner, _ := state.NewNDJSONManifestStore(filepath.Join(t.TempDir(), "m.ndjson"), []byte("k"))
	t.Cleanup(func() { _ = inner.Gzip(context.Background()) })
	sentinel := errors.New("simulated manifest write fault")
	ms := &failingManifestStore{failOn: 2, err: sentinel, inner: inner}

	_, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err == nil {
		t.Fatal("expected error when ManifestStore.AppendEntry fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT3HashCompare_RendererErrorIsNonFatal(t *testing.T) {
	src, dest, cands, sigs := seedTransferred(t, []seedFile{
		{rel: "a.txt", content: []byte("alpha")},
	})
	es, ms, _, _, _, _ := makeT3Stores(t)
	rend := &captureRenderer{err: errors.New("renderer broken")}

	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
		UIRenderer:    rend,
	})
	if err != nil {
		t.Fatalf("PS3: renderer errors must not abort; got %v", err)
	}
	if res == nil {
		t.Fatal("expected T3Result")
	}
	if res.FilesVerified != 1 {
		t.Errorf("FilesVerified = %d; want 1", res.FilesVerified)
	}
}

func TestRunT3HashCompare_CountConsistency(t *testing.T) {
	// One of each non-trivial classification: verified, hash_mismatch,
	// source_mutated, not_transferred. (source_unreadable / dest_unreadable
	// have dedicated tests with chmod.)
	files := []seedFile{
		{rel: "v.txt", content: []byte("verified")},
		{rel: "hm.txt", content: []byte("hm-source"), destDiffers: true},
		{rel: "mut.txt", content: []byte("mut-orig")},
		{rel: "nt.txt", content: []byte("never-copied"), skipDest: true},
	}
	src, dest, cands, sigs := seedTransferred(t, files)
	// Force mutation on mut.txt after signatures.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "mut.txt"), []byte("mut-MUTATED-LONGER"), 0o600); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	es, ms, _, _, _, _ := makeT3Stores(t)
	res, err := RunT3HashCompare(context.Background(), T3Input{
		SourceRoot:    src,
		DestRoot:      dest,
		Candidates:    cands,
		Signatures:    sigs,
		Mode:          types.ModeCopy,
		ManifestStore: ms,
		EventStore:    es,
	})
	if err != nil {
		t.Fatalf("RunT3HashCompare: %v", err)
	}
	sum := res.FilesVerified + res.FilesHashMismatch + res.FilesSourceMutated +
		res.FilesSourceUnreadable + res.FilesDestUnreadable + res.FilesNotTransferred
	if sum != res.FilesTotal {
		t.Errorf("count consistency: sum(per-status)=%d FilesTotal=%d", sum, res.FilesTotal)
	}
	if res.FilesTotal != len(files) {
		t.Errorf("FilesTotal=%d want %d", res.FilesTotal, len(files))
	}
}
