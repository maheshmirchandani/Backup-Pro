package state

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// canonicalEventKinds is the source-of-truth list of every state.Event.Kind
// the FlashBackup core engine knows about. Pulled from the "Canonical Event
// Kinds" table in docs/planning/2026-06-03-flashbackup-core-engine.md.
//
// Kinds marked "emitted in v0.1" are written by the shipping engine today;
// kinds marked "queued" are documented for follow-up tasks (Task 22a, 50a)
// and are NOT yet emitted, but they remain part of the canonical wire
// contract so downstream parsers and operator docs can refer to them.
//
// Adding a new Kind requires three changes:
//  1. emit the Kind from the runner phase that owns it,
//  2. add the Kind here,
//  3. add a "## <kind>" entry to docs/ERROR_CATALOG.md.
//
// The TestEventCatalog_* tests below enforce that 2 and 3 stay in lock-step.
var canonicalEventKinds = []string{
	// Run-level
	"phase_started",
	"phase_completed",
	"phase_aborted",
	"run_finished",
	// T0 preflight (lock_*, filesystem_refused, volume_uuid_changed queued for Task 22a/50a)
	"lock_acquired",
	"lock_stale_detected",
	"lock_contention",
	"filesystem_refused",
	"volume_uuid_changed",
	// T0+ enumerate
	"file_enumerated",
	// T1 transfer
	"transfer_started",
	"transfer_completed",
	"transfer_failed",
	// T2 hash-compare
	"file_completed",
	"hash_mismatch",
	"source_mutated",
	// T3 delete-source
	"atomic_gate_blocked",
	"delete_completed",
	"delete_skipped_mutated",
	"delete_failed",
	// T4 finalize
	"manifest_finalized",
}

// TestEventCatalog_ForwardCoverage asserts every canonical Event Kind has
// a "## <kind>" heading in docs/ERROR_CATALOG.md. Prevents drift where a
// new Kind ships without operator-facing documentation.
//
// Catalog completeness contract per master plan Task 53.
func TestEventCatalog_ForwardCoverage(t *testing.T) {
	catalog := readCatalog(t)
	for _, kind := range canonicalEventKinds {
		heading := "### `" + kind + "`"
		if !strings.Contains(catalog, heading) {
			t.Errorf("docs/ERROR_CATALOG.md missing %q heading for canonical Event Kind %q",
				heading, kind)
		}
	}
}

// TestEventCatalog_ReverseCoverage asserts every "### `<kind>`" heading in
// docs/ERROR_CATALOG.md corresponds to a canonical Event Kind. Prevents
// drift where a Kind is removed from the wire but its catalog entry
// lingers and confuses operators.
//
// Catalog completeness contract per master plan Task 53 (reverse direction).
func TestEventCatalog_ReverseCoverage(t *testing.T) {
	catalog := readCatalog(t)
	known := make(map[string]struct{}, len(canonicalEventKinds))
	for _, kind := range canonicalEventKinds {
		known[kind] = struct{}{}
	}

	// Match "### `<kind>`" headings at start-of-line. Anchoring on (?m)^
	// avoids picking up inline-formatted code-spans inside paragraphs that
	// happen to use the same backtick syntax.
	headingRE := regexp.MustCompile(`(?m)^### ` + "`" + `([a-z_]+)` + "`")
	matches := headingRE.FindAllStringSubmatch(catalog, -1)
	if len(matches) == 0 {
		t.Fatal("no '### `<kind>`' headings found in docs/ERROR_CATALOG.md; " +
			"either the heading style changed or the file is empty")
	}

	var orphans []string
	for _, m := range matches {
		kind := m[1]
		if _, ok := known[kind]; !ok {
			orphans = append(orphans, kind)
		}
	}
	if len(orphans) > 0 {
		sort.Strings(orphans)
		t.Errorf("docs/ERROR_CATALOG.md has entries for non-canonical Event Kinds %v; "+
			"either add them to canonicalEventKinds or remove the catalog entries",
			orphans)
	}
}

// TestEventCatalog_CodeEmittedKindsAreCanonical scans the internal/runner
// package for string-literal "Kind:" assignments inside state.Event struct
// literals, and asserts every emitted kind is in canonicalEventKinds. This
// is the strict half of the forward contract: any kind the code can WRITE
// must be known to the catalog list.
//
// Limitation: only catches string-literal kinds. Computed kinds (via
// variable or constant) would slip through; none exist in the current
// engine, but if one is introduced, add it explicitly to the regex or
// extend this test to walk the AST.
func TestEventCatalog_CodeEmittedKindsAreCanonical(t *testing.T) {
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	runnerDir := filepath.Join(repoRoot, "internal", "runner")

	known := make(map[string]struct{}, len(canonicalEventKinds))
	for _, kind := range canonicalEventKinds {
		known[kind] = struct{}{}
	}

	// Match `Kind:      "some_kind",` style assignments (variable whitespace,
	// snake_case lowercase identifier). Captures the kind string.
	kindRE := regexp.MustCompile(`Kind:\s*"([a-z_]+)"`)

	emitted := make(map[string]struct{})
	err = filepath.Walk(runnerDir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			return nil
		}
		// Skip test files: test fixtures may reference future / synthetic
		// kinds (e.g., "future_event_kind" in renderer_test.go) that are
		// intentionally outside the canonical set.
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, m := range kindRE.FindAllStringSubmatch(string(data), -1) {
			emitted[m[1]] = struct{}{}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk runner dir: %v", err)
	}

	if len(emitted) == 0 {
		t.Fatal("no string-literal Event.Kind values found under internal/runner; " +
			"either the regex went stale or the runner phases stopped emitting events")
	}

	var unknown []string
	for kind := range emitted {
		if _, ok := known[kind]; !ok {
			unknown = append(unknown, kind)
		}
	}
	if len(unknown) > 0 {
		sort.Strings(unknown)
		t.Errorf("internal/runner emits Event.Kind values not in canonicalEventKinds %v; "+
			"add them to canonicalEventKinds AND docs/ERROR_CATALOG.md",
			unknown)
	}
}

// readCatalog returns the contents of docs/ERROR_CATALOG.md or fails the
// test with a clear message. Centralized so all three contract tests share
// one I/O path.
func readCatalog(t *testing.T) string {
	t.Helper()
	repoRoot, err := findRepoRoot()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	catalogPath := filepath.Join(repoRoot, "docs", "ERROR_CATALOG.md")
	data, err := os.ReadFile(catalogPath)
	if err != nil {
		t.Fatalf("read %s: %v", catalogPath, err)
	}
	return string(data)
}

// findRepoRoot walks up from the current working directory looking for
// go.mod, which marks the FlashBackup repo root. Used so the catalog tests
// can locate docs/ERROR_CATALOG.md regardless of which package directory
// `go test` runs from.
func findRepoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
