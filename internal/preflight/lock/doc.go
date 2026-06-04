// Package lock implements file-based exclusive locking with strong stale
// detection for the FlashBackup runner.
//
// Invariants enforced:
//   - #18: stale detection via PID + start_time + host_uuid (recycled PIDs
//     don't fool us; locks from other machines don't fool us).
//   - #31: O_EXCL|O_CREAT|O_NOFOLLOW + flock; defends against symlink-attack
//     and forked-child FD inheritance respectively.
//
// Lock file format (JSON, single line):
//
//	{"pid":12345,"start_time_unix":1717000000,"host_uuid":"ABC...","nonce":"hex","volume_uuid":"DEF..."}
//
// Acquisition:
//  1. O_EXCL+O_CREAT+O_NOFOLLOW open at the lock path (mode 0600).
//  2. If EEXIST: parse, check liveness, treat as stale if dead/wrong-host/recycled-PID.
//  3. flock(LOCK_EX|LOCK_NB) on the FD.
//
// Release: close FD (releases flock), remove file. Idempotent.
//
// Call order during preflight (Task 22 T0 phase via Task 20 integration):
//
//  1. drives.EnumerateVolumes / queryVolume on the destination mountpoint
//     resolves the live VolumeUUID.
//  2. Task 19 (volume_uuid) captures that into PreflightContext.VolumeUUID.
//  3. lock.Acquire(ctx, <dotFlashbackupDir>/lock, volumeUUID) is called with
//     the captured UUID; the value is persisted in the on-disk Lock JSON so
//     subsequent phases can compare against it (invariant #30).
//  4. Task 20 stores the returned *LockHandle in PreflightContext.LockHandle.
//  5. The runner defers handle.Release() immediately after acquisition.
//
// Why volumeUUID is passed at Acquire (not later): atomic capture. The Lock
// JSON is the durable T0 record; reading it later tells us which volume the
// lock was acquired against. The runner re-queries the live VolumeUUID at
// every phase boundary and aborts on mismatch.
package lock
