package e2e

import (
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// init_test.go covers the end-to-end contract for `flashbackup init`:
//
//	AC-1: init on a fresh APFS volume produces .flashbackup/version.json
//	      with a valid 32-byte HMAC key, .metadata_never_index at the
//	      volume root, and extracts the embedded rsync under
//	      .flashbackup/bin/<sha>/rsync.
//
//	AC-2: init on an exFAT volume refuses with exit 2, prints the
//	      `diskutil eraseDisk APFS` reformat recipe and the
//	      ALL-DATA-WILL-BE-LOST warning to stderr, and writes nothing
//	      to the refused volume.
//
// Plus an adjacent contract test (RefusesWithoutResetKeys) that pins the
// safety gate keeping a second init from silently rotating the HMAC key.
//
// Every test gates itself on FLASHBACKUP_E2E=1 + macOS + hdiutil +
// diskutil so a plain `go test ./...` on any platform (or on macOS
// without the env var) skips cleanly. The Makefile's e2e-fast target
// matches "Init" in its -run filter, which selects every test in this
// file by name.
//
// Note: this file does NOT call e2e.SetupUSB for the AC-1 and AC-2 paths
// because SetupUSB itself runs `flashbackup init` against the mount, and
// the whole point of these tests is to exercise init directly. We mount
// via testutil.MountTempVolume and invoke RunInit ourselves; the
// already-initialized re-init test does use SetupUSB because that one
// needs a pre-initialized volume.

// TestE2E_Init_HappyPath_APFS covers AC-1: init produces .flashbackup/
// with mode 0o700, version.json with mode 0o600 + a valid 32-byte HMAC
// key, .metadata_never_index at the volume root, and a non-empty
// .flashbackup/bin/ directory (rsync extract landed).
func TestE2E_Init_HappyPath_APFS(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Mount a fresh APFS DMG; do NOT call SetupUSB (which runs init
	// for us). The whole point of this test is to drive init ourselves.
	mountpoint := testutil.MountTempVolume(t, "APFS")

	// Belt-and-suspenders: rsync extract sets uchg on the binary; clear
	// it before testutil's hdiutil detach runs (LIFO cleanup order).
	t.Cleanup(func() { clearImmutableRsync(mountpoint) })

	exitCode, stdout, stderr := RunInit(t, mountpoint)

	if exitCode != 0 {
		t.Fatalf("init exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// .flashbackup directory exists with mode 0o700.
	dotDir := filepath.Join(mountpoint, ".flashbackup")
	info, err := os.Stat(dotDir)
	if err != nil {
		t.Fatalf("stat .flashbackup: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf(".flashbackup is not a directory")
	}
	if info.Mode().Perm() != 0o700 {
		t.Errorf(".flashbackup mode: got %o want 0700", info.Mode().Perm())
	}

	// version.json exists, parses, has schema_version=1 and a 64-hex-char
	// (32-byte) HMAC key. We unmarshal into a local struct rather than
	// importing state.ReadVersionFile so this test pins the on-disk JSON
	// schema directly; a refactor that changed the field names without
	// updating consumers would surface here.
	versionPath := filepath.Join(dotDir, "version.json")
	data, err := os.ReadFile(versionPath)
	if err != nil {
		t.Fatalf("read version.json: %v", err)
	}
	var v struct {
		SchemaVersion int    `json:"schema_version"`
		HMACKey       string `json:"hmac_key"`
	}
	if err := json.Unmarshal(data, &v); err != nil {
		t.Fatalf("unmarshal version.json: %v", err)
	}
	if v.SchemaVersion != 1 {
		t.Errorf("schema_version: got %d want 1", v.SchemaVersion)
	}
	if _, err := hex.DecodeString(v.HMACKey); err != nil {
		t.Errorf("hmac_key not valid hex: %v", err)
	}
	if len(v.HMACKey) != 64 { // 32 bytes = 64 hex chars
		t.Errorf("hmac_key length: got %d want 64", len(v.HMACKey))
	}

	// version.json mode 0o600 (HMAC key is sensitive).
	if vInfo, err := os.Stat(versionPath); err == nil {
		if vInfo.Mode().Perm() != 0o600 {
			t.Errorf("version.json mode: got %o want 0600", vInfo.Mode().Perm())
		}
	}

	// .metadata_never_index exists at the volume root.
	if _, err := os.Stat(filepath.Join(mountpoint, ".metadata_never_index")); err != nil {
		t.Errorf(".metadata_never_index: %v", err)
	}

	// rsync extracted somewhere under .flashbackup/bin/. We do not pin
	// the exact sha here (that would couple the test to the embedded
	// payload's hash); a non-empty bin/ subtree is sufficient to prove
	// the extract step ran.
	binDir := filepath.Join(dotDir, "bin")
	entries, err := os.ReadDir(binDir)
	if err != nil {
		t.Fatalf("read bin dir: %v", err)
	}
	if len(entries) == 0 {
		t.Errorf(".flashbackup/bin is empty; rsync extraction failed")
	}
}

// TestE2E_Init_RefusesExFAT covers AC-2: init exits 2 on an exFAT
// volume, prints the `diskutil eraseDisk APFS` recipe + ALL-DATA-WILL-
// BE-LOST warning to stderr, and writes nothing to the refused volume.
//
// hdiutil's ExFAT support is broadly available on macOS 11+; on
// sandboxed CI hosts that disallow hdiutil create the helper skips
// cleanly, which is the correct outcome (we cannot prove the gate
// without a refused-fs fixture).
func TestE2E_Init_RefusesExFAT(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	mountpoint := testutil.MountTempVolume(t, "ExFAT")

	exitCode, stdout, stderr := RunInit(t, mountpoint)

	if exitCode != 2 {
		t.Errorf("init exit code: got %d want 2\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}
	// Reformat recipe must be in stderr; the implementer chose
	// `diskutil eraseDisk APFS` as the canonical phrase carried by
	// the UnsupportedError formatter.
	if !strings.Contains(stderr, "diskutil eraseDisk APFS") {
		t.Errorf("stderr missing reformat recipe; got: %s", stderr)
	}
	if !strings.Contains(stderr, "ALL DATA WILL BE LOST") {
		t.Errorf("stderr missing data-loss warning; got: %s", stderr)
	}
	// No .flashbackup directory and no .metadata_never_index should
	// appear on a refused volume; AC-2 says "writes no files to the USB".
	if _, err := os.Stat(filepath.Join(mountpoint, ".flashbackup")); err == nil {
		t.Errorf(".flashbackup created on refused exFAT; expected absent")
	}
	if _, err := os.Stat(filepath.Join(mountpoint, ".metadata_never_index")); err == nil {
		t.Errorf(".metadata_never_index created on refused exFAT; expected absent")
	}
}

// TestE2E_Init_AlreadyInitialized_RefusesWithoutResetKeys covers an
// adjacent safety contract: a second init on an already-initialized USB
// without --reset-keys must exit 2 with a message that names
// --reset-keys. This is the gate that keeps a careless re-init from
// silently rotating the HMAC key and invalidating every prior manifest.
//
// We use SetupUSB here (not MountTempVolume) because SetupUSB does the
// first init for us; we then RunInit a second time and assert the
// refusal.
func TestE2E_Init_AlreadyInitialized_RefusesWithoutResetKeys(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	usb := SetupUSB(t, 0)

	// Capture the original HMAC key so we can prove the refused re-init
	// did NOT rotate it. Two random 32-byte keys colliding has
	// negligible probability, so a same-key outcome after a successful
	// rotation would be a flake; a different-key outcome here would be
	// a real bug (silent rotation).
	versionPath := filepath.Join(usb, ".flashbackup", "version.json")
	originalKey := readHMACKeyJSON(t, versionPath)

	exitCode, stdout, stderr := RunInit(t, usb)

	if exitCode != 2 {
		t.Fatalf("second init exit code: got %d want 2\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}
	if !strings.Contains(stderr, "--reset-keys") {
		t.Errorf("stderr should name --reset-keys; got: %s", stderr)
	}
	if !strings.Contains(stderr, "already exists") {
		t.Errorf("stderr should mention already-exists; got: %s", stderr)
	}
	if got := readHMACKeyJSON(t, versionPath); got != originalKey {
		t.Errorf("HMAC key changed after refused init: %s -> %s", originalKey, got)
	}
}

// readHMACKeyJSON returns the hmac_key field from a version.json on
// disk. Local helper kept here (not promoted into helpers.go) because
// only this file needs it; cmd/flashbackup/init_test.go has its own
// copy for the same reason (package main vs package e2e).
func readHMACKeyJSON(t *testing.T, versionPath string) string {
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
