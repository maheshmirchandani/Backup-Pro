package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// ----------------------------------------------------------------------------
// Skip / mount helpers
//
// Shared helpers (RequireMacOS / RequireE2E / RequireHdiutil / MountTempVolume)
// live in internal/testutil. This file used to carry a local copy; the A1
// review amendment extracted it before Task 38 added a seventh duplicate.
// ----------------------------------------------------------------------------

// ----------------------------------------------------------------------------
// Unit tests (no DMG; exercise argv parsing + error paths)
// ----------------------------------------------------------------------------

// TestInit_MissingPathArg: bare `flashbackup init` (no positional path)
// must reject with exit 2 + a usage block on stderr. This is the most
// common operator mistake (typed the subcommand, forgot the path) so a
// regression here would surface as silent zero exit, which the dispatcher
// must not allow.
func TestInit_MissingPathArg(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "init"})
	if code != initExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, initExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "missing <USB-path>") {
		t.Errorf("stderr should explain missing arg, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestInit_TooManyArgs: extra positional args after the path are rejected.
// Guards against operators accidentally appending "/Volumes/USB --reset-keys"
// in the wrong order (flag must precede the path in some shells).
func TestInit_TooManyArgs(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", "/Volumes/A", "/Volumes/B"})
	if code != initExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, initExitCodeUsage)
	}
	if !strings.Contains(stderr, "unexpected extra arguments") {
		t.Errorf("stderr should explain extra args, got %q", stderr)
	}
}

// TestInit_HelpFlag: `flashbackup init --help` must exit 0 (deliberate
// user request for help; not an error) and print the usage block to
// stderr (flag.PrintDefaults goes to fs.Output, which we wired to stderr).
// A non-zero exit here would make scripted help-probes look like
// failures.
func TestInit_HelpFlag(t *testing.T) {
	for _, flag := range []string{"--help", "-h"} {
		t.Run(flag, func(t *testing.T) {
			code, _, stderr := runCapture(t, []string{"flashbackup", "init", flag})
			if code != 0 {
				t.Errorf("exit code: got %d, want 0", code)
			}
			if !strings.Contains(stderr, "Usage: flashbackup init") {
				t.Errorf("stderr should include usage line, got %q", stderr)
			}
		})
	}
}

// TestInit_UnknownFlag: an unrecognised flag (e.g. --rest-keys typo) must
// be rejected with exit 2. flag.Parse handles the error message itself; we
// only assert the exit code and that stderr is not empty.
func TestInit_UnknownFlag(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", "--rest-keys", "/Volumes/A"})
	if code != initExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, initExitCodeUsage)
	}
	if stderr == "" {
		t.Errorf("stderr should not be empty on unknown flag")
	}
}

// TestInit_NonexistentMountpoint: a path that does not resolve via
// EvalSymlinks must exit 2 + name the path. This is the friend-mode
// equivalent of "USB not plugged in"; we want a clear error, not a
// half-completed init that leaves stray state somewhere unexpected.
func TestInit_NonexistentMountpoint(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-mountpoint")
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", bogus})
	if code != initExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, initExitCodeUsage)
	}
	if !strings.Contains(stderr, "no-such-mountpoint") {
		t.Errorf("stderr should name the bad path, got %q", stderr)
	}
}

// TestInit_PathIsRegularFile: passing a regular file as <USB-path> must
// exit 2 with a "not a directory" message. Defence in depth: if a future
// refactor drops the IsDir() check, the subsequent .flashbackup MkdirAll
// would fail with a less-helpful errno; this test pins the friendlier
// surface.
func TestInit_PathIsRegularFile(t *testing.T) {
	tmp := t.TempDir()
	filePath := filepath.Join(tmp, "regular.file")
	if err := os.WriteFile(filePath, []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", filePath})
	if code != initExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, initExitCodeUsage)
	}
	if !strings.Contains(stderr, "not a directory") {
		t.Errorf("stderr should mention not-a-directory, got %q", stderr)
	}
}

// TestInit_ArgvParsing: table-driven coverage of the --reset-keys flag
// position and the positional path. Each row only asserts the exit code
// because the rest of the behaviour depends on whether the path resolves;
// we use a path that cannot exist so every row stops at the same stage
// (post-flag-parse, pre-filesystem-inspect). The "before path" and
// "after path" rows confirm Go's flag package's standard "flags must
// precede positionals" behaviour, which we rely on for predictable
// operator UX.
func TestInit_ArgvParsing(t *testing.T) {
	bogus := filepath.Join(t.TempDir(), "no-such-mountpoint")
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"path only", []string{"init", bogus}, initExitCodeUsage},
		{"reset-keys before path", []string{"init", "--reset-keys", bogus}, initExitCodeUsage},
		{"single-dash reset-keys", []string{"init", "-reset-keys", bogus}, initExitCodeUsage},
		{"reset-keys after path becomes extra arg", []string{"init", bogus, "--reset-keys"}, initExitCodeUsage},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			argv := append([]string{"flashbackup"}, tc.args...)
			code, _, _ := runCapture(t, argv)
			if code != tc.want {
				t.Errorf("exit code: got %d, want %d", code, tc.want)
			}
		})
	}
}

// ----------------------------------------------------------------------------
// E2E tests (mount a DMG)
// ----------------------------------------------------------------------------

// TestInit_HappyPath_APFS covers AC-1. Mount a fresh APFS volume, run
// init, assert version.json has a 64-hex-char HMAC key, .flashbackup is
// mode 0o700, .metadata_never_index exists, and the rsync binary landed
// under .flashbackup/bin/<sha>/rsync.
func TestInit_HappyPath_APFS(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != 0 {
		t.Fatalf("init exit code: got %d, want 0\nstderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "FlashBackup initialized at") {
		t.Errorf("stdout should announce success, got %q", stdout)
	}

	// .flashbackup dir exists with mode 0700.
	dotDir := filepath.Join(dest, ".flashbackup")
	dotInfo, err := os.Stat(dotDir)
	if err != nil {
		t.Fatalf("stat .flashbackup: %v", err)
	}
	if !dotInfo.IsDir() {
		t.Errorf(".flashbackup is not a directory")
	}
	if dotInfo.Mode().Perm() != 0o700 {
		t.Errorf(".flashbackup mode = %o, want 0700", dotInfo.Mode().Perm())
	}

	// version.json exists, parses, has a 64-hex-char HMAC key.
	versionPath := filepath.Join(dotDir, "version.json")
	vf, err := state.ReadVersionFile(versionPath)
	if err != nil {
		t.Fatalf("ReadVersionFile: %v", err)
	}
	if vf.SchemaVersion != state.CurrentSchemaVersion {
		t.Errorf("schema_version: got %d, want %d", vf.SchemaVersion, state.CurrentSchemaVersion)
	}
	if len(vf.HMACKey) != state.HMACKeyBytes*2 {
		t.Errorf("HMAC key length = %d hex chars, want %d", len(vf.HMACKey), state.HMACKeyBytes*2)
	}

	// version.json mode 0600 (HMAC key is sensitive; matches
	// state.WriteVersionFile's contract).
	vInfo, err := os.Stat(versionPath)
	if err != nil {
		t.Fatalf("stat version.json: %v", err)
	}
	if vInfo.Mode().Perm() != 0o600 {
		t.Errorf("version.json mode = %o, want 0600", vInfo.Mode().Perm())
	}

	// .metadata_never_index exists at the volume root with mode 0644.
	indexMarker := filepath.Join(dest, ".metadata_never_index")
	mInfo, err := os.Stat(indexMarker)
	if err != nil {
		t.Fatalf("stat .metadata_never_index: %v", err)
	}
	if mInfo.Mode().Perm() != 0o644 {
		t.Errorf(".metadata_never_index mode = %o, want 0644", mInfo.Mode().Perm())
	}

	// rsync extracted somewhere under .flashbackup/bin/<sha>/rsync. We do
	// not pin the exact sha here (would couple this test to the embedded
	// placeholder's hash); instead we just walk one level deep and assert
	// "rsync" exists under at least one subdir of bin/.
	binDir := filepath.Join(dotDir, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		t.Fatalf("read .flashbackup/bin: %v", err)
	}
	foundRsync := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		candidate := filepath.Join(binDir, e.Name(), "rsync")
		if _, err := os.Stat(candidate); err == nil {
			foundRsync = true
			break
		}
	}
	if !foundRsync {
		t.Errorf("no rsync binary found under %s", binDir)
	}
	// Best-effort cleanup of the immutable flag the rsync extract may
	// have set; otherwise t.TempDir's RemoveAll could trip on darwin.
	// hdiutil detach will free the volume regardless, but clearing the
	// flag avoids leaking the DMG-backing tmpfile in /tmp.
	if foundRsync {
		for _, e := range entries {
			if e.IsDir() {
				_ = clearImmutableForTestInit(filepath.Join(binDir, e.Name(), "rsync"))
			}
		}
	}
}

// TestInit_AlreadyInitialized_RefusesWithoutResetKeys: a second init
// without --reset-keys must exit 2 with a clear message naming the
// existing version.json. This is the safety contract that prevents
// silent HMAC-key rotation (which would invalidate every prior manifest).
func TestInit_AlreadyInitialized_RefusesWithoutResetKeys(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("first init failed: code=%d stderr=%s", code, stderr)
	}
	versionPath := filepath.Join(dest, ".flashbackup", "version.json")
	originalKey := readHMACKey(t, versionPath)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != initExitCodeUsage {
		t.Fatalf("second init: got %d, want %d\nstdout=%s\nstderr=%s",
			code, initExitCodeUsage, stdout, stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr should mention already-exists, got %q", stderr)
	}
	if !strings.Contains(stderr, "--reset-keys") {
		t.Errorf("stderr should suggest --reset-keys, got %q", stderr)
	}
	// Key must be unchanged (refusal preserved the existing manifest's
	// integrity guarantee).
	if got := readHMACKey(t, versionPath); got != originalKey {
		t.Errorf("HMAC key changed after refused init: %s -> %s", originalKey, got)
	}
}

// TestInit_AlreadyInitialized_OverwritesWithResetKeys: passing
// --reset-keys must rotate the HMAC key. Cryptographically, two random
// 32-byte keys colliding has negligible probability, so a same-key
// outcome means the rotation did not happen.
func TestInit_AlreadyInitialized_OverwritesWithResetKeys(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "APFS")
	if code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest}); code != 0 {
		t.Fatalf("first init failed: code=%d stderr=%s", code, stderr)
	}
	versionPath := filepath.Join(dest, ".flashbackup", "version.json")
	originalKey := readHMACKey(t, versionPath)

	code, _, stderr := runCapture(t, []string{"flashbackup", "init", "--reset-keys", dest})
	if code != 0 {
		t.Fatalf("reset-keys init: got %d, want 0\nstderr=%s", code, stderr)
	}
	rotatedKey := readHMACKey(t, versionPath)
	if rotatedKey == originalKey {
		t.Errorf("--reset-keys did not rotate HMAC key (still %s)", rotatedKey)
	}
}

// TestInit_ExFAT_RefusesWithRecipe covers AC-2. Mounts a fresh exFAT
// volume via hdiutil; init must refuse with exit 2 and the
// `diskutil eraseDisk APFS` reformat recipe printed to stderr. We also
// assert that the .flashbackup directory was NOT created on the refused
// volume; AC-2 says "writes no files to the USB."
//
// hdiutil's exFAT support is broadly available on macOS 11+; on
// sandboxed CI hosts that disallow hdiutil create, the helper skips
// the test, which is the correct outcome (we cannot prove the gate
// without a refused-fs fixture). Local macOS test runs hit this path.
func TestInit_ExFAT_RefusesWithRecipe(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)

	dest := testutil.MountTempVolume(t, "ExFAT")
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != initExitCodeUsage {
		t.Fatalf("init on MS-DOS: got %d, want %d\nstdout=%s\nstderr=%s",
			code, initExitCodeUsage, stdout, stderr)
	}
	if !strings.Contains(stderr, "diskutil eraseDisk APFS") {
		t.Errorf("stderr should include reformat recipe, got %q", stderr)
	}
	if !strings.Contains(stderr, "FlashBackup requires APFS or HFS+") {
		t.Errorf("stderr should state APFS/HFS+ requirement, got %q", stderr)
	}
	if !strings.Contains(stderr, "ALL DATA WILL BE LOST") {
		t.Errorf("stderr should warn about data loss, got %q", stderr)
	}

	// Refused volume must be untouched.
	if _, err := os.Stat(filepath.Join(dest, ".flashbackup")); err == nil {
		t.Errorf(".flashbackup should NOT exist on refused volume")
	}
	if _, err := os.Stat(filepath.Join(dest, ".metadata_never_index")); err == nil {
		t.Errorf(".metadata_never_index should NOT exist on refused volume")
	}
}

// TestInit_RealAPFS_RunInitDirect covers a unit-test code path that does
// not need hdiutil: we call runInit against /tmp (which on macOS is APFS,
// on Linux is typically tmpfs and will skip). This exercises the happy
// path's wiring without paying the hdiutil cost, so coverage stays above
// the >=70% gate for cmd/flashbackup even when FLASHBACKUP_E2E is unset.
func TestInit_RealAPFS_RunInitDirect(t *testing.T) {
	testutil.RequireMacOS(t)
	// Use a tempdir under the system root volume (APFS on modern macOS).
	// t.TempDir() returns a path under $TMPDIR, which on darwin is a
	// per-user APFS mount, so filesystem.Inspect will accept it.
	dest := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := runInit(context.Background(), []string{dest}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("runInit: got %d, want 0\nstderr=%s", code, stderr.String())
	}
	versionPath := filepath.Join(dest, ".flashbackup", "version.json")
	if _, err := state.ReadVersionFile(versionPath); err != nil {
		t.Fatalf("ReadVersionFile: %v", err)
	}

	// Clear immutable flag on extracted rsync so t.TempDir cleanup works.
	binDir := filepath.Join(dest, ".flashbackup", "bin")
	if entries, err := os.ReadDir(binDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = clearImmutableForTestInit(filepath.Join(binDir, e.Name(), "rsync"))
			}
		}
	}
}

// TestInit_RealAPFS_RefusesWithoutResetKeys: same trick as above; a
// second runInit without --reset-keys must refuse. Pure-Go path so it
// runs on every `go test ./...` on macOS, no hdiutil required.
func TestInit_RealAPFS_RefusesWithoutResetKeys(t *testing.T) {
	testutil.RequireMacOS(t)
	dest := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := runInit(context.Background(), []string{dest}, &stdout, &stderr); code != 0 {
		t.Fatalf("first runInit: got %d, want 0\nstderr=%s", code, stderr.String())
	}
	stdout.Reset()
	stderr.Reset()
	code := runInit(context.Background(), []string{dest}, &stdout, &stderr)
	if code != initExitCodeUsage {
		t.Fatalf("second runInit: got %d, want %d\nstderr=%s",
			code, initExitCodeUsage, stderr.String())
	}
	if !strings.Contains(stderr.String(), "--reset-keys") {
		t.Errorf("stderr should suggest --reset-keys, got %q", stderr.String())
	}

	// Cleanup immutable flag.
	binDir := filepath.Join(dest, ".flashbackup", "bin")
	if entries, err := os.ReadDir(binDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = clearImmutableForTestInit(filepath.Join(binDir, e.Name(), "rsync"))
			}
		}
	}
}

// TestInit_RealAPFS_ResetKeysRotates: --reset-keys path on a non-DMG
// fixture; checks that the HMAC key actually changes.
func TestInit_RealAPFS_ResetKeysRotates(t *testing.T) {
	testutil.RequireMacOS(t)
	dest := t.TempDir()

	var stdout, stderr bytes.Buffer
	if code := runInit(context.Background(), []string{dest}, &stdout, &stderr); code != 0 {
		t.Fatalf("first runInit: got %d, want 0\nstderr=%s", code, stderr.String())
	}
	versionPath := filepath.Join(dest, ".flashbackup", "version.json")
	originalKey := readHMACKey(t, versionPath)

	stdout.Reset()
	stderr.Reset()
	if code := runInit(context.Background(), []string{"--reset-keys", dest}, &stdout, &stderr); code != 0 {
		t.Fatalf("reset-keys runInit: got %d, want 0\nstderr=%s", code, stderr.String())
	}
	rotatedKey := readHMACKey(t, versionPath)
	if rotatedKey == originalKey {
		t.Errorf("--reset-keys did not rotate HMAC key")
	}

	// Cleanup immutable flag.
	binDir := filepath.Join(dest, ".flashbackup", "bin")
	if entries, err := os.ReadDir(binDir); err == nil {
		for _, e := range entries {
			if e.IsDir() {
				_ = clearImmutableForTestInit(filepath.Join(binDir, e.Name(), "rsync"))
			}
		}
	}
}

// readHMACKey is a small helper that returns the HMAC key field from a
// version.json on disk. Fails the test on read/parse error so callers can
// inline the assertion without nesting if-err handling.
func readHMACKey(t *testing.T, versionPath string) string {
	t.Helper()
	data, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	var vf struct {
		HMACKey string `json:"hmac_key"`
	}
	if err := json.Unmarshal(data, &vf); err != nil {
		t.Fatalf("parse version.json: %v", err)
	}
	if vf.HMACKey == "" {
		t.Fatal("HMAC key empty in version.json")
	}
	return vf.HMACKey
}
