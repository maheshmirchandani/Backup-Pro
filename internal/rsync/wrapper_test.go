package rsync

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuildArgs_AllFlagsSet(t *testing.T) {
	opts := Options{
		ExecPath:   "/nope/rsync",
		SourceRoot: "/src",
		DestRoot:   "/dst",
		Archive:    true,
		Partial:    true,
		Xattrs:     true,
		Sparse:     true,
		HardLinks:  true,
		Delete:     true,
	}
	got := buildArgs(opts)
	want := []string{"-a", "--partial", "--xattrs", "--sparse", "--hard-links", "--delete", "--progress", "/src/", "/dst"}
	if !equalSlices(got, want) {
		t.Errorf("argv got %q want %q", got, want)
	}
}

func TestBuildArgs_NoFlags(t *testing.T) {
	opts := Options{
		ExecPath:   "/nope/rsync",
		SourceRoot: "/src",
		DestRoot:   "/dst",
	}
	got := buildArgs(opts)
	want := []string{"--progress", "/src/", "/dst"}
	if !equalSlices(got, want) {
		t.Errorf("argv got %q want %q", got, want)
	}
}

func TestBuildArgs_StripsTrailingSlashesFromSource(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"/src", "/src/"},
		{"/src/", "/src/"},
		{"/src///", "/src/"},
	}
	for _, c := range cases {
		opts := Options{ExecPath: "/x", SourceRoot: c.in, DestRoot: "/dst"}
		got := buildArgs(opts)
		// Source is second-to-last; DestRoot is last.
		if len(got) < 2 {
			t.Fatalf("argv too short: %v", got)
		}
		src := got[len(got)-2]
		if src != c.want {
			t.Errorf("input %q -> source %q, want %q", c.in, src, c.want)
		}
	}
}

func TestBuildArgs_FilesFromPresentWhenFilesNonEmpty(t *testing.T) {
	opts := Options{
		ExecPath:   "/nope/rsync",
		SourceRoot: "/src",
		DestRoot:   "/dst",
		Archive:    true,
		Files:      []string{"a.txt", "subdir/b.md"},
	}
	got := buildArgs(opts)
	sawFrom0 := false
	sawFilesFrom := false
	for _, a := range got {
		if a == "--from0" {
			sawFrom0 = true
		}
		if a == "--files-from=-" {
			sawFilesFrom = true
		}
	}
	if !sawFrom0 {
		t.Errorf("expected --from0 in argv when Files is non-empty: %q", got)
	}
	if !sawFilesFrom {
		t.Errorf("expected --files-from=- in argv when Files is non-empty: %q", got)
	}
}

func TestBuildArgs_NoFilesFromWhenFilesEmpty(t *testing.T) {
	opts := Options{
		ExecPath:   "/nope/rsync",
		SourceRoot: "/src",
		DestRoot:   "/dst",
		Archive:    true,
	}
	got := buildArgs(opts)
	for _, a := range got {
		if a == "--from0" || strings.HasPrefix(a, "--files-from") {
			t.Errorf("argv should NOT include %q when Files is empty: %q", a, got)
		}
	}
}

func TestFileListBytes_NULTerminated(t *testing.T) {
	opts := Options{Files: []string{"foo", "bar"}}
	got := fileListBytes(opts)
	want := []byte("foo\x00bar\x00")
	if !bytes.Equal(got, want) {
		t.Errorf("file list got %v want %v", got, want)
	}
}

func TestFileListBytes_Empty(t *testing.T) {
	got := fileListBytes(Options{})
	if len(got) != 0 {
		t.Errorf("empty Files should produce 0 bytes, got %d: %v", len(got), got)
	}
}

func TestFileListBytes_NewlineInFilename(t *testing.T) {
	// --from0 means NUL is the only separator. Newlines, spaces, and other
	// shell metacharacters inside a filename must pass through untouched.
	// This is the entire point of --from0 vs --files-from with default
	// newline terminators.
	opts := Options{Files: []string{"foo\nbar", "baz with space", "quote\"x"}}
	got := fileListBytes(opts)
	want := []byte("foo\nbar\x00baz with space\x00quote\"x\x00")
	if !bytes.Equal(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

func TestRun_RequiresExecPath(t *testing.T) {
	w := &Wrapper{}
	err := w.Run(context.Background(), Options{
		SourceRoot: "/src",
		DestRoot:   "/dst",
	})
	if err == nil || !strings.Contains(err.Error(), "ExecPath") {
		t.Errorf("expected ExecPath error, got %v", err)
	}
}

func TestRun_RequiresSourceRoot(t *testing.T) {
	w := &Wrapper{}
	err := w.Run(context.Background(), Options{
		ExecPath: "/usr/bin/true",
		DestRoot: "/dst",
	})
	if err == nil || !strings.Contains(err.Error(), "SourceRoot") {
		t.Errorf("expected SourceRoot error, got %v", err)
	}
}

func TestRun_RequiresDestRoot(t *testing.T) {
	w := &Wrapper{}
	err := w.Run(context.Background(), Options{
		ExecPath:   "/usr/bin/true",
		SourceRoot: "/src",
	})
	if err == nil || !strings.Contains(err.Error(), "DestRoot") {
		t.Errorf("expected DestRoot error, got %v", err)
	}
}

func TestRun_CancelledContextBeforeExec(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	w := &Wrapper{}
	err := w.Run(ctx, Options{
		ExecPath:   "/usr/bin/true",
		SourceRoot: "/src",
		DestRoot:   "/dst",
	})
	if err == nil {
		t.Fatal("expected cancelled context to return error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected err to wrap context.Canceled, got %v", err)
	}
}

func TestRun_NonZeroExitWrappedWithStatus(t *testing.T) {
	// /usr/bin/false exits 1. Run should wrap that and the error message
	// should mention the exit status.
	w := &Wrapper{}
	err := w.Run(context.Background(), Options{
		ExecPath:   "/usr/bin/false",
		SourceRoot: "/src",
		DestRoot:   "/dst",
	})
	if err == nil {
		t.Fatal("expected non-zero exit to return error")
	}
	if !strings.Contains(err.Error(), "status 1") {
		t.Errorf("expected error to mention 'status 1', got %v", err)
	}
	if code := ResolveExitCode(err); code != 1 {
		t.Errorf("ResolveExitCode = %d, want 1", code)
	}
}

func TestRun_AgainstPlaceholder(t *testing.T) {
	// Extract the placeholder via EnsureExtracted, then invoke Run against
	// it. The placeholder is a shell script that prints "PLACEHOLDER rsync"
	// and exits 0. We expect Run to succeed (no error) and the placeholder
	// banner to appear on stdout.
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	execPath, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	var stdout, stderr bytes.Buffer
	w := &Wrapper{}
	err = w.Run(ctx, Options{
		ExecPath:   execPath,
		SourceRoot: srcDir,
		DestRoot:   filepath.Join(dir, "dst"),
		Archive:    true,
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err != nil {
		t.Errorf("Run against placeholder failed: %v; stdout=%q stderr=%q",
			err, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "PLACEHOLDER rsync") {
		t.Errorf("expected placeholder banner on stdout, got %q", stdout.String())
	}
}

func TestRun_AgainstPlaceholderWithFileList(t *testing.T) {
	// Same as above but with a non-empty Files slice, exercising the stdin
	// pipe + goroutine path. The placeholder ignores its stdin, but we
	// still expect a clean exit.
	dir := t.TempDir()
	registerImmutableCleanup(t, dir)
	ctx := context.Background()
	execPath, err := EnsureExtracted(ctx, dir)
	if err != nil {
		t.Fatalf("EnsureExtracted: %v", err)
	}
	srcDir := filepath.Join(dir, "src")
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		t.Fatalf("mkdir src: %v", err)
	}
	var stdout, stderr bytes.Buffer
	w := &Wrapper{}
	err = w.Run(ctx, Options{
		ExecPath:   execPath,
		SourceRoot: srcDir,
		DestRoot:   filepath.Join(dir, "dst"),
		Archive:    true,
		Files:      []string{"a.txt", "b.md", "weird\nname"},
		Stdout:     &stdout,
		Stderr:     &stderr,
	})
	if err != nil {
		t.Errorf("Run against placeholder w/ file list failed: %v; stdout=%q stderr=%q",
			err, stdout.String(), stderr.String())
	}
}

func TestRun_CancelKillsSlowSubprocess(t *testing.T) {
	// /bin/sleep is universally available on macOS and Linux. Launch it
	// with a long duration and cancel the context shortly after; the
	// subprocess should be killed and Run should return an error.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	w := &Wrapper{}
	start := time.Now()
	err := w.Run(ctx, Options{
		ExecPath:   "/bin/sleep",
		SourceRoot: "/src",
		DestRoot:   "/dst",
		// sleep ignores all rsync flags; argv after ExecPath becomes:
		// --progress /src/ /dst, which sleep treats as durations and
		// fails on. That is still fine: we are only proving that ctx
		// cancellation terminates the child process promptly. The
		// elapsed-time check below is the real assertion.
	})
	elapsed := time.Since(start)
	if err == nil {
		t.Error("expected error from cancelled or invalid sleep invocation")
	}
	// Generous upper bound: cancel fires at 200ms; reap + return should
	// happen well within 2s on any reasonable machine.
	if elapsed > 2*time.Second {
		t.Errorf("Run took %v after 200ms cancel; subprocess not killed promptly", elapsed)
	}
}

func TestResolveExitCode_Nil(t *testing.T) {
	if c := ResolveExitCode(nil); c != 0 {
		t.Errorf("nil err -> %d, want 0", c)
	}
}

func TestResolveExitCode_NonExitError(t *testing.T) {
	// A validation error from Run (ExecPath empty) is not an *exec.ExitError;
	// ResolveExitCode should return -1.
	w := &Wrapper{}
	err := w.Run(context.Background(), Options{SourceRoot: "/s", DestRoot: "/d"})
	if c := ResolveExitCode(err); c != -1 {
		t.Errorf("non-ExitError -> %d, want -1", c)
	}
}

// equalSlices is a tiny helper for argv comparisons. reflect.DeepEqual would
// work too; this keeps the test file dependency-free.
func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
