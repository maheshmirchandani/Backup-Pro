package hash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"pgregory.net/rapid"
)

// Property: StreamSHA256 over arbitrary byte slices must match the stdlib
// reference hash regardless of internal chunk boundaries (invariant #41).
func TestSHA256_ChunkBoundaryInvariance(t *testing.T) {
	rapid.Check(t, func(t *rapid.T) {
		data := rapid.SliceOfN(rapid.Byte(), 0, 200000).Draw(t, "data")

		h := sha256.New()
		h.Write(data)
		want := hex.EncodeToString(h.Sum(nil))

		got, _, err := StreamSHA256(context.Background(), bytes.NewReader(data))
		if err != nil {
			t.Fatalf("error: %v", err)
		}
		if got != want {
			t.Fatalf("hash mismatch: stdlib=%s stream=%s len=%d", want, got, len(data))
		}
	})
}
