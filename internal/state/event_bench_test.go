package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"
)

// BenchmarkEventAppend_WithoutSync measures append throughput without
// per-event fsync.
//
// Amendment 2026-06-03 (multi-hat round): per-event fsync would cost an
// estimated 5-15 min added wall time on a 100K-file backup against a USB
// disk; the Checkpoint pattern bounds disk-durability to phase boundaries
// while keeping append throughput at memory speeds. This benchmark makes
// the win visible: ns/op and allocs/op should remain small.
func BenchmarkEventAppend_WithoutSync(b *testing.B) {
	tmp := filepath.Join(b.TempDir(), "events.ndjson")
	store, err := NewNDJSONEventStore(tmp)
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	ev := Event{
		V:         1,
		Timestamp: time.Now().UTC(),
		Phase:     "T2",
		Kind:      "file_completed",
		Path:      "Documents/example.pdf",
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := store.Append(ctx, ev); err != nil {
			b.Fatal(err)
		}
	}
}
