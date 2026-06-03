package hash

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

func TestStreamSHA256_Stream(t *testing.T) {
	data := []byte("hello world\n")
	h := sha256.New()
	h.Write(data)
	want := hex.EncodeToString(h.Sum(nil))

	got, n, err := StreamSHA256(context.Background(), bytes.NewReader(data))
	if err != nil {
		t.Fatalf("StreamSHA256 error: %v", err)
	}
	if n != int64(len(data)) {
		t.Errorf("byte count got %d want %d", n, len(data))
	}
	if got != want {
		t.Errorf("hash got %s want %s", got, want)
	}
}

func TestStreamSHA256_Empty(t *testing.T) {
	got, n, err := StreamSHA256(context.Background(), bytes.NewReader(nil))
	if err != nil {
		t.Fatalf("StreamSHA256 error: %v", err)
	}
	want := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if got != want || n != 0 {
		t.Errorf("got hash=%s n=%d, want hash=%s n=0", got, n, want)
	}
}

func TestStreamSHA256_CancelledContext(t *testing.T) {
	data := bytes.Repeat([]byte("x"), 10*1024*1024) // 10 MB
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel before invoking
	_, _, err := StreamSHA256(ctx, bytes.NewReader(data))
	if err == nil {
		t.Error("expected error on cancelled context, got nil")
	}
}
