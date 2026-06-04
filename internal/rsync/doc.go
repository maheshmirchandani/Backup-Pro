// Package rsync embeds and extracts GNU rsync (built by scripts/build-rsync.sh
// at Task 12a). The extracted binary is verified by SHA256 on every launch,
// installed at chmod 0500, and best-effort marked immutable via chflags uchg.
//
// Invariants enforced:
//   - #29: codesign self-verify of the `flashbackup` binary is separate; this
//     package only guards the embedded rsync's integrity at extract time.
//   - #38: embedded rsync source pin lives in scripts/build-rsync.sh and the
//     SHA256 computed lazily from the embed payload (EmbeddedSHA256).
//
// Hardening recipe (Hacker hat amendment 2026-06-03):
//   - extract to <dotFlashbackupDir>/bin/<sha256-of-embedded>/rsync
//   - write to a .tmp sibling, fsync, reverify SHA256 from disk, then rename
//   - chmod 0500 (owner read+exec; no write; no group/other access)
//   - best-effort chflags uchg (user immutable); non-fatal if it fails
//
// macOS has no fexecve. This recipe is the next-best mitigation: the SHA256
// is verified at extraction AND the extracted path is keyed by the SHA256
// itself, so any attacker tampering with the file invalidates both the
// file's content AND its location.
//
// Scope (v0.1, Task 12):
//   - The embedded payload is `internal/rsync/bin/rsync.placeholder`, a
//     small shell script. Task 12a will replace it with a universal2 GNU
//     rsync 3.4.1 binary. No Go-side API change is required at that swap;
//     EmbeddedSHA256 recomputes from the new bytes automatically.
//   - Public API: EmbeddedSHA256, EnsureExtracted.
//
// Consumers (planned): Task 13's rsync subprocess wrapper calls
// EnsureExtracted once during preflight, then exec's the returned absolute
// path with arguments.
package rsync
