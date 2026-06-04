package runner

import (
	"bufio"
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
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Helpers reused from sibling test files (same package):
//   - captureRenderer, readNDJSON, eventKinds, faultingEventStore,
//     canonicalRunID, makeT1EventStore, seedTransferred, seedFile.
//
// Task 26 specifics: tests share two helpers below (seedAllVerifiedT3Result,
// makeT4Dirs) and never touch the manifest because T3 (delete-source) reads
// PerFileStatus from T3Result, not from a manifest.

// makeT4Dirs allocates a fresh dotDir + RunID for a T3 run; the test owns
// these so it can assert deletion-log.ndjson presence/absence. Mirrors
// makeT3Stores's events half but does NOT open the EventStore (callers
// who want one use makeT1EventStore so they can pick the path).
func makeT4Dirs(t *testing.T) (dotDir, runID, runDir string) {
	t.Helper()
	dotDir = filepath.Join(t.TempDir(), ".flashbackup")
	runID = canonicalRunID
	runDir = filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir run dir: %v", err)
	}
	return dotDir, runID, runDir
}

// seedAllVerifiedT3Result builds a T3Result where every Candidate is
// StatusVerified. Mirrors the gate-passed shape Task 26 expects to see from
// Task 25 on the happy path.
func seedAllVerifiedT3Result(cands []selection.Candidate) *T3Result {
	r := &T3Result{
		FilesTotal:    len(cands),
		FilesVerified: len(cands),
		PerFileStatus: make(map[string]state.FileStatus, len(cands)),
	}
	for _, c := range cands {
		r.PerFileStatus[c.RelativePath] = state.StatusVerified
	}
	return r
}

// makeT4EventStore opens a fresh events.ndjson under <dotDir>/runs/<runID>/.
// Different from makeT1EventStore in that the test supplies the dotDir +
// runID (so the deletion-log lives in the same run dir).
func makeT4EventStore(t *testing.T, runDir string) (state.EventStore, string) {
	t.Helper()
	path := filepath.Join(runDir, "events.ndjson")
	es, err := state.NewNDJSONEventStore(path)
	if err != nil {
		t.Fatalf("open events store: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })
	return es, path
}

// readDeletionLog parses every NDJSON line of a deletion-log.ndjson file.
// Returns the parsed objects; fails the test on parse errors. An empty file
// (zero lines) returns a nil slice (NOT an error).
func readDeletionLog(t *testing.T, path string) []map[string]any {
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
			t.Fatalf("parse deletion-log line %q: %v", string(line), err)
		}
		out = append(out, m)
	}
	if err := scan.Err(); err != nil && err != io.EOF {
		t.Fatalf("scan deletion-log %s: %v", path, err)
	}
	return out
}

// ---- 1. Copy-mode short-circuit ---------------------------------------

func TestRunT4DeleteSource_CopyMode_ShortCircuit(t *testing.T) {
	files := []seedFile{
		{rel: "a.txt", content: []byte("a")},
		{rel: "b.txt", content: []byte("b")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	rend := &captureRenderer{}
	t3Res := seedAllVerifiedT3Result(cands)

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeCopy,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T4Result")
	}
	if res.GateBlocked {
		t.Error("GateBlocked = true on copy mode; want false")
	}
	if res.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d; want 0 on copy mode", res.FilesDeleted)
	}
	if res.PerFileOutcome != nil {
		t.Errorf("PerFileOutcome should be nil on copy mode short-circuit; got %v",
			res.PerFileOutcome)
	}
	// Source files must still exist.
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); err != nil {
			t.Errorf("copy mode unlinked source %q: %v", c.AbsolutePath, err)
		}
	}
	// Audit log: only phase_started + phase_completed. The phase_completed
	// Details carries skipped:true to make the no-op visible in support
	// bundles (a missing phase_completed would mean failure).
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	if len(kinds) != 2 || kinds[0] != "phase_started" || kinds[1] != "phase_completed" {
		t.Errorf("copy-mode event kinds = %v; want [phase_started, phase_completed]", kinds)
	}
	for i, ev := range events {
		if ev["phase"] != "T3" {
			t.Errorf("event[%d].phase = %v; want T3", i, ev["phase"])
		}
	}
	pc, _ := events[1]["details"].(map[string]any)
	if v, _ := pc["skipped"].(bool); !v {
		t.Errorf("phase_completed.details.skipped = %v; want true", pc["skipped"])
	}
	// deletion-log.ndjson must NOT exist in copy mode (we never opened it).
	delLog := filepath.Join(runDir, "deletion-log.ndjson")
	if _, err := os.Stat(delLog); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deletion-log.ndjson should not exist on copy mode; stat = %v", err)
	}
	// Renderer should still see phase_started + phase_completed (status ok)
	// so a TUI under copy mode renders one normal phase pass.
	ui := rend.seen()
	if len(ui) != 2 {
		t.Fatalf("copy-mode renderer events = %d; want 2", len(ui))
	}
	if ui[0].Kind != types.UIEvtPhaseStarted || ui[0].Phase != types.PhaseDelete {
		t.Errorf("ui[0] = %+v; want PhaseStarted/T3", ui[0])
	}
	if ui[1].Kind != types.UIEvtPhaseCompleted || ui[1].Status != "ok" || ui[1].Phase != types.PhaseDelete {
		t.Errorf("ui[1] = %+v; want PhaseCompleted/ok/T3", ui[1])
	}
}

// Sanity-check that copy mode never opens deletion-log.ndjson even when a
// stale file already exists under <runDir>/. Tied to test #12 in the plan.
func TestRunT4DeleteSource_CopyMode_DoesNotTouchExistingDeletionLog(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)

	// Plant a pre-existing deletion-log with a known sentinel marker.
	preDel := filepath.Join(runDir, "deletion-log.ndjson")
	sentinel := []byte(`{"v":1,"path":"pre-existing","status":"deleted"}` + "\n")
	if err := os.WriteFile(preDel, sentinel, 0o644); err != nil {
		t.Fatalf("write pre-existing deletion-log: %v", err)
	}

	es, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)
	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeCopy,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}

	got, err := os.ReadFile(preDel)
	if err != nil {
		t.Fatalf("read deletion-log: %v", err)
	}
	if string(got) != string(sentinel) {
		t.Errorf("copy-mode mutated existing deletion-log: got %q; want %q",
			string(got), string(sentinel))
	}
}

// ---- 2. Atomic gate fires ---------------------------------------------

func TestRunT4DeleteSource_AtomicGateFires(t *testing.T) {
	files := []seedFile{
		{rel: "ok-1.txt", content: []byte("one")},
		{rel: "ok-2.txt", content: []byte("two")},
		{rel: "bad.txt", content: []byte("three")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	rend := &captureRenderer{}

	// T3Result: 2 verified, 1 hash_mismatch (gate must fire because total!=verified).
	t3Res := &T3Result{
		FilesTotal:        3,
		FilesVerified:     2,
		FilesHashMismatch: 1,
		PerFileStatus: map[string]state.FileStatus{
			"ok-1.txt": state.StatusVerified,
			"ok-2.txt": state.StatusVerified,
			"bad.txt":  state.StatusHashMismatch,
		},
	}

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T4Result")
	}
	if !res.GateBlocked {
		t.Error("GateBlocked = false; want true when verified != total")
	}
	if res.FilesDeleted != 0 {
		t.Errorf("FilesDeleted = %d; want 0 when gate fires", res.FilesDeleted)
	}
	if res.FilesEligibleForDelete != 2 {
		t.Errorf("FilesEligibleForDelete = %d; want 2 (verified count)", res.FilesEligibleForDelete)
	}

	// Source tree intact (this is THE invariant the atomic gate exists for).
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); err != nil {
			t.Errorf("gate-blocked: source %q deleted: %v", c.AbsolutePath, err)
		}
	}

	// Audit: phase_started + atomic_gate_blocked + phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started", "atomic_gate_blocked", "phase_completed"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("event[%d] = %q; want %q", i, kinds[i], k)
		}
	}
	gate, _ := events[1]["details"].(map[string]any)
	if fc, ok := gate["failed_count"].(float64); !ok || int(fc) != 1 {
		t.Errorf("atomic_gate_blocked.details.failed_count = %v; want 1", gate["failed_count"])
	}

	// Renderer: PhaseStarted + PhaseCompleted (status="aborted" because the
	// gate firing is a protective abort from the user's PoV; the phase still
	// completed on the audit side).
	ui := rend.seen()
	if len(ui) != 2 {
		t.Fatalf("renderer events = %d; want 2", len(ui))
	}
	if ui[1].Kind != types.UIEvtPhaseCompleted || ui[1].Status != "aborted" {
		t.Errorf("ui[1] = %+v; want PhaseCompleted/aborted", ui[1])
	}

	// deletion-log.ndjson: contract says we do not open it when the gate
	// fires (gate fires BEFORE the per-file loop). Lock that as: file
	// does not exist.
	delLog := filepath.Join(runDir, "deletion-log.ndjson")
	if _, err := os.Stat(delLog); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deletion-log.ndjson should not exist when gate fires; stat = %v", err)
	}
}

// ---- 3. Happy path ----------------------------------------------------

func TestRunT4DeleteSource_HappyPath_AllDeleted(t *testing.T) {
	files := []seedFile{
		{rel: "a.txt", content: []byte("alpha")},
		{rel: "b.md", content: []byte("bravo")},
		{rel: "sub/c.pdf", content: []byte("charlie")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	rend := &captureRenderer{}
	t3Res := seedAllVerifiedT3Result(cands)

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T4Result")
	}
	if res.GateBlocked {
		t.Error("GateBlocked = true on happy path")
	}
	if res.FilesDeleted != len(files) {
		t.Errorf("FilesDeleted = %d; want %d", res.FilesDeleted, len(files))
	}
	if res.FilesSkippedMutated != 0 || res.FilesDeleteFailed != 0 {
		t.Errorf("expected zero skipped/failed; got %+v", res)
	}
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("source %q still exists after delete; stat = %v", c.AbsolutePath, err)
		}
		if got := res.PerFileOutcome[c.RelativePath]; got != state.DeletionDeleted {
			t.Errorf("PerFileOutcome[%q] = %q; want deleted", c.RelativePath, got)
		}
	}

	// Audit: phase_started + N delete_completed + phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started"}
	for range files {
		want = append(want, "delete_completed")
	}
	want = append(want, "phase_completed")
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("event[%d] = %q; want %q", i, kinds[i], k)
		}
	}

	// deletion-log.ndjson exists with N lines, each status="deleted".
	delLog := filepath.Join(runDir, "deletion-log.ndjson")
	if res.DeletionLogPath != delLog {
		t.Errorf("DeletionLogPath = %q; want %q", res.DeletionLogPath, delLog)
	}
	lines := readDeletionLog(t, delLog)
	if len(lines) != len(files) {
		t.Fatalf("deletion-log lines = %d; want %d", len(lines), len(files))
	}
	for i, ln := range lines {
		if v, _ := ln["v"].(float64); int(v) != 1 {
			t.Errorf("deletion-log[%d].v = %v; want 1", i, ln["v"])
		}
		if ln["status"] != string(state.DeletionDeleted) {
			t.Errorf("deletion-log[%d].status = %v; want %q", i, ln["status"], state.DeletionDeleted)
		}
		if _, ok := ln["path"].(string); !ok {
			t.Errorf("deletion-log[%d].path missing or wrong type", i)
		}
		if _, ok := ln["attempted_at"].(string); !ok {
			t.Errorf("deletion-log[%d].attempted_at missing", i)
		}
	}

	// Renderer: PhaseStarted + N FileCompleted + PhaseCompleted.
	ui := rend.seen()
	if len(ui) != 1+len(files)+1 {
		t.Fatalf("renderer events = %d; want %d", len(ui), 1+len(files)+1)
	}
	if ui[0].Kind != types.UIEvtPhaseStarted || ui[0].Phase != types.PhaseDelete {
		t.Errorf("ui[0] = %+v; want PhaseStarted/T3", ui[0])
	}
	for i := 0; i < len(files); i++ {
		if ui[1+i].Kind != types.UIEvtFileCompleted {
			t.Errorf("ui[%d].Kind = %v; want FileCompleted", 1+i, ui[1+i].Kind)
		}
		if ui[1+i].Status != string(state.DeletionDeleted) {
			t.Errorf("ui[%d].Status = %q; want deleted", 1+i, ui[1+i].Status)
		}
	}
	if last := ui[len(ui)-1]; last.Kind != types.UIEvtPhaseCompleted || last.Status != "ok" {
		t.Errorf("ui[last] = %+v; want PhaseCompleted/ok", last)
	}
}

// ---- 4. Per-file mutation re-stat skip --------------------------------

func TestRunT4DeleteSource_MutationReStatSkip(t *testing.T) {
	files := []seedFile{
		{rel: "stable-1.txt", content: []byte("stable-one")},
		{rel: "mutated.txt", content: []byte("original-content")},
		{rel: "stable-2.txt", content: []byte("stable-two")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	// Mutate one source file AFTER signature capture. The user could have
	// touched the source between T2 hashing and T3 deletion; the T3 re-stat
	// is defense in depth on top of T2's mutation gate (invariant #8).
	mutFull := filepath.Join(src, "mutated.txt")
	time.Sleep(20 * time.Millisecond) // ensure mtime delta
	if err := os.WriteFile(mutFull, []byte("MUTATED-WITH-EXTRA-BYTES-XXXXX"), 0o600); err != nil {
		t.Fatalf("mutate source: %v", err)
	}

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}
	if res.FilesDeleted != 2 {
		t.Errorf("FilesDeleted = %d; want 2 (two stable files)", res.FilesDeleted)
	}
	if res.FilesSkippedMutated != 1 {
		t.Errorf("FilesSkippedMutated = %d; want 1", res.FilesSkippedMutated)
	}

	// Mutated file still on disk; unmutated ones gone.
	if _, err := os.Stat(mutFull); err != nil {
		t.Errorf("mutated source unlinked despite re-stat skip: %v", err)
	}
	for _, c := range cands {
		if c.RelativePath == "mutated.txt" {
			continue
		}
		if _, err := os.Stat(c.AbsolutePath); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("stable source %q still exists: %v", c.AbsolutePath, err)
		}
	}

	// PerFileOutcome: deleted for stable, skipped_mutated for mutated.
	if got := res.PerFileOutcome["mutated.txt"]; got != state.DeletionSkippedMutated {
		t.Errorf("PerFileOutcome[mutated.txt] = %q; want skipped_mutated", got)
	}
	for _, rel := range []string{"stable-1.txt", "stable-2.txt"} {
		if got := res.PerFileOutcome[rel]; got != state.DeletionDeleted {
			t.Errorf("PerFileOutcome[%q] = %q; want deleted", rel, got)
		}
	}

	// Audit: delete_skipped_mutated event for the mutated file.
	events := readNDJSON(t, eventsPath)
	var skipped map[string]any
	for _, ev := range events {
		if ev["kind"] == "delete_skipped_mutated" {
			skipped = ev
			break
		}
	}
	if skipped == nil {
		t.Fatal("expected delete_skipped_mutated event")
	}
	det, _ := skipped["details"].(map[string]any)
	if det["path"] != "mutated.txt" {
		t.Errorf("delete_skipped_mutated.details.path = %v; want mutated.txt", det["path"])
	}

	// deletion-log.ndjson: 3 lines (2 deleted + 1 skipped_mutated).
	lines := readDeletionLog(t, filepath.Join(runDir, "deletion-log.ndjson"))
	if len(lines) != 3 {
		t.Fatalf("deletion-log lines = %d; want 3", len(lines))
	}
	var skippedSeen bool
	for _, ln := range lines {
		if ln["path"] == "mutated.txt" && ln["status"] == string(state.DeletionSkippedMutated) {
			skippedSeen = true
		}
	}
	if !skippedSeen {
		t.Error("deletion-log missing mutated.txt skipped_mutated line")
	}
}

// ---- 5. Per-file permission-denied ------------------------------------

func TestRunT4DeleteSource_PermissionDenied(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission-denied semantics not portable to Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	files := []seedFile{
		{rel: "ok.txt", content: []byte("ok")},
		{rel: "lockeddir/denied.txt", content: []byte("denied-bytes")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	// chmod the PARENT directory of denied.txt to 0555 so os.Remove fails
	// with EACCES (POSIX: removing an entry requires write+execute on the
	// containing directory, not on the entry itself).
	lockedDir := filepath.Join(src, "lockeddir")
	if err := os.Chmod(lockedDir, 0o555); err != nil {
		t.Fatalf("chmod lockeddir: %v", err)
	}
	// Restore at the end so t.TempDir's cleanup can recurse.
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o700) })

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT4DeleteSource: %v", err)
	}
	if res.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d; want 1 (ok.txt)", res.FilesDeleted)
	}
	if res.FilesDeleteFailed != 1 {
		t.Errorf("FilesDeleteFailed = %d; want 1 (denied.txt)", res.FilesDeleteFailed)
	}
	// Denied file is still on disk.
	deniedPath := filepath.Join(src, "lockeddir", "denied.txt")
	if _, err := os.Stat(deniedPath); err != nil {
		t.Errorf("denied.txt should still exist; stat = %v", err)
	}
	// PerFileOutcome records failed_permission.
	if got := res.PerFileOutcome["lockeddir/denied.txt"]; got != state.DeletionFailedPermission {
		t.Errorf("PerFileOutcome[denied] = %q; want failed_permission", got)
	}

	// Audit: delete_failed event with errno + error Details.
	events := readNDJSON(t, eventsPath)
	var failed map[string]any
	for _, ev := range events {
		if ev["kind"] == "delete_failed" {
			failed = ev
			break
		}
	}
	if failed == nil {
		t.Fatal("expected delete_failed event")
	}
	det, _ := failed["details"].(map[string]any)
	if det["path"] != "lockeddir/denied.txt" {
		t.Errorf("delete_failed.details.path = %v; want lockeddir/denied.txt", det["path"])
	}
	if _, ok := det["error"].(string); !ok {
		t.Errorf("delete_failed.details.error missing or wrong type")
	}

	// deletion-log includes the failed entry with status=failed_permission.
	lines := readDeletionLog(t, filepath.Join(runDir, "deletion-log.ndjson"))
	var found bool
	for _, ln := range lines {
		if ln["path"] == "lockeddir/denied.txt" && ln["status"] == string(state.DeletionFailedPermission) {
			found = true
		}
	}
	if !found {
		t.Error("deletion-log missing failed_permission entry")
	}
}

// ---- 6. Cancelled context at entry ------------------------------------

func TestRunT4DeleteSource_CancelledContextAtEntry(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunT4DeleteSource(ctx, T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected cancelled-context error at entry")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}
	if res != nil {
		t.Errorf("expected nil result; got %+v", res)
	}
	// No source files touched.
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); err != nil {
			t.Errorf("source %q touched on entry-cancel: %v", c.AbsolutePath, err)
		}
	}
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty events.ndjson on entry-cancel; got %q", string(data))
	}
}

// ---- 7. Cancelled context mid-loop ------------------------------------

// midCancelT4Store cancels a tied cancel func after the Nth per-file event
// (delete_completed | delete_failed | delete_skipped_mutated). Used to
// exercise mid-loop cancellation.
type midCancelT4Store struct {
	inner       state.EventStore
	cancel      context.CancelFunc
	cancelAfter int
	mu          sync.Mutex
	count       int
}

func (m *midCancelT4Store) Append(ctx context.Context, ev state.Event) error {
	if err := m.inner.Append(ctx, ev); err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	switch ev.Kind {
	case "delete_completed", "delete_failed", "delete_skipped_mutated":
		m.count++
		if m.count == m.cancelAfter {
			m.cancel()
		}
	}
	return nil
}

func (m *midCancelT4Store) Checkpoint(ctx context.Context) error {
	return m.inner.Checkpoint(ctx)
}

func (m *midCancelT4Store) Close() error { return m.inner.Close() }

func TestRunT4DeleteSource_CancelledMidLoop(t *testing.T) {
	const nFiles = 12
	var files []seedFile
	for i := 0; i < nFiles; i++ {
		files = append(files, seedFile{
			rel:     fmt.Sprintf("f-%02d.txt", i),
			content: []byte(fmt.Sprintf("file-%02d", i)),
		})
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, eventsPath := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	es := &midCancelT4Store{inner: innerES, cancel: cancel, cancelAfter: 3}

	_, err := RunT4DeleteSource(ctx, T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected mid-loop cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}

	// At least 3 files unlinked on disk; the rest left intact (loop exited
	// after the cancelAfter-th iteration).
	gone := 0
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); errors.Is(err, os.ErrNotExist) {
			gone++
		}
	}
	if gone < 3 {
		t.Errorf("unlinked count = %d; want >= 3 (cancel after 3 events)", gone)
	}
	if gone == nFiles {
		t.Errorf("all %d files unlinked; loop did not honor cancellation", nFiles)
	}

	// Audit: phase_aborted present, phase_completed absent.
	events := readNDJSON(t, eventsPath)
	var sawAborted, sawCompleted bool
	for _, ev := range events {
		switch ev["kind"] {
		case "phase_aborted":
			sawAborted = true
		case "phase_completed":
			sawCompleted = true
		}
	}
	if !sawAborted {
		t.Error("expected phase_aborted on mid-loop cancellation")
	}
	if sawCompleted {
		t.Error("phase_completed must NOT be present on mid-loop cancellation")
	}
}

// ---- 8. EventStore Append failures (per kind) --------------------------

func TestRunT4DeleteSource_AppendPhaseStartedFails(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)
	sentinel := errors.New("simulated phase_started fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when phase_started fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
	// Source files NOT touched (we never made it past the entry event).
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); err != nil {
			t.Errorf("source touched despite phase_started fail: %v", err)
		}
	}
}

func TestRunT4DeleteSource_AppendAtomicGateBlockedFails(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := &T3Result{
		FilesTotal: 1, FilesVerified: 0, FilesHashMismatch: 1,
		PerFileStatus: map[string]state.FileStatus{"a.txt": state.StatusHashMismatch},
	}
	sentinel := errors.New("simulated atomic_gate_blocked fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "atomic_gate_blocked", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when atomic_gate_blocked fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT4DeleteSource_AppendDeleteCompletedFails(t *testing.T) {
	files := []seedFile{
		{rel: "a.txt", content: []byte("a")},
		{rel: "b.txt", content: []byte("b")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)
	sentinel := errors.New("simulated delete_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "delete_completed", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when delete_completed fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT4DeleteSource_AppendDeleteSkippedMutatedFails(t *testing.T) {
	files := []seedFile{{rel: "mut.txt", content: []byte("orig")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	// Force mutation so the loop hits the skipped_mutated branch.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(src, "mut.txt"), []byte("MUTATED-LONGER"), 0o600); err != nil {
		t.Fatalf("mutate: %v", err)
	}

	sentinel := errors.New("simulated delete_skipped_mutated fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "delete_skipped_mutated", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when delete_skipped_mutated fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT4DeleteSource_AppendDeleteFailedFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission-denied semantics not portable to Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	files := []seedFile{{rel: "lockeddir/denied.txt", content: []byte("d")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	lockedDir := filepath.Join(src, "lockeddir")
	if err := os.Chmod(lockedDir, 0o555); err != nil {
		t.Fatalf("chmod lockeddir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(lockedDir, 0o700) })

	sentinel := errors.New("simulated delete_failed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "delete_failed", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when delete_failed fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT4DeleteSource_AppendPhaseCompletedFails(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	innerES, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)
	sentinel := errors.New("simulated phase_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when phase_completed fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

// ---- 9. deletion-log open failure --------------------------------------

func TestRunT4DeleteSource_DeletionLogOpenFails(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX directory-mode semantics not portable to Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("permission test requires non-root user")
	}
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, _ := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	// Lock the run dir to 0500 (r-x): events.ndjson already open survives
	// (FD held), but a fresh OpenFile for deletion-log.ndjson fails EACCES.
	if err := os.Chmod(runDir, 0o500); err != nil {
		t.Fatalf("chmod run dir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(runDir, 0o700) })

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected deletion-log open failure")
	}
	if !strings.Contains(err.Error(), "deletion-log") {
		t.Errorf("expected deletion-log in error chain; got %v", err)
	}
	// No source files unlinked: we abort before any os.Remove.
	for _, c := range cands {
		if _, err := os.Stat(c.AbsolutePath); err != nil {
			t.Errorf("source touched despite open failure: %v", err)
		}
	}
}

// ---- 10. deletion-log Sync failure mid-loop ----------------------------

// failingDeletionLog wraps a real *os.File and fails Sync after the Nth call.
// Injected via a package-level hook so tests can swap the writer.
type failingDeletionLog struct {
	mu       sync.Mutex
	inner    *os.File
	failAt   int // 1-indexed call number that fails
	calls    int
	failWith error
}

func (f *failingDeletionLog) Write(p []byte) (int, error) {
	return f.inner.Write(p)
}

func (f *failingDeletionLog) Sync() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.calls == f.failAt {
		return f.failWith
	}
	return f.inner.Sync()
}

func (f *failingDeletionLog) Close() error { return f.inner.Close() }

func TestRunT4DeleteSource_DeletionLogSyncFailsMidLoop(t *testing.T) {
	files := []seedFile{
		{rel: "a.txt", content: []byte("a")},
		{rel: "b.txt", content: []byte("b")},
		{rel: "c.txt", content: []byte("c")},
	}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	t3Res := seedAllVerifiedT3Result(cands)

	// Inject a writer whose 2nd Sync fails. After failing Sync(), the 3rd
	// file's unlink should NOT be attempted (deletion-log IS the
	// crash-recovery contract; without a durable record we cannot proceed).
	sentinel := errors.New("simulated sync fault")
	t.Cleanup(restoreDeletionLogTestHook())
	deletionLogTestHook = func(path string) (deletionLogWriter, error) {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
		if err != nil {
			return nil, err
		}
		return &failingDeletionLog{inner: f, failAt: 2, failWith: sentinel}, nil
	}

	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected deletion-log sync failure to abort the phase")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}

	// The third file must NOT have been unlinked.
	cFile := filepath.Join(src, "c.txt")
	if _, err := os.Stat(cFile); err != nil {
		t.Errorf("third file unlinked despite sync abort: %v", err)
	}
	// The deletion-log file IS on disk (partial) - this is by design.
	if _, err := os.Stat(filepath.Join(runDir, "deletion-log.ndjson")); err != nil {
		t.Errorf("deletion-log should exist on partial abort: %v", err)
	}
	// phase_aborted, not phase_completed.
	events := readNDJSON(t, eventsPath)
	var sawAborted, sawCompleted bool
	for _, ev := range events {
		switch ev["kind"] {
		case "phase_aborted":
			sawAborted = true
		case "phase_completed":
			sawCompleted = true
		}
	}
	if !sawAborted {
		t.Error("expected phase_aborted on sync failure")
	}
	if sawCompleted {
		t.Error("phase_completed must not appear on sync failure")
	}
}

// ---- 11. Renderer error non-fatal (PS3) ------------------------------

func TestRunT4DeleteSource_RendererErrorIsNonFatal(t *testing.T) {
	files := []seedFile{{rel: "a.txt", content: []byte("a")}}
	src, _, cands, sigs := seedTransferred(t, files)
	dotDir, runID, runDir := makeT4Dirs(t)
	es, _ := makeT4EventStore(t, runDir)
	rend := &captureRenderer{err: errors.New("renderer broken")}
	t3Res := seedAllVerifiedT3Result(cands)

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: src,
		Candidates: cands,
		Signatures: sigs,
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("PS3: renderer errors must not abort; got %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T4Result")
	}
	if res.FilesDeleted != 1 {
		t.Errorf("FilesDeleted = %d; want 1", res.FilesDeleted)
	}
	// Renderer was called for every UIEvent despite errors (PhaseStarted +
	// FileCompleted + PhaseCompleted).
	ui := rend.seen()
	if len(ui) != 3 {
		t.Errorf("renderer should be called 3 times despite errors; got %d", len(ui))
	}
}

// ---- Nil-store + nil-T3Result guards ----------------------------------

func TestRunT4DeleteSource_NilEventStore(t *testing.T) {
	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: "/tmp", Mode: types.ModeMove,
		T3Result:   &T3Result{},
		EventStore: nil,
	})
	if err == nil || !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected EventStore nil error; got %v", err)
	}
}

func TestRunT4DeleteSource_NilT3ResultInMoveMode(t *testing.T) {
	dotDir, runID, runDir := makeT4Dirs(t)
	es, _ := makeT4EventStore(t, runDir)
	_, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: "/tmp",
		Mode:       types.ModeMove,
		T3Result:   nil,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil || !strings.Contains(err.Error(), "T3Result") {
		t.Errorf("expected T3Result nil error in move mode; got %v", err)
	}
}

// ---- Empty Candidates ------------------------------------------------

func TestRunT4DeleteSource_EmptyCandidates_MoveMode(t *testing.T) {
	dotDir, runID, runDir := makeT4Dirs(t)
	es, eventsPath := makeT4EventStore(t, runDir)
	t3Res := &T3Result{FilesTotal: 0, FilesVerified: 0, PerFileStatus: map[string]state.FileStatus{}}

	res, err := RunT4DeleteSource(context.Background(), T4Input{
		SourceRoot: t.TempDir(),
		Candidates: nil,
		Signatures: map[string]types.Signature{},
		Mode:       types.ModeMove,
		T3Result:   t3Res,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("empty Candidates should not error: %v", err)
	}
	if res == nil || res.FilesDeleted != 0 {
		t.Fatalf("FilesDeleted = %d; want 0", res.FilesDeleted)
	}
	// Audit: phase_started + phase_completed only.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	if len(kinds) != 2 || kinds[0] != "phase_started" || kinds[1] != "phase_completed" {
		t.Errorf("empty Candidates events = %v; want [phase_started, phase_completed]", kinds)
	}
	// deletion-log.ndjson exists (we opened it) but is empty.
	delLog := filepath.Join(runDir, "deletion-log.ndjson")
	st, err := os.Stat(delLog)
	if err != nil {
		t.Fatalf("deletion-log.ndjson should exist (gate passed, opened); stat = %v", err)
	}
	if st.Size() != 0 {
		t.Errorf("deletion-log.ndjson size = %d; want 0 on empty Candidates", st.Size())
	}
}
