package types

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

// TestPhase_StringValues confirms every Phase constant has the exact wire
// string the spec requires. The strings are persisted in events.ndjson and
// rendered to the operator; drift breaks both renderers and on-disk parsers.
func TestPhase_StringValues(t *testing.T) {
	cases := []struct {
		got  Phase
		want string
	}{
		{PhasePreflight, "T0"},
		{PhaseEnumerate, "T0+"},
		{PhaseTransfer, "T1"},
		{PhaseHashCompare, "T2"},
		{PhaseDelete, "T3"},
		{PhaseFinalize, "T4"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("Phase %v = %q; want %q", c.got, string(c.got), c.want)
		}
	}
}

// TestPhase_NoDuplicateStrings catches accidental copy-paste errors where two
// Phase constants share the same wire string. A duplicate would silently
// corrupt phase telemetry.
func TestPhase_NoDuplicateStrings(t *testing.T) {
	all := []Phase{
		PhasePreflight, PhaseEnumerate, PhaseTransfer,
		PhaseHashCompare, PhaseDelete, PhaseFinalize,
	}
	seen := make(map[string]Phase, len(all))
	for _, p := range all {
		if dup, ok := seen[string(p)]; ok {
			t.Errorf("duplicate Phase string %q used by %v and %v", string(p), dup, p)
		}
		seen[string(p)] = p
	}
}

func TestMode_StringValues(t *testing.T) {
	cases := []struct {
		got  Mode
		want string
	}{
		{ModeCopy, "copy"},
		{ModeMove, "move"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("Mode %v = %q; want %q", c.got, string(c.got), c.want)
		}
	}
}

func TestMode_NoDuplicateStrings(t *testing.T) {
	all := []Mode{ModeCopy, ModeMove}
	seen := make(map[string]Mode, len(all))
	for _, m := range all {
		if dup, ok := seen[string(m)]; ok {
			t.Errorf("duplicate Mode string %q used by %v and %v", string(m), dup, m)
		}
		seen[string(m)] = m
	}
}

func TestUIEventKind_StringValues(t *testing.T) {
	cases := []struct {
		got  UIEventKind
		want string
	}{
		{UIEvtPhaseStarted, "phase_started"},
		{UIEvtPhaseCompleted, "phase_completed"},
		{UIEvtFileStarted, "file_started"},
		{UIEvtFileCompleted, "file_completed"},
		{UIEvtFileFailed, "file_failed"},
		{UIEvtProgress, "progress"},
		{UIEvtPrompt, "prompt"},
		{UIEvtSummary, "summary"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("UIEventKind %v = %q; want %q", c.got, string(c.got), c.want)
		}
	}
}

func TestUIEventKind_NoDuplicateStrings(t *testing.T) {
	all := []UIEventKind{
		UIEvtPhaseStarted, UIEvtPhaseCompleted,
		UIEvtFileStarted, UIEvtFileCompleted, UIEvtFileFailed,
		UIEvtProgress, UIEvtPrompt, UIEvtSummary,
	}
	seen := make(map[string]UIEventKind, len(all))
	for _, k := range all {
		if dup, ok := seen[string(k)]; ok {
			t.Errorf("duplicate UIEventKind string %q used by %v and %v", string(k), dup, k)
		}
		seen[string(k)] = k
	}
}

// TestRunOptions_ZeroValue confirms a zero-valued RunOptions is safe to
// construct (no init-required fields, no panic on field reads). Runner code
// often layers options over a zero base; a panic here would block construction.
func TestRunOptions_ZeroValue(t *testing.T) {
	var o RunOptions
	_ = o.DestRoot
	_ = o.Mode
	_ = o.DryRun
	_ = o.Delete
	_ = o.UIRenderer
	_ = o.Profile
}

// TestRunResult_ZeroValue mirrors the RunOptions zero-value test. RunResult
// is the orchestrator return type; the runner builds it up across phases
// from a zero base, so the type system must accept that pattern.
func TestRunResult_ZeroValue(t *testing.T) {
	var r RunResult
	if r.RunID != "" {
		t.Errorf("zero RunResult.RunID = %q; want empty", r.RunID)
	}
	if !r.StartedAt.IsZero() || !r.FinishedAt.IsZero() {
		t.Error("zero RunResult timestamps should be zero")
	}
	if r.FilesTotal != 0 || r.FilesSucceeded != 0 || r.FilesFailed != 0 {
		t.Error("zero RunResult counters should be 0")
	}
	if r.BytesTotal != 0 {
		t.Error("zero RunResult.BytesTotal should be 0")
	}
	if r.DeletionsSkippedDueToMutation != 0 {
		t.Error("zero RunResult.DeletionsSkippedDueToMutation should be 0")
	}
	if r.ExitStatus != "" {
		t.Errorf("zero RunResult.ExitStatus = %q; want empty", r.ExitStatus)
	}
}

func TestUIEvent_ZeroValue(t *testing.T) {
	var ev UIEvent
	_ = ev.Kind
	_ = ev.Phase
	_ = ev.Path
	_ = ev.Status
	_ = ev.Err
	if ev.Progress != nil {
		t.Error("zero UIEvent.Progress should be nil (pointer field)")
	}
	if !ev.Timestamp.IsZero() {
		t.Error("zero UIEvent.Timestamp should be zero")
	}
}

func TestProgressInfo_ZeroValue(t *testing.T) {
	var p ProgressInfo
	if p.BytesDone != 0 || p.BytesTotal != 0 || p.FilesDone != 0 || p.FilesTotal != 0 {
		t.Error("zero ProgressInfo counters should be 0")
	}
	if p.CurrentFile != "" {
		t.Error("zero ProgressInfo.CurrentFile should be empty")
	}
	if p.BytesPerSec != 0 || p.ETASeconds != 0 {
		t.Error("zero ProgressInfo rate/eta should be 0")
	}
}

// TestSignature_ZeroAndEquality ensures Signature is a plain comparable value
// type. The T3 mutation gate (invariant #8) re-stats source and uses == to
// detect change; if Signature ever becomes non-comparable, the gate breaks.
func TestSignature_ZeroAndEquality(t *testing.T) {
	var z Signature
	if z.Size != 0 || z.MtimeNS != 0 {
		t.Error("zero Signature must be (0, 0)")
	}
	a := Signature{Size: 100, MtimeNS: 1718000000000000000}
	b := Signature{Size: 100, MtimeNS: 1718000000000000000}
	c := Signature{Size: 100, MtimeNS: 1718000000000000001}
	if a != b {
		t.Error("equal Signatures must compare equal")
	}
	if a == c {
		t.Error("Signatures differing in MtimeNS must compare unequal")
	}
}

// noopRenderer is a compile-time witness that the Renderer interface is
// implementable with a simple value receiver. If Renderer ever drifts (e.g.,
// adds a method, changes a signature) this file stops compiling, which is
// the desired behavior.
type noopRenderer struct {
	calls int
}

func (n *noopRenderer) OnEvent(_ context.Context, _ UIEvent) error {
	n.calls++
	return nil
}

// Compile-time check that *noopRenderer satisfies Renderer. The blank-var
// trick is the idiomatic Go way to lock an interface contract in a test.
var _ Renderer = (*noopRenderer)(nil)

// TestRenderer_InterfaceShape exercises the renderer surface end-to-end so a
// future reviewer can see the intended call pattern in the test file.
func TestRenderer_InterfaceShape(t *testing.T) {
	var r Renderer = &noopRenderer{}
	ev := UIEvent{
		Kind:      UIEvtFileCompleted,
		Phase:     PhaseHashCompare,
		Path:      "Documents/foo.pdf",
		Status:    string(UIEvtFileCompleted),
		Timestamp: time.Now(),
	}
	if err := r.OnEvent(context.Background(), ev); err != nil {
		t.Errorf("noopRenderer.OnEvent returned err: %v", err)
	}
	if got := r.(*noopRenderer).calls; got != 1 {
		t.Errorf("noopRenderer.calls = %d; want 1", got)
	}
}

// TestUIEvent_ErrFieldRoundTrip confirms the Err field on UIEvent preserves
// the error identity (no string-only conversion). Renderers like the TUI may
// type-switch on errors.Is or errors.As to surface specific failure modes.
func TestUIEvent_ErrFieldRoundTrip(t *testing.T) {
	sentinel := errors.New("disk full")
	ev := UIEvent{Kind: UIEvtFileFailed, Err: sentinel}
	if !errors.Is(ev.Err, sentinel) {
		t.Error("UIEvent.Err lost error identity")
	}
}

// TestUIEvent_ProgressPointer documents intent: Progress is a pointer so
// non-progress events (phase_started, prompt, summary) carry a nil. If this
// ever becomes a value type, every event allocates 48 bytes of unused
// counters, which matters at the 50-events-per-second rate the spec targets.
func TestUIEvent_ProgressPointer(t *testing.T) {
	ev := UIEvent{Kind: UIEvtPhaseStarted}
	if ev.Progress != nil {
		t.Error("phase_started event must carry nil Progress")
	}
	p := &ProgressInfo{BytesDone: 1024, BytesTotal: 2048}
	ev2 := UIEvent{Kind: UIEvtProgress, Progress: p}
	if ev2.Progress == nil || ev2.Progress.BytesDone != 1024 {
		t.Error("progress event lost its ProgressInfo")
	}
	// Confirm we are storing by pointer, not by copy: the pointer in the
	// event must address the same memory as the one the caller passed in.
	if reflect.ValueOf(ev2.Progress).Pointer() != reflect.ValueOf(p).Pointer() {
		t.Error("UIEvent.Progress should hold the original pointer, not a copy")
	}
}
