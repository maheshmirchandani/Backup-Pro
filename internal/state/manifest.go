package state

import (
	"context"
	"time"
)

// FileStatus is the T2 classification for one file in a run.
type FileStatus string

const (
	StatusVerified         FileStatus = "verified"
	StatusHashMismatch     FileStatus = "hash_mismatch"
	StatusSourceMutated    FileStatus = "source_mutated"
	StatusNotTransferred   FileStatus = "not_transferred"
	StatusSourceUnreadable FileStatus = "source_unreadable"
	StatusDestUnreadable   FileStatus = "dest_unreadable"
)

// DeletionStatus is the T3 outcome (move mode only).
type DeletionStatus string

const (
	DeletionDeleted          DeletionStatus = "deleted"
	DeletionSkippedMutated   DeletionStatus = "skipped_mutated"
	DeletionFailedImmutable  DeletionStatus = "failed_immutable"
	DeletionFailedPermission DeletionStatus = "failed_permission"
)

// IntegrityStatus is the per-entry HMAC check outcome at verify time. Distinct
// from FileStatus (which is the T2 byte-level classification). Used by AC-19:
// `flashbackup verify` recomputes HMAC for every manifest line and reports
// IntegrityFailed when the keyed checksum does not match.
type IntegrityStatus string

const (
	IntegrityVerified IntegrityStatus = "integrity_verified"
	IntegrityFailed   IntegrityStatus = "integrity_failed"
)

// ManifestEntry is one line in manifest.ndjson(.gz).
//
// HMAC is a keyed integrity checksum over (V, Path, Size, MtimeNS,
// SHA256Source, CopiedAt, Status) using a per-USB key from version.json
// (invariant #33, rewritten 2026-06-03 to "keyed integrity checksum, not
// authentication": it detects silent corruption and casual tampering on the
// destination volume, but a determined attacker with write access to the USB
// can simply read the key out of version.json on the same volume).
//
// V is the schema version (invariant #13). Bump V before changing field shape
// rather than silently re-shaping.
type ManifestEntry struct {
	V              int            `json:"v"`
	Path           string         `json:"path"`
	Size           int64          `json:"size"`
	MtimeNS        int64          `json:"mtime_ns"`
	SHA256Source   string         `json:"sha256_source"`
	CopiedAt       time.Time      `json:"copied_at"`
	Status         FileStatus     `json:"status"`
	DeletionStatus DeletionStatus `json:"deletion_status,omitempty"`
	HMAC           string         `json:"hmac,omitempty"`
}

// ManifestStore writes per-file entries during T2 directly to a stream-gzip
// writer (invariant #57: gzip-stream during T2, not gzip-after at T4).
//
// Single-writer contract: ManifestStore is safe for ONE T2 goroutine only.
// The internal mutex defends against test-suite misuse, NOT against concurrent
// hashing workers. A future parallel-hashing implementation must use the actor
// pattern (one goroutine owns the store, others send entries on a channel).
//
// Contract:
//   - AppendEntry: computes HMAC, JSON-encodes the entry, writes one line +
//     '\n' through the gzip stream. Bytes land in the gzip writer's internal
//     buffer; flush-to-disk is not guaranteed until Gzip().
//   - Gzip: T4 finalization. Closes the gzip writer (flushing the gzip
//     trailer), fsyncs the underlying file, closes the file, atomically
//     renames .tmp.gz to .gz, fsyncs the parent dir. Idempotent.
//   - All public methods take ctx and return early on ctx.Err().
type ManifestStore interface {
	AppendEntry(ctx context.Context, e ManifestEntry) error
	Gzip(ctx context.Context) error
}
