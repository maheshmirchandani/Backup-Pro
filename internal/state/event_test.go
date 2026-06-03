package state

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestEvent_JSONShape locks the wire format. If this test fails because of a
// field rename or tag change, bump Event.V and add a migration note before
// changing the assertion.
func TestEvent_JSONShape(t *testing.T) {
	ev := Event{
		V:         1,
		Timestamp: time.Date(2026, 6, 3, 14, 30, 15, 0, time.UTC),
		Phase:     "T1",
		Kind:      "file_completed",
		Path:      "Documents/foo.pdf",
		Details:   map[string]any{"bytes": 12345},
	}
	got, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	want := `{"v":1,"timestamp":"2026-06-03T14:30:15Z","phase":"T1","kind":"file_completed","path":"Documents/foo.pdf","details":{"bytes":12345}}`
	if !bytes.Equal(got, []byte(want)) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func TestNDJSONEventStore_AppendCheckpointClose(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	ctx := context.Background()
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	ev := Event{V: 1, Timestamp: time.Unix(0, 0).UTC(), Phase: "T0", Kind: "phase_started"}
	if err := store.Append(ctx, ev); err != nil {
		t.Fatalf("append: %v", err)
	}
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `{"v":1,"timestamp":"1970-01-01T00:00:00Z","phase":"T0","kind":"phase_started"}` + "\n"
	if string(data) != want {
		t.Errorf("got %q\nwant %q", string(data), want)
	}
}

// TestEvent_CanonicalKinds_RoundTrip ensures every Kind from the plan's
// Canonical Event Kinds table marshals + unmarshals without loss. If a new
// Kind is added to the table, add it here too.
func TestEvent_CanonicalKinds_RoundTrip(t *testing.T) {
	kinds := []string{
		"phase_started", "phase_completed", "phase_aborted",
		"lock_acquired", "lock_stale_detected", "lock_contention",
		"filesystem_refused", "volume_uuid_changed",
		"file_enumerated", "transfer_started", "transfer_completed", "transfer_failed",
		"file_completed", "hash_mismatch", "source_mutated",
		"atomic_gate_blocked", "delete_completed", "delete_skipped_mutated", "delete_failed",
		"manifest_finalized", "run_finished",
	}
	if len(kinds) != 21 {
		t.Fatalf("expected 21 canonical kinds, got %d", len(kinds))
	}
	for _, kind := range kinds {
		kind := kind
		t.Run(kind, func(t *testing.T) {
			ev := Event{V: 1, Timestamp: time.Unix(1700000000, 0).UTC(), Phase: "T1", Kind: kind}
			data, err := json.Marshal(ev)
			if err != nil {
				t.Fatalf("marshal %s: %v", kind, err)
			}
			var got Event
			if err := json.Unmarshal(data, &got); err != nil {
				t.Fatalf("unmarshal %s: %v", kind, err)
			}
			if got.Kind != kind {
				t.Errorf("round-trip lost kind: got %q want %q", got.Kind, kind)
			}
			if got.V != 1 || got.Phase != "T1" {
				t.Errorf("round-trip lost fields: %+v", got)
			}
		})
	}
}

func TestNDJSONEventStore_AppendAfterClose(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = store.Append(context.Background(), Event{V: 1, Kind: "phase_started"})
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected closed error, got %v", err)
	}
}

func TestNDJSONEventStore_CheckpointAfterClose(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	err = store.Checkpoint(context.Background())
	if err == nil || !strings.Contains(err.Error(), "closed") {
		t.Errorf("expected closed error, got %v", err)
	}
}

func TestNDJSONEventStore_CloseIdempotent(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Errorf("second close (should be idempotent): %v", err)
	}
}

func TestNDJSONEventStore_AppendCancelledContext(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.Append(ctx, Event{V: 1, Kind: "phase_started"})
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

func TestNDJSONEventStore_CheckpointCancelledContext(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer store.Close()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = store.Checkpoint(ctx)
	if err == nil {
		t.Error("expected error on cancelled context")
	}
}

// TestNDJSONEventStore_ConcurrentAppend asserts the documented thread-safety
// guarantee: many goroutines appending in parallel produce exactly N
// well-formed lines, no torn writes.
func TestNDJSONEventStore_ConcurrentAppend(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	ctx := context.Background()

	const goroutines = 16
	const perG = 50
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < perG; i++ {
				ev := Event{
					V:         1,
					Timestamp: time.Unix(int64(g*1000+i), 0).UTC(),
					Phase:     "T1",
					Kind:      "file_completed",
					Path:      "p",
				}
				if err := store.Append(ctx, ev); err != nil {
					t.Errorf("append: %v", err)
					return
				}
			}
		}(g)
	}
	wg.Wait()
	if err := store.Checkpoint(ctx); err != nil {
		t.Fatalf("checkpoint: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	data, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != goroutines*perG {
		t.Fatalf("expected %d lines, got %d", goroutines*perG, len(lines))
	}
	for i, line := range lines {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err != nil {
			t.Errorf("line %d not valid JSON: %v (line: %q)", i, err, line)
		}
	}
}
