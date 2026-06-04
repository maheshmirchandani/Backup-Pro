package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteTmpThenRename_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	data := []byte(`{"hello":"world"}`)
	if err := WriteTmpThenRename(path, data, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != string(data) {
		t.Errorf("got %q want %q", got, data)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp still present after rename")
	}
}

func TestWriteTmpThenRename_Overwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := WriteTmpThenRename(path, []byte("first"), 0644); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if err := WriteTmpThenRename(path, []byte("second"), 0644); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := os.ReadFile(path)
	if string(got) != "second" {
		t.Errorf("got %q want %q", got, "second")
	}
}

func TestWriteTmpThenRename_PermissionMode(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "f.json")
	if err := WriteTmpThenRename(path, []byte("x"), 0600); err != nil {
		t.Fatalf("write: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("got mode %o want 0600", info.Mode().Perm())
	}
}

func TestWriteTmpThenRename_DirMissing(t *testing.T) {
	// Parent dir doesn't exist; tmp create should fail with wrapped error
	err := WriteTmpThenRename("/nonexistent/dir/file.json", []byte("x"), 0644)
	if err == nil {
		t.Fatal("expected error for missing parent dir")
	}
	if !strings.Contains(err.Error(), "create tmp") {
		t.Errorf("expected wrapped 'create tmp' error, got %q", err)
	}
}
