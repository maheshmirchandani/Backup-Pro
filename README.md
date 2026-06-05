# FlashBackup

> Portable USB-runnable macOS backup utility with atomic move semantics, source+dest hash compare, and a no-install single-binary design.

## Status

v0.1.0-core (Phase 0 dogfood). Pre-1.0. Breaking changes possible between minor versions.

## What it does

- Backs up files from a source directory to a FlashBackup-prepared USB drive.
- COPY mode: source files preserved.
- MOVE mode: source files DELETED ONLY AFTER hash-compare succeeds on the USB (atomic gate).
- VERIFY: re-hashes the destination against the manifest's HMAC-protected SHA256 records.

## Requirements

- macOS 13 or later (tested on macOS 14 / 15 / 16).
- USB drive formatted as APFS or HFS+ (init refuses exFAT with a reformat recipe).
- GNU rsync 3.x (available via `brew install rsync`).

## Install (Phase 0 dogfood)

1. Download the binary from [GitHub Releases](https://github.com/maheshmirchandani/Backup-Pro/releases).
2. Make it executable: `chmod +x flashbackup`.
3. Run it directly: `./flashbackup --version`.

### Gatekeeper bypass for unsigned binary

Phase 0 binaries are NOT signed or notarized. macOS will refuse to run them with "cannot be opened because the developer cannot be verified." Bypass:

```bash
xattr -d com.apple.quarantine ./flashbackup
```

Then re-run. This removes the quarantine flag set by the browser or download tool. (Plan 2 will ship signed + notarized binaries.)

## Usage

### Initialize a USB drive

```bash
flashbackup init /Volumes/FLASHBKP
```

### Create a backup profile

```bash
flashbackup profiles new my-docs /Volumes/FLASHBKP
```

This opens `$EDITOR` with a JSON skeleton. Fill in:

- source (absolute path to the directory you want to back up)
- includes (glob patterns, default `**/*`)
- excludes (glob patterns)

### Run a backup

```bash
flashbackup backup my-docs /Volumes/FLASHBKP
```

For move mode (deletes source after verification):

```bash
flashbackup backup --move my-docs /Volumes/FLASHBKP
```

You will be prompted to type DELETE (exact case) to confirm.

### Verify a previous backup

```bash
flashbackup verify /Volumes/FLASHBKP
```

Verifies the latest run. Use `flashbackup verify --all /Volumes/FLASHBKP` for every run.

### See current state

```bash
flashbackup status /Volumes/FLASHBKP
flashbackup status --json /Volumes/FLASHBKP
```

## License

GPLv3. See [LICENSE](LICENSE).

## Reporting bugs

File a GitHub issue at https://github.com/maheshmirchandani/Backup-Pro/issues with:

- `flashbackup --version` output
- `flashbackup status /Volumes/<USB>` output
- The `events.ndjson` file from the affected run (see [docs/ERROR_CATALOG.md](docs/ERROR_CATALOG.md) for what events mean).

## Roadmap

- v0.1.0-core (now): CLI only, no TUI, no signed releases.
- Plan 2: Bubble Tea TUI, signed + notarized release pipeline, complete docs.

## Limitations of v0.1

- No `--delete` mirror mode (queued as Task 51c).
- No orphan-run `crashed_resumed` finalization (queued as Task 50a).
- exFAT / FAT32 destinations are refused.
- Binary not signed; requires the `xattr` bypass above.
