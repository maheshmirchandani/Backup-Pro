package plain

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// newTestRenderer is a typed-buffer constructor that returns both the
// renderer and the buffer the test asserts against. Pulled out so the
// table-driven event tests stay terse.
func newTestRenderer(t *testing.T, isTTY bool) (types.Renderer, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	return NewPlainRenderer(buf, isTTY), buf
}

// fixedTime gives every test the same timestamp so summary lines are
// reproducible without time.Now() flakiness.
var fixedTime = time.Date(2026, 6, 5, 14, 30, 0, 0, time.UTC)

// TestOnEvent_NonTTY_AllKinds is the golden table for the non-TTY mode.
// Every UIEventKind that the runner or rehash emits is exercised once,
// matching the format contract in renderer.go.
func TestOnEvent_NonTTY_AllKinds(t *testing.T) {
	cases := []struct {
		name string
		ev   types.UIEvent
		want string
	}{
		{
			name: "phase_started T0",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseStarted,
				Phase:     types.PhasePreflight,
				Timestamp: fixedTime,
			},
			want: "=> T0 preflight starting\n",
		},
		{
			name: "phase_started verify (non-runner phase)",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseStarted,
				Phase:     types.Phase("verify"),
				Timestamp: fixedTime,
			},
			want: "=> verify starting\n",
		},
		{
			name: "phase_completed ok",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseTransfer,
				Status:    "ok",
				Timestamp: fixedTime,
			},
			want: "OK T1 transfer\n",
		},
		{
			name: "phase_completed aborted with error",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseHashCompare,
				Status:    "aborted",
				Err:       errors.New("digest mismatch"),
				Timestamp: fixedTime,
			},
			want: "!! T2 hash compare aborted: digest mismatch\n",
		},
		{
			name: "phase_completed aborted without error",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseDelete,
				Status:    "aborted",
				Timestamp: fixedTime,
			},
			want: "!! T3 delete source aborted\n",
		},
		{
			name: "phase_completed skipped",
			ev: types.UIEvent{
				Kind:      types.UIEvtPhaseCompleted,
				Phase:     types.PhaseDelete,
				Status:    "skipped",
				Timestamp: fixedTime,
			},
			want: "-- T3 delete source skipped\n",
		},
		{
			name: "file_started non-TTY",
			ev: types.UIEvent{
				Kind:      types.UIEvtFileStarted,
				Phase:     types.PhaseTransfer,
				Path:      "Documents/big.mp4",
				Timestamp: fixedTime,
			},
			want: "   start Documents/big.mp4\n",
		},
		{
			name: "file_completed non-TTY with status",
			ev: types.UIEvent{
				Kind:      types.UIEvtFileCompleted,
				Phase:     types.PhaseHashCompare,
				Path:      "Documents/foo.pdf",
				Status:    "verified",
				Timestamp: fixedTime,
			},
			want: "   OK Documents/foo.pdf (verified)\n",
		},
		{
			name: "file_completed non-TTY no status",
			ev: types.UIEvent{
				Kind:      types.UIEvtFileCompleted,
				Phase:     types.PhaseTransfer,
				Path:      "Documents/bar.txt",
				Timestamp: fixedTime,
			},
			want: "   OK Documents/bar.txt\n",
		},
		{
			name: "file_failed with err",
			ev: types.UIEvent{
				Kind:      types.UIEvtFileFailed,
				Phase:     types.PhaseHashCompare,
				Path:      "Documents/baz.txt",
				Err:       errors.New("EIO: bad sector"),
				Timestamp: fixedTime,
			},
			want: "   !! Documents/baz.txt: EIO: bad sector\n",
		},
		{
			name: "file_failed no err",
			ev: types.UIEvent{
				Kind:      types.UIEvtFileFailed,
				Phase:     types.PhaseDelete,
				Path:      "Documents/qux.txt",
				Timestamp: fixedTime,
			},
			want: "   !! Documents/qux.txt\n",
		},
		{
			name: "progress non-TTY dropped",
			ev: types.UIEvent{
				Kind:  types.UIEvtProgress,
				Phase: types.PhaseTransfer,
				Progress: &types.ProgressInfo{
					BytesDone: 50, BytesTotal: 100,
					FilesDone: 3, FilesTotal: 10,
					CurrentFile: "Documents/a.txt", BytesPerSec: 2048,
				},
				Timestamp: fixedTime,
			},
			want: "",
		},
		{
			name: "prompt no newline",
			ev: types.UIEvent{
				Kind:      types.UIEvtPrompt,
				Phase:     types.PhaseDelete,
				Status:    "Type DELETE to confirm move:",
				Timestamp: fixedTime,
			},
			want: "Type DELETE to confirm move: ",
		},
		{
			name: "summary",
			ev: types.UIEvent{
				Kind:      types.UIEvtSummary,
				Status:    "ok",
				Timestamp: fixedTime,
			},
			want: "\nRun complete.\n" +
				"  exit status: ok\n" +
				"  finished at: 2026-06-05T14:30:00Z\n" +
				"  details: see <USB>/.flashbackup/runs/<RunID>/events.ndjson\n",
		},
		{
			name: "summary unknown status",
			ev: types.UIEvent{
				Kind:      types.UIEvtSummary,
				Status:    "",
				Timestamp: fixedTime,
			},
			want: "\nRun complete.\n" +
				"  exit status: (unknown)\n" +
				"  finished at: 2026-06-05T14:30:00Z\n" +
				"  details: see <USB>/.flashbackup/runs/<RunID>/events.ndjson\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, buf := newTestRenderer(t, false)
			if err := r.OnEvent(context.Background(), tc.ev); err != nil {
				t.Fatalf("OnEvent returned err: %v", err)
			}
			if got := buf.String(); got != tc.want {
				t.Errorf("OnEvent output mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestOnEvent_TTY_SuppressesPerFile pins the rule that TTY mode drops
// per-file started and per-file completed lines: the progress bar is the
// user's signal and per-file scroll at 50/sec is unreadable.
func TestOnEvent_TTY_SuppressesPerFile(t *testing.T) {
	cases := []types.UIEventKind{types.UIEvtFileStarted, types.UIEvtFileCompleted}
	for _, k := range cases {
		t.Run(string(k), func(t *testing.T) {
			r, buf := newTestRenderer(t, true)
			err := r.OnEvent(context.Background(), types.UIEvent{
				Kind: k, Phase: types.PhaseTransfer, Path: "x", Timestamp: fixedTime,
			})
			if err != nil {
				t.Fatalf("OnEvent returned err: %v", err)
			}
			if got := buf.String(); got != "" {
				t.Errorf("TTY mode should suppress %s, got %q", k, got)
			}
		})
	}
}

// TestOnEvent_TTY_FileFailedAlwaysEmits proves failures always surface
// regardless of mode (per renderer.go writeFileFailed comment).
func TestOnEvent_TTY_FileFailedAlwaysEmits(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtFileFailed, Phase: types.PhaseHashCompare,
		Path: "Documents/foo", Err: errors.New("boom"), Timestamp: fixedTime,
	})
	if err != nil {
		t.Fatalf("OnEvent returned err: %v", err)
	}
	if got := buf.String(); got != "   !! Documents/foo: boom\n" {
		t.Errorf("file_failed should always emit, got %q", got)
	}
}

// TestOnEvent_TTY_ProgressThrottle confirms the 10 Hz cap: 100 rapid
// progress events must produce at most 11 progress lines (the first one
// always fires; subsequent ones gated by 100ms).
func TestOnEvent_TTY_ProgressThrottle(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	progress := &types.ProgressInfo{
		BytesDone: 1, BytesTotal: 100,
		FilesDone: 1, FilesTotal: 1,
		CurrentFile: "foo", BytesPerSec: 1024,
	}
	for i := 0; i < 100; i++ {
		err := r.OnEvent(context.Background(), types.UIEvent{
			Kind: types.UIEvtProgress, Phase: types.PhaseTransfer,
			Progress: progress, Timestamp: fixedTime,
		})
		if err != nil {
			t.Fatalf("OnEvent iter %d: %v", i, err)
		}
	}
	// Each progress line starts with \r. Count them.
	got := strings.Count(buf.String(), "\r")
	if got > 11 {
		t.Errorf("throttle exceeded: %d progress lines (want <= 11)", got)
	}
	if got < 1 {
		t.Errorf("first progress line should always fire (got %d)", got)
	}
}

// TestOnEvent_TTY_ProgressThrottleResetsAfterWait confirms throttle is
// time-based, not count-based: after waiting > 100ms a second progress
// event fires.
func TestOnEvent_TTY_ProgressThrottleResetsAfterWait(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	progress := &types.ProgressInfo{
		BytesDone: 1, BytesTotal: 100, FilesDone: 1, FilesTotal: 1,
		CurrentFile: "foo", BytesPerSec: 1024,
	}
	ev := types.UIEvent{Kind: types.UIEvtProgress, Phase: types.PhaseTransfer, Progress: progress, Timestamp: fixedTime}
	if err := r.OnEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	if err := r.OnEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	count1 := strings.Count(buf.String(), "\r")
	time.Sleep(progressThrottle + 20*time.Millisecond)
	if err := r.OnEvent(context.Background(), ev); err != nil {
		t.Fatal(err)
	}
	count2 := strings.Count(buf.String(), "\r")
	if count1 != 1 {
		t.Errorf("expected exactly 1 progress line within throttle, got %d", count1)
	}
	if count2 != 2 {
		t.Errorf("expected 2 progress lines after wait, got %d", count2)
	}
}

// TestOnEvent_TTY_ProgressLineFlushedByNextEvent confirms that an event
// following a dangling progress line gets a leading \n so it lands on a
// fresh line instead of appending to the open progress line.
func TestOnEvent_TTY_ProgressLineFlushedByNextEvent(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	progress := &types.ProgressInfo{
		BytesDone: 50, BytesTotal: 100, FilesDone: 1, FilesTotal: 2,
		CurrentFile: "foo", BytesPerSec: 2048,
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtProgress, Phase: types.PhaseTransfer,
		Progress: progress, Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtPhaseCompleted, Phase: types.PhaseTransfer,
		Status: "ok", Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	// The output should contain a newline before the OK line so it does
	// not concatenate to the dangling progress line.
	if !strings.Contains(out, "\nOK T1 transfer\n") {
		t.Errorf("phase_completed after progress should land on its own line, got %q", out)
	}
}

// TestOnEvent_UnknownKind_FailsOpen confirms PS3 fail-open: an unknown
// UIEventKind produces a "??" line and a nil return. A panic or error
// here would defeat the contract that renderer errors never abort the run.
func TestOnEvent_UnknownKind_FailsOpen(t *testing.T) {
	r, buf := newTestRenderer(t, false)
	err := r.OnEvent(context.Background(), types.UIEvent{
		Kind:  types.UIEventKind("future_event_kind"),
		Phase: types.PhaseTransfer, Path: "x", Status: "y",
		Timestamp: fixedTime,
	})
	if err != nil {
		t.Errorf("unknown kind should not return error (PS3 fail-open), got %v", err)
	}
	if !strings.HasPrefix(buf.String(), "?? future_event_kind") {
		t.Errorf("unknown kind should produce ?? fallback line, got %q", buf.String())
	}
}

// errWriter is an io.Writer that always returns a sentinel error. Used to
// confirm OnEvent wraps and returns underlying Writer failures (the
// contract surface the runner's emitUI silently swallows).
type errWriter struct{}

var errSentinel = errors.New("disk full")

func (errWriter) Write(_ []byte) (int, error) {
	return 0, errSentinel
}

// TestOnEvent_WriterError_Propagates confirms that io.Writer errors are
// wrapped and returned so cmd can decide how to handle a broken terminal.
func TestOnEvent_WriterError_Propagates(t *testing.T) {
	r := NewPlainRenderer(errWriter{}, false)
	err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtPhaseStarted, Phase: types.PhasePreflight, Timestamp: fixedTime,
	})
	if err == nil {
		t.Fatal("expected error from failing writer, got nil")
	}
	if !errors.Is(err, errSentinel) {
		t.Errorf("OnEvent should wrap underlying error, got %v", err)
	}
}

// TestNewPlainRenderer_NilWriterPanics locks the documented contract that
// a nil io.Writer panics at construction. A nil writer is always a caller
// bug; surfacing it at the construction site rather than later in a phase
// emit is easier to debug.
func TestNewPlainRenderer_NilWriterPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("NewPlainRenderer(nil, _) should panic; did not")
		}
	}()
	_ = NewPlainRenderer(nil, false)
}

// concurrentLineWriter is a thread-safe writer that records every Write
// call as a separate entry. The concurrency test asserts that no entry
// contains a partial line from another goroutine.
type concurrentLineWriter struct {
	mu      sync.Mutex
	buf     bytes.Buffer
	written int64
}

func (w *concurrentLineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n, err := w.buf.Write(p)
	atomic.AddInt64(&w.written, int64(n))
	return n, err
}

// TestOnEvent_Concurrent_NoGarbling fires 100 goroutines emitting events
// in parallel; with -race this confirms no data race, and the asserted
// shape proves the mutex serializes lines so no event line is interleaved
// with another.
func TestOnEvent_Concurrent_NoGarbling(t *testing.T) {
	w := &concurrentLineWriter{}
	r := NewPlainRenderer(w, false)

	const goroutines = 100
	const perGoroutine = 10
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for g := 0; g < goroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < perGoroutine; i++ {
				_ = r.OnEvent(context.Background(), types.UIEvent{
					Kind:      types.UIEvtFileFailed,
					Phase:     types.PhaseHashCompare,
					Path:      fmt.Sprintf("g%d/f%d", id, i),
					Err:       fmt.Errorf("error from %d/%d", id, i),
					Timestamp: fixedTime,
				})
			}
		}(g)
	}
	wg.Wait()

	// Parse the output: every non-empty line must match the file_failed
	// shape. An interleaved write would produce a line with two "   !!"
	// prefixes or split a path mid-string.
	lines := strings.Split(w.buf.String(), "\n")
	gotLines := 0
	for _, line := range lines {
		if line == "" {
			continue
		}
		gotLines++
		if !strings.HasPrefix(line, "   !! g") {
			t.Errorf("garbled line: %q", line)
		}
		// Confirm the line carries exactly one path / one error
		// suffix shape. A double-emit would show "/f" twice.
		if strings.Count(line, "/f") != 1 {
			t.Errorf("possibly interleaved line: %q", line)
		}
	}
	want := goroutines * perGoroutine
	if gotLines != want {
		t.Errorf("got %d lines, want %d", gotLines, want)
	}
}

// TestPhaseLabel_AllWireStrings catches drift between the runner Phase
// wire-string contract and the renderer's translation table. A missing
// case here would surface in user-facing output as a raw "T1" instead of
// the human-readable label.
func TestPhaseLabel_AllWireStrings(t *testing.T) {
	cases := map[types.Phase]string{
		types.PhasePreflight:   "T0 preflight",
		types.PhaseEnumerate:   "T0+ enumerate",
		types.PhaseTransfer:    "T1 transfer",
		types.PhaseHashCompare: "T2 hash compare",
		types.PhaseDelete:      "T3 delete source",
		types.PhaseFinalize:    "T4 finalize",
		types.Phase("verify"):  "verify",
		types.Phase(""):        "(unknown phase)",
	}
	for p, want := range cases {
		if got := phaseLabel(p); got != want {
			t.Errorf("phaseLabel(%q) = %q, want %q", string(p), got, want)
		}
	}
	// Forward-compat passthrough: an unknown wire string is rendered
	// verbatim so future phases land in operator output before this
	// table is updated.
	if got := phaseLabel(types.Phase("T7-future")); got != "T7-future" {
		t.Errorf("phaseLabel passthrough failed, got %q", got)
	}
}

// TestPercent_EdgeCases covers div-by-zero and overflow clamps. The
// progress bar must not show a "100%" for an empty run nor a negative
// number for a stale counter.
func TestPercent_EdgeCases(t *testing.T) {
	cases := []struct {
		done, total int64
		want        int
	}{
		{0, 0, 0},
		{0, 100, 0},
		{50, 100, 50},
		{100, 100, 100},
		{200, 100, 100},
		{-1, 100, 0},
		{50, -1, 0},
	}
	for _, tc := range cases {
		if got := percent(tc.done, tc.total); got != tc.want {
			t.Errorf("percent(%d, %d) = %d, want %d", tc.done, tc.total, got, tc.want)
		}
	}
}

// TestFormatSpeed_AllUnits pins the unit-thresholds. A 1023 B/s rate
// must render as B/s, not as "1.0 KiB/s" via flooring.
func TestFormatSpeed_AllUnits(t *testing.T) {
	cases := []struct {
		bps  int64
		want string
	}{
		{0, ""},
		{-1, ""},
		{500, "500 B/s"},
		{1024, "1.0 KiB/s"},
		{1024 * 1024, "1.0 MiB/s"},
		{1024 * 1024 * 1024, "1.0 GiB/s"},
		{5 * 1024 * 1024 * 1024, "5.0 GiB/s"},
	}
	for _, tc := range cases {
		if got := formatSpeed(tc.bps); got != tc.want {
			t.Errorf("formatSpeed(%d) = %q, want %q", tc.bps, got, tc.want)
		}
	}
}

// TestOnEvent_ProgressNilProgressInfo confirms defensive handling: a
// progress event with nil ProgressInfo is a caller bug; the renderer
// drops it rather than panicking on a nil deref.
func TestOnEvent_ProgressNilProgressInfo(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtProgress, Phase: types.PhaseTransfer,
		Progress: nil, Timestamp: fixedTime,
	})
	if err != nil {
		t.Errorf("nil ProgressInfo should drop silently, got err %v", err)
	}
	if buf.Len() != 0 {
		t.Errorf("nil ProgressInfo should produce no output, got %q", buf.String())
	}
}

// TestOnEvent_PhaseCompleted_UnknownStatus exercises the forward-compat
// branch: a Status value the renderer does not classify still produces a
// line (with a "**" sigil) so the operator sees the unknown shape rather
// than silence.
func TestOnEvent_PhaseCompleted_UnknownStatus(t *testing.T) {
	r, buf := newTestRenderer(t, false)
	err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtPhaseCompleted, Phase: types.PhaseTransfer,
		Status: "future_status", Timestamp: fixedTime,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := buf.String(); got != "** T1 transfer future_status\n" {
		t.Errorf("unknown phase status should emit ** line, got %q", got)
	}
}

// TestOnEvent_SummaryAfterProgress confirms the summary block flushes a
// dangling progress line first (leading \n on the block).
func TestOnEvent_SummaryAfterProgress(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	progress := &types.ProgressInfo{
		BytesDone: 100, BytesTotal: 100, FilesDone: 1, FilesTotal: 1,
		CurrentFile: "foo", BytesPerSec: 1024,
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtProgress, Phase: types.PhaseTransfer,
		Progress: progress, Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtSummary, Status: "ok", Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\n\nRun complete.") {
		t.Errorf("summary after progress should flush dangling line, got %q", buf.String())
	}
}

// TestOnEvent_PromptAfterProgress confirms the prompt path also flushes
// the dangling progress line.
func TestOnEvent_PromptAfterProgress(t *testing.T) {
	r, buf := newTestRenderer(t, true)
	progress := &types.ProgressInfo{
		BytesDone: 100, BytesTotal: 100, FilesDone: 1, FilesTotal: 1,
		CurrentFile: "foo", BytesPerSec: 1024,
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtProgress, Phase: types.PhaseTransfer,
		Progress: progress, Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.OnEvent(context.Background(), types.UIEvent{
		Kind: types.UIEvtPrompt, Phase: types.PhaseDelete,
		Status: "Type DELETE:", Timestamp: fixedTime,
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "\nType DELETE: ") {
		t.Errorf("prompt after progress should flush dangling line, got %q", buf.String())
	}
}
