package state

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// ndjsonManifestStore is the v0.1 ManifestStore: NDJSON written through a
// stream-gzip writer to <path>.tmp.gz, atomically renamed to <path>.gz at
// finalize.
//
// Performance notes (per multi-hat Performance review):
//   - mac is allocated once and Reset() between entries (~10x fewer allocs).
//   - canonBuf is a reusable bytes.Buffer for canonical encoding, reset
//     between entries.
//   - gzip.BestSpeed (level 1) gives 80-90% of default compression ratio at
//     3-5x throughput; manifest NDJSON is highly compressible so the ratio
//     loss is small.
type ndjsonManifestStore struct {
	mu        sync.Mutex
	path      string
	hmacKey   []byte
	tmpFile   *os.File
	gzWriter  *gzip.Writer
	jsonEnc   *json.Encoder
	mac       hash.Hash    // reused HMAC instance, Reset() between entries
	canonBuf  bytes.Buffer // reused canonical-encoding scratch buffer
	open      bool
	finalized bool
}

// NewNDJSONManifestStore opens path+".tmp.gz" for stream-gzip writing per
// invariant #57. Gzip() renames .tmp.gz to .gz when done.
//
// hmacKey is the per-USB keyed-checksum key, read from version.json. It is
// retained for the lifetime of the store.
func NewNDJSONManifestStore(path string, hmacKey []byte) (ManifestStore, error) {
	tmpPath := path + ".tmp.gz"
	f, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open manifest tmp: %w", err)
	}
	gz, err := gzip.NewWriterLevel(f, gzip.BestSpeed)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("create gzip writer: %w", err)
	}
	return &ndjsonManifestStore{
		path:     path,
		hmacKey:  hmacKey,
		tmpFile:  f,
		gzWriter: gz,
		jsonEnc:  json.NewEncoder(gz),
		mac:      hmac.New(sha256.New, hmacKey),
		open:     true,
	}, nil
}

// AppendEntry computes the entry's keyed integrity checksum (invariant #33),
// then JSON-encodes the full struct (including HMAC field) as one NDJSON line
// through the gzip stream. See ManifestStore docstring for the durability
// contract.
func (s *ndjsonManifestStore) AppendEntry(ctx context.Context, e ManifestEntry) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("append manifest entry: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("append manifest entry: manifest store closed")
	}
	e.HMAC = s.computeHMACLocked(e)
	if err := s.jsonEnc.Encode(e); err != nil {
		return fmt.Errorf("encode manifest entry: %w", err)
	}
	return nil
}

// computeHMACLocked computes the keyed integrity checksum over the canonical
// length-prefixed encoding. Caller must hold s.mu (the reused mac + canonBuf
// are not safe for concurrent use).
func (s *ndjsonManifestStore) computeHMACLocked(e ManifestEntry) string {
	s.canonBuf.Reset()
	writeCanonical(&s.canonBuf, e)
	s.mac.Reset()
	s.mac.Write(s.canonBuf.Bytes())
	return hex.EncodeToString(s.mac.Sum(nil))
}

// writeCanonical writes the length-prefixed canonical encoding of e into buf.
//
// SECURITY: length-prefixed (not delimiter-separated) encoding is required.
// A delimiter-based scheme like fmt.Sprintf("%d|%s|%d|...", ...) is forgeable:
// a path containing '|' can be crafted so that two different (path, size)
// tuples produce identical canonical strings and therefore identical HMACs.
// AMENDMENT 2026-06-03 (multi-hat round). See TestHMAC_PipeSeparatorForgeryRejected.
func writeCanonical(buf *bytes.Buffer, e ManifestEntry) {
	// binary.Write to bytes.Buffer never returns an error
	_ = binary.Write(buf, binary.BigEndian, uint32(e.V))
	writeLenPrefixed(buf, e.Path)
	_ = binary.Write(buf, binary.BigEndian, e.Size)
	_ = binary.Write(buf, binary.BigEndian, e.MtimeNS)
	writeLenPrefixed(buf, e.SHA256Source)
	writeLenPrefixed(buf, e.CopiedAt.UTC().Format(time.RFC3339Nano))
	writeLenPrefixed(buf, string(e.Status))
}

// writeLenPrefixed writes a uint32 length (BigEndian) followed by the string
// bytes. This is the building block that makes the canonical encoding
// non-forgeable.
func writeLenPrefixed(buf *bytes.Buffer, s string) {
	_ = binary.Write(buf, binary.BigEndian, uint32(len(s)))
	buf.WriteString(s)
}

// Gzip closes the gzip writer (flushing the gzip trailer), fsyncs the
// underlying tmp file, closes it, atomically renames .tmp.gz to .gz, and
// fsyncs the parent dir to durably persist the rename. Idempotent: a second
// call returns nil without re-running.
func (s *ndjsonManifestStore) Gzip(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("gzip manifest: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.finalized {
		return nil
	}
	if err := s.gzWriter.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	if err := s.tmpFile.Sync(); err != nil {
		return fmt.Errorf("fsync manifest: %w", err)
	}
	if err := s.tmpFile.Close(); err != nil {
		return fmt.Errorf("close manifest file: %w", err)
	}
	tmpPath := s.path + ".tmp.gz"
	finalPath := s.path + ".gz"
	if err := os.Rename(tmpPath, finalPath); err != nil {
		return fmt.Errorf("rename manifest %s -> %s: %w", tmpPath, finalPath, err)
	}
	// fsync parent dir to durably persist the rename (matches WriteTmpThenRename).
	dir, err := os.Open(filepath.Dir(finalPath))
	if err != nil {
		return fmt.Errorf("open parent dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	// Remove any leftover uncompressed manifest from older schema versions.
	_ = os.Remove(s.path)
	s.finalized = true
	s.open = false
	return nil
}

// VerifyHMAC returns true if e.HMAC matches the keyed integrity checksum
// computed for the entry under hmacKey. Uses hmac.Equal for constant-time
// comparison. Use this when reading manifests during `flashbackup verify`
// (AC-19).
//
// Per invariant #33 (rewritten 2026-06-03): this is a keyed integrity
// checksum, not authentication. It detects silent destination corruption and
// casual tampering, but an attacker with write access to the USB can read the
// key from version.json on the same volume.
func VerifyHMAC(e ManifestEntry, hmacKey []byte) bool {
	mac := hmac.New(sha256.New, hmacKey)
	var buf bytes.Buffer
	writeCanonical(&buf, e)
	mac.Write(buf.Bytes())
	expected := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(e.HMAC), []byte(expected))
}
