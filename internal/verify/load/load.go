package load

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// rawLineMaxBytes bounds the raw-line capture stored in SchemaError. Keeps the
// per-error payload small in support bundles even when the offending line is
// huge (e.g., a megabyte of garbage piped in by a corrupted writer).
const rawLineMaxBytes = 200

// scannerMaxBytes is the per-line cap for the underlying bufio.Scanner. A
// normal manifest line is well under 4 KiB; this gives 16x headroom for
// pathological paths and unusual statuses before the scanner gives up.
const scannerMaxBytes = 64 * 1024

// ctxCheckInterval is the entry interval at which Load polls ctx.Err. Matches
// the t1_enumerate.go cadence so cancellation latency feels uniform across
// the pipeline.
const ctxCheckInterval = 256

// IntegrityError is one entry that failed HMAC verification. The entry is
// surfaced (with its content as-read) so the caller can report which path
// tampered. AC-19 verify reporting depends on the entry being present in the
// result; a silent skip would defeat the keyed integrity checksum threat
// model (invariant #33).
type IntegrityError struct {
	// LineNumber is the 1-indexed offset within the gzipped stream of the
	// offending line. Useful in support bundles to correlate with raw gunzip
	// output.
	LineNumber int

	// Entry is the as-read entry whose HMAC did not match the recomputed
	// checksum. The HMAC field on Entry is the persisted (mismatched) value.
	Entry state.ManifestEntry

	// Reason is a short, human-readable explanation. Currently always
	// "hmac mismatch"; finer granularity may be added if a future failure
	// mode is distinguishable (e.g., a key-length mismatch caught at compare
	// rather than version-file load).
	Reason string
}

// SchemaError is one line that failed to parse as a ManifestEntry or whose
// per-entry V field could not be evaluated. Distinct from IntegrityError
// (which surfaces tampered but well-formed entries).
type SchemaError struct {
	// LineNumber is the 1-indexed offset within the gzipped stream.
	LineNumber int

	// RawLine is the offending line, truncated to rawLineMaxBytes. Useful for
	// operator debugging; bounded so a malformed multi-megabyte line does not
	// inflate the LoadResult.
	RawLine string

	// Reason is a short, human-readable explanation: "invalid json",
	// "empty entry", "missing hmac", etc.
	Reason string
}

// LoadOptions configures Load. All fields are required; an empty path is
// rejected at entry rather than silently substituting a default.
type LoadOptions struct {
	// ManifestPath is the absolute path to the per-run manifest.ndjson.gz
	// (NOT .tmp.gz). See internal/runner/t5_finalize.manifestBaseFilename.
	ManifestPath string

	// VersionFilePath is the absolute path to <USB>/.flashbackup/version.json
	// carrying the per-USB HMAC key. Loaded via state.ReadVersionFile which
	// is fail-closed (invariant #11): missing or corrupt version.json aborts.
	VersionFilePath string
}

// LoadResult is the typed output of Load. EntriesScanned is the total line
// count and always equals len(Entries) + len(IntegrityErrors) + len(SchemaErrors).
type LoadResult struct {
	// Entries are the manifest entries that passed both HMAC verification
	// and schema parse. These are safe for downstream verify pipeline use
	// (Task 31 hash recompute against destination files).
	Entries []state.ManifestEntry

	// IntegrityErrors are entries that parsed cleanly but failed HMAC
	// verification (invariant #33; AC-19 path).
	IntegrityErrors []IntegrityError

	// SchemaErrors are lines that failed JSON parse or basic shape checks.
	SchemaErrors []SchemaError

	// SchemaVersion is the on-USB schema version from version.json. Always
	// state.CurrentSchemaVersion (1) in v0.1 (ReadVersionFile rejects
	// anything else as a pipeline error).
	SchemaVersion int

	// EntriesScanned is the total line count, regardless of outcome.
	EntriesScanned int
}

// Load opens the gzipped manifest at opts.ManifestPath, verifies each entry's
// HMAC inline against the per-USB key loaded via state.ReadVersionFile
// (fail-closed; no init), and returns the entries plus per-line error slices.
//
// Errors returned by Load itself are PIPELINE errors: file open failure, gzip
// stream failure, version.json read failure, schema_version mismatch on the
// version file, or a per-entry V field that disagrees with
// state.CurrentSchemaVersion (a structural manifest issue, distinct from a
// tamper).
//
// Per-line failures (HMAC mismatch, JSON parse error) land in IntegrityErrors
// or SchemaErrors and DO NOT abort the load. Caller decides how to surface
// them (Task 32 will map them to VerifyResult.FilesIntegrityFailed and the
// "integrity_failed" exit status).
func Load(ctx context.Context, opts LoadOptions) (*LoadResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("verify load: %w", err)
	}
	if opts.ManifestPath == "" {
		return nil, errors.New("verify load: ManifestPath is empty")
	}
	if opts.VersionFilePath == "" {
		return nil, errors.New("verify load: VersionFilePath is empty")
	}

	vf, err := state.ReadVersionFile(opts.VersionFilePath)
	if err != nil {
		return nil, fmt.Errorf("verify load: read version file %q: %w", opts.VersionFilePath, err)
	}

	// hex decode shape was already validated by ReadVersionFile; the decode
	// here cannot fail in practice. We keep the defensive surface so a
	// future widening of the key field (e.g. base64) does not silently
	// truncate.
	hmacKey, err := hex.DecodeString(vf.HMACKey)
	if err != nil {
		return nil, fmt.Errorf("verify load: decode hmac key: %w", err)
	}

	f, err := os.Open(opts.ManifestPath)
	if err != nil {
		return nil, fmt.Errorf("verify load: open manifest %q: %w", opts.ManifestPath, err)
	}
	defer f.Close()

	gr, err := gzip.NewReader(f)
	if err != nil {
		return nil, fmt.Errorf("verify load: gzip reader for %q: %w", opts.ManifestPath, err)
	}
	// gr.Close is called explicitly below on the success path to surface
	// trailer-validation errors (truncated gzip). On error paths the defer
	// here is the safety net: a second Close after a successful close is a
	// no-op for gzip.Reader.
	defer gr.Close()

	result := &LoadResult{
		SchemaVersion: vf.SchemaVersion,
	}

	scanner := bufio.NewScanner(gr)
	scanner.Buffer(make([]byte, 0, 4096), scannerMaxBytes)

	lineNo := 0
	for scanner.Scan() {
		lineNo++
		result.EntriesScanned++

		if lineNo%ctxCheckInterval == 0 {
			if err := ctx.Err(); err != nil {
				return nil, fmt.Errorf("verify load: cancelled at line %d: %w", lineNo, err)
			}
		}

		line := scanner.Bytes()
		if len(line) == 0 {
			// An empty gzipped line is structurally malformed (the writer
			// always emits `{...}\n`). Surface as a SchemaError so the
			// operator sees the line count, not silently ignored.
			result.SchemaErrors = append(result.SchemaErrors, SchemaError{
				LineNumber: lineNo,
				RawLine:    "",
				Reason:     "empty line",
			})
			continue
		}

		var entry state.ManifestEntry
		if err := json.Unmarshal(line, &entry); err != nil {
			result.SchemaErrors = append(result.SchemaErrors, SchemaError{
				LineNumber: lineNo,
				RawLine:    truncateLine(line),
				Reason:     fmt.Sprintf("invalid json: %v", err),
			})
			continue
		}

		// Structural schema check on the per-entry V field. A mixed-V manifest
		// is a structural corruption (the writer always uses the current
		// schema), distinct from a tamper. Surface as a pipeline error per
		// master plan line 2477 ("reject if schema_version != 1").
		if entry.V != state.CurrentSchemaVersion {
			return nil, fmt.Errorf("verify load: manifest line %d has schema_version %d, this build expects %d",
				lineNo, entry.V, state.CurrentSchemaVersion)
		}

		if entry.HMAC == "" {
			result.SchemaErrors = append(result.SchemaErrors, SchemaError{
				LineNumber: lineNo,
				RawLine:    truncateLine(line),
				Reason:     "missing hmac",
			})
			continue
		}

		// invariant #33: keyed integrity checksum over the length-prefixed
		// canonical encoding. state.VerifyHMAC reuses the same canonical
		// helper as the writer; do not reimplement here.
		if !state.VerifyHMAC(entry, hmacKey) {
			result.IntegrityErrors = append(result.IntegrityErrors, IntegrityError{
				LineNumber: lineNo,
				Entry:      entry,
				Reason:     "hmac mismatch",
			})
			continue
		}

		result.Entries = append(result.Entries, entry)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("verify load: scan manifest %q at line %d: %w", opts.ManifestPath, lineNo, err)
	}

	// Final ctx check so a cancellation arriving after the last line still
	// surfaces (the in-loop check only fires on every ctxCheckInterval'th
	// iteration).
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("verify load: cancelled after %d lines: %w", lineNo, err)
	}

	// gzip.Reader.Close validates the gzip trailer; a deferred Close swallows
	// the error, so we explicitly close here to surface truncation.
	if err := gr.Close(); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("verify load: close gzip reader for %q: %w", opts.ManifestPath, err)
	}

	return result, nil
}

// truncateLine returns line as a string truncated to rawLineMaxBytes. Used by
// SchemaError.RawLine to keep per-error payloads bounded.
func truncateLine(line []byte) string {
	if len(line) <= rawLineMaxBytes {
		return string(line)
	}
	return string(line[:rawLineMaxBytes])
}
