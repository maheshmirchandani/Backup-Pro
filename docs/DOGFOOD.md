# FlashBackup v0.1.0-core Dogfood Checklist

> Phase 0: MM-only validation, 2 weeks. After 2 weeks of clean runs we open to the inner circle.

## Pre-run setup

1. Format a USB drive as APFS (the FlashBackup design refuses exFAT/FAT32 with a printed reformat recipe). Recommended size: 1TB+ for realistic workloads.
2. Install GNU rsync 3.x: `brew install rsync`. Apple's built-in `/usr/bin/rsync` is openrsync and lacks `--from0`/`--xattrs` which FlashBackup requires.
3. Download the unsigned `flashbackup` binary from [GitHub Releases](https://github.com/maheshmirchandani/Backup-Pro/releases).
4. Bypass Gatekeeper: `xattr -d com.apple.quarantine ./flashbackup`.
5. Verify the binary: `./flashbackup --version`. Expected output:
   ```
   flashbackup v0.1.0-core (rsync 3.4.1, commit <sha>, built YYYY-MM-DD)

   This program is free software: you can redistribute it and/or modify it
   under the terms of the GNU General Public License v3 as published by the
   Free Software Foundation. This program comes with ABSOLUTELY NO WARRANTY.
   See LICENSE for details.
   ```

## Initialize the USB

```bash
./flashbackup init /Volumes/<USB-name>
```

Expected output: `FlashBackup initialized at /Volumes/<USB-name>` (or similar success message). The USB now has:
- `<USB>/.flashbackup/` directory (mode 0700).
- `<USB>/.flashbackup/version.json` (HMAC key, mode 0600).
- `<USB>/.flashbackup/bin/<sha256>/rsync` (extracted GNU rsync).
- `<USB>/.metadata_never_index` (Spotlight suppression).

## Create a profile

```bash
./flashbackup profiles new my-docs /Volumes/<USB-name>
```

`$EDITOR` opens with a JSON skeleton. Fill in:
- `source`: absolute path to the directory you want to back up (e.g. `/Users/<you>/Documents`).
- `includes`: glob patterns. Default `["*"]` is a starting point; refine for your workflow.
- `excludes`: paths to skip (e.g. `[".DS_Store", "**/.git/**"]`).

Save and exit. The profile is stored at `<USB>/.flashbackup/profiles.json`.

## Run a backup

```bash
./flashbackup backup my-docs /Volumes/<USB-name>
```

Expected:
- One line per phase: `=> T0 preflight starting`, `OK T0 preflight`, etc.
- A `Run complete.` summary block at the end with `exit status: ok`.
- Files copied under `<USB>/<hostname>-<username>/...` mirroring the source tree.

For move mode (deletes source after verification):

```bash
./flashbackup backup --move my-docs /Volumes/<USB-name>
```

You will see a multi-line warning + prompt: `Type DELETE (exact case) to proceed, anything else to abort:`. Type `DELETE` and press Enter. Source files are deleted only after they verify on the USB; the atomic gate ensures NO source files are deleted if even one fails to verify.

## Verify a backup

```bash
./flashbackup verify /Volumes/<USB-name>
```

Re-hashes every dest file. Expected exit 0; `Run complete.` block with `exit status: ok`. The verify writes `<USB>/.flashbackup/runs/<runID>/verifications/<verifyID>/summary.json` and `results.ndjson` for the forensic trail.

## Inspect state

```bash
./flashbackup status /Volumes/<USB-name>
./flashbackup status --json /Volumes/<USB-name>
```

## When something goes wrong

### make debug-bundle (Phase 0)

```bash
cd Backup-Pro
make debug-bundle RUN=<run-id> USB=/Volumes/<USB-name>
```

Find `<run-id>` via `./flashbackup status /Volumes/<USB-name>` (it lists recent runs by ID) or by listing `<USB>/.flashbackup/runs/`. The bundle is written as `flashbackup-debug-<run-id>.tgz` in the current directory and contains:
- The full `runs/<run-id>/` directory for that run (events.ndjson, manifest, deletion-log if move-mode, verifications, rsync.log).
- The USB's `version.json`.
- The append-only `runs.ndjson` index.

Attach the tarball to your bug report.

### Event meanings

See [docs/ERROR_CATALOG.md](ERROR_CATALOG.md) for what each `state.Event.Kind` means and what to do about it.

### Sev1 contact

For data-loss-imminent issues (e.g. atomic gate failed and source files were deleted; manifest tamper detected on a known-good USB), email Mahesh directly: see GitHub profile.

## Known limitations of v0.1

- `--delete` mirror mode is not implemented (Task 51c queued).
- Orphan-run `crashed_resumed` finalization is not implemented (Task 50a queued); a killed backup leaves an open `started` line in runs.ndjson that the next backup does not close.
- exFAT and FAT32 destinations are refused; reformat the USB to APFS.
- The binary is not signed or notarized; `xattr` bypass required.
- No TUI yet (Plan 2 ships Bubble Tea TUI).
