package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Lock is the JSON payload of the lock file. Captured at acquisition;
// re-verified by future preflight calls to detect remount / wrong machine.
type Lock struct {
	PID           int    `json:"pid"`
	StartTimeUnix int64  `json:"start_time_unix"`
	HostUUID      string `json:"host_uuid"`
	// Nonce is 16 random bytes hex-encoded; defends against PID + start_time +
	// host_uuid collision in pathological cases.
	Nonce string `json:"nonce"`
	// VolumeUUID is captured at T0 for invariant #30 cross-check; this package
	// stores it but does not validate it.
	VolumeUUID string `json:"volume_uuid"`
}

// LockHandle is returned by Acquire. Release MUST be called on it to clean up
// the lock file. Use defer.
type LockHandle struct {
	path string
	file *os.File
}

// ErrLockHeld means another live FlashBackup process owns the lock.
// Inspect the wrapped *Lock for diagnostic details (which PID/host).
var ErrLockHeld = errors.New("lock held by live process")

// HeldLockError wraps ErrLockHeld with the holder's Lock for diagnostics.
type HeldLockError struct {
	Holder Lock
}

func (e *HeldLockError) Error() string {
	return fmt.Sprintf("lock held by PID=%d host=%s since %s; run 'flashbackup status' for details",
		e.Holder.PID, e.Holder.HostUUID, time.Unix(e.Holder.StartTimeUnix, 0).Format(time.RFC3339))
}

// Unwrap exposes the sentinel for errors.Is checks.
func (e *HeldLockError) Unwrap() error { return ErrLockHeld }

// errStaleLockRecovered is an internal sentinel signalling that the caller
// should retry Acquire after a stale lock was unlinked.
var errStaleLockRecovered = errors.New("stale lock recovered; retrying")

var (
	hostUUIDOnce sync.Once
	hostUUID     string

	selfStartOnce sync.Once
	selfStart     int64
)

// getHostUUID returns this machine's stable identifier (darwin: ioreg
// IOPlatformUUID; other: os.Hostname). Cached for the process lifetime.
func getHostUUID() string {
	hostUUIDOnce.Do(func() {
		hostUUID = lookupHostUUID()
	})
	return hostUUID
}

// getSelfStartTime returns this process's approximate start time as a Unix
// timestamp. We capture it once at first call (typically at preflight init),
// which is sufficient for stale-detection: a recycled PID elsewhere on the
// system will have a different start_time when ps reports it.
func getSelfStartTime() int64 {
	selfStartOnce.Do(func() {
		selfStart = time.Now().Unix()
	})
	return selfStart
}

// Acquire tries to acquire the lock at lockFilePath. On success returns a
// LockHandle that must be Released. On failure returns:
//   - *HeldLockError if the file exists and contains a live, matching process
//   - other wrapped errors for filesystem issues
//
// Acquisition strategy:
//  1. Try O_EXCL | O_CREAT | O_WRONLY | O_NOFOLLOW on lockFilePath with mode 0600.
//  2. If success: write Lock JSON, flock(LOCK_EX|LOCK_NB), return handle.
//  3. If EEXIST: open existing file read-only, parse Lock, check liveness
//     (PID alive + start_time matches + host_uuid matches); return *HeldLockError
//     if live; if stale, unlink and retry from step 1 ONCE.
//
// volumeUUID is passed in (typically from internal/drives queryVolume on the
// USB destination); it's just stored, not validated here.
func Acquire(ctx context.Context, lockFilePath, volumeUUID string) (*LockHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}

	// Prime the start-time cache once so that even short-lived processes
	// retain a stable value across multiple Acquire calls.
	_ = getSelfStartTime()

	for attempt := 0; attempt < 2; attempt++ {
		handle, err := tryAcquireOnce(lockFilePath, volumeUUID)
		if err == nil {
			return handle, nil
		}
		var heldErr *HeldLockError
		if errors.As(err, &heldErr) {
			// Truly held by another live process: return immediately.
			return nil, err
		}
		if errors.Is(err, errStaleLockRecovered) {
			// Stale lock was removed; loop and retry the create.
			continue
		}
		return nil, err
	}
	return nil, fmt.Errorf("acquire lock: stale-recovery loop exhausted")
}

func tryAcquireOnce(lockFilePath, volumeUUID string) (*LockHandle, error) {
	// 1. Try to create exclusively.
	f, err := os.OpenFile(lockFilePath, os.O_CREATE|os.O_EXCL|os.O_WRONLY|syscall.O_NOFOLLOW, 0600)
	if err == nil {
		return finishAcquire(f, lockFilePath, volumeUUID)
	}

	if errors.Is(err, syscall.ELOOP) {
		return nil, fmt.Errorf("lock path %q is a symlink (refusing for safety): %w", lockFilePath, err)
	}
	if !errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("create lock file: %w", err)
	}

	// File exists. Parse it and decide stale-vs-held.
	existing, err := os.OpenFile(lockFilePath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		if errors.Is(err, syscall.ELOOP) {
			return nil, fmt.Errorf("lock path %q is a symlink (refusing for safety): %w", lockFilePath, err)
		}
		return nil, fmt.Errorf("open existing lock: %w", err)
	}
	defer existing.Close()

	var l Lock
	if err := json.NewDecoder(existing).Decode(&l); err != nil {
		// Corrupted lock: treat as stale.
		_ = os.Remove(lockFilePath)
		return nil, errStaleLockRecovered
	}
	if l.HostUUID != getHostUUID() {
		// Lock came from a different machine (e.g. USB moved between Macs).
		_ = os.Remove(lockFilePath)
		return nil, errStaleLockRecovered
	}
	if !processAlive(l.PID) {
		_ = os.Remove(lockFilePath)
		return nil, errStaleLockRecovered
	}
	// PID alive + same host: verify start_time match to defend against PID recycling.
	startNow, perr := processStartTimeUnix(l.PID)
	if perr == nil {
		if abs64(startNow-l.StartTimeUnix) > 5 {
			// Recycled PID; stale.
			_ = os.Remove(lockFilePath)
			return nil, errStaleLockRecovered
		}
	}
	// If perr != nil we conservatively treat the lock as held (better to
	// refuse acquisition than to clobber a possibly-live FlashBackup).
	return nil, &HeldLockError{Holder: l}
}

func finishAcquire(f *os.File, lockFilePath, volumeUUID string) (*LockHandle, error) {
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		_ = f.Close()
		_ = os.Remove(lockFilePath)
		return nil, fmt.Errorf("generate lock nonce: %w", err)
	}
	l := Lock{
		PID:           os.Getpid(),
		StartTimeUnix: getSelfStartTime(),
		HostUUID:      getHostUUID(),
		Nonce:         hex.EncodeToString(nonceBytes),
		VolumeUUID:    volumeUUID,
	}
	enc := json.NewEncoder(f)
	if err := enc.Encode(&l); err != nil {
		_ = f.Close()
		_ = os.Remove(lockFilePath)
		return nil, fmt.Errorf("write lock json: %w", err)
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(lockFilePath)
		return nil, fmt.Errorf("fsync lock: %w", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		_ = os.Remove(lockFilePath)
		return nil, fmt.Errorf("flock lock: %w", err)
	}
	return &LockHandle{path: lockFilePath, file: f}, nil
}

// Release closes the FD (auto-releasing flock) and removes the lock file.
// Safe to call multiple times; second and subsequent calls are no-ops.
func (h *LockHandle) Release() error {
	if h == nil || h.file == nil {
		return nil
	}
	// LOCK_UN is implicit on close, but being explicit makes intent obvious.
	_ = syscall.Flock(int(h.file.Fd()), syscall.LOCK_UN)
	closeErr := h.file.Close()
	h.file = nil
	removeErr := os.Remove(h.path)
	if removeErr != nil && errors.Is(removeErr, os.ErrNotExist) {
		removeErr = nil
	}
	switch {
	case closeErr != nil && removeErr != nil:
		return fmt.Errorf("close lock: %w; remove lock: %v", closeErr, removeErr)
	case closeErr != nil:
		return fmt.Errorf("close lock: %w", closeErr)
	case removeErr != nil:
		return fmt.Errorf("remove lock file: %w", removeErr)
	}
	return nil
}

// processAlive reports whether pid refers to a process this user can signal.
// EPERM is treated as alive (process exists; we just lack permission to signal it).
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	if err == nil {
		return true
	}
	return errors.Is(err, syscall.EPERM)
}

// processStartTimeUnix returns the start time of a running process by
// parsing `ps -o lstart= -p <pid>` output. On macOS and Linux ps produces
// the same format (e.g. "Wed Jun  4 12:34:56 2026") when called with
// LANG=C / default locale.
func processStartTimeUnix(pid int) (int64, error) {
	out, err := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return 0, fmt.Errorf("ps -o lstart for pid %d: %w", pid, err)
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return 0, fmt.Errorf("ps returned empty lstart for pid %d", pid)
	}
	parsed, err := time.ParseInLocation("Mon Jan _2 15:04:05 2006", raw, time.Local)
	if err != nil {
		return 0, fmt.Errorf("parse lstart %q: %w", raw, err)
	}
	return parsed.Unix(), nil
}

func abs64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}
