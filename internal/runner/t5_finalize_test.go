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
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Helpers reused from sibling test files (same package):
//   - canonicalRunID, captureRenderer, readNDJSON, eventKinds, makeStores,
//     faultingEventStore, faultingRunLogStore.
//
// Task 27 helpers (defined here):
//   - makeT5Stores: opens events.ndjson + runs.ndjson + manifest store with
//     two manifest entries already appended (closest to the real shape T4
//     sees at entry: a populated unfinalized manifest).
//   - readT5ManifestGz: decompresses the finalized .gz manifest and returns
//     ManifestEntry slices for assertion.

// makeT5Stores opens the per-run stores for a T4 finalize test under a fresh
// dot dir. Returns the open EventStore, RunLogStore, ManifestStore, plus
// the on-disk paths so tests can assert events.ndjson contents and the
// finalized manifest.
//
// The ManifestStore comes back with two AppendEntry calls already executed
// (this is the realistic state at T4 entry: T2 has streamed N lines and
// closed nothing yet). The two entries are deterministic so tests can
// re-decompress and parse them.
func makeT5Stores(t *testing.T) (
	es state.EventStore, rls state.RunLogStore, ms state.ManifestStore,
	dotDir, runID, eventsPath, runsPath, manifestBase string,
) {
	t.Helper()
	dotDir = filepath.Join(t.TempDir(), ".flashbackup")
	runID = canonicalRunID
	runDir := filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}

	eventsPath = filepath.Join(runDir, "events.ndjson")
	var err error
	es, err = state.NewNDJSONEventStore(eventsPath)
	if err != nil {
		t.Fatalf("open events store: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	runsPath = filepath.Join(dotDir, "runs.ndjson")
	rls, err = state.NewNDJSONRunLogStore(runsPath)
	if err != nil {
		t.Fatalf("open runlog store: %v", err)
	}
	t.Cleanup(func() { _ = rls.Close() })

	manifestBase = filepath.Join(runDir, "manifest.ndjson")
	ms, err = state.NewNDJSONManifestStore(manifestBase, []byte("t5-test-key"))
	if err != nil {
		t.Fatalf("open manifest store: %v", err)
	}
	// Cleanup-only Gzip: if a test path errors before calling Gzip itself,
	// drain the writer so the temp dir cleanup does not leak open FDs.
	t.Cleanup(func() { _ = ms.Gzip(context.Background()) })

	// Append two entries up-front so the manifest is non-empty (mirrors
	// the realistic T4 entry state where T2 wrote N lines).
	now := time.Now().UTC()
	for i, p := range []string{"a.txt", "b/c.txt"} {
		if err := ms.AppendEntry(context.Background(), state.ManifestEntry{
			V:            1,
			Path:         p,
			Size:         int64(i + 1),
			MtimeNS:      now.UnixNano(),
			SHA256Source: fmt.Sprintf("0000000000000000000000000000000000000000000000000000000000%02x", i),
			CopiedAt:     now,
			Status:       state.StatusVerified,
		}); err != nil {
			t.Fatalf("seed manifest entry %d: %v", i, err)
		}
	}

	return es, rls, ms, dotDir, runID, eventsPath, runsPath, manifestBase
}

// readT5ManifestGz decompresses the finalized .gz manifest at base + ".gz"
// and returns one ManifestEntry per non-empty NDJSON line.
func readT5ManifestGz(t *testing.T, base string) []state.ManifestEntry {
	t.Helper()
	gzPath := base + ".gz"
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

// preCreateRunDir creates an empty run dir under <dotDir>/runs/<name>/.
// Used by retention-pruning tests to plant fake historical runs.
//
// File mode 0o700 mirrors makeStores. Empty file inside (sentinel) so
// RemoveAll has actual content to remove and assertions can distinguish
// pruned from kept (the sentinel goes away with the dir).
func preCreateRunDir(t *testing.T, dotDir, name string) {
	t.Helper()
	p := filepath.Join(dotDir, "runs", name)
	if err := os.MkdirAll(p, 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", p, err)
	}
	if err := os.WriteFile(filepath.Join(p, "sentinel"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write sentinel in %s: %v", p, err)
	}
}

// makeRunIDsBefore returns N RunIDs that lexically sort BEFORE base.
// canonicalRunID = "2026-06-04T0900Z-0001"; we generate older days with
// the same suffix. They sort: oldest first.
func makeRunIDsBefore(n int) []string {
	out := make([]string, n)
	// 2026-06-01..2026-06-03 + earlier hours: keep simple, use day-1..day-n.
	// Format: "2026-05-DDTHHMMZ-0001". 28 days available, generous.
	for i := 0; i < n; i++ {
		day := i + 1
		out[i] = fmt.Sprintf("2026-05-%02dT0900Z-0001", day)
	}
	return out
}

// baseT5Input returns a T5Input populated with realistic happy-path values
// (move mode, exit_status=ok, 2 files succeeded matching the seeded
// manifest). Tests override individual fields as needed.
func baseT5Input(t *testing.T, es state.EventStore, rls state.RunLogStore, ms state.ManifestStore,
	dotDir, runID string, rend types.Renderer) T5Input {
	t.Helper()
	return T5Input{
		RunID:                         runID,
		FlashbackupVersion:            "0.1.0-test",
		StartedAt:                     time.Now().UTC().Add(-5 * time.Minute),
		SourceRoot:                    "/Users/tester/src",
		DestRoot:                      "/Volumes/USB/host-user",
		Mode:                          types.ModeMove,
		ProfileName:                   "docs",
		ExitStatus:                    types.ExitStatusOK,
		DotDir:                        dotDir,
		FilesTotal:                    2,
		FilesSucceeded:                2,
		FilesFailed:                   0,
		BytesTotal:                    1234,
		DeletionsSkippedDueToMutation: 0,
		ManifestStore:                 ms,
		EventStore:                    es,
		RunLogStore:                   rls,
		UIRenderer:                    rend,
	}
}

// ---- 1. Happy path ----------------------------------------------------

func TestRunT5Finalize_HappyPath(t *testing.T) {
	es, rls, ms, dotDir, runID, eventsPath, runsPath, manifestBase := makeT5Stores(t)
	rend := &captureRenderer{}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, rend)

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T5Result")
	}

	// ManifestPath ends in .gz, NOT .tmp.gz.
	if !strings.HasSuffix(res.ManifestPath, "manifest.ndjson.gz") {
		t.Errorf("ManifestPath = %q; want suffix manifest.ndjson.gz", res.ManifestPath)
	}
	if strings.HasSuffix(res.ManifestPath, ".tmp.gz") {
		t.Errorf("ManifestPath = %q; should not be .tmp.gz", res.ManifestPath)
	}
	// .tmp.gz gone; .gz present.
	if _, err := os.Stat(manifestBase + ".tmp.gz"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf(".tmp.gz still exists: stat = %v", err)
	}
	if _, err := os.Stat(manifestBase + ".gz"); err != nil {
		t.Errorf(".gz missing: stat = %v", err)
	}

	// Decompress and confirm the two seeded entries are present.
	entries := readT5ManifestGz(t, manifestBase)
	if len(entries) != 2 {
		t.Fatalf("manifest entries = %d; want 2", len(entries))
	}
	if entries[0].Path != "a.txt" || entries[1].Path != "b/c.txt" {
		t.Errorf("manifest paths = [%q, %q]; want [a.txt, b/c.txt]",
			entries[0].Path, entries[1].Path)
	}

	// Audit log: [phase_started, manifest_finalized, phase_completed, run_finished].
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started", "manifest_finalized", "phase_completed", "run_finished"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("event[%d] = %q; want %q", i, kinds[i], k)
		}
	}
	// All four events have phase "T4".
	for i, ev := range events {
		if ev["phase"] != "T4" {
			t.Errorf("event[%d].phase = %v; want T4", i, ev["phase"])
		}
	}
	// manifest_finalized.Details has tmp_path + final_path.
	mfDetails, _ := events[1]["details"].(map[string]any)
	if _, ok := mfDetails["tmp_path"].(string); !ok {
		t.Errorf("manifest_finalized.details.tmp_path missing or wrong type")
	}
	if _, ok := mfDetails["final_path"].(string); !ok {
		t.Errorf("manifest_finalized.details.final_path missing or wrong type")
	}
	// phase_completed.Details has duration_ms + pruned_count.
	pcDetails, _ := events[2]["details"].(map[string]any)
	if _, ok := pcDetails["duration_ms"].(float64); !ok {
		t.Errorf("phase_completed.details.duration_ms missing or wrong type: %v",
			pcDetails["duration_ms"])
	}
	if pc, ok := pcDetails["pruned_count"].(float64); !ok || int(pc) != 0 {
		t.Errorf("phase_completed.details.pruned_count = %v; want 0", pcDetails["pruned_count"])
	}
	// run_finished.Details.exit_status == in.ExitStatus.
	rfDetails, _ := events[3]["details"].(map[string]any)
	if rfDetails["exit_status"] != types.ExitStatusOK {
		t.Errorf("run_finished.details.exit_status = %v; want %q",
			rfDetails["exit_status"], types.ExitStatusOK)
	}

	// runs.ndjson contains "finished" line.
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 {
		t.Fatalf("runs.ndjson lines = %d; want 1 (only finished; runner.Run owns started)", len(runs))
	}
	if runs[0]["event"] != "finished" {
		t.Errorf("runs[0].event = %v; want finished", runs[0]["event"])
	}
	if runs[0]["run_id"] != runID {
		t.Errorf("runs[0].run_id = %v; want %s", runs[0]["run_id"], runID)
	}
	if runs[0]["exit_status"] != types.ExitStatusOK {
		t.Errorf("runs[0].exit_status = %v; want %q", runs[0]["exit_status"], types.ExitStatusOK)
	}
	if runs[0]["mode"] != "move" {
		t.Errorf("runs[0].mode = %v; want move", runs[0]["mode"])
	}

	// Renderer: PhaseStarted + PhaseCompleted(ok).
	ui := rend.seen()
	if len(ui) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(ui))
	}
	if ui[0].Kind != types.UIEvtPhaseStarted || ui[0].Phase != types.PhaseFinalize {
		t.Errorf("ui[0] = %+v; want PhaseStarted/T4", ui[0])
	}
	if ui[1].Kind != types.UIEvtPhaseCompleted || ui[1].Status != "ok" || ui[1].Phase != types.PhaseFinalize {
		t.Errorf("ui[1] = %+v; want PhaseCompleted/ok/T4", ui[1])
	}

	// PrunedRunIDs: empty (no historical dirs planted).
	if len(res.PrunedRunIDs) != 0 {
		t.Errorf("PrunedRunIDs = %v; want empty", res.PrunedRunIDs)
	}
}

// ---- 2. Retention pruning -- prune the 2 oldest of 12 ----------------

func TestRunT5Finalize_RetentionPrunesOldest(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	// Plant 11 older run dirs PLUS the current RunID dir already exists
	// (makeT5Stores created it). Total = 12. With limit=10, prune 2.
	older := makeRunIDsBefore(11)
	for _, name := range older {
		preCreateRunDir(t, dotDir, name)
	}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 10

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	if len(res.PrunedRunIDs) != 2 {
		t.Fatalf("PrunedRunIDs len = %d (%v); want 2", len(res.PrunedRunIDs), res.PrunedRunIDs)
	}
	// The 2 OLDEST should be pruned; older was generated day 1..11, so
	// 2026-05-01 and 2026-05-02 are the two oldest.
	if res.PrunedRunIDs[0] != older[0] || res.PrunedRunIDs[1] != older[1] {
		t.Errorf("PrunedRunIDs = %v; want [%s, %s]",
			res.PrunedRunIDs, older[0], older[1])
	}
	// Disk: oldest 2 gone, current + 9 newer remain.
	runsDir := filepath.Join(dotDir, "runs")
	for _, name := range older[:2] {
		if _, err := os.Stat(filepath.Join(runsDir, name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("oldest dir %s should be gone; stat = %v", name, err)
		}
	}
	for _, name := range older[2:] {
		if _, err := os.Stat(filepath.Join(runsDir, name)); err != nil {
			t.Errorf("kept dir %s missing: %v", name, err)
		}
	}
	// Current RunID dir present.
	if _, err := os.Stat(filepath.Join(runsDir, runID)); err != nil {
		t.Errorf("current RunID dir missing: %v", err)
	}
}

// ---- 3. Retention does NOT prune the current RunID -------------------
//
// Artificial scenario: current RunID is the oldest chronologically. With
// 11 dirs (current + 10 newer) and limit 10, prune count is 1; that 1
// would lexically be the current RunID, but the rule "never prune
// current" overrides. Result: nothing is pruned (we skip the current
// and we do not prune anything else because the loop only attempts the
// (count - limit) OLDEST entries).
//
// Then locking the contract: with 11 dirs where the current is OLDEST,
// PrunedRunIDs is empty and current dir survives.

func TestRunT5Finalize_RetentionSkipsCurrentRun(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	// Plant 10 dirs that all sort AFTER canonicalRunID. canonicalRunID =
	// "2026-06-04T0900Z-0001"; pick days strictly later in 2026-06.
	var newer []string
	for i := 0; i < 10; i++ {
		newer = append(newer, fmt.Sprintf("2026-06-%02dT0900Z-0001", 10+i))
	}
	for _, name := range newer {
		preCreateRunDir(t, dotDir, name)
	}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 10

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	// PrunedRunIDs is empty: the only candidate to prune (current, oldest)
	// is protected, so we drop the prune for that slot.
	if len(res.PrunedRunIDs) != 0 {
		t.Errorf("PrunedRunIDs = %v; want empty when only candidate is the current run",
			res.PrunedRunIDs)
	}
	// Current RunID dir survives.
	if _, err := os.Stat(filepath.Join(dotDir, "runs", runID)); err != nil {
		t.Errorf("current RunID dir missing: %v", err)
	}
	// All newer dirs survive.
	for _, name := range newer {
		if _, err := os.Stat(filepath.Join(dotDir, "runs", name)); err != nil {
			t.Errorf("newer dir %s missing: %v", name, err)
		}
	}
}

// ---- 4. Manifest Gzip failure ----------------------------------------

// failingGzipManifestStore wraps a real ManifestStore but fails Gzip().
// AppendEntry still goes to the real store so the test can use the
// makeT5Stores seeded entries.
type failingGzipManifestStore struct {
	inner state.ManifestStore
	err   error
}

func (f *failingGzipManifestStore) AppendEntry(ctx context.Context, e state.ManifestEntry) error {
	return f.inner.AppendEntry(ctx, e)
}
func (f *failingGzipManifestStore) Gzip(_ context.Context) error { return f.err }

func TestRunT5Finalize_ManifestGzipFails(t *testing.T) {
	es, rls, inner, dotDir, runID, eventsPath, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated gzip fault")
	ms := &failingGzipManifestStore{inner: inner, err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)

	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error on Gzip failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// run_finished NOT emitted; "finished" line NOT written.
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "run_finished" {
			t.Errorf("run_finished should not appear when Gzip fails")
		}
	}
	if data, statErr := os.ReadFile(runsPath); statErr == nil && len(data) > 0 {
		t.Errorf("runs.ndjson should be empty on Gzip failure; got %q", string(data))
	}
	// phase_aborted should be present (this branch went through t5Abort).
	var sawAborted bool
	for _, ev := range events {
		if ev["kind"] == "phase_aborted" {
			sawAborted = true
		}
	}
	if !sawAborted {
		t.Error("expected phase_aborted on Gzip failure")
	}
}

// ---- 5. AppendFinished failure ---------------------------------------

func TestRunT5Finalize_AppendFinishedFails(t *testing.T) {
	innerES, innerRLS, ms, dotDir, runID, eventsPath, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated runlog fault")
	// Reuse faultingRunLogStore from t0_preflight_test.go. We need it to
	// fail AppendFinished specifically (not AppendStarted, which is
	// what faultingRunLogStore models by default). The store fails
	// AppendStarted via flag; for AppendFinished we need a custom wrapper.

	rls := &failingAppendFinishedStore{inner: innerRLS, err: sentinel}

	in := baseT5Input(t, innerES, rls, ms, dotDir, runID, nil)

	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error on AppendFinished failure")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// run_finished NOT emitted (it would be misleading to mark the run
	// finished in events.ndjson when runs.ndjson cannot record it).
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "run_finished" {
			t.Errorf("run_finished should not appear when AppendFinished fails")
		}
	}
	// runs.ndjson empty.
	if data, statErr := os.ReadFile(runsPath); statErr == nil && len(data) > 0 {
		t.Errorf("runs.ndjson should be empty on AppendFinished failure; got %q", string(data))
	}
}

// failingAppendFinishedStore wraps a real RunLogStore and fails
// AppendFinished. (faultingRunLogStore in sibling test only fails
// AppendStarted, so we need a distinct wrapper.)
type failingAppendFinishedStore struct {
	inner state.RunLogStore
	err   error
}

func (f *failingAppendFinishedStore) AppendStarted(ctx context.Context, s state.StartedRun) error {
	return f.inner.AppendStarted(ctx, s)
}
func (f *failingAppendFinishedStore) AppendFinished(_ context.Context, _ state.FinishedRun) error {
	return f.err
}
func (f *failingAppendFinishedStore) Checkpoint(ctx context.Context) error {
	return f.inner.Checkpoint(ctx)
}
func (f *failingAppendFinishedStore) Close() error { return f.inner.Close() }

// ---- 6. Cancelled context at entry -----------------------------------

func TestRunT5Finalize_CancelledContextAtEntry(t *testing.T) {
	es, rls, ms, dotDir, runID, eventsPath, _, _ := makeT5Stores(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	res, err := RunT5Finalize(ctx, in)
	if err == nil {
		t.Fatal("expected error on entry cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled; got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result; got %+v", res)
	}
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("events.ndjson should be empty on entry-cancel; got %q", string(data))
	}
}

// ---- 7. EventStore Append failures (per kind) ------------------------

func TestRunT5Finalize_AppendPhaseStartedFails(t *testing.T) {
	innerES, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)
	sentinel := errors.New("simulated phase_started fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when phase_started fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT5Finalize_AppendManifestFinalizedFails(t *testing.T) {
	innerES, rls, ms, dotDir, runID, _, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated manifest_finalized fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "manifest_finalized", err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when manifest_finalized fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// Manifest WAS Gzipped (we got past that step) but the audit Append
	// failed, so we did NOT proceed to AppendFinished.
	if data, statErr := os.ReadFile(runsPath); statErr == nil && len(data) > 0 {
		t.Errorf("runs.ndjson should be empty when manifest_finalized Append fails; got %q",
			string(data))
	}
}

func TestRunT5Finalize_AppendPhaseCompletedFails(t *testing.T) {
	innerES, rls, ms, dotDir, runID, _, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated phase_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when phase_completed fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// AppendFinished happens BEFORE phase_completed in the implementation,
	// so the finished line IS on disk despite the audit failure (correct
	// per ordering: the runs.ndjson durability comes before the audit
	// terminal event).
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "finished" {
		t.Errorf("expected finished line on disk despite phase_completed failure; got %v", runs)
	}
}

func TestRunT5Finalize_AppendRunFinishedFails(t *testing.T) {
	innerES, rls, ms, dotDir, runID, eventsPath, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated run_finished fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "run_finished", err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when run_finished fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// AppendFinished + phase_completed both succeeded by this point;
	// only the terminal run_finished failed.
	events := readNDJSON(t, eventsPath)
	var sawCompleted, sawRunFinished bool
	for _, ev := range events {
		switch ev["kind"] {
		case "phase_completed":
			sawCompleted = true
		case "run_finished":
			sawRunFinished = true
		}
	}
	if !sawCompleted {
		t.Error("expected phase_completed before run_finished failure")
	}
	if sawRunFinished {
		t.Error("run_finished should not appear when its own Append fails")
	}
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "finished" {
		t.Errorf("expected finished line on disk; got %v", runs)
	}
}

// ---- 8. Prune-failure non-fatal --------------------------------------

func TestRunT5Finalize_PruneFailureNonFatal(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory-mode semantics not portable to Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	// Plant 12 older dirs (total 13 with current). Limit 10 means 3
	// oldest get pruned (assuming all succeed). We will make ONE of those
	// 3 oldest undeletable (chmod parent runs/ readonly briefly? no --
	// that would block ALL prunes. Instead chmod the OLDEST candidate's
	// CONTENT so RemoveAll fails on that subtree but the others succeed.
	older := makeRunIDsBefore(12)
	for _, name := range older {
		preCreateRunDir(t, dotDir, name)
	}

	// Make older[1] (the 2nd-oldest, which WILL be in the prune set)
	// undeletable: create a nested dir inside with 0o000 perms on the
	// container so RemoveAll cannot recurse into it.
	stubbornDir := filepath.Join(dotDir, "runs", older[1], "stubborn")
	if err := os.MkdirAll(stubbornDir, 0o700); err != nil {
		t.Fatalf("mkdir stubborn: %v", err)
	}
	innerLocked := filepath.Join(stubbornDir, "innerlocked")
	if err := os.MkdirAll(innerLocked, 0o700); err != nil {
		t.Fatalf("mkdir innerlocked: %v", err)
	}
	if err := os.WriteFile(filepath.Join(innerLocked, "file"), []byte("x"), 0o600); err != nil {
		t.Fatalf("write inner file: %v", err)
	}
	// Chmod the OUTER stubborn dir to 0o500 so RemoveAll cannot enter
	// to remove inner contents. Restore on cleanup so t.TempDir's
	// teardown can recurse.
	if err := os.Chmod(stubbornDir, 0o500); err != nil {
		t.Fatalf("chmod stubborn: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(stubbornDir, 0o700) })

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 10

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v (prune failures should be non-fatal)", err)
	}
	// Expected prune set is older[0..2] (3 oldest). older[1] is
	// undeletable, so PrunedRunIDs has the other 2.
	if len(res.PrunedRunIDs) != 2 {
		t.Errorf("PrunedRunIDs len = %d (%v); want 2 (3 attempted minus 1 stubborn)",
			len(res.PrunedRunIDs), res.PrunedRunIDs)
	}
	// older[1] should NOT be in PrunedRunIDs.
	for _, p := range res.PrunedRunIDs {
		if p == older[1] {
			t.Errorf("stubborn dir %s should not be in PrunedRunIDs", older[1])
		}
	}
	// older[1] should still be on disk.
	if _, err := os.Stat(filepath.Join(dotDir, "runs", older[1])); err != nil {
		t.Errorf("stubborn dir should remain on disk; stat = %v", err)
	}
}

// ---- 9. Renderer error non-fatal (PS3) -------------------------------

func TestRunT5Finalize_RendererErrorIsNonFatal(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)
	rend := &captureRenderer{err: errors.New("renderer broken")}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, rend)
	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("PS3: renderer errors must not abort; got %v", err)
	}
	if res == nil {
		t.Fatal("expected T5Result")
	}
	// Renderer was called for PhaseStarted + PhaseCompleted despite the
	// error on each.
	ui := rend.seen()
	if len(ui) != 2 {
		t.Errorf("renderer should be called 2 times despite errors; got %d", len(ui))
	}
}

// ---- 10. RetentionLimit=0 defaults to 10 -----------------------------

func TestRunT5Finalize_RetentionLimitZeroDefaults(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	older := makeRunIDsBefore(11)
	for _, name := range older {
		preCreateRunDir(t, dotDir, name)
	}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 0 // explicit zero; should use DefaultRetentionLimit (10)

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	if len(res.PrunedRunIDs) != 2 {
		t.Errorf("PrunedRunIDs len = %d (%v); want 2 (12 - default 10)",
			len(res.PrunedRunIDs), res.PrunedRunIDs)
	}
}

// ---- 11. RetentionLimit=1 keeps only current -------------------------

func TestRunT5Finalize_RetentionLimitOneKeepsOnlyCurrent(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	older := makeRunIDsBefore(2)
	for _, name := range older {
		preCreateRunDir(t, dotDir, name)
	}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 1

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	if len(res.PrunedRunIDs) != 2 {
		t.Errorf("PrunedRunIDs len = %d (%v); want 2 (kept just current)",
			len(res.PrunedRunIDs), res.PrunedRunIDs)
	}
	// Current dir still exists.
	if _, err := os.Stat(filepath.Join(dotDir, "runs", runID)); err != nil {
		t.Errorf("current RunID dir missing: %v", err)
	}
	// Older dirs gone.
	for _, name := range older {
		if _, err := os.Stat(filepath.Join(dotDir, "runs", name)); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("older dir %s should be gone; stat = %v", name, err)
		}
	}
}

// ---- 12. Nil-store guards --------------------------------------------

func TestRunT5Finalize_NilEventStore(t *testing.T) {
	_, err := RunT5Finalize(context.Background(), T5Input{
		EventStore:    nil,
		ManifestStore: &noopManifestStore{},
		RunLogStore:   &noopRunLogStore{},
	})
	if err == nil || !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected EventStore nil error; got %v", err)
	}
}

func TestRunT5Finalize_NilManifestStore(t *testing.T) {
	es, rls, _, dotDir, runID, _, _, _ := makeT5Stores(t)
	in := baseT5Input(t, es, rls, nil, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "ManifestStore") {
		t.Errorf("expected ManifestStore nil error; got %v", err)
	}
}

func TestRunT5Finalize_NilRunLogStore(t *testing.T) {
	es, _, ms, dotDir, runID, _, _, _ := makeT5Stores(t)
	in := baseT5Input(t, es, nil, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "RunLogStore") {
		t.Errorf("expected RunLogStore nil error; got %v", err)
	}
}

// noopManifestStore / noopRunLogStore satisfy the interfaces just enough
// to hit the EventStore-nil guard path in the test above (nil-EventStore
// short-circuits before any of these are called).
type noopManifestStore struct{}

func (n *noopManifestStore) AppendEntry(context.Context, state.ManifestEntry) error { return nil }
func (n *noopManifestStore) Gzip(context.Context) error                             { return nil }

type noopRunLogStore struct{}

func (n *noopRunLogStore) AppendStarted(context.Context, state.StartedRun) error   { return nil }
func (n *noopRunLogStore) AppendFinished(context.Context, state.FinishedRun) error { return nil }
func (n *noopRunLogStore) Checkpoint(context.Context) error                        { return nil }
func (n *noopRunLogStore) Close() error                                            { return nil }

// ---- 13. EventStore Checkpoint failure at phase end ------------------

func TestRunT5Finalize_FinalCheckpointFails(t *testing.T) {
	innerES, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)
	sentinel := errors.New("simulated checkpoint fault")
	es := &faultingEventStore{inner: innerES, failCheckpointAll: true, err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when final events Checkpoint fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

// ---- 14. Pruning ignores non-canonical names + non-dirs ---------------
//
// Defensive contract: an arbitrary file under <DotDir>/runs/ (or a dir
// whose name does not match the RunID regex) must NEVER be removed by
// the prune pass. Tests with operator-planted notes.txt + a random
// uuid-named dir; both survive.

func TestRunT5Finalize_PruneIgnoresNonRunIDEntries(t *testing.T) {
	es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)

	// Plant 11 valid OLDER run dirs so retention pruning kicks in.
	older := makeRunIDsBefore(11)
	for _, name := range older {
		preCreateRunDir(t, dotDir, name)
	}
	// Plant junk: a file directly in runs/ and a non-canonical dir.
	junkFile := filepath.Join(dotDir, "runs", "notes.txt")
	if err := os.WriteFile(junkFile, []byte("operator notes"), 0o600); err != nil {
		t.Fatalf("write junk file: %v", err)
	}
	junkDir := filepath.Join(dotDir, "runs", "scratch-dir")
	if err := os.MkdirAll(junkDir, 0o700); err != nil {
		t.Fatalf("mkdir junk dir: %v", err)
	}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.RetentionLimit = 10

	res, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	if len(res.PrunedRunIDs) != 2 {
		t.Errorf("PrunedRunIDs len = %d (%v); want 2 (only RunID-named dirs counted)",
			len(res.PrunedRunIDs), res.PrunedRunIDs)
	}
	if _, err := os.Stat(junkFile); err != nil {
		t.Errorf("junk file should survive prune; stat = %v", err)
	}
	if _, err := os.Stat(junkDir); err != nil {
		t.Errorf("junk dir should survive prune; stat = %v", err)
	}
}

// ---- 15. Concurrency sanity: T5 is safe under concurrent EventStore use
//
// Not a strict requirement of the task; included to lock the contract
// that RunT5Finalize uses EventStore.Append + Checkpoint sequentially
// from a single goroutine. The state.EventStore is safe for concurrent
// callers; we just confirm RunT5Finalize itself does not introduce a
// hidden goroutine that could write events after return.

func TestRunT5Finalize_NoLingeringGoroutineAfterReturn(t *testing.T) {
	es, rls, ms, dotDir, runID, eventsPath, _, _ := makeT5Stores(t)
	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)

	_, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	// Snapshot the events.ndjson size; wait briefly; confirm no new
	// writes appeared (no lingering goroutine).
	st1, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat events: %v", err)
	}
	time.Sleep(50 * time.Millisecond)
	st2, err := os.Stat(eventsPath)
	if err != nil {
		t.Fatalf("stat events 2: %v", err)
	}
	if st1.Size() != st2.Size() {
		t.Errorf("events.ndjson grew after RunT5Finalize returned: %d -> %d",
			st1.Size(), st2.Size())
	}
}

// ---- 16. Mode=Copy still finalizes correctly --------------------------
//
// Copy mode shouldn't change T4's behavior (no T3 deletes happened, but
// the run still finishes normally). Lock that the "finished" line
// records Mode="copy" and ExitStatus="ok".

func TestRunT5Finalize_CopyModeFinishes(t *testing.T) {
	es, rls, ms, dotDir, runID, _, runsPath, _ := makeT5Stores(t)
	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	in.Mode = types.ModeCopy
	in.DeletionsSkippedDueToMutation = 0

	_, err := RunT5Finalize(context.Background(), in)
	if err != nil {
		t.Fatalf("RunT5Finalize: %v", err)
	}
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "finished" {
		t.Fatalf("expected finished line; got %v", runs)
	}
	if runs[0]["mode"] != "copy" {
		t.Errorf("runs[0].mode = %v; want copy", runs[0]["mode"])
	}
}

// ---- 17. ExitStatus propagates through to events + runs.ndjson --------

func TestRunT5Finalize_ExitStatusPropagates(t *testing.T) {
	cases := []string{
		types.ExitStatusOK,
		types.ExitStatusPartial,
		types.ExitStatusCopyOnlyAbortedDelete,
	}
	for _, want := range cases {
		want := want
		t.Run(want, func(t *testing.T) {
			es, rls, ms, dotDir, runID, eventsPath, runsPath, _ := makeT5Stores(t)
			in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
			in.ExitStatus = want

			if _, err := RunT5Finalize(context.Background(), in); err != nil {
				t.Fatalf("RunT5Finalize: %v", err)
			}
			// runs.ndjson exit_status.
			runs := readNDJSON(t, runsPath)
			if len(runs) != 1 || runs[0]["exit_status"] != want {
				t.Errorf("runs[0].exit_status = %v; want %q",
					runs[0]["exit_status"], want)
			}
			// run_finished.Details.exit_status.
			events := readNDJSON(t, eventsPath)
			for _, ev := range events {
				if ev["kind"] == "run_finished" {
					d, _ := ev["details"].(map[string]any)
					if d["exit_status"] != want {
						t.Errorf("run_finished.exit_status = %v; want %q",
							d["exit_status"], want)
					}
				}
			}
		})
	}
}

// ---- 18. RunLog Checkpoint failure ------------------------------------

func TestRunT5Finalize_RunLogCheckpointFails(t *testing.T) {
	es, innerRLS, ms, dotDir, runID, eventsPath, runsPath, _ := makeT5Stores(t)
	sentinel := errors.New("simulated runlog checkpoint fault")
	rls := &faultingRunLogStore{inner: innerRLS, failCheckpointAll: true, err: sentinel}

	in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
	_, err := RunT5Finalize(context.Background(), in)
	if err == nil {
		t.Fatal("expected error when runlog Checkpoint fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// AppendFinished landed (in the page cache via faultingRunLogStore.inner)
	// but checkpoint fsync failed; the test asserts the wrapped error is
	// surfaced. The on-disk runs.ndjson contains the line via inner
	// AppendFinished (no Checkpoint required to write to page cache).
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "finished" {
		t.Errorf("expected finished line in page cache; got %v", runs)
	}
	// run_finished NOT emitted (we aborted before that step).
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "run_finished" {
			t.Errorf("run_finished should not appear when runlog Checkpoint fails")
		}
	}
	// phase_aborted should be present.
	var sawAborted bool
	for _, ev := range events {
		if ev["kind"] == "phase_aborted" {
			sawAborted = true
		}
	}
	if !sawAborted {
		t.Error("expected phase_aborted on runlog Checkpoint failure")
	}
}

// ---- 19. Race test: ensure no concurrent state mutation in RunT5Finalize
//
// RunT5Finalize must not race against concurrent EventStore callers in
// the wider runner package. The state.EventStore IS concurrent-safe, but
// finalize's internal logic must not introduce data races. Run under -race
// implicitly via the test target; this test just exercises the path twice
// concurrently with two independent stores to confirm no shared state.

func TestRunT5Finalize_NoDataRaceAcrossInstances(t *testing.T) {
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			es, rls, ms, dotDir, runID, _, _, _ := makeT5Stores(t)
			in := baseT5Input(t, es, rls, ms, dotDir, runID, nil)
			if _, err := RunT5Finalize(context.Background(), in); err != nil {
				t.Errorf("concurrent finalize: %v", err)
			}
		}()
	}
	wg.Wait()
}
