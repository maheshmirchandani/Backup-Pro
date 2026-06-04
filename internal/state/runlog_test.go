package state

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// newRunLogStore is a shared helper that opens a fresh runs.ndjson in a temp
// dir. Marked t.Helper() per the QA hat amendment so failures point at the
// caller, not this helper.
func newRunLogStore(t *testing.T) (RunLogStore, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.ndjson")
	store, err := NewNDJSONRunLogStore(path)
	if err != nil {
		t.Fatalf("open runs.ndjson: %v", err)
	}
	return store, path
}

// writeRunLogFixture writes a pre-baked runs.ndjson fixture and returns its
// path. Used by ReadRunLog tests so they don't need a real store.
func writeRunLogFixture(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runs.ndjson")
	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return path
}

func TestRunLogStore_StartedAndFinished(t *testing.T) {
	store, path := newRunLogStore(t)
	ctx := context.Background()

	startedAt := time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC)
	if err := store.AppendStarted(ctx, StartedRun{
		V: 1, FlashbackupVersion: "0.1.0-core", RunID: "2026-06-03T1430Z-aaaa",
		StartedAt: startedAt, Mode: "copy", Profile: "my-docs",
		SourceRoot: "/Users/me/Docs", DestRoot: "/Volumes/USB",
	}); err != nil {
		t.Fatalf("append started: %v", err)
	}

	finishedAt := startedAt.Add(20 * time.Minute)
	if err := store.AppendFinished(ctx, FinishedRun{
		V: 1, FlashbackupVersion: "0.1.0-core", RunID: "2026-06-03T1430Z-aaaa",
		StartedAt: startedAt, FinishedAt: finishedAt, Mode: "copy", Profile: "my-docs",
		SourceRoot: "/Users/me/Docs", DestRoot: "/Volumes/USB",
		FilesTotal: 100, FilesSucceeded: 100, FilesFailed: 0, BytesTotal: 1000000,
		DeletionsSkippedDueToMutation: 0, ExitStatus: "ok",
	}); err != nil {
		t.Fatalf("append finished: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d: %s", len(lines), string(data))
	}
	if !strings.Contains(lines[0], `"event":"started"`) {
		t.Errorf("line 0 missing started: %s", lines[0])
	}
	if !strings.Contains(lines[1], `"event":"finished"`) {
		t.Errorf("line 1 missing finished: %s", lines[1])
	}

	// Round-trip through ReadRunLog to verify both decode cleanly.
	entries, err := ReadRunLog(path)
	if err != nil {
		t.Fatalf("read run log: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
	if entries[0].Event != "started" || entries[0].Started == nil {
		t.Errorf("entry 0: want started, got %+v", entries[0])
	}
	if entries[0].Started.RunID != "2026-06-03T1430Z-aaaa" {
		t.Errorf("entry 0 RunID mismatch: %q", entries[0].Started.RunID)
	}
	if entries[1].Event != "finished" || entries[1].Finished == nil {
		t.Errorf("entry 1: want finished, got %+v", entries[1])
	}
	if entries[1].Finished.FilesTotal != 100 || entries[1].Finished.ExitStatus != "ok" {
		t.Errorf("entry 1 fields wrong: %+v", entries[1].Finished)
	}
	if !entries[1].Finished.FinishedAt.Equal(finishedAt) {
		t.Errorf("entry 1 FinishedAt mismatch: got %v want %v", entries[1].Finished.FinishedAt, finishedAt)
	}
}

// TestRunLogStore_TornWriteRecovery is the table-driven expansion required
// by the AMENDMENT 2026-06-03 (multi-hat round). Each subtest verifies
// behavior for one realistic corruption shape.
func TestRunLogStore_TornWriteRecovery(t *testing.T) {
	validStarted := `{"v":1,"event":"started","flashbackup_version":"0.1.0-core","run_id":"good1","started_at":"2026-06-03T14:30:00Z","mode":"copy","source_root":"/src","dest_root":"/dst"}`
	validFinished := `{"v":1,"event":"finished","flashbackup_version":"0.1.0-core","run_id":"good1","started_at":"2026-06-03T14:30:00Z","finished_at":"2026-06-03T14:50:00Z","mode":"copy","source_root":"/src","dest_root":"/dst","files_total":1,"files_succeeded":1,"files_failed":0,"bytes_total":1,"deletions_skipped_due_to_mutation":0,"exit_status":"ok"}`
	validStarted2 := `{"v":1,"event":"started","flashbackup_version":"0.1.0-core","run_id":"good2","started_at":"2026-06-03T15:30:00Z","mode":"copy","source_root":"/src","dest_root":"/dst"}`
	tornLine := `{"v":1,"event":"started","run`

	cases := []struct {
		name          string
		content       string
		wantEntries   int
		wantErrCount  int // number of underlying errors expected in errors.Join chain (0 means no error)
		validateEntry func(t *testing.T, entries []RunLogEntry)
	}{
		{
			name:         "empty_file",
			content:      "",
			wantEntries:  0,
			wantErrCount: 0,
		},
		{
			name:         "single_torn_line_no_newline",
			content:      tornLine,
			wantEntries:  0,
			wantErrCount: 1,
		},
		{
			name:         "single_torn_line_with_newline",
			content:      tornLine + "\n",
			wantEntries:  0,
			wantErrCount: 1,
		},
		{
			// Most realistic crash case: a series of valid runs, then the
			// process died mid-write of the next line. bufio.Scanner DOES
			// return that final no-newline line, so the parse error is
			// reported, the prior valid entries are preserved.
			name:         "torn_at_eof_no_newline",
			content:      validStarted + "\n" + validFinished + "\n" + tornLine,
			wantEntries:  2,
			wantErrCount: 1,
			validateEntry: func(t *testing.T, entries []RunLogEntry) {
				t.Helper()
				if entries[0].Event != "started" || entries[0].Started.RunID != "good1" {
					t.Errorf("entry 0 wrong: %+v", entries[0])
				}
				if entries[1].Event != "finished" || entries[1].Finished.RunID != "good1" {
					t.Errorf("entry 1 wrong: %+v", entries[1])
				}
			},
		},
		{
			// Valid + torn (terminated by \n) + valid. Verifies the scanner
			// recovers on the line AFTER the torn one. Matches the plan's
			// original torn-write fixture.
			name:         "torn_mid_with_recovery",
			content:      validStarted + "\n" + validFinished + "\n" + tornLine + "\n" + validStarted2 + "\n",
			wantEntries:  3,
			wantErrCount: 1,
			validateEntry: func(t *testing.T, entries []RunLogEntry) {
				t.Helper()
				if entries[2].Event != "started" || entries[2].Started.RunID != "good2" {
					t.Errorf("entry 2 should be good2 started: %+v", entries[2])
				}
			},
		},
		{
			name:         "all_torn",
			content:      tornLine + "\n" + tornLine + "\n" + tornLine + "\n",
			wantEntries:  0,
			wantErrCount: 3,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := writeRunLogFixture(t, tc.content)
			entries, err := ReadRunLog(path)

			if len(entries) != tc.wantEntries {
				t.Errorf("entries: got %d want %d (entries=%+v)", len(entries), tc.wantEntries, entries)
			}

			switch tc.wantErrCount {
			case 0:
				if err != nil {
					t.Errorf("expected nil error, got %v", err)
				}
			default:
				if err == nil {
					t.Fatalf("expected error with %d underlying errors, got nil", tc.wantErrCount)
				}
				// errors.Join packages a slice; unwrap to count.
				unwrapped := unwrapJoined(err)
				if len(unwrapped) != tc.wantErrCount {
					t.Errorf("expected %d underlying errors, got %d: %v", tc.wantErrCount, len(unwrapped), unwrapped)
				}
			}

			if tc.validateEntry != nil && len(entries) == tc.wantEntries {
				tc.validateEntry(t, entries)
			}
		})
	}
}

// unwrapJoined extracts the slice of errors stored by errors.Join.
// errors.Join returns a *joinError that implements Unwrap() []error.
func unwrapJoined(err error) []error {
	type multiUnwrapper interface{ Unwrap() []error }
	if mu, ok := err.(multiUnwrapper); ok {
		return mu.Unwrap()
	}
	return []error{err}
}

func TestRunLogStore_AppendCancelledContext(t *testing.T) {
	store, _ := newRunLogStore(t)
	defer store.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := store.AppendStarted(ctx, StartedRun{V: 1, RunID: "x"}); err == nil {
		t.Error("expected error from AppendStarted on cancelled ctx")
	}
	if err := store.AppendFinished(ctx, FinishedRun{V: 1, RunID: "x"}); err == nil {
		t.Error("expected error from AppendFinished on cancelled ctx")
	}
	if err := store.Checkpoint(ctx); err == nil {
		t.Error("expected error from Checkpoint on cancelled ctx")
	}
}

func TestRunLogStore_AppendAfterClose(t *testing.T) {
	store, _ := newRunLogStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	err := store.AppendStarted(context.Background(), StartedRun{V: 1, RunID: "x"})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("AppendStarted: expected error containing 'closed', got %v", err)
	}
	err = store.AppendFinished(context.Background(), FinishedRun{V: 1, RunID: "x"})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("AppendFinished: expected error containing 'closed', got %v", err)
	}
	err = store.Checkpoint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("Checkpoint: expected error containing 'closed', got %v", err)
	}
}

func TestRunLogStore_CloseIdempotent(t *testing.T) {
	store, _ := newRunLogStore(t)
	if err := store.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("second close (should be idempotent): %v", err)
	}
}

func TestRunLogStore_Checkpoint(t *testing.T) {
	store, path := newRunLogStore(t)
	ctx := context.Background()

	startedAt := time.Date(2026, 6, 4, 10, 0, 0, 0, time.UTC)
	if err := store.AppendStarted(ctx, StartedRun{
		V: 1, FlashbackupVersion: "0.1.0-core", RunID: "ckpt-1",
		StartedAt: startedAt, Mode: "copy",
		SourceRoot: "/src", DestRoot: "/dst",
	}); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Verify the data hit the file. (We can't truly assert fsync without
	// crashing the kernel; this just confirms Checkpoint didn't lose data.)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Contains(data, []byte(`"run_id":"ckpt-1"`)) {
		t.Errorf("checkpointed data missing from file: %s", string(data))
	}
}

// TestRunLogStore_ConcurrentAppend asserts the documented thread-safety
// guarantee: many goroutines appending in parallel produce well-formed
// lines, no torn writes.
func TestRunLogStore_ConcurrentAppend(t *testing.T) {
	store, path := newRunLogStore(t)
	ctx := context.Background()

	const goroutines = 8
	const perG = 25
	errCh := make(chan error, goroutines*perG)
	done := make(chan struct{}, goroutines)

	for g := 0; g < goroutines; g++ {
		go func(g int) {
			defer func() { done <- struct{}{} }()
			for i := 0; i < perG; i++ {
				sr := StartedRun{
					V: 1, FlashbackupVersion: "0.1.0-core",
					RunID:      "rid",
					StartedAt:  time.Unix(int64(g*1000+i), 0).UTC(),
					Mode:       "copy",
					SourceRoot: "/src",
					DestRoot:   "/dst",
				}
				if err := store.AppendStarted(ctx, sr); err != nil {
					errCh <- err
				}
			}
		}(g)
	}
	for g := 0; g < goroutines; g++ {
		<-done
	}
	close(errCh)
	for err := range errCh {
		t.Errorf("concurrent append: %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("expected %d lines, got %d", goroutines*perG, len(lines))
	}
	for i, line := range lines {
		var sr StartedRun
		if err := json.Unmarshal([]byte(line), &sr); err != nil {
			t.Errorf("line %d not valid JSON: %v (line: %q)", i, err, line)
		}
	}
}

// TestReadRunLog_LineTooLong verifies that a line above the 256 KB cap
// returns a distinct bufio.ErrTooLong-wrapped error rather than silently
// truncating. AMENDMENT 2026-06-03.
func TestReadRunLog_LineTooLong(t *testing.T) {
	// 300 KB of 'x' inside a JSON string field — beyond the 256 KB cap.
	bigField := strings.Repeat("x", 300*1024)
	line := `{"v":1,"event":"started","run_id":"big","big_field":"` + bigField + `"}` + "\n"
	path := writeRunLogFixture(t, line)

	entries, err := ReadRunLog(path)
	if err == nil {
		t.Fatalf("expected error, got nil (entries=%d)", len(entries))
	}
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("expected errors.Is(err, bufio.ErrTooLong), got %v", err)
	}
	if !strings.Contains(err.Error(), "line exceeds") {
		t.Errorf("expected error to mention 'line exceeds', got %v", err)
	}
}

func TestReadRunLog_UnknownEventField(t *testing.T) {
	line := `{"v":1,"event":"weird","run_id":"x"}` + "\n"
	path := writeRunLogFixture(t, line)

	entries, err := ReadRunLog(path)
	if err == nil {
		t.Fatalf("expected error for unknown event, got nil")
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries, got %d", len(entries))
	}
	if !strings.Contains(err.Error(), `unknown event "weird"`) {
		t.Errorf("expected error mentioning unknown event 'weird', got %v", err)
	}
}

// TestReadRunLog_MissingFile verifies the open error is propagated cleanly.
func TestReadRunLog_MissingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.ndjson")
	entries, err := ReadRunLog(path)
	if err == nil {
		t.Fatalf("expected error for missing file, got entries=%+v", entries)
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected errors.Is(err, os.ErrNotExist), got %v", err)
	}
}

// TestStartedRun_JSONShape and TestFinishedRun_JSONShape lock the wire
// format. If either fails because of a field rename or tag change, bump
// StartedRun.V / FinishedRun.V and add a migration note before changing the
// assertion. Mirrors TestEvent_JSONShape.
func TestStartedRun_JSONShape(t *testing.T) {
	r := StartedRun{
		V:                  1,
		Event:              "started",
		FlashbackupVersion: "0.1.0-core",
		RunID:              "2026-06-03T1430Z-aaaa",
		StartedAt:          time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC),
		Mode:               "copy",
		Profile:            "my-docs",
		SourceRoot:         "/src",
		DestRoot:           "/dst",
	}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"event":"started","flashbackup_version":"0.1.0-core","run_id":"2026-06-03T1430Z-aaaa","started_at":"2026-06-03T14:30:00Z","mode":"copy","profile":"my-docs","source_root":"/src","dest_root":"/dst"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestFinishedRun_JSONShape(t *testing.T) {
	r := FinishedRun{
		V:                             1,
		Event:                         "finished",
		FlashbackupVersion:            "0.1.0-core",
		RunID:                         "2026-06-03T1430Z-aaaa",
		StartedAt:                     time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC),
		FinishedAt:                    time.Date(2026, 6, 3, 14, 50, 0, 0, time.UTC),
		Mode:                          "copy",
		SourceRoot:                    "/src",
		DestRoot:                      "/dst",
		FilesTotal:                    100,
		FilesSucceeded:                100,
		FilesFailed:                   0,
		BytesTotal:                    1000000,
		DeletionsSkippedDueToMutation: 0,
		ExitStatus:                    "ok",
	}
	got, err := json.Marshal(r)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"event":"finished","flashbackup_version":"0.1.0-core","run_id":"2026-06-03T1430Z-aaaa","started_at":"2026-06-03T14:30:00Z","finished_at":"2026-06-03T14:50:00Z","mode":"copy","source_root":"/src","dest_root":"/dst","files_total":100,"files_succeeded":100,"files_failed":0,"bytes_total":1000000,"deletions_skipped_due_to_mutation":0,"exit_status":"ok"}`
	if string(got) != want {
		t.Errorf("got %s\nwant %s", got, want)
	}
}
