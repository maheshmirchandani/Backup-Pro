//go:build !faultinject

package runner

import (
	"context"
	"errors"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestRelease_Parse_EmptyOK(t *testing.T) {
	got, err := Parse(nil)
	if err != nil || got != nil {
		t.Fatalf("Parse(nil) = (%v, %v), want (nil, nil)", got, err)
	}
	got, err = Parse([]string{})
	if err != nil || got != nil {
		t.Fatalf("Parse([]) = (%v, %v), want (nil, nil)", got, err)
	}
}

func TestRelease_Parse_NonEmptyRefused(t *testing.T) {
	got, err := Parse([]string{"corrupt:phase=T1"})
	if got != nil {
		t.Fatalf("Parse(non-empty): want nil slice, got %v", got)
	}
	if !errors.Is(err, ErrFaultinjectStripped) {
		t.Fatalf("Parse(non-empty): want ErrFaultinjectStripped, got %v", err)
	}
}

func TestRelease_Activate_NoOp(t *testing.T) {
	// Should not panic, should not retain anything observable. The only
	// observable in the release build is that Hook still returns nil.
	Activate([]Fault{{Action: ActionKill, Phase: "T1"}})
	if err := Hook(context.Background(), PointT1Progress, HookArgs{Phase: "T1"}); err != nil {
		t.Fatalf("Hook after Activate (release): %v", err)
	}
	Activate(nil)
}

func TestRelease_Hook_AlwaysNil(t *testing.T) {
	allActions := []Action{
		ActionCorrupt, ActionKill, ActionMutateSource,
		ActionUnmount, ActionDiskFull, ActionPermissionDenied,
	}
	for _, a := range allActions {
		args := HookArgs{
			Phase:       "T1",
			CurrentFile: "any",
			FilesDone:   100,
			FilesTotal:  100,
			BytesDone:   1 << 30,
			BytesTotal:  1 << 30,
			DestRoot:    "/dev/null",
			SourceRoot:  "/dev/null",
		}
		Activate([]Fault{{Action: a, Phase: "T1"}})
		if err := Hook(context.Background(), PointT1Progress, args); err != nil {
			t.Fatalf("Hook(%s) release: got %v, want nil", a, err)
		}
	}
}

func TestRelease_HookSourceLocation(t *testing.T) {
	// Proves the symbol-scan gate's premise: Hook resolves to the release
	// stub file in builds without the faultinject tag. We use reflect on the
	// Hook function value to recover the *runtime.Func, then ask it for the
	// (file, line) of its entry PC.
	v := reflect.ValueOf(Hook)
	pc := v.Pointer()
	fn := runtime.FuncForPC(pc)
	if fn == nil {
		t.Fatal("FuncForPC returned nil for Hook")
	}
	file, _ := fn.FileLine(pc)
	if !strings.HasSuffix(file, "faultinject_release.go") {
		t.Fatalf("Hook source file: got %q, want suffix faultinject_release.go", file)
	}
}

func TestRelease_SentinelsDefined(t *testing.T) {
	// Callers compile against ErrFaultKill, ErrFaultDiskFull,
	// ErrFaultinjectStripped under either build tag. Confirm the sentinels
	// are non-nil error values in the release build too.
	for name, e := range map[string]error{
		"ErrFaultinjectStripped": ErrFaultinjectStripped,
		"ErrFaultKill":           ErrFaultKill,
		"ErrFaultDiskFull":       ErrFaultDiskFull,
	} {
		if e == nil {
			t.Fatalf("%s is nil in release build", name)
		}
		if e.Error() == "" {
			t.Fatalf("%s has empty message", name)
		}
	}
}
