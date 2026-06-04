package state

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixture writes content to path with 0600 perms for fail-closed read tests.
func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
}

func TestVersionFile_WriteRead(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")

	v, err := InitVersionFile(path, "0.1.0-test", false)
	if err != nil {
		t.Fatalf("init: %v", err)
	}
	if v.SchemaVersion != CurrentSchemaVersion {
		t.Errorf("schema_version: got %d want %d", v.SchemaVersion, CurrentSchemaVersion)
	}
	if v.FlashbackupVersion != "0.1.0-test" {
		t.Errorf("flashbackup_version: got %q want %q", v.FlashbackupVersion, "0.1.0-test")
	}
	if len(v.HMACKey) != HMACKeyBytes*2 {
		t.Errorf("hmac_key length: got %d hex chars want %d", len(v.HMACKey), HMACKeyBytes*2)
	}

	got, err := ReadVersionFile(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got != v {
		t.Errorf("round-trip mismatch: got %+v want %+v", got, v)
	}
}

func TestReadVersionFile_NotExist(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected error for missing file")
	}
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("expected wrapped os.ErrNotExist, got %v", err)
	}
}

func TestReadVersionFile_Unparseable(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, `{not valid json}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected parse error")
	}
	if !strings.Contains(err.Error(), "--reset-keys") {
		t.Errorf("expected error to mention --reset-keys remediation, got %q", err)
	}
}

func TestReadVersionFile_SchemaVersionMismatch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, `{
  "schema_version": 2,
  "flashbackup_version": "0.1.0",
  "hmac_key": "0000000000000000000000000000000000000000000000000000000000000000"
}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected schema-version mismatch error")
	}
	if !strings.Contains(err.Error(), "schema_version=2") {
		t.Errorf("expected error to mention schema_version=2, got %q", err)
	}
	if !strings.Contains(err.Error(), "unsupported") {
		t.Errorf("expected error to say unsupported, got %q", err)
	}
}

func TestReadVersionFile_SchemaVersionWrongType(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, `{
  "schema_version": "1",
  "flashbackup_version": "0.1.0",
  "hmac_key": "0000000000000000000000000000000000000000000000000000000000000000"
}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected parse error for string schema_version")
	}
	if !strings.Contains(err.Error(), "parse version.json") {
		t.Errorf("expected wrapped parse error, got %q", err)
	}
}

func TestReadVersionFile_MissingHMACKey(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, `{
  "schema_version": 1,
  "flashbackup_version": "0.1.0",
  "hmac_key": ""
}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected error for empty hmac_key")
	}
	if !strings.Contains(err.Error(), "length") {
		t.Errorf("expected key-length error, got %q", err)
	}
}

func TestReadVersionFile_HMACKeyWrongLength(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	// 16 hex chars = 8 bytes (too short)
	writeFixture(t, path, `{
  "schema_version": 1,
  "flashbackup_version": "0.1.0",
  "hmac_key": "deadbeefcafebabe"
}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected key-length error")
	}
	if !strings.Contains(err.Error(), "length") {
		t.Errorf("expected key-length error, got %q", err)
	}
	if !strings.Contains(err.Error(), "32") {
		t.Errorf("expected error to mention expected 32 bytes, got %q", err)
	}
}

func TestReadVersionFile_HMACKeyNotHex(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, `{
  "schema_version": 1,
  "flashbackup_version": "0.1.0",
  "hmac_key": "not-hex-at-all"
}`)

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected hex-decode error")
	}
	if !strings.Contains(err.Error(), "hex") {
		t.Errorf("expected hex-decode error, got %q", err)
	}
}

func TestInitVersionFile_NoForceRefusesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")

	// First init succeeds (no file yet)
	v1, err := InitVersionFile(path, "0.1.0", false)
	if err != nil {
		t.Fatalf("first init: %v", err)
	}

	// Second init without force must fail
	_, err = InitVersionFile(path, "0.1.0", false)
	if err == nil {
		t.Fatal("expected refusal to overwrite existing file without force")
	}
	if !strings.Contains(err.Error(), "--reset-keys") {
		t.Errorf("expected error to mention --reset-keys, got %q", err)
	}

	// Second init with force=true overwrites and produces a new key
	v2, err := InitVersionFile(path, "0.1.0", true)
	if err != nil {
		t.Fatalf("force overwrite: %v", err)
	}
	if v1.HMACKey == v2.HMACKey {
		t.Errorf("expected new HMAC key on force overwrite; got identical keys")
	}
}

func TestInitVersionFile_KeyIsRandom(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")

	v1, err := InitVersionFile(path, "0.1.0", false)
	if err != nil {
		t.Fatalf("first init: %v", err)
	}
	v2, err := InitVersionFile(path, "0.1.0", true)
	if err != nil {
		t.Fatalf("second init: %v", err)
	}
	if v1.HMACKey == v2.HMACKey {
		t.Errorf("HMAC keys identical across two InitVersionFile calls; rand.Read may not be sourcing entropy")
	}
	// Sanity: not all zeros
	zeros := strings.Repeat("0", HMACKeyBytes*2)
	if v1.HMACKey == zeros || v2.HMACKey == zeros {
		t.Errorf("HMAC key is all zeros; rand.Read failed silently")
	}
}

func TestWriteVersionFile_Mode0600(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")

	if _, err := InitVersionFile(path, "0.1.0", false); err != nil {
		t.Fatalf("init: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("got mode %o want 0600 (HMAC key is sensitive)", info.Mode().Perm())
	}
}

func TestVersionFile_EmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "version.json")
	writeFixture(t, path, "")

	_, err := ReadVersionFile(path)
	if err == nil {
		t.Fatal("expected parse error for empty file")
	}
	if !strings.Contains(err.Error(), "parse version.json") {
		t.Errorf("expected wrapped parse error, got %q", err)
	}
}
