package symlink

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// realTempDir returns t.TempDir() with all leading symlinks resolved. On
// macOS, t.TempDir() returns a path under "/var/folders/..." but "/var" is
// itself a symlink to "/private/var", which would trip the symlink-refusal
// gate. We resolve once at the test root so each test exercises the gate
// against its own constructed symlinks, not the host filesystem's.
func realTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("EvalSymlinks(%q): %v", dir, err)
	}
	return resolved
}

func TestWalkAndBaseline_HappyPath(t *testing.T) {
	dir := realTempDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := WalkAndBaseline(context.Background(), sub)
	if err != nil {
		t.Fatalf("WalkAndBaseline: %v", err)
	}
	if len(base.Components) == 0 {
		t.Error("baseline components empty")
	}
	if base.Components[0].Path != "/" {
		t.Errorf("first component = %q, want /", base.Components[0].Path)
	}
	last := base.Components[len(base.Components)-1]
	if last.Path != sub {
		t.Errorf("last component = %q, want %q", last.Path, sub)
	}
	if !last.IsDir {
		t.Errorf("last component IsDir = false, want true")
	}
}

func TestWalkAndBaseline_RejectsRelativePath(t *testing.T) {
	_, err := WalkAndBaseline(context.Background(), "relative/path")
	if err == nil {
		t.Fatal("expected error for relative path")
	}
	if !strings.Contains(err.Error(), "absolute") {
		t.Errorf("error should mention 'absolute': %v", err)
	}
}

func TestWalkAndBaseline_RefusesSymlinkComponent(t *testing.T) {
	dir := realTempDir(t)
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	linkPath := filepath.Join(dir, "link")
	if err := os.Symlink(realDir, linkPath); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	deeper := filepath.Join(linkPath, "child")
	if err := os.MkdirAll(deeper, 0700); err != nil {
		t.Fatal(err)
	}
	_, err := WalkAndBaseline(context.Background(), deeper)
	if err == nil {
		t.Fatal("expected SymlinkError")
	}
	var symErr *SymlinkError
	if !errors.As(err, &symErr) {
		t.Errorf("expected SymlinkError, got %T: %v", err, err)
	}
	if symErr.Component != linkPath {
		t.Errorf("offending component = %q, want %q", symErr.Component, linkPath)
	}
	if !errors.Is(err, ErrSymlinkInPath) {
		t.Errorf("expected errors.Is(err, ErrSymlinkInPath) to be true")
	}
}

func TestVerify_NoChange(t *testing.T) {
	dir := realTempDir(t)
	base, err := WalkAndBaseline(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := Verify(context.Background(), dir, base); err != nil {
		t.Errorf("Verify on unchanged path: %v", err)
	}
}

func TestVerify_DetectsReplacedDirectory(t *testing.T) {
	dir := realTempDir(t)
	sub := filepath.Join(dir, "sub")
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := WalkAndBaseline(context.Background(), sub)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(sub); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sub, 0700); err != nil {
		t.Fatal(err)
	}
	err = Verify(context.Background(), sub, base)
	if err == nil {
		t.Fatal("expected ComponentChangedError after replacing directory")
	}
	if !errors.Is(err, ErrComponentChanged) {
		t.Errorf("expected ErrComponentChanged, got %v", err)
	}
}

func TestVerify_DetectsSymlinkSwap(t *testing.T) {
	dir := realTempDir(t)
	realDir := filepath.Join(dir, "real")
	if err := os.MkdirAll(realDir, 0700); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(realDir, "target")
	if err := os.MkdirAll(target, 0700); err != nil {
		t.Fatal(err)
	}
	base, err := WalkAndBaseline(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(realDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("/tmp", realDir); err != nil {
		t.Skipf("symlink not supported: %v", err)
	}
	err = Verify(context.Background(), target, base)
	if err == nil {
		t.Fatal("expected verify failure after symlink swap")
	}
}

func TestSplitComponents(t *testing.T) {
	got := splitComponents("/Volumes/USB/.flashbackup")
	want := []string{"/", "/Volumes", "/Volumes/USB", "/Volumes/USB/.flashbackup"}
	if len(got) != len(want) {
		t.Fatalf("got %d components, want %d: %v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i] != w {
			t.Errorf("[%d] got %q want %q", i, got[i], w)
		}
	}
}

func TestWalkAndBaseline_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := WalkAndBaseline(ctx, "/")
	if err == nil {
		t.Fatal("expected cancelled ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
}
