package e2e

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// lock_test.go covers AC-11 and AC-12 end-to-end through the cmd layer:
//
//	TestE2E_LockContention_HeldBlocksConcurrentBackup (AC-11):
//	  A lock file representing a live, matching process at
//	  <USB>/.flashbackup/lock blocks a concurrent backup invocation with
//	  exit 2 (preflight_failed) and an error message naming the lock.
//	  The held lock file is not unlinked or rewritten by the refused run.
//
//	TestE2E_LockContention_StaleLockBypassed (AC-12):
//	  A lock file pointing at a dead PID (host matches but the process is
//	  gone) is detected as stale by preflight, transparently unlinked, and
//	  the backup proceeds to exit 0. After the run the lock file no longer
//	  carries the dead PID; either it has been replaced by the runner (and
//	  then released; absent at the end of the run) or, if observed mid-run,
//	  would carry the runner's PID. The test asserts the after-run shape:
//	  exit 0 + the lock file no longer matches the seeded stale PID, plus
//	  a successful "finished" line in runs.ndjson.
//
// Lock file shape (mirror of internal/preflight/lock.Lock; intentionally
// re-declared LOCALLY so a producer-side schema change has to update this
// test too rather than the test silently importing the renamed fields):
//
//	type lockFile struct {
//	    PID           int    `json:"pid"`
//	    StartTimeUnix int64  `json:"start_time_unix"`
//	    HostUUID      string `json:"host_uuid"`
//	    Nonce         string `json:"nonce"`
//	    VolumeUUID    string `json:"volume_uuid"`
//	}
//
// The lock file lives at <USB>/.flashbackup/lock (NOT lock.json); the
// preflight package joins DotDir + "lock" (preflight.go gate 7).
//
// AC-11 stale-vs-held discipline (per internal/preflight/lock/lock.go):
//
//	A lock is treated as "held" only when ALL THREE conditions hold:
//	  1. PID is alive (syscall.Kill(pid, 0) succeeds or returns EPERM).
//	  2. host_uuid matches the current machine's IOPlatformUUID.
//	  3. start_time_unix is within 5 seconds of `ps -o lstart= -p PID`.
//	If any check fails the lock is unlinked and acquisition retries
//	(stale recovery). To produce a genuinely held lock we therefore use
//	THIS test process's own PID + the matching start_time the lock
//	package will query, plus the machine's real IOPlatformUUID.
//
// Tagged into the e2e-fast Makefile gate via the "LockContention" run-name
// pattern (Makefile e2e-fast target's -run filter).

// lockFile is the LOCAL on-disk shape mirroring internal/preflight/lock.Lock.
// Kept here so a future producer rename surfaces as a parse mismatch in
// this test rather than silently passing.
type lockFile struct {
	PID           int    `json:"pid"`
	StartTimeUnix int64  `json:"start_time_unix"`
	HostUUID      string `json:"host_uuid"`
	Nonce         string `json:"nonce"`
	VolumeUUID    string `json:"volume_uuid"`
}

// TestE2E_LockContention_HeldBlocksConcurrentBackup covers AC-11.
//
// Setup: init the USB via SetupUSB (which already runs `flashbackup init`
// and then releases its own lock at completion). Seed a tiny-fixture
// profile so the would-be backup has something to enumerate. Write a
// lock file by hand carrying the test process's own PID, matching
// start_time (queried the same way the lock package queries), and the
// machine's real IOPlatformUUID. Run backup; expect exit 2 and a stderr
// message that names the lock. The seeded lock file must be unchanged
// afterward (the refused acquire must not have unlinked or rewritten it).
func TestE2E_LockContention_HeldBlocksConcurrentBackup(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// SetupUSB performs `flashbackup init`, then releases its lock and
	// removes the lock file as part of LockHandle.Release(). After SetupUSB
	// returns, no lock file exists. We then plant our own held-lock fixture
	// before running backup.
	usb := SetupUSB(t, 64)

	// A profile is required for `backup` to advance past argv parsing. We
	// seed it before planting the lock so the test exercises the lock
	// refusal at the preflight gate, not a profile-not-found earlier exit.
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "lock-held", source, []string{"*"}, nil)

	// Plant a held lock. The test's own PID is alive; matching start_time
	// + host_uuid forces the lock package onto the "held" branch
	// (lock.go's tryAcquireOnce, the *HeldLockError return) rather than
	// the stale-recovery branch.
	testPID := os.Getpid()
	startTime, err := psLstartUnix(testPID)
	if err != nil {
		t.Fatalf("query own ps -o lstart=: %v", err)
	}
	hostUUID, err := queryIOPlatformUUID()
	if err != nil {
		t.Fatalf("query IOPlatformUUID via ioreg: %v", err)
	}

	lockPath := filepath.Join(usb, ".flashbackup", "lock")
	seeded := lockFile{
		PID:           testPID,
		StartTimeUnix: startTime,
		HostUUID:      hostUUID,
		Nonce:         "00000000000000000000000000000000",
		VolumeUUID:    "test-seeded-held-lock",
	}
	writeLockFile(t, lockPath, seeded)

	// Capture seeded bytes so we can prove the refused acquire did NOT
	// rewrite (or unlink + recreate with new content) the held lock. A
	// byte-equal compare after the refused run is the strongest possible
	// assertion that the holder's state was respected.
	beforeBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read seeded lock pre-run: %v", err)
	}

	exitCode, stdout, stderr := RunBackup(t, "lock-held", usb)

	if exitCode != 2 {
		t.Fatalf("backup exit code: got %d want 2 (preflight_failed)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// The HeldLockError message format is
	//   "lock held by PID=<n> host=<uuid> since <rfc3339> (<dur>); ..."
	// (see internal/preflight/lock/lock.go HeldLockError.Error). We assert
	// on the stable "lock" substring + the PID we planted so a future
	// message tweak does not silently mask the gate firing.
	combined := stdout + "\n" + stderr
	if !strings.Contains(combined, "lock") {
		t.Errorf("backup output missing the word \"lock\"\nstdout: %s\nstderr: %s",
			stdout, stderr)
	}
	if !strings.Contains(combined, fmt.Sprintf("PID=%d", testPID)) {
		t.Errorf("backup output missing the seeded PID=%d\nstdout: %s\nstderr: %s",
			testPID, stdout, stderr)
	}

	// Lock file untouched: byte-equal compare. The refused Acquire path
	// in lock.go (tryAcquireOnce, *HeldLockError branch) does not call
	// os.Remove or rewrite the existing file.
	afterBytes, err := os.ReadFile(lockPath)
	if err != nil {
		t.Fatalf("read seeded lock post-run: %v", err)
	}
	if string(beforeBytes) != string(afterBytes) {
		t.Errorf("held lock was rewritten by the refused run\nbefore: %s\nafter:  %s",
			beforeBytes, afterBytes)
	}

	// Belt-and-suspenders: re-parse the post-run file and confirm the
	// seeded PID and nonce are still present (a producer-side schema
	// change that quietly altered the byte layout would surface here as
	// a parse mismatch in addition to the byte-compare above).
	var afterLock lockFile
	if err := json.Unmarshal(afterBytes, &afterLock); err != nil {
		t.Fatalf("parse post-run lock: %v", err)
	}
	if afterLock.PID != testPID {
		t.Errorf("post-run lock PID: got %d want %d", afterLock.PID, testPID)
	}
	if afterLock.Nonce != seeded.Nonce {
		t.Errorf("post-run lock nonce: got %q want %q", afterLock.Nonce, seeded.Nonce)
	}

	// Tidy up: remove our seeded lock so t.Cleanup-driven hdiutil detach
	// doesn't trip on a leftover. Failure to remove is non-fatal (the
	// hdiutil detach will eject the volume regardless).
	_ = os.Remove(lockPath)
}

// TestE2E_LockContention_StaleLockBypassed covers AC-12.
//
// Setup: init the USB. Seed a tiny-fixture profile. Plant a lock file at
// <USB>/.flashbackup/lock pointing at a PID that is provably dead (we
// spawn /bin/true, wait for it, then use its reaped PID). The host_uuid
// matches the local machine so the only failing liveness check is the
// PID itself. The lock package's tryAcquireOnce detects the dead PID
// (processAlive returns false), unlinks the file, and returns
// errStaleLockRecovered; Acquire loops once and creates a fresh lock.
//
// We require a real GNU rsync to advance past T2 transfer; without one
// the embedded placeholder is a no-op shell stub and the run would
// fail later for an unrelated reason. Mirrors backup_happy_test.go.
//
// Assertions: exit 0, runs.ndjson contains a finished line, and the
// post-run on-disk lock state no longer matches the seeded stale PID
// (either absent because the runner released, or holding the runner's
// PID if observed mid-run).
func TestE2E_LockContention_StaleLockBypassed(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	usb := SetupUSB(t, 64)
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "lock-stale", source, []string{"*"}, nil)

	// Produce a reaped PID: spawn /bin/true, Wait for it, capture .Pid.
	// After Wait, the kernel has reaped the process; the PID is no longer
	// a live process (syscall.Kill(pid, 0) returns ESRCH). Using the
	// dead-but-known-recent PID is closer to the AC-12 wording ("stale
	// lock from a crashed PID") than a wild high integer like 999999;
	// either would trigger stale recovery, but a recently-spawned-and-
	// reaped PID is a more honest simulation of "a previous flashbackup
	// run crashed without releasing".
	deadPID, err := spawnAndReap()
	if err != nil {
		t.Fatalf("spawn + reap helper subprocess: %v", err)
	}

	hostUUID, err := queryIOPlatformUUID()
	if err != nil {
		t.Fatalf("query IOPlatformUUID via ioreg: %v", err)
	}

	lockPath := filepath.Join(usb, ".flashbackup", "lock")
	stale := lockFile{
		PID:           deadPID,
		StartTimeUnix: time.Now().Unix() - 3600, // value is irrelevant once dead-PID check fires
		HostUUID:      hostUUID,
		Nonce:         "ffffffffffffffffffffffffffffffff",
		VolumeUUID:    "test-seeded-stale-lock",
	}
	writeLockFile(t, lockPath, stale)

	exitCode, stdout, stderr := RunBackup(t, "lock-stale", usb)

	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0 (stale-lock bypass)\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}

	// runs.ndjson should now carry started + finished from a successful
	// run; the stale-lock recovery is transparent to the audit trail.
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("no finished line in runs.ndjson after stale-lock bypass")
	}

	// Post-run: the runner's LockHandle.Release unlinks the lock file as
	// the very last act of a successful run. So the lock should be
	// absent. If for any reason it is still on disk (e.g., a future
	// refactor leaves a window between unlink and process exit), we
	// tolerate that ONLY if the contents no longer point at the dead PID
	// we planted. The substantive AC-12 check is: the dead-PID stale
	// lock did NOT block this run, and is no longer the lock-file state.
	postBytes, err := os.ReadFile(lockPath)
	if err != nil {
		if !os.IsNotExist(err) {
			t.Fatalf("post-run stat lock: %v", err)
		}
		// Lock file absent: this is the expected happy-path outcome after
		// a successful run + Release. The stale dead-PID lock did not
		// survive; the AC-12 contract is satisfied.
		return
	}
	// Lock file present but contents must NOT still match the dead PID.
	var post lockFile
	if err := json.Unmarshal(postBytes, &post); err != nil {
		t.Fatalf("parse post-run lock: %v\nbytes: %s", err, postBytes)
	}
	if post.PID == deadPID && post.Nonce == stale.Nonce {
		t.Errorf("stale dead-PID lock was NOT replaced; backup somehow exited 0 without taking the lock\npost-run lock: %+v",
			post)
	}
}

// writeLockFile marshals l to JSON and writes it to path with mode 0o600
// (matching the mode the producer uses; see lock.go finishAcquire).
// Failures fail the test via t.Fatalf.
func writeLockFile(t *testing.T, path string, l lockFile) {
	t.Helper()
	data, err := json.Marshal(&l)
	if err != nil {
		t.Fatalf("marshal seeded lock: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write seeded lock to %s: %v", path, err)
	}
}

// psLstartUnix returns the start time of pid as a Unix timestamp, parsed
// from `ps -o lstart= -p <pid>`. Mirrors the producer's processStartTimeUnix
// helper (lock.go) byte-for-byte, including the LC_ALL=C / LANG=C
// environment so the lstart format is the canonical English
// "Wed Jun  4 12:34:56 2026". Without these env vars a localized macOS
// would return a string Go's time.ParseInLocation cannot consume, and
// the test would silently mis-fixture the held lock.
//
// Re-implemented here (rather than imported from internal/preflight/lock)
// because the helper is unexported. Drift between this copy and the
// producer's would manifest as the AC-11 test failing in a confusing way
// (stale recovery instead of HeldLockError), which is itself a strong
// signal to update the test.
func psLstartUnix(pid int) (int64, error) {
	cmd := exec.Command("/bin/ps", "-o", "lstart=", "-p", strconv.Itoa(pid))
	cmd.Env = append(os.Environ(), "LC_ALL=C", "LANG=C")
	out, err := cmd.Output()
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

// queryIOPlatformUUID returns the machine's stable IOPlatformUUID by
// parsing `ioreg -rd1 -c IOPlatformExpertDevice`, mirroring the producer's
// host UUID lookup (internal/preflight/lock/host_uuid_darwin.go). Falls
// back to os.Hostname() if ioreg fails or the regex misses, mirroring the
// producer's fallback so the seeded lock's host_uuid always matches what
// the lock package computes on this machine.
//
// Re-implemented here because the producer's lookupHostUUID is unexported.
// Drift between this copy and the producer would cause the AC-11 test to
// observe stale recovery (wrong host -> unlink + retry) instead of a held
// lock, surfacing as a confusing test failure that would itself prompt a
// re-sync of the two implementations.
func queryIOPlatformUUID() (string, error) {
	out, err := exec.Command("/usr/sbin/ioreg", "-rd1", "-c", "IOPlatformExpertDevice").Output()
	if err != nil {
		return fallbackHostnameForLockTest(), nil
	}
	re := regexp.MustCompile(`"IOPlatformUUID" = "([^"]+)"`)
	m := re.FindSubmatch(out)
	if len(m) < 2 {
		return fallbackHostnameForLockTest(), nil
	}
	return strings.TrimSpace(string(m[1])), nil
}

// fallbackHostnameForLockTest mirrors the producer's fallbackHostname
// helper for the rare branch where ioreg fails (sandboxed CI) or its
// output cannot be parsed. Returns "unknown-host" on a final fallback so
// the producer and the test fixture stay in lockstep.
func fallbackHostnameForLockTest() string {
	h, err := os.Hostname()
	if err != nil {
		return "unknown-host"
	}
	return h
}

// spawnAndReap launches /bin/true, waits for it to exit, and returns its
// (now reaped) PID. After Wait, the kernel has cleaned up the process
// entry; syscall.Kill(pid, 0) returns ESRCH and processAlive in the lock
// package returns false. The reaped PID is a faithful simulation of a
// "previous flashbackup crashed without releasing"; it represents a value
// that was valid at some recent point and is now provably dead.
//
// /bin/true is preferred over an absurd PID like 999999 because:
//   - It is a real process the kernel definitely scheduled.
//   - The PID is in the kernel's normal allocation range, so a producer
//     that ever added a "PID > some threshold = stale" optimization would
//     not paper over the test.
//   - The reap is deterministic; we control the timing.
func spawnAndReap() (int, error) {
	cmd := exec.Command("/usr/bin/true")
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("start /usr/bin/true: %w", err)
	}
	pid := cmd.Process.Pid
	if err := cmd.Wait(); err != nil {
		// /usr/bin/true exits 0; a non-nil Wait error here means
		// something unusual (e.g., the binary was missing under a
		// sandboxed PATH). Surface it so the caller can decide.
		return 0, fmt.Errorf("wait /usr/bin/true (pid %d): %w", pid, err)
	}
	return pid, nil
}
