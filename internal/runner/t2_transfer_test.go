package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/rsync"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/selection"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// Why stub rsync instead of the real binary?
//
// Task 14 already exercises the real bundled rsync 3.4.1 against a
// golden-file progress capture in internal/rsync. Task 24 is about the
// runner's event-streaming and audit-event wiring; a real rsync would
// add USB-mount-style setup and per-OS skips without testing anything
// rsync-package tests don't already cover. We pay for that orthogonality
// by accepting that buildArgs / argv parsing is implicitly tested twice:
// once in the rsync package's pure-function unit tests and once here via
// the command_line audit-detail assertion.

// fakeRsyncPath returns the absolute path to one of the testdata shell
// scripts. Skips the test if the script is not executable (defense against
// a clean checkout that loses the +x bit; ought never to happen because
// Go preserves file modes in testdata/).
func fakeRsyncPath(t *testing.T, name string) string {
	t.Helper()
	// runtime.Caller(0) -> this file; sibling testdata/ is the root.
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller(0) failed")
	}
	p := filepath.Join(filepath.Dir(thisFile), "testdata", name)
	info, err := os.Stat(p)
	if err != nil {
		t.Skipf("fake rsync %s not found: %v", p, err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Skipf("fake rsync %s not executable (perm=%v); chmod +x testdata/*.sh", p, info.Mode().Perm())
	}
	return p
}

// makeT2Stores opens a fresh events.ndjson under a tmp DotDir. T1 does
// not write to runs.ndjson (T0 / T4 own that file).
func makeT2Stores(t *testing.T) (es state.EventStore, dotDir string, runID string, eventsPath string) {
	t.Helper()
	dotDir = filepath.Join(t.TempDir(), ".flashbackup")
	runID = canonicalRunID
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
	return store, dotDir, runID, eventsPath
}

// seedCandidates makes 2 small files in a temp source tree and returns
// the matching selection.Candidate slice. The fake-rsync scripts ignore
// the actual file contents; what matters is that Candidates is non-empty
// so the rsync path is exercised.
func seedCandidates(t *testing.T) (src string, cands []selection.Candidate) {
	t.Helper()
	src = t.TempDir()
	files := []string{"a.txt", "sub/b.md"}
	for _, rel := range files {
		full := filepath.Join(src, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir: %v", err)
		}
		if err := os.WriteFile(full, []byte("payload-"+rel), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
	}
	for _, rel := range files {
		full := filepath.Join(src, filepath.FromSlash(rel))
		st, err := os.Stat(full)
		if err != nil {
			t.Fatalf("stat: %v", err)
		}
		cands = append(cands, selection.Candidate{
			RelativePath: rel,
			AbsolutePath: full,
			Size:         st.Size(),
			MtimeNS:      st.ModTime().UnixNano(),
		})
	}
	return src, cands
}

func TestRunT2Transfer_HappyPath(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, eventsPath := makeT2Stores(t)
	rend := &captureRenderer{}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatalf("mkdir dest: %v", err)
	}

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		Mode:       types.ModeCopy,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT2Transfer: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T2Result")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
	if res.RsyncLogPath == "" {
		t.Error("RsyncLogPath empty")
	}

	// Audit log: phase_started, transfer_started, transfer_completed, phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started", "transfer_started", "transfer_completed", "phase_completed"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	for i, k := range want {
		if kinds[i] != k {
			t.Errorf("event[%d] kind = %q; want %q", i, kinds[i], k)
		}
	}
	for i, ev := range events {
		if ev["phase"] != "T1" {
			t.Errorf("event[%d].phase = %v; want T1", i, ev["phase"])
		}
	}

	// transfer_started carries command_line + file_count.
	tsDetails, ok := events[1]["details"].(map[string]any)
	if !ok {
		t.Fatalf("transfer_started.details missing: %v", events[1])
	}
	cmdLine, ok := tsDetails["command_line"].(string)
	if !ok || cmdLine == "" {
		t.Errorf("transfer_started.details.command_line = %v; want non-empty string", tsDetails["command_line"])
	}
	if !strings.Contains(cmdLine, "--progress") {
		t.Errorf("command_line missing --progress: %q", cmdLine)
	}
	// Lockstep with rsync.BuildArgs: the audit command_line MUST be
	// byte-equal to the subprocess argv reconstruction. A future change
	// that builds the audit string from one Options snapshot and invokes
	// rsync from a different one (or that adds a new flag to BuildArgs
	// but not to the audit) would silently produce a misleading audit
	// line. This assertion locks single-source-of-truth.
	expectedOpts := rsync.Options{
		ExecPath:   fakeRsyncPath(t, "rsync-ok.sh"),
		SourceRoot: src,
		DestRoot:   dest,
		Files:      candidateRelPaths(cands),
		Archive:    true,
		Partial:    true,
		Xattrs:     true,
	}
	wantCmdLine := rsyncCommandLine(expectedOpts)
	if cmdLine != wantCmdLine {
		t.Errorf("command_line lockstep with rsync.BuildArgs failed:\n got:  %q\n want: %q", cmdLine, wantCmdLine)
	}
	if fc, ok := tsDetails["file_count"].(float64); !ok || int(fc) != len(cands) {
		t.Errorf("transfer_started.details.file_count = %v; want %d", tsDetails["file_count"], len(cands))
	}

	// transfer_completed carries exit_code=0 + duration_ms.
	tcDetails, ok := events[2]["details"].(map[string]any)
	if !ok {
		t.Fatalf("transfer_completed.details missing: %v", events[2])
	}
	if ec, ok := tcDetails["exit_code"].(float64); !ok || int(ec) != 0 {
		t.Errorf("transfer_completed.details.exit_code = %v; want 0", tcDetails["exit_code"])
	}
	if dm, ok := tcDetails["duration_ms"].(float64); !ok || dm < 0 {
		t.Errorf("transfer_completed.details.duration_ms = %v; want non-negative number", tcDetails["duration_ms"])
	}

	// rsync.log exists at the expected path and is non-empty (the fake
	// wrote a "sent X bytes" line via the parser's PassThrough).
	info, statErr := os.Stat(res.RsyncLogPath)
	if statErr != nil {
		t.Fatalf("rsync.log stat: %v", statErr)
	}
	if info.Size() == 0 {
		t.Error("rsync.log empty; expected fake-rsync output to land via PassThrough")
	}

	// Renderer saw phase_started then phase_completed (ok). No progress
	// events from rsync-ok.sh (it emits only a summary line, which the
	// parser classifies as ProgressSummary -- no UIEvent emitted).
	ui := rend.seen()
	if len(ui) < 2 {
		t.Fatalf("renderer events = %d; want at least 2", len(ui))
	}
	if ui[0].Kind != types.UIEvtPhaseStarted || ui[0].Phase != types.PhaseTransfer {
		t.Errorf("renderer[0] = %+v; want PhaseStarted/T1", ui[0])
	}
	lastUI := ui[len(ui)-1]
	if lastUI.Kind != types.UIEvtPhaseCompleted || lastUI.Status != "ok" || lastUI.Phase != types.PhaseTransfer {
		t.Errorf("renderer[last] = %+v; want PhaseCompleted/ok/T1", lastUI)
	}
}

func TestRunT2Transfer_RendererCapturesProgress(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, _ := makeT2Stores(t)
	rend := &captureRenderer{}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-progress.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("RunT2Transfer: %v", err)
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
	// FilesAttempted should be 1 (the script emits exactly one xfr#1 tail).
	if res.FilesAttempted != 1 {
		t.Errorf("FilesAttempted = %d; want 1", res.FilesAttempted)
	}
	// BytesTransferred should be the last-seen byte counter (1,048,576).
	if res.BytesTransferred != 1048576 {
		t.Errorf("BytesTransferred = %d; want 1048576", res.BytesTransferred)
	}

	// Renderer must have received at least one progress event with a
	// non-zero BytesDone (the 50% line carries 524,288 bytes).
	ui := rend.seen()
	var sawProgress bool
	for _, ev := range ui {
		if ev.Kind == types.UIEvtProgress && ev.Phase == types.PhaseTransfer {
			if ev.Progress == nil {
				t.Errorf("UIEvtProgress has nil Progress: %+v", ev)
				continue
			}
			if ev.Progress.BytesDone > 0 {
				sawProgress = true
				if ev.Progress.CurrentFile == "" {
					t.Errorf("UIEvtProgress.Progress.CurrentFile empty: %+v", ev.Progress)
				}
			}
		}
	}
	if !sawProgress {
		t.Errorf("renderer did not receive any UIEvtProgress with non-zero BytesDone; events: %+v", ui)
	}
}

func TestRunT2Transfer_NonZeroExit(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, eventsPath := makeT2Stores(t)
	rend := &captureRenderer{}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-fails-23.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err == nil {
		t.Fatal("expected error on non-zero rsync exit")
	}
	if res == nil {
		t.Fatal("expected non-nil T2Result on rsync failure (carries ExitCode forward)")
	}
	if res.ExitCode != 23 {
		t.Errorf("ExitCode = %d; want 23", res.ExitCode)
	}
	if !strings.Contains(err.Error(), "rsync") {
		t.Errorf("error chain missing 'rsync': %v", err)
	}

	// Audit log: phase_started, transfer_started, transfer_failed, phase_aborted.
	// No transfer_completed, no phase_completed.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	wantKinds := []string{"phase_started", "transfer_started", "transfer_failed", "phase_aborted"}
	if len(kinds) != len(wantKinds) {
		t.Fatalf("event kinds = %v; want %v", kinds, wantKinds)
	}
	for i, k := range wantKinds {
		if kinds[i] != k {
			t.Errorf("event[%d] kind = %q; want %q", i, kinds[i], k)
		}
	}

	// transfer_failed carries exit_code:23 + error.
	tfDetails, ok := events[2]["details"].(map[string]any)
	if !ok {
		t.Fatalf("transfer_failed.details missing: %v", events[2])
	}
	if ec, ok := tfDetails["exit_code"].(float64); !ok || int(ec) != 23 {
		t.Errorf("transfer_failed.details.exit_code = %v; want 23", tfDetails["exit_code"])
	}
	if _, ok := tfDetails["error"]; !ok {
		t.Errorf("transfer_failed.details missing error: %v", tfDetails)
	}

	// Renderer's final event: PhaseCompleted Status=aborted with the
	// wrapped error preserved.
	ui := rend.seen()
	lastUI := ui[len(ui)-1]
	if lastUI.Kind != types.UIEvtPhaseCompleted || lastUI.Status != "aborted" {
		t.Errorf("renderer[last] = %+v; want PhaseCompleted/aborted", lastUI)
	}
	if lastUI.Err == nil {
		t.Error("renderer[last].Err nil; want the wrapped rsync error")
	}
}

func TestRunT2Transfer_CancelledMidTransfer(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, eventsPath := makeT2Stores(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	// Cancel 50ms after start; rsync-slow.sh sleeps 30s.
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	res, err := RunT2Transfer(ctx, T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-slow.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if res == nil {
		t.Fatal("expected non-nil T2Result on cancellation (carries ExitCode forward)")
	}
	// exec.CommandContext sends SIGKILL on ctx cancel; should reap within
	// a couple seconds on any reasonable machine.
	if elapsed > 5*time.Second {
		t.Errorf("RunT2Transfer took %v after 50ms cancel; subprocess not killed promptly", elapsed)
	}

	// phase_completed must be absent: the phase did not complete.
	events := readNDJSON(t, eventsPath)
	for _, ev := range events {
		if ev["kind"] == "phase_completed" {
			t.Errorf("phase_completed should be absent on cancellation; got events %v", eventKinds(events))
		}
		if ev["kind"] == "transfer_completed" {
			t.Errorf("transfer_completed should be absent on cancellation; got events %v", eventKinds(events))
		}
	}
}

func TestRunT2Transfer_EmptyCandidates(t *testing.T) {
	src := t.TempDir()
	es, dotDir, runID, eventsPath := makeT2Stores(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	// Pass /bin/false as RsyncPath: if the empty-Candidates short-circuit
	// is wired correctly, rsync is never invoked and the bogus path is
	// harmless. If the short-circuit regresses, the test fails loudly
	// with "rsync exited with status 1".
	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  "/bin/false",
		Candidates: nil,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT2Transfer with zero candidates: %v", err)
	}
	if res == nil {
		t.Fatal("expected non-nil T2Result")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0 for empty-candidate short-circuit", res.ExitCode)
	}
	if res.FilesAttempted != 0 || res.BytesTransferred != 0 {
		t.Errorf("FilesAttempted=%d BytesTransferred=%d; want 0/0", res.FilesAttempted, res.BytesTransferred)
	}

	// Audit trail still shows the full success-trio + phase_completed so
	// downstream phases see a normal close-out.
	events := readNDJSON(t, eventsPath)
	kinds := eventKinds(events)
	want := []string{"phase_started", "transfer_started", "transfer_completed", "phase_completed"}
	if len(kinds) != len(want) {
		t.Fatalf("event kinds = %v; want %v", kinds, want)
	}
	// transfer_started.details.file_count == 0.
	tsDetails, _ := events[1]["details"].(map[string]any)
	if fc, ok := tsDetails["file_count"].(float64); !ok || int(fc) != 0 {
		t.Errorf("transfer_started.details.file_count = %v; want 0", tsDetails["file_count"])
	}
}

func TestRunT2Transfer_NilEventStore(t *testing.T) {
	_, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: "/tmp",
		DestRoot:   "/tmp",
		RsyncPath:  "/bin/true",
		EventStore: nil,
	})
	if err == nil {
		t.Fatal("expected error on nil EventStore")
	}
	if !strings.Contains(err.Error(), "EventStore") {
		t.Errorf("expected error to mention EventStore; got %v", err)
	}
}

func TestRunT2Transfer_CancelledContextAtEntry(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, eventsPath := makeT2Stores(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	res, err := RunT2Transfer(ctx, T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected cancelled-context error at entry")
	}
	if res != nil {
		t.Errorf("expected nil result on entry-cancel, got %+v", res)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled in chain; got %v", err)
	}
	// No store writes happened.
	if data, statErr := os.ReadFile(eventsPath); statErr == nil && len(data) > 0 {
		t.Errorf("expected empty events.ndjson at entry-cancel; got %q", string(data))
	}
}

func TestRunT2Transfer_AppendPhaseStartedFails(t *testing.T) {
	src, cands := seedCandidates(t)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated phase_started fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_started", err: sentinel}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	dotDir := filepath.Join(t.TempDir(), ".flashbackup")

	_, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      canonicalRunID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when phase_started Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT2Transfer_AppendTransferStartedFails(t *testing.T) {
	src, cands := seedCandidates(t)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated transfer_started fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "transfer_started", err: sentinel}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	dotDir := filepath.Join(t.TempDir(), ".flashbackup")

	_, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      canonicalRunID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when transfer_started Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT2Transfer_AppendTransferCompletedFails(t *testing.T) {
	src, cands := seedCandidates(t)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated transfer_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "transfer_completed", err: sentinel}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	dotDir := filepath.Join(t.TempDir(), ".flashbackup")

	_, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      canonicalRunID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when transfer_completed Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT2Transfer_AppendPhaseCompletedFails(t *testing.T) {
	src, cands := seedCandidates(t)
	innerES, _ := makeT1EventStore(t)
	sentinel := errors.New("simulated phase_completed fault")
	es := &faultingEventStore{inner: innerES, failAppendKind: "phase_completed", err: sentinel}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	dotDir := filepath.Join(t.TempDir(), ".flashbackup")

	_, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      canonicalRunID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when phase_completed Append fails")
	}
	if !errors.Is(err, sentinel) {
		t.Errorf("expected wrapped sentinel; got %v", err)
	}
}

func TestRunT2Transfer_RendererErrorIsNonFatal(t *testing.T) {
	src, cands := seedCandidates(t)
	es, dotDir, runID, _ := makeT2Stores(t)
	rend := &captureRenderer{err: errors.New("renderer broken")}
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-progress.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
		UIRenderer: rend,
	})
	if err != nil {
		t.Fatalf("PS3: renderer errors must not abort; got %v", err)
	}
	if res == nil {
		t.Fatal("expected T2Result")
	}
	if res.ExitCode != 0 {
		t.Errorf("ExitCode = %d; want 0", res.ExitCode)
	}
}

func TestRunT2Transfer_RsyncLogOpenFails(t *testing.T) {
	src, cands := seedCandidates(t)
	es, _, runID, _ := makeT2Stores(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	// Force MkdirAll to fail by setting DotDir to a path that includes a
	// component that already exists as a file (regular file, not a dir).
	// Then runs/<runID> cannot be created beneath it.
	parent := t.TempDir()
	notADir := filepath.Join(parent, ".flashbackup")
	if err := os.WriteFile(notADir, []byte("blocker"), 0o600); err != nil {
		t.Fatalf("seed blocking file: %v", err)
	}

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     notADir,
		RunID:      runID,
		EventStore: es,
	})
	if err == nil {
		t.Fatal("expected error when rsync.log mkdir fails")
	}
	if res != nil {
		t.Errorf("expected nil T2Result on rsync.log open failure, got %+v", res)
	}
	if !strings.Contains(err.Error(), "rsync.log") && !strings.Contains(err.Error(), "run dir") {
		t.Errorf("expected error to mention rsync.log or run dir; got %v", err)
	}
}

func TestRunT2Transfer_MkdirAllCreatesRunDir(t *testing.T) {
	src, cands := seedCandidates(t)
	dest := filepath.Join(t.TempDir(), "dest")
	if err := os.MkdirAll(dest, 0o700); err != nil {
		t.Fatal(err)
	}

	// Fresh DotDir that does NOT pre-exist. RunT2Transfer must MkdirAll
	// the runs/<RunID>/ chain itself.
	dotDir := filepath.Join(t.TempDir(), ".flashbackup-fresh")
	runID := canonicalRunID

	// We need an EventStore at runs/<RunID>/events.ndjson; for this test
	// open one AFTER asserting the run dir does not pre-exist, so the
	// store-open does not create the directory and we are truly testing
	// RunT2Transfer's MkdirAll.
	runDir := filepath.Join(dotDir, "runs", runID)
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("test setup: run dir should not exist yet: %v", err)
	}
	// Open the store at a separate scratch path so this test isolates
	// RunT2Transfer's MkdirAll responsibility from the EventStore's
	// open path.
	scratch := filepath.Join(t.TempDir(), "events.ndjson")
	es, err := state.NewNDJSONEventStore(scratch)
	if err != nil {
		t.Fatalf("open events: %v", err)
	}
	t.Cleanup(func() { _ = es.Close() })

	res, err := RunT2Transfer(context.Background(), T2Input{
		SourceRoot: src,
		DestRoot:   dest,
		RsyncPath:  fakeRsyncPath(t, "rsync-ok.sh"),
		Candidates: cands,
		DotDir:     dotDir,
		RunID:      runID,
		EventStore: es,
	})
	if err != nil {
		t.Fatalf("RunT2Transfer: %v", err)
	}
	// run dir must now exist.
	info, statErr := os.Stat(runDir)
	if statErr != nil {
		t.Fatalf("run dir not created: %v", statErr)
	}
	if !info.IsDir() {
		t.Errorf("expected run dir, got %v", info.Mode())
	}
	// rsync.log lives under it.
	if !strings.HasPrefix(res.RsyncLogPath, runDir) {
		t.Errorf("RsyncLogPath %q not under runDir %q", res.RsyncLogPath, runDir)
	}
}
