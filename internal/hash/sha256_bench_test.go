package hash

import (
	"bytes"
	"context"
	"testing"
)

// BenchmarkStreamSHA256_LargeFile measures throughput on a 100 MB buffer.
// SLO (spec invariant #55): >= 1 GB/s on Apple Silicon (~100 ms per iteration).
func BenchmarkStreamSHA256_LargeFile(b *testing.B) {
	data := bytes.Repeat([]byte("A"), 100*1024*1024)
	ctx := context.Background()
	b.SetBytes(int64(len(data)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, _, err := StreamSHA256(ctx, bytes.NewReader(data))
		if err != nil {
			b.Fatal(err)
		}
	}
}
