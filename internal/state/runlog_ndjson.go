package state

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
)

// maxRunLogLineBytes is the upper bound on a single runs.ndjson line.
//
// AMENDMENT 2026-06-03: a 256 KB cap is well above any plausible line (a
// FinishedRun is well under 2 KB even with maximal path strings) and small
// enough that hitting it is a clear signal of corruption rather than
// silently truncating data, as a 16 MB cap would risk.
const maxRunLogLineBytes = 256 * 1024

// ndjsonRunLogStore is the v0.1 RunLogStore implementation: one JSON object
// per line, append-only, page-cache-durable on Append*, disk-durable on
// Checkpoint.
type ndjsonRunLogStore struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	open bool
}

// NewNDJSONRunLogStore opens (or creates) path for append. Each Append*
// writes one JSON line followed by '\n' to the page cache. Caller must
// Checkpoint at run boundaries for disk-durability and Close at end of run.
//
// File mode 0644 is intentional: runs.ndjson contains no secrets.
func NewNDJSONRunLogStore(path string) (RunLogStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open runs.ndjson: %w", err)
	}
	return &ndjsonRunLogStore{f: f, enc: json.NewEncoder(f), open: true}, nil
}

// AppendStarted writes a "started" line. Event is force-set to "started" so
// callers cannot accidentally mislabel a line.
func (s *ndjsonRunLogStore) AppendStarted(ctx context.Context, r StartedRun) error {
	r.Event = "started"
	return s.encode(ctx, r)
}

// AppendFinished writes a "finished" line. Event is force-set to "finished".
func (s *ndjsonRunLogStore) AppendFinished(ctx context.Context, r FinishedRun) error {
	r.Event = "finished"
	return s.encode(ctx, r)
}

func (s *ndjsonRunLogStore) encode(ctx context.Context, v any) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("append run log: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("append run log: run log closed")
	}
	if err := s.enc.Encode(v); err != nil {
		return fmt.Errorf("encode runlog: %w", err)
	}
	return nil
}

// Checkpoint fsyncs the underlying file. Runner should call this after each
// AppendStarted / AppendFinished so that a power loss does not orphan a
// started run that finished but never made it to disk.
func (s *ndjsonRunLogStore) Checkpoint(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("checkpoint run log: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("checkpoint run log: run log closed")
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("fsync runs.ndjson: %w", err)
	}
	return nil
}

// Close releases the file handle. Idempotent: a second call returns nil
// without re-closing. Does NOT fsync; callers wanting disk-durability at
// shutdown must Checkpoint first.
func (s *ndjsonRunLogStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.open = false
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("close runs.ndjson: %w", err)
	}
	return nil
}

// RunLogEntry is the discriminated union returned by ReadRunLog. Exactly one
// of Started or Finished is non-nil, matching Event.
type RunLogEntry struct {
	Event    string       `json:"event"`
	Started  *StartedRun  `json:"-"`
	Finished *FinishedRun `json:"-"`
}

// ReadRunLog reads path line-by-line and returns the valid entries plus a
// joined error (via errors.Join) of any per-line parse failures. Callers can
// use errors.Is / errors.As to inspect individual underlying errors.
//
// Invariant #10 (torn-write tolerance): unparseable lines are SKIPPED, not
// fatal; their parse error is accumulated into the returned error chain so
// the caller still sees that recovery happened.
//
// AMENDMENT 2026-06-03: a line exceeding maxRunLogLineBytes is NOT silently
// truncated. The scanner's bufio.ErrTooLong is detected and wrapped in a
// distinct error so the caller can react (likely abort) rather than infer
// false history.
//
// Behavior of bufio.Scanner on a torn-at-EOF line (the most realistic crash
// scenario): a final line without a trailing '\n' IS still returned by
// Scan() once and then Scan() returns false. So a malformed half-line at EOF
// surfaces here as a parse error, not as silently-dropped bytes — which is
// the behavior we want.
func ReadRunLog(path string) ([]RunLogEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open runs.ndjson: %w", err)
	}
	defer f.Close()

	var (
		entries []RunLogEntry
		errs    []error
	)
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), maxRunLogLineBytes)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var peek struct {
			Event string `json:"event"`
		}
		if err := json.Unmarshal(line, &peek); err != nil {
			errs = append(errs, fmt.Errorf("line %d: parse event: %w", lineNum, err))
			continue
		}
		switch peek.Event {
		case "started":
			var sr StartedRun
			if err := json.Unmarshal(line, &sr); err != nil {
				errs = append(errs, fmt.Errorf("line %d: parse started: %w", lineNum, err))
				continue
			}
			entries = append(entries, RunLogEntry{Event: "started", Started: &sr})
		case "finished":
			var fr FinishedRun
			if err := json.Unmarshal(line, &fr); err != nil {
				errs = append(errs, fmt.Errorf("line %d: parse finished: %w", lineNum, err))
				continue
			}
			entries = append(entries, RunLogEntry{Event: "finished", Finished: &fr})
		default:
			errs = append(errs, fmt.Errorf("line %d: unknown event %q", lineNum, peek.Event))
		}
	}
	if err := scanner.Err(); err != nil {
		if errors.Is(err, bufio.ErrTooLong) {
			return entries, fmt.Errorf("scan runs.ndjson: line exceeds %d bytes: %w", maxRunLogLineBytes, err)
		}
		return entries, fmt.Errorf("scan runs.ndjson: %w", err)
	}
	if len(errs) > 0 {
		return entries, errors.Join(errs...)
	}
	return entries, nil
}
