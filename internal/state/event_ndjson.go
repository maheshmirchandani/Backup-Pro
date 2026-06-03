package state

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

// ndjsonEventStore is the v0.1 EventStore implementation: one JSON object per
// line, append-only, page-cache-durable on Append, disk-durable on Checkpoint.
type ndjsonEventStore struct {
	mu   sync.Mutex
	f    *os.File
	enc  *json.Encoder
	open bool
}

// NewNDJSONEventStore opens (or creates) path for append. Each Append writes
// one JSON line followed by '\n' to the page cache. Caller must Checkpoint at
// phase boundaries for disk-durability and Close at end of run.
//
// File mode 0644 is intentional: events.ndjson contains no secrets and may be
// read by support tooling running as the user.
func NewNDJSONEventStore(path string) (EventStore, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open events.ndjson: %w", err)
	}
	return &ndjsonEventStore{f: f, enc: json.NewEncoder(f), open: true}, nil
}

// Append encodes ev as one JSON line. Returns when the bytes are in the OS
// page cache (NOT fsynced). See EventStore docstring for the durability
// contract.
func (s *ndjsonEventStore) Append(ctx context.Context, ev Event) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("append event: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("append event: event store closed")
	}
	if err := s.enc.Encode(ev); err != nil {
		return fmt.Errorf("encode event: %w", err)
	}
	return nil
}

// Checkpoint fsyncs the underlying file. Runner calls this at phase
// boundaries so that crash recovery sees at worst one phase of lost events.
func (s *ndjsonEventStore) Checkpoint(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("checkpoint event store: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return fmt.Errorf("checkpoint event store: event store closed")
	}
	if err := s.f.Sync(); err != nil {
		return fmt.Errorf("fsync events.ndjson: %w", err)
	}
	return nil
}

// Close releases the file handle. Idempotent: a second call returns nil
// without re-closing. Does NOT fsync; callers wanting disk-durability at
// shutdown must Checkpoint first.
func (s *ndjsonEventStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.open {
		return nil
	}
	s.open = false
	if err := s.f.Close(); err != nil {
		return fmt.Errorf("close events.ndjson: %w", err)
	}
	return nil
}
