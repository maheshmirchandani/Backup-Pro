package e2e

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// AssertManifestExists fails the test if the per-run manifest.ndjson.gz
// is absent under <usb>/.flashbackup/runs/<runID>/. Used by happy-path
// tests after RunBackup completes.
func AssertManifestExists(t *testing.T, usb, runID string) {
	t.Helper()
	manifestGz := filepath.Join(usb, ".flashbackup", "runs", runID, "manifest.ndjson.gz")
	if _, err := os.Stat(manifestGz); err != nil {
		t.Errorf("manifest.ndjson.gz missing at %s: %v", manifestGz, err)
	}
}

// AssertRunsNDJSONHasFinishedLine reads <usb>/.flashbackup/runs.ndjson
// and verifies that at least one line has event=="finished". Returns
// the run_id from the last finished line (callers typically need it to
// locate the per-run dir for further assertions).
func AssertRunsNDJSONHasFinishedLine(t *testing.T, usb string) string {
	t.Helper()
	runsPath := filepath.Join(usb, ".flashbackup", "runs.ndjson")
	lines := readNDJSON(t, runsPath)
	if len(lines) == 0 {
		t.Fatalf("runs.ndjson empty or absent at %s", runsPath)
	}
	var lastRunID string
	finished := false
	for _, l := range lines {
		if event, _ := l["event"].(string); event == "finished" {
			finished = true
			if id, ok := l["run_id"].(string); ok {
				lastRunID = id
			}
		}
	}
	if !finished {
		t.Errorf("runs.ndjson has no finished line: %v", lines)
	}
	if lastRunID == "" {
		t.Errorf("finished line missing run_id: %v", lines)
	}
	return lastRunID
}

// AssertVerifySummaryExists fails the test if no summary.json is found
// under <usb>/.flashbackup/runs/<runID>/verifications/*/summary.json.
// Returns the full path to the first summary it finds (callers
// typically only run verify once per run, so there's only one).
func AssertVerifySummaryExists(t *testing.T, usb, runID string) string {
	t.Helper()
	verifyDir := filepath.Join(usb, ".flashbackup", "runs", runID, "verifications")
	entries, err := os.ReadDir(verifyDir)
	if err != nil {
		t.Fatalf("read verifications dir %s: %v", verifyDir, err)
	}
	if len(entries) == 0 {
		t.Fatalf("no verifications subdirs under %s", verifyDir)
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		summaryPath := filepath.Join(verifyDir, e.Name(), "summary.json")
		if _, err := os.Stat(summaryPath); err == nil {
			return summaryPath
		}
	}
	t.Fatalf("no summary.json under %s", verifyDir)
	return ""
}

// FixtureTreeSHA256 computes the canonical SHA256-of-tree for the
// directory at root, matching the recipe in test/fixtures/<name>/
// MANIFEST.txt:
//
//  1. List every regular file under root, paths relative to root.
//  2. Skip MANIFEST.txt and any file under a directory named .git.
//  3. Sort the list lexicographically by raw bytes.
//  4. For each path: write the bytes of the relative path, one
//     newline (0x0a), the file contents, one newline (0x0a), into
//     a hash buffer.
//  5. SHA-256 the buffer; hex-encode.
//
// The framing newline after each chunk prevents the concat-collision
// attack where files A="x" + B="y" and A="xy" + B="" would otherwise
// share a hash. Tests use this helper to assert that a SeedSource'd
// tree's bytes match the MANIFEST line.
func FixtureTreeSHA256(t *testing.T, root string) string {
	t.Helper()
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() == "MANIFEST.txt" {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, rel)
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	sort.Strings(paths)

	h := sha256.New()
	for _, rel := range paths {
		if _, err := fmt.Fprintf(h, "%s\n", rel); err != nil {
			t.Fatalf("hash rel %q: %v", rel, err)
		}
		f, err := os.Open(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("open %s: %v", rel, err)
		}
		if _, err := io.Copy(h, f); err != nil {
			f.Close()
			t.Fatalf("copy %s: %v", rel, err)
		}
		f.Close()
		if _, err := h.Write([]byte{'\n'}); err != nil {
			t.Fatalf("hash trailing newline for %s: %v", rel, err)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// readNDJSON reads an NDJSON file and returns each non-empty line as
// a map[string]any. Mirrors readNDJSON in cmd/flashbackup/backup_test.go;
// duplicated because that one is in package main and cannot be imported.
func readNDJSON(t *testing.T, path string) []map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var out []map[string]any
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("parse %s line %q: %v", path, line, err)
		}
		out = append(out, m)
	}
	return out
}
