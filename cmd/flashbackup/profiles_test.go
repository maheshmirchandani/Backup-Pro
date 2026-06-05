package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// ----------------------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------------------

// profilesUseTempUSB creates a host-temp directory that stands in for a
// mounted USB. profiles operations only touch <dest>/.flashbackup/profiles.json
// (no rsync, no immutable bits) so the e2e DMG dance is unnecessary for the
// CRUD unit tests; the .flashbackup subdir is created lazily by Store.NewStore.
func profilesUseTempUSB(t *testing.T) string {
	t.Helper()
	return t.TempDir()
}

// profilesSeed plants a profile directly via the Store so a test can assert
// on the subsequent CLI behaviour against a known starting state.
func profilesSeed(t *testing.T, usb string, p profiles.Profile) {
	t.Helper()
	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		t.Fatalf("NewStore: %v", err)
	}
	if err := store.Upsert(p); err != nil {
		t.Fatalf("seed Upsert: %v", err)
	}
}

// profilesSetEditorOverride installs the test editor seam and registers a
// t.Cleanup to reset it. Tests use a callback that reads the file, applies a
// transformation, and writes it back to simulate the operator's edits.
func profilesSetEditorOverride(t *testing.T, fn func([]byte) []byte) {
	t.Helper()
	editorRunOverrideForTest = func(path string) error {
		in, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read: %w", err)
		}
		out := fn(in)
		return os.WriteFile(path, out, 0o600)
	}
	t.Cleanup(func() { editorRunOverrideForTest = nil })
}

// ----------------------------------------------------------------------------
// Argv parsing + dispatcher
// ----------------------------------------------------------------------------

// TestProfiles_NoActionArg: bare `flashbackup profiles` must exit 2 with the
// usage block surfaced on stderr. Catches the operator who typed the
// subcommand and forgot the action verb.
func TestProfiles_NoActionArg(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles"})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "missing <action>") {
		t.Errorf("stderr should explain missing action, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestProfiles_UnknownAction: a misspelled verb (e.g. `lst`) trips the
// dispatcher default arm.
func TestProfiles_UnknownAction(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "lst", "/tmp"})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "unknown action") {
		t.Errorf("stderr should mention 'unknown action', got %q", stderr)
	}
	if !strings.Contains(stderr, "\"lst\"") {
		t.Errorf("stderr should quote the offending token, got %q", stderr)
	}
}

// TestProfiles_HelpFlag: --help and -h print usage to stdout, exit 0.
func TestProfiles_HelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", flag})
			if code != profilesExitCodeOK {
				t.Errorf("exit code: got %d, want 0", code)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty on --help, got %q", stderr)
			}
			if !strings.Contains(stdout, "Usage:") {
				t.Errorf("stdout should contain usage block, got %q", stdout)
			}
			if !strings.Contains(stdout, "profiles list") {
				t.Errorf("stdout should list the actions, got %q", stdout)
			}
		})
	}
}

// TestProfiles_ArgvParsing covers the shared <profile-name> <USB-path>
// positional shape across new / edit / delete / validate. Each action arm
// must reject the same set of bad arg counts identically.
func TestProfiles_ArgvParsing(t *testing.T) {
	usb := profilesUseTempUSB(t)
	cases := []struct {
		name   string
		argv   []string
		want   int
		stderr string
	}{
		{"new no name", []string{"flashbackup", "profiles", "new"}, profilesExitCodeUsage, "requires <profile-name>"},
		{"new only name", []string{"flashbackup", "profiles", "new", "foo"}, profilesExitCodeUsage, "requires <profile-name>"},
		{"new too many", []string{"flashbackup", "profiles", "new", "foo", usb, "extra"}, profilesExitCodeUsage, "unexpected extra arguments"},
		{"edit no name", []string{"flashbackup", "profiles", "edit"}, profilesExitCodeUsage, "requires <profile-name>"},
		{"delete no name", []string{"flashbackup", "profiles", "delete"}, profilesExitCodeUsage, "requires <profile-name>"},
		{"validate no name", []string{"flashbackup", "profiles", "validate"}, profilesExitCodeUsage, "requires <profile-name>"},
		{"list no path", []string{"flashbackup", "profiles", "list"}, profilesExitCodeUsage, "missing <USB-path>"},
		{"list too many", []string{"flashbackup", "profiles", "list", usb, "extra"}, profilesExitCodeUsage, "unexpected extra arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			code, _, stderr := runCapture(t, tc.argv)
			if code != tc.want {
				t.Errorf("exit code: got %d, want %d", code, tc.want)
			}
			if !strings.Contains(stderr, tc.stderr) {
				t.Errorf("stderr should contain %q, got %q", tc.stderr, stderr)
			}
		})
	}
}

// TestProfiles_NonexistentPath: every action with a USB path that does not
// resolve should exit 2 from the EvalSymlinks step. Matches init / backup /
// verify / status.
func TestProfiles_NonexistentPath(t *testing.T) {
	cases := [][]string{
		{"flashbackup", "profiles", "list", "/nonexistent/never/will-exist-profiles-test"},
		{"flashbackup", "profiles", "new", "foo", "/nonexistent/never/will-exist-profiles-test"},
		{"flashbackup", "profiles", "delete", "foo", "/nonexistent/never/will-exist-profiles-test"},
	}
	for _, argv := range cases {
		t.Run(argv[2], func(t *testing.T) {
			code, _, stderr := runCapture(t, argv)
			if code != profilesExitCodeUsage {
				t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
			}
			if !strings.Contains(stderr, "flashbackup profiles") {
				t.Errorf("stderr should be prefixed with 'flashbackup profiles', got %q", stderr)
			}
		})
	}
}

// TestRunProfiles_DirectCall_NoStdin exercises runProfiles directly with a
// nil stdin to confirm the parameter is accepted but not consumed. Matches
// the verify / status subcommand's handler-signature regression guard.
func TestRunProfiles_DirectCall_NoStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runProfiles(context.Background(), []string{}, nil, &stdout, &stderr)
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d (missing action)", code, profilesExitCodeUsage)
	}
}

// ----------------------------------------------------------------------------
// list
// ----------------------------------------------------------------------------

// TestProfiles_ListEmptyPlain: a fresh USB has no profiles; plain mode emits
// the "no profiles yet" anchor.
func TestProfiles_ListEmptyPlain(t *testing.T) {
	usb := profilesUseTempUSB(t)
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "list", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "no profiles yet") {
		t.Errorf("stdout should mention 'no profiles yet', got %q", stdout)
	}
}

// TestProfiles_ListEmptyJSON: --json on a fresh USB emits `[]`, never
// `null`, so downstream `jq length` always sees a number.
func TestProfiles_ListEmptyJSON(t *testing.T) {
	usb := profilesUseTempUSB(t)
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "list", usb, "--json"})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	var parsed []any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout)
	}
	if len(parsed) != 0 {
		t.Errorf("len: got %d, want 0", len(parsed))
	}
}

// TestProfiles_ListPlain: seed two profiles, assert each one's name appears
// on its own line with source + count labels.
func TestProfiles_ListPlain(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "alpha", Source: "/src/a",
		Includes: []string{"*.txt"}, Excludes: []string{"x.log"},
	})
	profilesSeed(t, usb, profiles.Profile{
		Name: "bravo", Source: "/src/b",
		Includes: []string{"*"},
	})
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "list", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("stdout should mention 'alpha', got %q", stdout)
	}
	if !strings.Contains(stdout, "bravo") {
		t.Errorf("stdout should mention 'bravo', got %q", stdout)
	}
	if !strings.Contains(stdout, "source=/src/a") {
		t.Errorf("stdout should show source for alpha, got %q", stdout)
	}
	if !strings.Contains(stdout, "includes=1") {
		t.Errorf("stdout should show include count, got %q", stdout)
	}
	if !strings.Contains(stdout, "excludes=0") {
		t.Errorf("stdout should show zero excludes for bravo, got %q", stdout)
	}
}

// TestProfiles_ListJSON: seed two profiles, assert the JSON array structure
// and per-item keys match the documented schema.
func TestProfiles_ListJSON(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "alpha", Source: "/src/a",
		Includes: []string{"*.txt"}, Excludes: []string{"x.log"},
	})
	profilesSeed(t, usb, profiles.Profile{
		Name: "bravo", Source: "/src/b",
		Includes: []string{"*"},
	})
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "list", "--json", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	var parsed []map[string]any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout)
	}
	if len(parsed) != 2 {
		t.Fatalf("len: got %d, want 2\nstdout=%s", len(parsed), stdout)
	}
	// Store.List returns alphabetical order; alpha < bravo.
	if n, _ := parsed[0]["name"].(string); n != "alpha" {
		t.Errorf("parsed[0].name: got %q, want alpha", n)
	}
	if s, _ := parsed[0]["source"].(string); s != "/src/a" {
		t.Errorf("parsed[0].source: got %q, want /src/a", s)
	}
	for _, want := range []string{"name", "source", "includes", "excludes"} {
		if _, ok := parsed[0][want]; !ok {
			t.Errorf("parsed[0] missing key %q", want)
		}
	}
	// bravo has no excludes seeded; the JSON shape must still surface an
	// empty array, never null.
	excludes, ok := parsed[1]["excludes"].([]any)
	if !ok {
		t.Errorf("parsed[1].excludes: not an array (got %T)", parsed[1]["excludes"])
	}
	if len(excludes) != 0 {
		t.Errorf("parsed[1].excludes: got %v, want []", excludes)
	}
}

// ----------------------------------------------------------------------------
// new
// ----------------------------------------------------------------------------

// TestProfiles_NewHappyPath: the editor override rewrites the skeleton into
// a valid profile; new must Upsert it + exit 0 + emit the "created" line.
func TestProfiles_NewHappyPath(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSetEditorOverride(t, func(in []byte) []byte {
		var p profiles.Profile
		if err := json.Unmarshal(in, &p); err != nil {
			t.Fatalf("editor override: unmarshal skeleton: %v", err)
		}
		p.Source = "/Users/me/Docs"
		p.Includes = []string{"*.md"}
		p.Excludes = []string{".DS_Store"}
		out, err := json.MarshalIndent(p, "", "  ")
		if err != nil {
			t.Fatalf("editor override: marshal: %v", err)
		}
		return out
	})

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "new", "docs", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "created profile") {
		t.Errorf("stdout should mention 'created profile', got %q", stdout)
	}
	// Round-trip via Get to confirm Upsert landed.
	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := store.Get("docs")
	if err != nil {
		t.Fatalf("Get docs: %v", err)
	}
	if got.Source != "/Users/me/Docs" {
		t.Errorf("Source: got %q, want %q", got.Source, "/Users/me/Docs")
	}
}

// TestProfiles_NewAlreadyExists: a name collision against a seeded profile
// must exit 2 + tell the operator to use `edit` instead.
func TestProfiles_NewAlreadyExists(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "docs", Source: "/Users/me/Docs",
	})
	// Editor override should never be invoked; install one that fails the
	// test if it is, so a regression that calls the editor before the
	// collision check trips.
	profilesSetEditorOverride(t, func(in []byte) []byte {
		t.Errorf("editor should not run when profile already exists")
		return in
	})

	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "new", "docs", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr should mention 'already exists', got %q", stderr)
	}
}

// TestProfiles_NewEditorEmptiesFile: the override writes an empty buffer
// (simulating the operator who saved an empty file). Parse fails; new exits
// 2 with an "operator-fixable" error message.
func TestProfiles_NewEditorEmptiesFile(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSetEditorOverride(t, func(in []byte) []byte { return nil })
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "new", "x", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "parse edited JSON") {
		t.Errorf("stderr should mention parse error, got %q", stderr)
	}
}

// TestProfiles_NewValidationFails: the override saves syntactically valid
// JSON whose patterns violate the allowlist (`..` traversal). new must
// reject + exit 2 BEFORE Upsert touches disk.
func TestProfiles_NewValidationFails(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSetEditorOverride(t, func(in []byte) []byte {
		p := profiles.Profile{
			Name:     "x",
			Source:   "/src",
			Includes: []string{"../etc/passwd"},
		}
		out, _ := json.MarshalIndent(p, "", "  ")
		return out
	})
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "new", "x", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "must not contain ..") {
		t.Errorf("stderr should surface the validation reason, got %q", stderr)
	}
	// Confirm no profile was written.
	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	all, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 0 {
		t.Errorf("len: got %d, want 0 (Upsert must not have run)", len(all))
	}
}

// TestProfiles_NewEditorRenamesProfile: the override changes the profile
// name field. new must reject (rename is a separate concern).
func TestProfiles_NewEditorRenamesProfile(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSetEditorOverride(t, func(in []byte) []byte {
		var p profiles.Profile
		_ = json.Unmarshal(in, &p)
		p.Name = "different"
		out, _ := json.MarshalIndent(p, "", "  ")
		return out
	})
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "new", "intended", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "rename not supported") {
		t.Errorf("stderr should reject rename, got %q", stderr)
	}
}

// ----------------------------------------------------------------------------
// edit
// ----------------------------------------------------------------------------

// TestProfiles_EditNotFound: a profile name that does not exist in the
// store must exit 2 with the store's "not found" message.
func TestProfiles_EditNotFound(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSetEditorOverride(t, func(in []byte) []byte {
		t.Errorf("editor should not run when profile not found")
		return in
	})
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "edit", "nope", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr should mention 'not found', got %q", stderr)
	}
}

// TestProfiles_EditHappyPath: seed a profile, override the editor to mutate
// the source; edit must Upsert + the round-tripped value must reflect the
// change.
func TestProfiles_EditHappyPath(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "docs", Source: "/old", Includes: []string{"*"},
	})
	profilesSetEditorOverride(t, func(in []byte) []byte {
		var p profiles.Profile
		_ = json.Unmarshal(in, &p)
		p.Source = "/new/source"
		out, _ := json.MarshalIndent(p, "", "  ")
		return out
	})
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "edit", "docs", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "updated profile") {
		t.Errorf("stdout should mention 'updated profile', got %q", stdout)
	}
	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, _ := profiles.NewStore(storePath)
	got, err := store.Get("docs")
	if err != nil {
		t.Fatal(err)
	}
	if got.Source != "/new/source" {
		t.Errorf("Source: got %q, want %q", got.Source, "/new/source")
	}
}

// TestProfiles_EditRejectsRename: a name change in the edited JSON must be
// rejected so a stale alias does not silently land alongside the original.
func TestProfiles_EditRejectsRename(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "docs", Source: "/Users/me/Docs",
	})
	profilesSetEditorOverride(t, func(in []byte) []byte {
		var p profiles.Profile
		_ = json.Unmarshal(in, &p)
		p.Name = "other"
		out, _ := json.MarshalIndent(p, "", "  ")
		return out
	})
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "edit", "docs", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "rename not supported") {
		t.Errorf("stderr should reject rename, got %q", stderr)
	}
}

// ----------------------------------------------------------------------------
// delete
// ----------------------------------------------------------------------------

// TestProfiles_DeleteNotFound: a missing profile is reported with the
// store's "not found" message + exit 2.
func TestProfiles_DeleteNotFound(t *testing.T) {
	usb := profilesUseTempUSB(t)
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "delete", "ghost", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr should mention 'not found', got %q", stderr)
	}
}

// TestProfiles_DeleteHappyPath: seed two; delete one; assert the survivor
// is intact + the deleted one is absent.
func TestProfiles_DeleteHappyPath(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{Name: "alpha", Source: "/a"})
	profilesSeed(t, usb, profiles.Profile{Name: "bravo", Source: "/b"})

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "delete", "alpha", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "deleted profile") {
		t.Errorf("stdout should mention 'deleted profile', got %q", stdout)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Errorf("stdout should name the deleted profile, got %q", stdout)
	}

	storePath := filepath.Join(usb, ".flashbackup", "profiles.json")
	store, _ := profiles.NewStore(storePath)
	if _, err := store.Get("alpha"); err == nil {
		t.Errorf("alpha should no longer exist after delete")
	}
	if _, err := store.Get("bravo"); err != nil {
		t.Errorf("bravo should still exist: %v", err)
	}
}

// ----------------------------------------------------------------------------
// validate
// ----------------------------------------------------------------------------

// TestProfiles_ValidateNotFound: a missing profile is exit 2 (operator-
// fixable; likely a name typo).
func TestProfiles_ValidateNotFound(t *testing.T) {
	usb := profilesUseTempUSB(t)
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "validate", "ghost", usb})
	if code != profilesExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, profilesExitCodeUsage)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("stderr should mention 'not found', got %q", stderr)
	}
}

// TestProfiles_ValidateHappyPath: a profile that passed Upsert must
// re-validate. Exit 0 + "OK" anchor on stdout.
func TestProfiles_ValidateHappyPath(t *testing.T) {
	usb := profilesUseTempUSB(t)
	profilesSeed(t, usb, profiles.Profile{
		Name: "docs", Source: "/Users/me/Docs",
		Includes: []string{"*.md"}, Excludes: []string{".DS_Store"},
	})
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "validate", "docs", usb})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstderr=%s", code, stderr)
	}
	if !strings.Contains(stdout, "OK") {
		t.Errorf("stdout should contain 'OK', got %q", stdout)
	}
}

// TestProfiles_ValidateRejectsTamperedFile: bypass Upsert and write a
// profiles.json with an invalid include pattern directly. validate must
// detect + exit 1 (integrity signal; on-disk state diverged from what the
// store accepted).
func TestProfiles_ValidateRejectsTamperedFile(t *testing.T) {
	usb := profilesUseTempUSB(t)
	dotDir := filepath.Join(usb, ".flashbackup")
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := profiles.ProfilesDoc{
		V: profiles.SchemaVersion,
		Profiles: []profiles.Profile{
			{
				V: profiles.SchemaVersion, Name: "tampered", Source: "/src",
				// `..` is forbidden by the allowlist; Upsert would refuse.
				// We bypass via direct file write to simulate an out-of-
				// band tamper.
				Includes: []string{"../etc/passwd"},
			},
		},
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dotDir, "profiles.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, []string{"flashbackup", "profiles", "validate", "tampered", usb})
	if code != profilesExitCodeRuntime {
		t.Errorf("exit code: got %d, want %d (integrity)", code, profilesExitCodeRuntime)
	}
	if !strings.Contains(stderr, "must not contain ..") {
		t.Errorf("stderr should surface the validation reason, got %q", stderr)
	}
}

// ----------------------------------------------------------------------------
// E2E: post-init list against a real mounted volume
// ----------------------------------------------------------------------------

// profilesInitUSB mirrors verifyCmdInitUSB / statusInitUSB. Keeping the helper
// per test file means a future tweak to the init seam touches one file per
// subcommand; the duplication is intentional.
func profilesInitUSB(t *testing.T, dest string) {
	t.Helper()
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
}

// TestProfiles_AfterInitListEmpty: init a USB; list with --json must emit
// `[]`. This exercises the real disk path (Store.NewStore creating the
// .flashbackup dir if missing, then reading the empty doc).
func TestProfiles_AfterInitListEmpty(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	profilesInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "profiles", "list", "--json", dest})
	if code != profilesExitCodeOK {
		t.Fatalf("exit code: got %d, want 0\nstdout=%s\nstderr=%s", code, stdout, stderr)
	}
	var parsed []any
	if err := json.Unmarshal([]byte(stdout), &parsed); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout)
	}
	if len(parsed) != 0 {
		t.Errorf("len: got %d, want 0", len(parsed))
	}
}
