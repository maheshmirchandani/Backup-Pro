package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
)

// VersionFile is the schema version + provenance + HMAC key stored at
// <USB>/.flashbackup/version.json. Invariant #11 (corruption recovery is
// FAIL-CLOSED, not silently re-init), #13 (schema_version), #33 (per-USB
// HMAC key).
type VersionFile struct {
	SchemaVersion      int    `json:"schema_version"`
	FlashbackupVersion string `json:"flashbackup_version"`
	HMACKey            string `json:"hmac_key"` // hex-encoded 32 bytes
}

// CurrentSchemaVersion is the schema version this build understands.
const CurrentSchemaVersion = 1

// HMACKeyBytes is the expected raw key length (32 bytes -> 64 hex chars).
const HMACKeyBytes = 32

// WriteVersionFile writes v atomically (write-tmp + fsync + rename + fsync-dir)
// using the shared atomic.go helper. File mode 0600 (HMAC key is sensitive).
func WriteVersionFile(path string, v VersionFile) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal version.json: %w", err)
	}
	return WriteTmpThenRename(path, data, 0600)
}

// ReadVersionFile reads path and validates the schema_version matches
// CurrentSchemaVersion. Fails CLOSED on:
//   - missing file (returns wrapped os.ErrNotExist; caller must run init)
//   - unparseable JSON (returns wrapped error suggesting `flashbackup init --reset-keys`)
//   - schema_version != CurrentSchemaVersion (returns clear version-mismatch error)
//   - hmac_key absent or wrong length (returns invalid-key error)
//
// Per invariant #11 (refined 2026-06-03 multi-hat round): silent re-init
// defeats the integrity-checksum threat model. Caller must use InitVersionFile
// for explicit initialization.
func ReadVersionFile(path string) (VersionFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return VersionFile{}, fmt.Errorf("read version.json: %w", err)
	}
	var v VersionFile
	if err := json.Unmarshal(data, &v); err != nil {
		return VersionFile{}, fmt.Errorf("parse version.json (corrupted or tampered; run `flashbackup init --reset-keys` to reinitialize): %w", err)
	}
	if v.SchemaVersion != CurrentSchemaVersion {
		return VersionFile{}, fmt.Errorf("version.json schema_version=%d unsupported (this build expects %d)", v.SchemaVersion, CurrentSchemaVersion)
	}
	// Validate HMAC key shape (hex-encoded 32 bytes -> 64 hex chars)
	keyBytes, err := hex.DecodeString(v.HMACKey)
	if err != nil {
		return VersionFile{}, fmt.Errorf("version.json hmac_key is not hex-encoded: %w", err)
	}
	if len(keyBytes) != HMACKeyBytes {
		return VersionFile{}, fmt.Errorf("version.json hmac_key length %d != expected %d bytes", len(keyBytes), HMACKeyBytes)
	}
	return v, nil
}

// InitVersionFile creates a fresh version.json with a randomly-generated
// HMAC key. ONLY called by `flashbackup init` (and `flashbackup init
// --reset-keys`, where force=true overwrites an existing file).
//
// Refuses to overwrite an existing valid version.json unless force=true.
// Reason: overwriting silently would invalidate all prior manifests because
// the HMAC key changes. The friction is the feature.
func InitVersionFile(path, flashbackupVersion string, force bool) (VersionFile, error) {
	if !force {
		if _, err := os.Stat(path); err == nil {
			return VersionFile{}, fmt.Errorf("version.json exists at %s; pass --reset-keys to overwrite (this invalidates all prior manifests)", path)
		} else if !os.IsNotExist(err) {
			return VersionFile{}, fmt.Errorf("stat version.json: %w", err)
		}
	}
	keyBytes := make([]byte, HMACKeyBytes)
	if _, err := rand.Read(keyBytes); err != nil {
		return VersionFile{}, fmt.Errorf("generate hmac key: %w", err)
	}
	v := VersionFile{
		SchemaVersion:      CurrentSchemaVersion,
		FlashbackupVersion: flashbackupVersion,
		HMACKey:            hex.EncodeToString(keyBytes),
	}
	if err := WriteVersionFile(path, v); err != nil {
		return VersionFile{}, fmt.Errorf("init version.json: %w", err)
	}
	return v, nil
}
