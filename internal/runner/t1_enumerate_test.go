package runner

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Tests reuse captureRenderer / readNDJSON / faultingEventStore from
// t0_preflight_test.go (same package). T0+ does not need an APFS volume:
// the source can be any writable tree (t.TempDir) and the destination is
// not touched by this phase (T0 already validated it).

// makeT1EventStore opens a fresh events.ndjson under a tmp dir; mirrors
// makeStores's events half. We do not need the RunLogStore at T0+ (only
// T0 writes the runs.ndjson lines).
func makeT1EventStore(t *testing.T) (state.EventStore, string) {
	t.Helper()
	dotDir := filepath.Join(t.TempDir(), ".flashbackup", "runs", canonicalRunID)
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	path := filepath.Join(dotDir, "events.ndjson")
	es, err := state.NewNDJSONEventStore(path)
	if err != nil {
		t.Fatalf("open events store: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es, path
}

// seedTree creates a small source tree under dir; returns the relative
// paths (forward-slash, NFC) in canonical sort order.
func seedTree(t *testing.T, dir string) []string {
	t.Helper()
	files := []string{
		"a.txt",
		"b.md",
		"sub/c.pdf",
		"sub/deep/d.txt",
	}
	for _, rel := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", full, err)
		}
		if err := os.WriteFile(full, []byte("data-"+rel), 0o600); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}
	return files
}

// readNDJSONNumbers parses an NDJSON file using json.Decoder.UseNumber,
// so int64 values larger than 2^53 round-trip without precision loss.
// readNDJSON in t0_preflight_test.go uses the default decoder which is
// fine for the duration_ms range but not for nanosecond mtimes.
func readNDJSONNumbers(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	scan := bufio.NewScanner(bytes.NewReader(data))
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		dec := json.NewDecoder(bytes.NewReader(line))
		dec.UseNumber()
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("parse %q: %v", string(line), err)
		}
		out = append(out, m)
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// eventKinds extracts the "kind" field from each parsed ndjson line, in
// order. Used by tests that assert phase sequencing rather than per-field
// details.
func eventKinds(events []map[string]any) []string {
	out := make([]string, 0, len(events))
	for _, ev := range events {
		if k, ok := ev["kind"].(string); ok {
			out = append(out, k)
		}
	}
	return out
}

func TestRunT1Enumerate_HappyPath(t *testing.T) {
	src := t.TempDir()
	files := seedTree(t, src)

	es, eventsPath := makeT1EventStore(t)
	rend := &captureRenderer{}

	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "test", Source: src},
		SourceRoot: src,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT1Enumerate: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T1Result")
	}
	if len(res.Candidates) != len(files) {
		t.Errorf("Candidates len = %d; want %d (%v)", len(res.Candidates), len(files), files)
	}
	// Signatures must have one entry per Candidate with the same Size/MtimeNS.
	if len(res.Signatures) != len(res.Candidates) {
		t.Errorf("Signatures len = %d; want %d", len(res.Signatures), len(res.Candidates))
	}
	for _, c := range res.Candidates {
		sig, ok := res.Signatures[c.RelativePath]
		if !ok {
			t.Errorf("Signatures missing %q", c.RelativePath)
			continue
		}
		if sig.Size != c.Size {
			t.Errorf("Signatures[%q].Size = %d; want %d", c.RelativePath, sig.Size, c.Size)
		}
		if sig.MtimeNS != c.MtimeNS {
			t.Errorf("Signatures[%q].MtimeNS = %d; want %d", c.RelativePath, sig.MtimeNS, c.MtimeNS)
		}
	}

	// Audit log: phase_started, then N file_enumerated (one per Candidate
	// in walk order), then phase_completed.
	events := readNDJSON(t, eventsPath)
	wantLen := 1 + len(files) + 1
	if len(events) != wantLen {
		t.Fatalf("events.ndjson lines = %d; want %d", len(events), wantLen)
	}
	if events[0]["kind"] != "phase_started" || events[0]["phase"] != "T0+" {
		t.Errorf("events[0] = %v; want phase_started/T0+", events[0])
	}
	if events[wantLen-1]["kind"] != "phase_completed" || events[wantLen-1]["phase"] != "T0+" {
		t.Errorf("events[last] = %v; want phase_completed/T0+", events[wantLen-1])
	}
	completedDetails, ok := events[wantLen-1]["details"].(map[string]any)
	if !ok {
		t.Fatalf("phase_completed.details missing: %v", events[wantLen-1])
	}
	if v, ok := completedDetails["duration_ms"].(float64); !ok || v < 0 {
		t.Errorf("phase_completed.details.duration_ms = %v; want non-negative number", completedDetails["duration_ms"])
	}

	// Each interior event is a file_enumerated for a Candidate, in the
	// same order Candidates appear, with path/size/mtime_ns details.
	for i, c := range res.Candidates {
		ev := events[1+i]
		if ev["kind"] != "file_enumerated" {
			t.Errorf("events[%d].kind = %v; want file_enumerated", 1+i, ev["kind"])
		}
		if ev["phase"] != "T0+" {
			t.Errorf("events[%d].phase = %v; want T0+", 1+i, ev["phase"])
		}
		if ev["path"] != c.RelativePath {
			t.Errorf("events[%d].path = %v; want %q", 1+i, ev["path"], c.RelativePath)
		}
		details, ok := ev["details"].(map[string]any)
		if !ok {
			t.Errorf("events[%d].details missing: %v", 1+i, ev)
			continue
		}
		if v, ok := details["size"].(float64); !ok || int64(v) != c.Size {
			t.Errorf("events[%d].details.size = %v; want %d", 1+i, details["size"], c.Size)
		}
		// mtime_ns is a nanosecond-resolution int64 that exceeds 2^53,
		// so the default json -> map[string]any path (which decodes
		// numbers into float64) loses precision in the test parser
		// despite the on-disk bytes being correct. To assert the wire
		// bytes hold the EXACT int64, re-parse the file using
		// json.Decoder.UseNumber via the helper below.
	}
	// Exact int64 round-trip of mtime_ns via UseNumber (the float64
	// path above is the readability convenience, this is the precision
	// assertion).
	exactEvents := readNDJSONNumbers(t, eventsPath)
	for i, c := range res.Candidates {
		ev := exactEvents[1+i]
		details, _ := ev["details"].(map[string]any)
		num, ok := details["mtime_ns"].(json.Number)
		if !ok {
			t.Errorf("events[%d].details.mtime_ns not a json.Number: %T", 1+i, details["mtime_ns"])
			continue
		}
		got, err := num.Int64()
		if err != nil {
			t.Errorf("events[%d].details.mtime_ns Int64(): %v", 1+i, err)
			continue
		}
		if got != c.MtimeNS {
			t.Errorf("events[%d].details.mtime_ns = %d; want %d (exact int64)", 1+i, got, c.MtimeNS)
		}
	}

	// Renderer saw phase_started then phase_completed (status=ok). No
	// per-file UIEvents: T0+ does not emit them in v0.1 (per Task 23
	// scope; T1 has UIEvtProgress).
	uiEvents := rend.seen()
	if len(uiEvents) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(uiEvents))
	}
	if uiEvents[0].Kind != types.UIEvtPhaseStarted || uiEvents[0].Phase != types.PhaseEnumerate {
		t.Errorf("renderer[0] = %+v; want PhaseStarted/T0+", uiEvents[0])
	}
	if uiEvents[1].Kind != types.UIEvtPhaseCompleted || uiEvents[1].Phase != types.PhaseEnumerate {
		t.Errorf("renderer[1] = %+v; want PhaseCompleted/T0+", uiEvents[1])
	}
	if uiEvents[1].Status != "ok" {
		t.Errorf("renderer[1].Status = %q; want ok", uiEvents[1].Status)
	}
}

func TestRunT1Enumerate_IncludesExcludesFilter(t *testing.T) {
	src := t.TempDir()
	// keep: .md and .txt; exclude draft-* and .tmp.
	for _, rel := range []string{"keep.md", "keep.txt", "draft-skip.md", "junk.tmp", "ignore.pdf"} {
		full := filepath.Join(src, rel)
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	es, _ := makeT1EventStore(t)
	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile: profiles.Profile{
			V:        1,
			Name:     "t",
			Source:   src,
			Includes: []string{"*.md", "*.txt"},
			Excludes: []string{"draft-*", "*.tmp"},
		},
		SourceRoot: src,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT1Enumerate: %v", err)
	}

	relPaths := make([]string, 0, len(res.Candidates))
	for _, c := range res.Candidates {
		relPaths = append(relPaths, c.RelativePath)
	}
	wantCands := map[string]struct{}{"keep.md": {}, "keep.txt": {}}
	if len(relPaths) != len(wantCands) {
		t.Errorf("Candidates = %v; want %v", relPaths, wantCands)
	}
	for _, p := range relPaths {
		if _, ok := wantCands[p]; !ok {
			t.Errorf("unexpected candidate %q", p)
		}
	}
	// Excludes hits should be in Skipped (Includes-misses are NOT recorded).
	skippedSet := map[string]struct{}{}
	for _, s := range res.Skipped {
		skippedSet[s] = struct{}{}
	}
	for _, want := range []string{"draft-skip.md", "junk.tmp"} {
		if _, ok := skippedSet[want]; !ok {
			t.Errorf("Skipped missing %q: got %v", want, res.Skipped)
		}
	}
}

func TestRunT1Enumerate_NFCCollisionsSurface(t *testing.T) {
	src := t.TempDir()

	// "café.txt" in NFC vs NFD form. The selection_test.go pattern shows
	// macOS may collapse NFC/NFD twins at write time on some volumes; skip
	// when that happens so we don't false-fail.
	nfcRaw := string([]byte{'c', 'a', 'f', 0xc3, 0xa9, '.', 't', 'x', 't'})
	nfdRaw := string([]byte{'c', 'a', 'f', 'e', 0xcc, 0x81, '.', 't', 'x', 't'})
	if nfcRaw == nfdRaw {
		t.Fatal("test setup bug: NFC and NFD raw bytes identical")
	}

	if err := os.WriteFile(filepath.Join(src, nfcRaw), []byte("nfc"), 0o600); err != nil {
		t.Fatalf("write nfc: %v", err)
	}
	if err := os.WriteFile(filepath.Join(src, nfdRaw), []byte("nfd"), 0o600); err != nil {
		t.Skipf("filesystem collapsed NFC/NFD twins: %v", err)
	}
	entries, err := os.ReadDir(src)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	if len(entries) < 2 {
		t.Skipf("filesystem collapsed twins; got %d entries", len(entries))
	}

	es, _ := makeT1EventStore(t)
	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT1Enumerate: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("expected zero Candidates (both forms collide), got %d", len(res.Candidates))
	}
	if len(res.CollidingPaths) != 2 {
		t.Errorf("expected 2 CollidingPaths, got %d: %v", len(res.CollidingPaths), res.CollidingPaths)
	}
}

func TestRunT1Enumerate_EmptySourceTree(t *testing.T) {
	src := t.TempDir() // exists but empty
	es, eventsPath := makeT1EventStore(t)

	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT1Enumerate: %v", err)
	}
	if len(res.Candidates) != 0 {
		t.Errorf("Candidates = %v; want empty", res.Candidates)
	}

	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started", "phase_completed"}
	if len(kinds) != len(want) || kinds[0] != want[0] || kinds[1] != want[1] {
		t.Errorf("event kinds = %v; want %v (no file_enumerated for an empty tree)", kinds, want)
	}
}

func TestRunT1Enumerate_SourceRootMissing(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "does-not-exist")
	es, eventsPath := makeT1EventStore(t)
	rend := &captureRenderer{}

	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: bogus},
		SourceRoot: bogus,
		EventStore: es,
		UIRenderer: rend,
	})
	if err == nil {
		t.Fatal("expected error on missing SourceRoot")
	}
	if res != nil {
		t.Errorf("expected nil result on Walk failure, got %+v", res)
	}

	events := readNDJSON(t, eventsPath)
	// phase_started + phase_aborted, no phase_completed.
	kinds := eventKinds(events)
	if len(kinds) != 2 || kinds[0] != "phase_started" || kinds[1] != "phase_aborted" {
		t.Fatalf("event kinds = %v; want [phase_started, phase_aborted]", kinds)
	}
	// phase_aborted carries error + duration_ms in details.
	details, ok := events[1]["details"].(map[string]any)
	if !ok {
		t.Fatalf("phase_aborted.details missing: %v", events[1])
	}
	if _, ok := details["error"]; !ok {
		t.Errorf("phase_aborted.details missing error: %v", details)
	}
	if v, ok := details["duration_ms"].(float64); !ok || v < 0 {
		t.Errorf("phase_aborted.details.duration_ms = %v; want non-negative number", details["duration_ms"])
	}

	// Renderer saw phase_started then phase_completed(status=aborted, Err set).
	ui := rend.seen()
	if len(ui) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(ui))
	}
	if ui[1].Kind != types.UIEvtPhaseCompleted {
		t.Errorf("renderer[1].Kind = %v; want PhaseCompleted", ui[1].Kind)
	}
	if ui[1].Status != "aborted" {
		t.Errorf("renderer[1].Status = %q; want aborted", ui[1].Status)
	}
	if ui[1].Err == nil {
		t.Error("renderer[1].Err nil; want the wrapped Walk error")
	}
}

func TestRunT1Enumerate_CancelledContextAtEntry(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src)
	es, eventsPath := makeT1EventStore(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunT1Enumerate(ctx, T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
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
	// No store writes happened (no Append, no Checkpoint).
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty events.ndjson at entry-cancel; got %q", string(data))
	}
}

// midCancelEventStore wraps an EventStore and cancels a tied cancel func
// after a specified number of file_enumerated Appends have landed. Used
// to exercise the cadenced-cancel branch deterministically (without
// relying on race-windows between goroutines).
type midCancelEventStore struct {
	inner       state.EventStore
	cancel      context.CancelFunc
	cancelAfter int // cancel after this many file_enumerated Appends
	fileCount   int
}

func (m *midCancelEventStore) Append(ctx context.Context, ev state.Event) error {
	if err := m.inner.Append(ctx, ev); err != nil {
		return err
	}
	if ev.Kind == "file_enumerated" {
		m.fileCount++
		if m.fileCount == m.cancelAfter {
			m.cancel()
		}
	}
	return nil
}

func (m *midCancelEventStore) Checkpoint(ctx context.Context) error {
	return m.inner.Checkpoint(ctx)
}

func (m *midCancelEventStore) Close() error { return m.inner.Close() }

func TestRunT1Enumerate_CancelledMidEnumeration(t *testing.T) {
	src := t.TempDir()
	// Seed enough files that the cadenced check (every 256) will trigger.
	const nFiles = 512
	for i := 0; i < nFiles; i++ {
		full := filepath.Join(src, fmt.Sprintf("f-%05d.txt", i))
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", full, err)
		}
	}

	es, eventsPath := makeT1EventStore(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mid := &midCancelEventStore{
		inner:       es,
		cancel:      cancel,
		cancelAfter: 100, // cancel after 100 files; cadenced check at 256 catches it
	}

	res, err := RunT1Enumerate(ctx, T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: mid,
	})
	if err == nil {
		t.Fatal("expected cancellation error mid-enumeration")
	}
	if res != nil {
		t.Errorf("expected nil result, got %+v", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}

	// We do NOT assert on the exact number of file_enumerated lines
	// because the cadenced check happens every 256 entries (not every
	// entry), so between 256 and 511 are valid landed counts.
	//
	// We DO assert phase_completed is absent: the phase was not completed.
	//
	// We do NOT assert phase_aborted is present. The runner has two
	// abort-on-cancellation paths:
	//   (a) Cadenced ctx-check at i%256==0 -> runT1Abort -> phase_aborted
	//       emitted under a fresh 5s background ctx.
	//   (b) Next EventStore.Append after cancellation returns ctx.Err
	//       from the NDJSON store's own entry guard -> mid-stream
	//       audit-failure branch, which intentionally SKIPS emitting
	//       phase_aborted (rationale: the audit store just signalled
	//       failure; re-Appending may compound the problem).
	// Which path wins depends on whether cancellation lands inside the
	// cadenced-check window or between two Append calls; both are
	// correct runner behavior. The only invariant the user observes is
	// "the run was not completed" + an error return.
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "phase_completed" {
			t.Error("phase_completed should be absent on mid-enumeration cancellation")
		}
	}
}

func TestRunT1Enumerate_RendererErrorIsNonFatal(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src)
	es, eventsPath := makeT1EventStore(t)
	rend := &captureRenderer{err: errors.New("renderer broken")}

	res, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("PS3: renderer errors must not abort; got %v", err)
	}
	if res == nil {
		t.Fatal("expected T1Result")
	}

	// Audit path still landed phase_started+file_enumerated*N+phase_completed.
	events := readNDJSON(t, eventsPath)
	if len(events) < 2 {
		t.Fatalf("expected at least phase_started+phase_completed in events.ndjson; got %d", len(events))
	}
	if events[0]["kind"] != "phase_started" {
		t.Errorf("events[0].kind = %v; want phase_started", events[0]["kind"])
	}
	if events[len(events)-1]["kind"] != "phase_completed" {
		t.Errorf("events[last].kind = %v; want phase_completed", events[len(events)-1]["kind"])
	}
}

func TestRunT1Enumerate_NilEventStore(t *testing.T) {
	_, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: "/tmp"},
		SourceRoot: "/tmp",
		EventStore: nil,
	})
	if err == nil {
		t.Fatal("expected error on nil EventStore")
	}
	if !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected error to mention EventStore; got %v", err)
	}
}

func TestRunT1Enumerate_AppendPhaseStartedFails(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated store fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}

	_, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when phase_started Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

// nthCallFailingEventStore fails the Nth Append of a given Kind. Used to
// test "fail the 2nd file_enumerated" without making faultingEventStore
// itself stateful (it matches by Kind only, which would fail every
// file_enumerated).
type nthCallFailingEventStore struct {
	inner          state.EventStore
	failKind       string
	failOn         int // 1-indexed: fail the Nth Append matching Kind
	seen           int
	err            error
	checkpointFail bool
}

func (n *nthCallFailingEventStore) Append(ctx context.Context, ev state.Event) error {
	if ev.Kind == n.failKind {
		n.seen++
		if n.seen == n.failOn {
			return n.err
		}
	}
	return n.inner.Append(ctx, ev)
}

func (n *nthCallFailingEventStore) Checkpoint(ctx context.Context) error {
	if n.checkpointFail {
		return n.err
	}
	return n.inner.Checkpoint(ctx)
}

func (n *nthCallFailingEventStore) Close() error { return n.inner.Close() }

func TestRunT1Enumerate_AppendFileEnumeratedFails(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src) // 4 files; we'll fail the 2nd file_enumerated
	innerES, eventsPath := makeT1EventStore(t)
	sentinel := errors.New("simulated mid-stream fault")
	es := &nthCallFailingEventStore{
		inner:    innerES,
		failKind: "file_enumerated",
		failOn:   2,
		err:      sentinel,
	}

	_, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error on 2nd file_enumerated failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// events.ndjson holds phase_started + exactly 1 file_enumerated (the
	// 1st one landed; the 2nd failed before write). NO phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	if len(kinds) != 2 || kinds[0] != "phase_started" || kinds[1] != "file_enumerated" {
		t.Errorf("event kinds = %v; want [phase_started, file_enumerated]", kinds)
	}
	for _, k := range kinds {
		if k == "phase_completed" {
			t.Error("phase_completed must not be emitted when file_enumerated mid-stream fails")
		}
	}
}

func TestRunT1Enumerate_AppendPhaseCompletedFails(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src)
	innerES, eventsPath := makeT1EventStore(t)
	sentinel := errors.New("simulated completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}

	_, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error on phase_completed failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// Orphan-completion: phase_started + 4 file_enumerated landed, NO
	// phase_completed (the truthful audit-trail outcome).
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	for _, k := range kinds {
		if k == "phase_completed" {
			t.Error("phase_completed must be absent when its Append failed")
		}
	}
	if len(events) < 1 || events[0]["kind"] != "phase_started" {
		t.Errorf("expected first event to be phase_started; got %v", events)
	}
}

func TestRunT1Enumerate_CheckpointFails(t *testing.T) {
	src := t.TempDir()
	seedTree(t, src)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated checkpoint fault")
	es := &faultingEventStore{inner: innerES, failCheckpointAll: true, err: sentinel}

	_, err := RunT1Enumerate(context.Background(), T1Input{
		Profile:    profiles.Profile{V: 1, Name: "t", Source: src},
		SourceRoot: src,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when Checkpoint fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}
