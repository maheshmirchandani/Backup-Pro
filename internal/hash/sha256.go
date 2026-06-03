// Package hash provides streaming SHA256 with constant memory regardless of
// input size. Invariant #1: source and destination both hashed; manifest
// records sha256_source captured at read time.
package hash

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"sync"
)

const bufSize = 1 << 20 // 1 MB; modern APFS prefers large sequential reads

// bufPool reuses 1 MB byte slices across StreamSHA256 calls.
// Per Performance hat: a 100K-file backup would otherwise allocate ~100 GB of garbage.
var bufPool = sync.Pool{
	New: func() any {
		buf := make([]byte, bufSize)
		return &buf
	},
}

// StreamSHA256 reads r to EOF, returning the hex-encoded SHA256 and total
// bytes read. Constant memory; uses a pooled 1 MB buffer.
//
// Cancellation: ctx is checked after each io.CopyBuffer cycle (granularity
// approximately one buffer's worth of reads). For a fully responsive cancel
// on multi-GB files, callers may want to wrap StreamSHA256 itself with a
// goroutine + select rather than relying on this internal check alone.
func StreamSHA256(ctx context.Context, r io.Reader) (digest string, n int64, err error) {
	if err := ctx.Err(); err != nil {
		return "", 0, fmt.Errorf("hash source: %w", err)
	}
	h := sha256.New()
	bufPtr := bufPool.Get().(*[]byte)
	defer bufPool.Put(bufPtr)
	written, copyErr := io.CopyBuffer(h, r, *bufPtr)
	if copyErr != nil {
		return "", written, fmt.Errorf("hash source: %w", copyErr)
	}
	if err := ctx.Err(); err != nil {
		return "", written, fmt.Errorf("hash source cancelled: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), written, nil
}
