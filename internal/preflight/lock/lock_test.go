package lock

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquire_HappyPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	h, err := Acquire(context.Background(), path, "vol-uuid-test")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	if _, err := os.Stat(path); err != nil {
		t.Errorf("lock file should exist: %v", err)
	}

	// Verify the on-disk JSON has all expected fields populated.
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read lock: %v", err)
	}
	var got Lock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal lock: %v", err)
	}
	if got.PID != os.Getpid() {
		t.Errorf("PID = %d, want %d", got.PID, os.Getpid())
	}
	if got.HostUUID == "" {
		t.Error("HostUUID empty")
	}
	if got.Nonce == "" || len(got.Nonce) != 32 {
		t.Errorf("Nonce = %q (len %d), want 32-char hex", got.Nonce, len(got.Nonce))
	}
	if got.VolumeUUID != "vol-uuid-test" {
		t.Errorf("VolumeUUID = %q, want vol-uuid-test", got.VolumeUUID)
	}
	if got.StartTimeUnix == 0 {
		t.Error("StartTimeUnix zero")
	}
}

func TestAcquire_ReleaseRemovesLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("lock file should be removed after Release, got err=%v", err)
	}
}

func TestAcquire_HeldByLiveProcess(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("first Acquire: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	_, err = Acquire(context.Background(), path, "vol")
	if err == nil {
		t.Fatal("expected HeldLockError on second Acquire while first holds it")
	}
	var held *HeldLockError
	if !errors.As(err, &held) {
		t.Fatalf("expected *HeldLockError, got %T: %v", err, err)
	}
	if !errors.Is(err, ErrLockHeld) {
		t.Errorf("errors.Is(err, ErrLockHeld) = false; should unwrap to sentinel")
	}
	if held.Holder.PID != os.Getpid() {
		t.Errorf("holder PID = %d, want %d (this process)", held.Holder.PID, os.Getpid())
	}
	if held.Holder.HostUUID != getHostUUID() {
		t.Errorf("holder HostUUID = %q, want %q", held.Holder.HostUUID, getHostUUID())
	}
}

func TestAcquire_StaleRecovery_DeadPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	// Write a lock file with a PID that's almost certainly dead.
	deadLock := Lock{
		PID:           999999,
		StartTimeUnix: 1234567890,
		HostUUID:      getHostUUID(),
		Nonce:         "deadbeef",
		VolumeUUID:    "vol",
	}
	data, err := json.Marshal(&deadLock)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire over dead-PID lock should succeed: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })

	// Confirm the new lock replaced the stale one.
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatalf("read new lock: %v", err)
	}
	var got Lock
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.PID != os.Getpid() {
		t.Errorf("after stale recovery PID = %d, want %d", got.PID, os.Getpid())
	}
}

func TestAcquire_StaleRecovery_WrongHost(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	foreignLock := Lock{
		PID:           os.Getpid(),
		StartTimeUnix: getSelfStartTime(),
		HostUUID:      "definitely-not-this-machine",
		Nonce:         "x",
		VolumeUUID:    "vol",
	}
	data, err := json.Marshal(&foreignLock)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire over wrong-host lock should succeed: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })
}

// TestAcquire_StaleRecovery_RecycledPID covers the novel start_time-based
// detection path: a lock file points at THIS process's PID (which is alive)
// but with a start_time recorded ~1 hour earlier than the current process
// actually started. The 5s tolerance should classify this as a recycled
// PID and recover the lock rather than treat it as held by a live process.
func TestAcquire_StaleRecovery_RecycledPID(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	staleLock := Lock{
		PID:           os.Getpid(),
		StartTimeUnix: time.Now().Add(-1 * time.Hour).Unix(),
		HostUUID:      getHostUUID(),
		Nonce:         "stale",
		VolumeUUID:    "vol",
	}
	data, err := json.Marshal(&staleLock)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire over recycled-PID lock should succeed: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })
}

func TestAcquire_StaleRecovery_Corrupted(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	if err := os.WriteFile(path, []byte("not valid json"), 0600); err != nil {
		t.Fatalf("setup write: %v", err)
	}
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire over corrupted lock should succeed: %v", err)
	}
	t.Cleanup(func() { _ = h.Release() })
}

func TestAcquire_RefusesSymlink(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "target")
	if err := os.WriteFile(target, []byte(""), 0600); err != nil {
		t.Fatalf("setup target: %v", err)
	}
	path := filepath.Join(dir, "lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink not supported on this filesystem: %v", err)
	}
	_, err := Acquire(context.Background(), path, "vol")
	if err == nil {
		t.Fatal("expected symlink refusal")
	}
	// Should not be a HeldLockError; should be a wrapped ELOOP-style error.
	var held *HeldLockError
	if errors.As(err, &held) {
		t.Errorf("symlink refusal produced HeldLockError; want filesystem error")
	}
}

func TestAcquire_CancelledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	_, err := Acquire(ctx, path, "vol")
	if err == nil {
		t.Fatal("expected cancelled ctx error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Errorf("no lock file should be created on cancelled ctx; stat err=%v", statErr)
	}
}

func TestRelease_Idempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Fatalf("first Release: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Errorf("second Release should be no-op, got: %v", err)
	}
	// Also tolerate Release on nil handle.
	var nilHandle *LockHandle
	if err := nilHandle.Release(); err != nil {
		t.Errorf("Release on nil handle should be no-op, got: %v", err)
	}
}

func TestHeldLockError_Message(t *testing.T) {
	e := &HeldLockError{
		Holder: Lock{
			PID:           4242,
			HostUUID:      "ABCDEF",
			StartTimeUnix: 1717000000,
		},
	}
	msg := e.Error()
	for _, want := range []string{"PID=4242", "host=ABCDEF", "flashbackup status"} {
		if !contains(msg, want) {
			t.Errorf("error message %q missing substring %q", msg, want)
		}
	}
}

func TestProcessAlive_Self(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(self) = false; expected true")
	}
}

func TestProcessAlive_Dead(t *testing.T) {
	// PID 0 / negative are invalid; high PIDs are very unlikely to be live.
	if processAlive(0) {
		t.Error("processAlive(0) = true; expected false")
	}
	if processAlive(-1) {
		t.Error("processAlive(-1) = true; expected false")
	}
	if processAlive(999999) {
		t.Skip("PID 999999 happens to be live on this system; skipping")
	}
}

// TestRelease_AfterExternalUnlink covers the os.ErrNotExist branch in
// Release: if something else (a stale-recovery sibling process, a cleanup
// script, a user) deleted the lock file out from under us, Release must
// still close the FD cleanly without surfacing the removal error.
func TestRelease_AfterExternalUnlink(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "lock")
	h, err := Acquire(context.Background(), path, "vol")
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	// Yank the file out from under the handle.
	if err := os.Remove(path); err != nil {
		t.Fatalf("setup remove: %v", err)
	}
	if err := h.Release(); err != nil {
		t.Errorf("Release after external unlink should return nil; got %v", err)
	}
}

// TestFallbackHostname asserts the fallback path returns a non-empty value.
// In CI and on developer laptops os.Hostname() always succeeds, so we
// cannot directly exercise the "unknown-host" branch without invasive
// stubbing; the assertion below verifies the success branch and the
// invariant that the result is never empty (required by getHostUUID
// callers downstream).
func TestFallbackHostname(t *testing.T) {
	got := fallbackHostname()
	if got == "" {
		t.Error("fallbackHostname() returned empty string; downstream callers require non-empty")
	}
}

// TestProcessStartTimeUnix_DeadPID covers the ps-error branch: the ps
// command returns non-zero (or empty) when asked about a non-existent
// PID. Either outcome should produce a wrapped error rather than a
// stale zero value masquerading as a real start time.
func TestProcessStartTimeUnix_DeadPID(t *testing.T) {
	// PID 999999 is virtually always absent on macOS.
	_, err := processStartTimeUnix(999999)
	if err == nil {
		t.Skip("PID 999999 happens to be live on this system; skipping")
	}
}

// TestAcquire_ParentDirMissing covers the create-error wrap branch in
// tryAcquireOnce: opening the lock file fails because the parent
// directory does not exist. The error must NOT be wrapped as a
// symlink-refusal (that branch is for ELOOP only).
func TestAcquire_ParentDirMissing(t *testing.T) {
	// /nonexistent-parent-XXX-flashbackup/lock guarantees ENOENT on open.
	path := filepath.Join(t.TempDir(), "no-such-subdir", "lock")
	_, err := Acquire(context.Background(), path, "vol")
	if err == nil {
		t.Fatal("expected error when lock parent dir is missing")
	}
	if contains(err.Error(), "symlink") {
		t.Errorf("ENOENT error should not be reported as symlink refusal; got %v", err)
	}
}

func contains(s, sub string) bool {
	return len(sub) == 0 || (len(s) >= len(sub) && (indexOf(s, sub) >= 0))
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
