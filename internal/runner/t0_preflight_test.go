package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// Shared mount + skip helpers live in internal/testutil. The runner-package
// specifics (makeStores, captureRenderer, canonicalRunID, seed* tree helpers)
// stay local because they are runner-specific scaffolding.

// setupDest mounts a fresh APFS DMG and seeds it with a valid version.json
// so the preflight FAIL-CLOSED gate 8 passes.
func setupDest(t *testing.T) string {
	t.Helper()
	dest := testutil.MountTempVolume(t, "APFS")
	dotDir := filepath.Join(dest, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	versionPath := filepath.Join(dotDir, "version.json")
	if _, err := state.InitVersionFile(versionPath, "test-version", false); err != nil {
		t.Fatalf("InitVersionFile: %v", err)
	}
	return dest
}

// makeStores opens the per-run events.ndjson + runs.ndjson under the
// canonical paths for a given DotDir + RunID. Mirrors what Task 29 will do.
// Returns both stores plus the paths so tests can assert on the on-disk
// contents after the phase returns.
func makeStores(t *testing.T, dotDir, runID string) (state.EventStore, state.RunLogStore, string, string) {
	t.Helper()
	runDir := filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	eventsPath := filepath.Join(runDir, "events.ndjson")
	runsPath := filepath.Join(dotDir, "runs.ndjson")

	es, err := state.NewNDJSONEventStore(eventsPath)
	if err != nil {
		t.Fatalf("open events store: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	rls, err := state.NewNDJSONRunLogStore(runsPath)
	if err != nil {
		t.Fatalf("open runlog store: %v", err)
	}
	t.Cleanup(func() { _ = rls.Close() })

	return es, rls, eventsPath, runsPath
}

// readNDJSON returns the parsed JSON objects from one NDJSON file.
// Skips blank lines, fails the test on parse error.
func readNDJSON(t *testing.T, path string) []map[string]any {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	var out []map[string]any
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 64*1024), 1<<20)
	for scan.Scan() {
		line := scan.Bytes()
		if len(line) == 0 {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal(line, &m); err != nil {
			t.Fatalf("parse line %q: %v", string(line), err)
		}
		out = append(out, m)
	}
	if err := scan.Err(); err != nil {
		t.Fatalf("scan %s: %v", path, err)
	}
	return out
}

// captureRenderer records every UIEvent it sees. Goroutine-safe so tests
// that fan events across goroutines do not race the slice.
type captureRenderer struct {
	mu     sync.Mutex
	events []types.UIEvent
	err    error // if non-nil, OnEvent returns this (used to exercise PS3)
}

func (c *captureRenderer) OnEvent(_ context.Context, ev types.UIEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.events = append(c.events, ev)
	return c.err
}

func (c *captureRenderer) seen() []types.UIEvent {
	c.mu.Lock()
	defer c.mu.Unlock()
	cp := make([]types.UIEvent, len(c.events))
	copy(cp, c.events)
	return cp
}

// canonicalRunID is the test RunID. Format mirrors the spec
// (UTC-RFC3339-no-colons + 4-hex suffix) but content is arbitrary; the
// runner does not parse it.
const canonicalRunID = "2026-06-04T0900Z-0001"

func TestRunT0Preflight_HappyPath(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	es, rls, eventsPath, runsPath := makeStores(t, dotDir, canonicalRunID)

	rend := &captureRenderer{}
	ctx := context.Background()

	res, err := RunT0Preflight(ctx, T0Input{
		RunID:              canonicalRunID,
		FlashbackupVersion: "0.1.0-test",
		DestRoot:           dest,
		SourceRoot:         "/Users/tester/Documents",
		Mode:               types.ModeCopy,
		ProfileName:        "my-docs",
		SkipCodesign:       true,
		EventStore:         es,
		RunLogStore:        rls,
		UIRenderer:         rend,
	})
	if err != nil {
		t.Fatalf("RunT0Preflight: %v", err)
	}
	if res == nil || res.PreflightContext == nil {
		t.Fatal("expected non-nil PreflightContext")
	}
	t.Cleanup(func() { _ = res.PreflightContext.Release() })

	// Audit log: two lines, phase_started then phase_completed.
	events := readNDJSON(t, eventsPath)
	if len(events) != 2 {
		t.Fatalf("events.ndjson lines = %d; want 2", len(events))
	}
	if events[0]["kind"] != "phase_started" {
		t.Errorf("event[0].kind = %v; want phase_started", events[0]["kind"])
	}
	if events[0]["phase"] != "T0" {
		t.Errorf("event[0].phase = %v; want T0", events[0]["phase"])
	}
	if events[1]["kind"] != "phase_completed" {
		t.Errorf("event[1].kind = %v; want phase_completed", events[1]["kind"])
	}
	if events[1]["phase"] != "T0" {
		t.Errorf("event[1].phase = %v; want T0", events[1]["phase"])
	}
	details, ok := events[1]["details"].(map[string]any)
	if !ok {
		t.Fatalf("event[1].details missing or wrong type: %v", events[1]["details"])
	}
	if v, ok := details["duration_ms"].(float64); !ok {
		t.Errorf("phase_completed.details missing or non-numeric duration_ms: %v", details)
	} else if v < 0 {
		t.Errorf("phase_completed.details duration_ms negative: %v", v)
	}

	// Run log: exactly one "started" line with the right fields.
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 {
		t.Fatalf("runs.ndjson lines = %d; want 1", len(runs))
	}
	r := runs[0]
	if r["event"] != "started" {
		t.Errorf("runs[0].event = %v; want started", r["event"])
	}
	if r["run_id"] != canonicalRunID {
		t.Errorf("runs[0].run_id = %v; want %s", r["run_id"], canonicalRunID)
	}
	if r["mode"] != "copy" {
		t.Errorf("runs[0].mode = %v; want copy", r["mode"])
	}
	if r["source_root"] != "/Users/tester/Documents" {
		t.Errorf("runs[0].source_root = %v", r["source_root"])
	}
	// DestRoot is absolutized inside Preflight; for a /Volumes/<X> mount on
	// macOS the absolute form is identical. Match by suffix to stay robust
	// against any future symlink resolution.
	if dr, _ := r["dest_root"].(string); !strings.HasPrefix(dr, "/Volumes/") {
		t.Errorf("runs[0].dest_root = %v; want /Volumes/-prefixed path", r["dest_root"])
	}
	if r["profile"] != "my-docs" {
		t.Errorf("runs[0].profile = %v; want my-docs", r["profile"])
	}
	if r["flashbackup_version"] != "0.1.0-test" {
		t.Errorf("runs[0].flashbackup_version = %v", r["flashbackup_version"])
	}

	// Renderer saw phase_started then phase_completed (status=ok).
	uiEvents := rend.seen()
	if len(uiEvents) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(uiEvents))
	}
	if uiEvents[0].Kind != types.UIEvtPhaseStarted || uiEvents[0].Phase != types.PhasePreflight {
		t.Errorf("renderer[0] = %+v; want PhaseStarted/T0", uiEvents[0])
	}
	if uiEvents[1].Kind != types.UIEvtPhaseCompleted || uiEvents[1].Phase != types.PhasePreflight {
		t.Errorf("renderer[1] = %+v; want PhaseCompleted/T0", uiEvents[1])
	}
	if uiEvents[1].Status != "ok" {
		t.Errorf("renderer[1].Status = %q; want ok", uiEvents[1].Status)
	}
}

func TestRunT0Preflight_NilUIRendererIsValid(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	es, rls, _, _ := makeStores(t, dotDir, canonicalRunID)

	res, err := RunT0Preflight(context.Background(), T0Input{
		RunID:              canonicalRunID,
		FlashbackupVersion: "0.1.0-test",
		DestRoot:           dest,
		SourceRoot:         "/tmp",
		Mode:               types.ModeCopy,
		SkipCodesign:       true,
		EventStore:         es,
		RunLogStore:        rls,
		// UIRenderer intentionally nil
	})
	if err != nil {
		t.Fatalf("RunT0Preflight with nil renderer: %v", err)
	}
	if res == nil || res.PreflightContext == nil {
		t.Fatal("expected non-nil PreflightContext")
	}
	t.Cleanup(func() { _ = res.PreflightContext.Release() })
}

func TestRunT0Preflight_RendererErrorIsNonFatal(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	es, rls, eventsPath, runsPath := makeStores(t, dotDir, canonicalRunID)

	rend := &captureRenderer{err: errors.New("renderer is broken")}
	res, err := RunT0Preflight(context.Background(), T0Input{
		RunID:              canonicalRunID,
		FlashbackupVersion: "0.1.0-test",
		DestRoot:           dest,
		SourceRoot:         "/tmp",
		Mode:               types.ModeCopy,
		SkipCodesign:       true,
		EventStore:         es,
		RunLogStore:        rls,
		UIRenderer:         rend,
	})
	// PS3: renderer errors must not abort the run.
	if err != nil {
		t.Fatalf("expected success despite renderer error; got %v", err)
	}
	if res == nil || res.PreflightContext == nil {
		t.Fatal("expected PreflightContext")
	}
	t.Cleanup(func() { _ = res.PreflightContext.Release() })

	// The started line still made it to disk (we did not abort).
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "started" {
		t.Errorf("expected one started line on disk, got %v", runs)
	}

	// And events.ndjson got both phase_started and phase_completed: the
	// renderer error didn't poison the audit path.
	events := readNDJSON(t, eventsPath)
	kinds := []string{}
	for _, ev := range events {
		if k, ok := ev["kind"].(string); ok {
			kinds = append(kinds, k)
		}
	}
	if len(kinds) != 2 || kinds[0] != "phase_started" || kinds[1] != "phase_completed" {
		t.Errorf("expected [phase_started, phase_completed] in events.ndjson; got %v", kinds)
	}
}

func TestRunT0Preflight_EmptyDestRoot_AbortsBeforeStarted(t *testing.T) {
	// No volume needed: preflight aborts at gate 2 (empty DestRoot).
	tmp := t.TempDir()
	dotDir := filepath.Join(tmp, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	es, rls, eventsPath, runsPath := makeStores(t, dotDir, canonicalRunID)

	rend := &captureRenderer{}
	res, err := RunT0Preflight(context.Background(), T0Input{
		RunID:              canonicalRunID,
		FlashbackupVersion: "0.1.0-test",
		DestRoot:           "", // forces preflight gate 2 fail
		SourceRoot:         "/tmp",
		Mode:               types.ModeCopy,
		SkipCodesign:       true,
		EventStore:         es,
		RunLogStore:        rls,
		UIRenderer:         rend,
	})
	if err == nil {
		t.Fatal("expected error from preflight on empty DestRoot")
	}
	if res != nil {
		t.Errorf("expected nil result on preflight failure, got %+v", res)
	}

	// Audit log must show phase_started THEN phase_aborted, both at T0.
	events := readNDJSON(t, eventsPath)
	if len(events) != 2 {
		t.Fatalf("events.ndjson lines = %d; want 2 (started+aborted)", len(events))
	}
	if events[0]["kind"] != "phase_started" {
		t.Errorf("event[0].kind = %v; want phase_started", events[0]["kind"])
	}
	if events[1]["kind"] != "phase_aborted" {
		t.Errorf("event[1].kind = %v; want phase_aborted", events[1]["kind"])
	}
	details, ok := events[1]["details"].(map[string]any)
	if !ok {
		t.Fatalf("phase_aborted.details missing: %v", events[1])
	}
	if v, ok := details["duration_ms"].(float64); !ok {
		t.Errorf("phase_aborted.details missing or non-numeric duration_ms: %v", details)
	} else if v < 0 {
		t.Errorf("phase_aborted.details duration_ms negative: %v", v)
	}
	if _, ok := details["error"]; !ok {
		t.Errorf("phase_aborted.details missing error: %v", details)
	}

	// Runs.ndjson must NOT have a started line (invariant #10: a run that
	// never started should leave no started line, so history reads cleanly).
	if _, statErr := os.Stat(runsPath); statErr == nil {
		runs := readNDJSON(t, runsPath)
		if len(runs) != 0 {
			t.Errorf("runs.ndjson should be empty on preflight abort; got %d lines: %v", len(runs), runs)
		}
	}

	// Renderer saw phase_started then phase_completed with Status=aborted.
	uiEvents := rend.seen()
	if len(uiEvents) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(uiEvents))
	}
	if uiEvents[1].Kind != types.UIEvtPhaseCompleted {
		t.Errorf("renderer[1].Kind = %v; want PhaseCompleted", uiEvents[1].Kind)
	}
	if uiEvents[1].Status != "aborted" {
		t.Errorf("renderer[1].Status = %q; want aborted", uiEvents[1].Status)
	}
	if uiEvents[1].Err == nil {
		t.Error("renderer[1].Err nil; want the preflight error")
	}
}

func TestRunT0Preflight_CancelledContextAtEntry(t *testing.T) {
	tmp := t.TempDir()
	dotDir := filepath.Join(tmp, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	es, rls, eventsPath, runsPath := makeStores(t, dotDir, canonicalRunID)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunT0Preflight(ctx, T0Input{
		RunID:              canonicalRunID,
		FlashbackupVersion: "0.1.0-test",
		DestRoot:           "/tmp",
		SourceRoot:         "/tmp",
		Mode:               types.ModeCopy,
		SkipCodesign:       true,
		EventStore:         es,
		RunLogStore:        rls,
	})
	if err == nil {
		t.Fatal("expected cancelled-context error")
	}
	if res != nil {
		t.Errorf("expected nil result, got %+v", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}

	// No store writes happened.
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty events.ndjson on entry-cancellation; got %q", string(data))
	}
	if data, statErr := os.ReadFile(runsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty runs.ndjson on entry-cancellation; got %q", string(data))
	}
}

func TestRunT0Preflight_NilEventStore(t *testing.T) {
	// Plumbing precondition: a nil EventStore is a programmer error, not a
	// runtime condition; we want a loud failure, not a panic.
	rls, err := state.NewNDJSONRunLogStore(filepath.Join(t.TempDir(), "runs.ndjson"))
	if err != nil {
		t.Fatalf("open runlog: %v", err)
	}
	t.Cleanup(func() { _ = rls.Close() })

	_, err = RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   nil,
		RunLogStore:  rls,
	})
	if err == nil {
		t.Fatal("expected error on nil EventStore")
	}
	if !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected error to mention EventStore; got %v", err)
	}
}

func TestRunT0Preflight_NilRunLogStore(t *testing.T) {
	es, err := state.NewNDJSONEventStore(filepath.Join(t.TempDir(), "events.ndjson"))
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	_, err = RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   es,
		RunLogStore:  nil,
	})
	if err == nil {
		t.Fatal("expected error on nil RunLogStore")
	}
	if !strings.Contains(err.Error(), "RunLogStore") {
		t.Errorf("expected error to mention RunLogStore; got %v", err)
	}
}

// faultingEventStore wraps a real EventStore and fails specific calls. The
// matcher is by Event.Kind for Append (so we can fail just phase_started or
// just phase_completed) and by a flag for Checkpoint.
type faultingEventStore struct {
	inner             state.EventStore
	failAppendKind    string // if non-empty, Append for matching Kind returns err
	failCheckpointAll bool
	err               error
}

func (f *faultingEventStore) Append(ctx context.Context, ev state.Event) error {
	if f.failAppendKind != "" && ev.Kind == f.failAppendKind {
		return f.err
	}
	return f.inner.Append(ctx, ev)
}

func (f *faultingEventStore) Checkpoint(ctx context.Context) error {
	if f.failCheckpointAll {
		return f.err
	}
	return f.inner.Checkpoint(ctx)
}

func (f *faultingEventStore) Close() error { return f.inner.Close() }

// faultingRunLogStore wraps a real RunLogStore and fails on demand.
type faultingRunLogStore struct {
	inner             state.RunLogStore
	failAppendStarted bool
	failCheckpointAll bool
	err               error
}

func (f *faultingRunLogStore) AppendStarted(ctx context.Context, s state.StartedRun) error {
	if f.failAppendStarted {
		return f.err
	}
	return f.inner.AppendStarted(ctx, s)
}

func (f *faultingRunLogStore) AppendFinished(ctx context.Context, fin state.FinishedRun) error {
	return f.inner.AppendFinished(ctx, fin)
}

func (f *faultingRunLogStore) Checkpoint(ctx context.Context) error {
	if f.failCheckpointAll {
		return f.err
	}
	return f.inner.Checkpoint(ctx)
}

func (f *faultingRunLogStore) Close() error { return f.inner.Close() }

// TestRunT0Preflight_AppendPhaseStartedFails covers the first Append-error
// branch (audit-write fail before Preflight runs).
func TestRunT0Preflight_AppendPhaseStartedFails(t *testing.T) {
	tmp := t.TempDir()
	dotDir := filepath.Join(tmp, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	innerES, innerRLS, _, _ := makeStores(t, dotDir, canonicalRunID)
	sentinel := errors.New("simulated store fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}

	_, err := RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     "/tmp",
		SourceRoot:   "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   es,
		RunLogStore:  innerRLS,
	})
	if err == nil {
		t.Fatal("expected error when phase_started Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

// TestRunT0Preflight_AppendStartedFails covers the post-Preflight failure
// branch where the RunLogStore.AppendStarted call fails: pc must be
// released (lock returned), no started line written.
func TestRunT0Preflight_AppendStartedFails(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	innerES, innerRLS, _, runsPath := makeStores(t, dotDir, canonicalRunID)
	sentinel := errors.New("simulated runlog fault")
	rls := &faultingRunLogStore{inner: innerRLS, failAppendStarted: true, err: sentinel}

	_, err := RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     dest,
		SourceRoot:   "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   innerES,
		RunLogStore:  rls,
	})
	if err == nil {
		t.Fatal("expected error when AppendStarted fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// Lock must have been released by the runner (we never got a
	// PreflightContext back, so the runner is responsible for cleanup).
	lockPath := filepath.Join(dotDir, "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock should be released on AppendStarted failure; stat err=%v", statErr)
	}

	// No "started" line on disk.
	if data, statErr := os.ReadFile(runsPath); statErr == nil && len(data) > 0 {
		t.Errorf("runs.ndjson should be empty after AppendStarted failure; got %q", string(data))
	}
}

// TestRunT0Preflight_AppendPhaseCompletedFails covers the branch where
// phase_completed Append fails after AppendStarted succeeded.
func TestRunT0Preflight_AppendPhaseCompletedFails(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	innerES, innerRLS, _, runsPath := makeStores(t, dotDir, canonicalRunID)
	sentinel := errors.New("simulated events fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}

	_, err := RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     dest,
		SourceRoot:   "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   es,
		RunLogStore:  innerRLS,
	})
	if err == nil {
		t.Fatal("expected error when phase_completed Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// The "started" line WAS written before phase_completed failed; the run
	// is now orphan-finalizable (invariant #10 two-line model: missing
	// "finished" line marks this as crashed_resumed on next preflight).
	// Asserting this here catches a future refactor that accidentally
	// shifts AppendStarted to AFTER phase_completed Append.
	runs := readNDJSON(t, runsPath)
	if len(runs) != 1 || runs[0]["event"] != "started" {
		t.Errorf("expected orphaned started line after phase_completed failure; got %v", runs)
	}

	// Lock must have been released (forward-progress did not happen).
	lockPath := filepath.Join(dotDir, "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock should be released after phase_completed failure; stat err=%v", statErr)
	}
}

// TestRunT0Preflight_CheckpointEventStoreFails covers the branch where
// the events.ndjson Checkpoint at phase end fails.
func TestRunT0Preflight_CheckpointEventStoreFails(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	innerES, innerRLS, _, _ := makeStores(t, dotDir, canonicalRunID)
	sentinel := errors.New("simulated checkpoint fault")
	es := &faultingEventStore{inner: innerES, failCheckpointAll: true, err: sentinel}

	_, err := RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     dest,
		SourceRoot:   "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   es,
		RunLogStore:  innerRLS,
	})
	if err == nil {
		t.Fatal("expected error when events Checkpoint fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// Lock must be released.
	lockPath := filepath.Join(dotDir, "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock should be released after events Checkpoint failure; stat err=%v", statErr)
	}
}

// TestRunT0Preflight_CheckpointRunLogStoreFails covers the branch where
// the runs.ndjson Checkpoint at phase end fails (after events Checkpoint
// already succeeded).
func TestRunT0Preflight_CheckpointRunLogStoreFails(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireDiskutil(t)

	dest := setupDest(t)
	dotDir := filepath.Join(dest, ".flashbackup")
	innerES, innerRLS, _, _ := makeStores(t, dotDir, canonicalRunID)
	sentinel := errors.New("simulated runlog checkpoint fault")
	rls := &faultingRunLogStore{inner: innerRLS, failCheckpointAll: true, err: sentinel}

	_, err := RunT0Preflight(context.Background(), T0Input{
		RunID:        canonicalRunID,
		DestRoot:     dest,
		SourceRoot:   "/tmp",
		Mode:         types.ModeCopy,
		SkipCodesign: true,
		EventStore:   innerES,
		RunLogStore:  rls,
	})
	if err == nil {
		t.Fatal("expected error when runlog Checkpoint fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// Lock must be released.
	lockPath := filepath.Join(dotDir, "lock")
	if _, statErr := os.Stat(lockPath); !os.IsNotExist(statErr) {
		t.Errorf("lock should be released after runlog Checkpoint failure; stat err=%v", statErr)
	}
}
