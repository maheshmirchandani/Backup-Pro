package main

// init.go implements the `flashbackup init <USB-path>` subcommand
// (Task 35, AC-1 + AC-2). It initializes a USB drive for FlashBackup use:
//
//   1. Resolve the supplied path to an absolute, symlink-free mountpoint.
//   2. Probe the filesystem via internal/preflight/filesystem; APFS and
//      HFS+ are accepted, exFAT / msdos / unknown are refused with the
//      reformat recipe baked into UnsupportedError (AC-2).
//   3. Create <mountpoint>/.flashbackup/ with mode 0o700.
//   4. Touch <mountpoint>/.metadata_never_index (mode 0o644) so Spotlight
//      does not index the backup volume. Idempotent: existing file is left
//      alone.
//   5. Extract the embedded rsync binary via rsync.EnsureExtracted; the
//      binary lands under <mountpoint>/.flashbackup/bin/<sha256>/rsync
//      with SHA256 verification (matches the path the runner consults at
//      gate 9, so init + run agree).
//   6. Write a fresh <mountpoint>/.flashbackup/version.json with a random
//      32-byte HMAC key via state.InitVersionFile. Refuses to overwrite
//      an existing version.json unless --reset-keys is passed (the friction
//      is the feature: overwriting silently would invalidate every prior
//      manifest because the HMAC key changes).
//
// Exit codes follow the contract in cmd/flashbackup/doc.go:
//
//	0  success
//	2  usage error (no path, unknown flag, refusal: exFAT, already
//	   initialized without --reset-keys, missing mountpoint)
//	1  reserved for runtime failure (e.g. rsync extract failed on a
//	   readable APFS volume); init aborts and prints the wrapped error
//
// AC-1 (first-time init on APFS) is exercised by TestInit_HappyPath_APFS.
// AC-2 (exFAT refusal with reformat recipe) is exercised by
// TestInit_ExFAT_RefusesWithRecipe.

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/preflight/filesystem"
	"github.com/maheshmirchandani/Backup-Pro/internal/rsync"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// initExitCodeRuntime is the exit code for a runtime failure that aborts
// init after preflight refusal (i.e., the filesystem was acceptable but a
// later step like rsync extraction failed). Kept distinct from exit 2
// (usage / refusal) so a wrapper script can tell "you gave me bad input"
// apart from "I tried and broke partway through."
const initExitCodeRuntime = 1

// initExitCodeUsage matches the binary-wide usage-error convention from
// doc.go: 2 for any input the operator can fix by re-issuing with
// different args.
const initExitCodeUsage = 2

// runInit is the testable entry point for the `init` subcommand. argv is
// the trailing args after "init" (i.e., argv[0] is the positional path or
// a flag, NOT the subcommand name). stdout receives success output;
// stderr receives errors and the refusal message.
//
// The context is the signal-aware ctx from main; init itself is short
// (statfs + a small file write + rsync extract) but each step honours
// ctx.Err() through its own internal checks (e.g. rsync.EnsureExtracted
// re-checks ctx during the SHA256 stream).
func runInit(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	// Local FlagSet so we don't pollute the global flag.CommandLine.
	// ContinueOnError lets us print our own usage on a flag error instead
	// of the default os.Exit(2) inside the flag package.
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	resetKeys := fs.Bool("reset-keys", false,
		"overwrite an existing version.json with a fresh HMAC key "+
			"(WARNING: invalidates every prior manifest on this USB)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: flashbackup init <USB-path> [--reset-keys]")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Initializes a USB volume for FlashBackup use:")
		fmt.Fprintln(stderr, "  - verifies the filesystem is APFS or HFS+")
		fmt.Fprintln(stderr, "  - creates <USB>/.flashbackup/ (mode 0700)")
		fmt.Fprintln(stderr, "  - writes <USB>/.metadata_never_index (suppress Spotlight)")
		fmt.Fprintln(stderr, "  - extracts the embedded rsync binary (SHA256-verified)")
		fmt.Fprintln(stderr, "  - writes a fresh version.json with a random HMAC key")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		// flag.ErrHelp is the deliberate "user asked for help" signal
		// from the stdlib flag package; we treat it as exit 0 so a
		// scripted probe of `flashbackup init --help` does not look
		// like a failure. Any other flag-parse error (unknown flag,
		// malformed value) is a real usage error, exit 2. fs.Parse has
		// already written the error message and the usage block via
		// fs.Output (which we set to stderr above).
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return initExitCodeUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "flashbackup init: missing <USB-path> argument")
		fs.Usage()
		return initExitCodeUsage
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "flashbackup init: unexpected extra arguments after path: %v\n", rest[1:])
		fs.Usage()
		return initExitCodeUsage
	}
	usbPath := rest[0]

	// Step 1: resolve to absolute + symlink-free mountpoint. EvalSymlinks
	// fails on a nonexistent path, which is the desired behaviour: init
	// only ever runs against a mounted volume the operator chose. We do
	// not auto-create the mountpoint.
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: resolve %q: %v\n", usbPath, err)
		return initExitCodeUsage
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: %q: %v\n", abs, err)
		return initExitCodeUsage
	}
	mountpoint := resolved

	// Mountpoint must be a directory (init only runs against a mounted
	// volume root). A regular file at that path is operator error, not
	// a runtime crash.
	mpInfo, err := os.Stat(mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: stat %q: %v\n", mountpoint, err)
		return initExitCodeUsage
	}
	if !mpInfo.IsDir() {
		fmt.Fprintf(stderr, "flashbackup init: %q is not a directory\n", mountpoint)
		return initExitCodeUsage
	}

	// Step 2: filesystem inspect + validate. The validator's UnsupportedError
	// carries a formatted `diskutil eraseDisk APFS` recipe; we print it
	// verbatim to satisfy AC-2.
	fsInfo, err := filesystem.Inspect(ctx, mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: filesystem inspect: %v\n", err)
		return initExitCodeUsage
	}
	if err := filesystem.Validate(fsInfo); err != nil {
		// AC-2 contract: print the reformat recipe (carried inside the
		// error message by UnsupportedError) and exit 2. We do NOT create
		// the .flashbackup dir or touch any files on a refused volume.
		fmt.Fprintf(stderr, "flashbackup init: %v\n", err)
		return initExitCodeUsage
	}

	dotDir := filepath.Join(mountpoint, ".flashbackup")
	versionPath := filepath.Join(dotDir, "version.json")

	// Step 3: version.json overwrite gate runs BEFORE we touch any files
	// on the volume. If we already have an initialized USB and the
	// operator forgot --reset-keys, we refuse early so a second init does
	// not idempotently re-touch .metadata_never_index or re-extract rsync
	// (both harmless, but the user expects "refuse cleanly").
	if !*resetKeys {
		if _, err := os.Stat(versionPath); err == nil {
			fmt.Fprintf(stderr,
				"flashbackup init: %s already exists; pass --reset-keys to overwrite "+
					"(WARNING: invalidates every prior manifest on this USB)\n",
				versionPath)
			return initExitCodeUsage
		} else if !errors.Is(err, os.ErrNotExist) {
			fmt.Fprintf(stderr, "flashbackup init: stat existing version.json: %v\n", err)
			return initExitCodeRuntime
		}
	}

	// Step 4: create the .flashbackup directory. MkdirAll is idempotent;
	// existing 0o700 dir is left alone, missing one is created with the
	// design-spec mode (the HMAC key + run history live here, so the dir
	// is private to the owning user).
	if err := os.MkdirAll(dotDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "flashbackup init: create %s: %v\n", dotDir, err)
		return initExitCodeRuntime
	}

	// Step 5: .metadata_never_index. Mode 0o644 (no secrets, just a
	// Spotlight marker; world-readable is fine). Idempotent: we use
	// O_CREATE without O_TRUNC so a pre-existing file with arbitrary
	// content survives (Spotlight only cares about presence).
	indexMarker := filepath.Join(mountpoint, ".metadata_never_index")
	f, err := os.OpenFile(indexMarker, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: write %s: %v\n", indexMarker, err)
		return initExitCodeRuntime
	}
	if err := f.Close(); err != nil {
		fmt.Fprintf(stderr, "flashbackup init: close %s: %v\n", indexMarker, err)
		return initExitCodeRuntime
	}

	// Step 6: extract embedded rsync via the shared extract API. Path
	// lands under <dotDir>/bin/<sha256>/rsync; the runner's preflight
	// gate 9 consults the same function, so init + run agree on the
	// extraction layout without a hard-coded path. The current embedded
	// payload is the bin/rsync.placeholder shell stub; Task 12a will
	// swap in the real universal2 GNU rsync 3.4.1 binary and no init-side
	// change is required.
	rsyncPath, err := rsync.EnsureExtracted(ctx, dotDir)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: extract rsync: %v\n", err)
		return initExitCodeRuntime
	}

	// Step 7: write version.json. force = *resetKeys; the InitVersionFile
	// helper also enforces the "refuse without force" rule internally, but
	// we have already done the same check above and printed a friendlier
	// error message; the duplicate guard is defence in depth (two
	// independent gates means a future refactor that drops the cmd-level
	// gate still cannot silently rotate keys).
	vf, err := state.InitVersionFile(versionPath, Version, *resetKeys)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup init: write version.json: %v\n", err)
		return initExitCodeRuntime
	}

	// Success. Stable single-line first-line so a wrapper that greps the
	// output gets a deterministic anchor; the per-detail lines follow on
	// subsequent lines. Schema version + rsync path are surfaced because
	// the operator who runs init is also the person who would file a bug
	// report if a later run rejects this USB.
	fmt.Fprintf(stdout, "FlashBackup initialized at %s\n", mountpoint)
	fmt.Fprintf(stdout, "  version.json:           %s (schema_version=%d)\n", versionPath, vf.SchemaVersion)
	fmt.Fprintf(stdout, "  rsync:                  %s\n", rsyncPath)
	fmt.Fprintf(stdout, "  .metadata_never_index:  %s\n", indexMarker)
	return 0
}
