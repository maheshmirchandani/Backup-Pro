//go:build faultinject

package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func resetActiveForTest(t *testing.T) {
	t.Helper()
	Activate(nil)
	t.Cleanup(func() { Activate(nil) })
}

func TestParse_HappyPath(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Fault
	}{
		{
			name: "corrupt-T1-file",
			in:   "corrupt:phase=T1:file=foo.pdf",
			want: Fault{Action: ActionCorrupt, Phase: "T1", File: "foo.pdf"},
		},
		{
			name: "kill-T1-after-pct",
			in:   "kill:phase=T1:after_pct=50",
			want: Fault{Action: ActionKill, Phase: "T1", AfterPct: 50},
		},
		{
			name: "kill-T2-file",
			in:   "kill:phase=T2:file=foo.pdf",
			want: Fault{Action: ActionKill, Phase: "T2", File: "foo.pdf"},
		},
		{
			name: "kill-T3-after-count",
			in:   "kill:phase=T3:after_count=10",
			want: Fault{Action: ActionKill, Phase: "T3", AfterCount: 10},
		},
		{
			name: "mutate-source-T2-pre",
			in:   "mutate-source:phase=T2-pre:file=foo.pdf",
			want: Fault{Action: ActionMutateSource, Phase: "T2-pre", File: "foo.pdf"},
		},
		{
			name: "mutate-source-T3-pre",
			in:   "mutate-source:phase=T3-pre:file=foo.pdf",
			want: Fault{Action: ActionMutateSource, Phase: "T3-pre", File: "foo.pdf"},
		},
		{
			name: "unmount-T1",
			in:   "unmount:phase=T1",
			want: Fault{Action: ActionUnmount, Phase: "T1"},
		},
		{
			name: "disk-full-T1",
			in:   "disk-full:phase=T1",
			want: Fault{Action: ActionDiskFull, Phase: "T1"},
		},
		{
			name: "permission-denied-T3",
			in:   "permission-denied:phase=T3:file=foo.pdf",
			want: Fault{Action: ActionPermissionDenied, Phase: "T3", File: "foo.pdf"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse([]string{tc.in})
			if err != nil {
				t.Fatalf("Parse(%q) error: %v", tc.in, err)
			}
			if len(got) != 1 {
				t.Fatalf("Parse(%q) returned %d faults, want 1", tc.in, len(got))
			}
			if !reflect.DeepEqual(got[0], tc.want) {
				t.Fatalf("Parse(%q) = %+v, want %+v", tc.in, got[0], tc.want)
			}
		})
	}
}

func TestParse_RejectsUnknownAction(t *testing.T) {
	_, err := Parse([]string{"explode:phase=T1"})
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	var bad *ErrInvalidSpec
	if !errors.As(err, &bad) {
		t.Fatalf("Parse: want *ErrInvalidSpec, got %T", err)
	}
	if !strings.Contains(bad.Error(), "explode") {
		t.Fatalf("Parse: error should mention 'explode': %v", bad)
	}
}

func TestParse_RejectsUnknownKeyword(t *testing.T) {
	_, err := Parse([]string{"corrupt:phase=T1:zorch=42"})
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	if !strings.Contains(err.Error(), "zorch") {
		t.Fatalf("Parse: error should mention 'zorch': %v", err)
	}
}

func TestParse_RejectsMutuallyExclusive(t *testing.T) {
	_, err := Parse([]string{"kill:phase=T1:after_pct=50:after_count=10"})
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
	if !strings.Contains(err.Error(), "mutually exclusive") {
		t.Fatalf("Parse: error should mention mutual exclusion: %v", err)
	}
}

func TestParse_RejectsEmptyPhase(t *testing.T) {
	_, err := Parse([]string{"corrupt:phase="})
	if err == nil {
		t.Fatal("Parse: want error, got nil")
	}
}

func TestParse_RejectsPctOutOfRange(t *testing.T) {
	for _, s := range []string{"kill:phase=T1:after_pct=0", "kill:phase=T1:after_pct=101"} {
		if _, err := Parse([]string{s}); err == nil {
			t.Fatalf("Parse(%q): want error, got nil", s)
		}
	}
}

func TestParse_RejectsZeroCount(t *testing.T) {
	if _, err := Parse([]string{"kill:phase=T1:after_count=0"}); err == nil {
		t.Fatal("Parse: want error, got nil")
	}
}

func TestParse_RejectsMissingPhase(t *testing.T) {
	if _, err := Parse([]string{"kill:file=foo.pdf"}); err == nil {
		t.Fatal("Parse: want error, got nil")
	}
}

func TestParse_OrderPreserved(t *testing.T) {
	in := []string{
		"corrupt:phase=T1:file=a",
		"kill:phase=T2:after_count=5",
		"mutate-source:phase=T3-pre:file=b",
	}
	got, err := Parse(in)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Parse returned %d, want 3", len(got))
	}
	if got[0].Action != ActionCorrupt || got[1].Action != ActionKill || got[2].Action != ActionMutateSource {
		t.Fatalf("Parse order wrong: %+v", got)
	}
}

func TestParse_EmptyInputReturnsNil(t *testing.T) {
	got, err := Parse(nil)
	if err != nil || got != nil {
		t.Fatalf("Parse(nil) = (%v, %v), want (nil, nil)", got, err)
	}
	got, err = Parse([]string{})
	if err != nil || got != nil {
		t.Fatalf("Parse([]) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestHook_NoActiveFaults(t *testing.T) {
	resetActiveForTest(t)
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"}); err != nil {
		t.Fatalf("Hook with no active faults: %v", err)
	}
}

func TestHook_RespectsContext(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"kill:phase=T1"})
	Activate(faults)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := Hook(ctx, PointT1Progress, HookArgs{Phase: "T1"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Hook on cancelled ctx: got %v, want context.Canceled", err)
	}
}

func TestHook_KillFiresOnPhaseAndFileMatch(t *testing.T) {
	resetActiveForTest(t)
	faults, err := Parse([]string{"kill:phase=T2:file=foo.pdf"})
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	Activate(faults)

	if err := Hook(context.Background(), PointT2PerFile, HookArgs{Phase: "T2", CurrentFile: "bar.pdf"}); err != nil {
		t.Fatalf("Hook with file mismatch: got %v, want nil", err)
	}
	// re-arm: file-mismatch should not have fired, so the fault is still armed
	if err := Hook(context.Background(), PointT2PerFile, HookArgs{Phase: "T2", CurrentFile: "foo.pdf"}); !errors.Is(err, ErrFaultKill) {
		t.Fatalf("Hook with matching file: got %v, want ErrFaultKill", err)
	}
	// one-shot: second call should be a no-op
	if err := Hook(context.Background(), PointT2PerFile, HookArgs{Phase: "T2", CurrentFile: "foo.pdf"}); err != nil {
		t.Fatalf("Hook second call: got %v, want nil", err)
	}
}

func TestHook_KillFiresOnPhaseOnlyWhenFileEmpty(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"kill:phase=T1"})
	Activate(faults)
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T2"}); err != nil {
		t.Fatalf("Hook with phase mismatch: %v", err)
	}
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", CurrentFile: "anything"}); !errors.Is(err, ErrFaultKill) {
		t.Fatalf("Hook with phase match: got %v, want ErrFaultKill", err)
	}
}

func TestHook_KillFiresAfterPct(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"kill:phase=T1:after_pct=50"})
	Activate(faults)
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", BytesDone: 49, BytesTotal: 100}); err != nil {
		t.Fatalf("Hook below threshold: %v", err)
	}
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", BytesDone: 50, BytesTotal: 100}); !errors.Is(err, ErrFaultKill) {
		t.Fatalf("Hook at threshold: got %v, want ErrFaultKill", err)
	}
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", BytesDone: 100, BytesTotal: 100}); err != nil {
		t.Fatalf("Hook one-shot violated: got %v, want nil", err)
	}
}

func TestHook_AfterPctSkipsWhenBytesTotalZero(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"kill:phase=T1:after_pct=50"})
	Activate(faults)
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", BytesDone: 0, BytesTotal: 0}); err != nil {
		t.Fatalf("Hook with zero total: %v", err)
	}
}

func TestHook_KillFiresAfterCount(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"kill:phase=T3:after_count=10"})
	Activate(faults)
	if err := Hook(context.Background(), PointT3PerFile, HookArgs{Phase: "T3", FilesDone: 9}); err != nil {
		t.Fatalf("Hook below count: %v", err)
	}
	if err := Hook(context.Background(), PointT3PerFile, HookArgs{Phase: "T3", FilesDone: 10}); !errors.Is(err, ErrFaultKill) {
		t.Fatalf("Hook at count: got %v, want ErrFaultKill", err)
	}
	if err := Hook(context.Background(), PointT3PerFile, HookArgs{Phase: "T3", FilesDone: 11}); err != nil {
		t.Fatalf("Hook one-shot violated: %v", err)
	}
}

func TestHook_KillTestOverride(t *testing.T) {
	resetActiveForTest(t)
	sentinel := errors.New("test-replaced-kill")
	prev := SetKillActionForTest(func(_ context.Context, _ HookArgs) error { return sentinel })
	t.Cleanup(func() { SetKillActionForTest(prev) })
	faults, _ := Parse([]string{"kill:phase=T1"})
	Activate(faults)
	err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"})
	if !errors.Is(err, sentinel) {
		t.Fatalf("Hook with override: got %v, want %v", err, sentinel)
	}
}

func TestHook_PermissionDenied(t *testing.T) {
	resetActiveForTest(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "fixture")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	faults, _ := Parse([]string{"permission-denied:phase=T3:file=fixture"})
	Activate(faults)
	t.Cleanup(func() { _ = RunCleanups() })

	if err := Hook(context.Background(), PointT3PerFile, HookArgs{Phase: "T3", CurrentFile: "fixture", DestRoot: dir}); err != nil {
		t.Fatalf("Hook: %v", err)
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0 {
		t.Fatalf("perm after fault: got %o, want 0", info.Mode().Perm())
	}
	if err := RunCleanups(); err != nil {
		t.Fatalf("RunCleanups: %v", err)
	}
	info, err = os.Stat(target)
	if err != nil {
		t.Fatalf("stat after cleanup: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("perm after cleanup: got %o, want 0600", info.Mode().Perm())
	}
}

func TestHook_Corrupt(t *testing.T) {
	resetActiveForTest(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "fixture")
	original := []byte{0x10, 0x20, 0x30}
	if err := os.WriteFile(target, original, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	faults, _ := Parse([]string{"corrupt:phase=T1:file=fixture"})
	Activate(faults)

	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1", CurrentFile: "fixture", DestRoot: dir}); err != nil {
		t.Fatalf("Hook: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("readback: %v", err)
	}
	if got[0] == original[0] {
		t.Fatalf("byte 0 unchanged after corrupt: got %x", got[0])
	}
	if got[1] != original[1] || got[2] != original[2] {
		t.Fatalf("corrupt touched bytes beyond offset 0: got %x", got)
	}
}

func TestHook_MutateSource(t *testing.T) {
	resetActiveForTest(t)
	dir := t.TempDir()
	target := filepath.Join(dir, "fixture")
	if err := os.WriteFile(target, []byte("payload"), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	beforeInfo, _ := os.Stat(target)
	faults, _ := Parse([]string{"mutate-source:phase=T2-pre:file=fixture"})
	Activate(faults)

	if err := Hook(context.Background(), PointT2PreHash, HookArgs{Phase: "T2-pre", CurrentFile: "fixture", SourceRoot: dir}); err != nil {
		t.Fatalf("Hook: %v", err)
	}
	afterInfo, _ := os.Stat(target)
	if afterInfo.Size() == beforeInfo.Size() {
		t.Fatalf("mutate-source did not grow file: before=%d after=%d", beforeInfo.Size(), afterInfo.Size())
	}
}

func TestHook_DiskFullRequiresDestRoot(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"disk-full:phase=T1"})
	Activate(faults)
	err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"})
	if err == nil || !strings.Contains(err.Error(), "DestRoot") {
		t.Fatalf("Hook without DestRoot: got %v, want DestRoot error", err)
	}
}

func TestHook_UnmountRequiresDestRoot(t *testing.T) {
	resetActiveForTest(t)
	faults, _ := Parse([]string{"unmount:phase=T1"})
	Activate(faults)
	err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"})
	if err == nil || !strings.Contains(err.Error(), "DestRoot") {
		t.Fatalf("Hook without DestRoot: got %v, want DestRoot error", err)
	}
}

func TestRunCleanups_LIFO(t *testing.T) {
	// Drain any leftovers from earlier tests, then verify LIFO order.
	_ = RunCleanups()
	var order []int
	RegisterCleanup(func() error { order = append(order, 1); return nil })
	RegisterCleanup(func() error { order = append(order, 2); return nil })
	RegisterCleanup(func() error { order = append(order, 3); return nil })
	if err := RunCleanups(); err != nil {
		t.Fatalf("RunCleanups: %v", err)
	}
	if !reflect.DeepEqual(order, []int{3, 2, 1}) {
		t.Fatalf("cleanup order: got %v, want [3 2 1]", order)
	}
}

func TestActivate_NilClears(t *testing.T) {
	faults, _ := Parse([]string{"kill:phase=T1"})
	Activate(faults)
	Activate(nil)
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"}); err != nil {
		t.Fatalf("Hook after Activate(nil): %v", err)
	}
}
