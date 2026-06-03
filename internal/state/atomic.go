package state

import (
	"fmt"
	"os"
	"path/filepath"
)

// WriteTmpThenRename writes data to path+".tmp" with the given mode, fsyncs the
// file, renames to path, and fsyncs the parent dir. Standard atomic-write
// pattern: a crash at any point leaves either the old file or the new file
// intact, never a partial write.
//
// Used by manifest finalization, version.json writes, and profile writes
// (Tasks 6, 7, 9). Lives in the state package because all callers depend on it
// for invariant #4 (no partial files on disk).
func WriteTmpThenRename(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	// fsync parent dir to durably persist the rename
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	return nil
}
