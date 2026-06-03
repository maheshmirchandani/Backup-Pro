# FlashBackup

Portable USB-runnable macOS backup utility with strict integrity guarantees.

**Status:** v0.1 in development. Not yet released.

## What it does

- Backs up files from your Mac to a USB drive (copy or atomic move).
- Verifies every byte via SHA256 source+dest comparison.
- Refuses to delete source files if any single file failed verification.
- Re-checks integrity on demand via `flashbackup verify`.

## Requirements

- macOS 13 (Ventura) or newer.
- USB drive formatted as APFS or HFS+ (not exFAT; init will refuse and print a reformat recipe).
- Apple Silicon or Intel Mac (universal2 binary).

## Quickstart (placeholder; Plan 2 will polish)

```
flashbackup init /Volumes/MYBKP
flashbackup profiles new my-docs --source ~/Documents
flashbackup backup my-docs
flashbackup verify
```

## License

GPLv3. See [LICENSE](LICENSE) and [THIRD_PARTY_LICENSES.md](THIRD_PARTY_LICENSES.md) (generated post-Plan-1).

Embedded GNU rsync 3.x is also GPLv3. Source available via written offer; contact mahesh.mirchandani@gmail.com.

## Source

Design spec: [docs/specs/2026-06-03-1532-flashbackup-design.md](docs/specs/2026-06-03-1532-flashbackup-design.md).
Implementation plan: [docs/planning/2026-06-03-flashbackup-core-engine.md](docs/planning/2026-06-03-flashbackup-core-engine.md).
