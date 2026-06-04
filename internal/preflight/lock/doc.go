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
// Consumers (planned): Task 20 preflight integration calls Acquire at T0 and
// defers Release. The captured VolumeUUID is later cross-checked per phase
// to enforce invariant #30 (USB drive must remain mounted at the same UUID).
package lock
