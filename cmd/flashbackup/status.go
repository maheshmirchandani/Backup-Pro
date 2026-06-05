package main

// status.go implements the `flashbackup status [--json] <USB-path>`
// subcommand (Task 39). It surfaces the USB-level state for the operator
// without acquiring the runner lock or otherwise mutating disk state:
//
//  1. Argv parse: --json flag + a single positional <USB-path>.
//  2. Resolve the USB path (abs + EvalSymlinks; matches init.go + backup.go +
//     verify.go).
//  3. Read filesystem type + capacity (drives.Query against the mountpoint).
//  4. Compose the namespace prefix (paths.Prefix(hostname, username)).
//  5. Read lock status by stat'ing <DotDir>/lock; presence -> "held", absent
//     -> "free". We deliberately do NOT call lock.Acquire here even with an
//     immediate release: an Acquire race would clobber a real backup, and
//     status is a read-only view.
//  6. Count retained_runs by enumerating <DotDir>/runs/ for canonical-pattern
//     dirs.
//  7. Read last_run by scanning <DotDir>/runs.ndjson for the LAST `finished`
//     line; absent -> omit last_run.
//  8. Read last_verify by walking <DotDir>/runs/<runID>/verifications/*/summary.json
//     and selecting the one with the newest verified_at; absent -> omit
//     last_verify.
//  9. Emit:
//       - --json: marshal statusRecord and write to stdout (the locked schema
//         from API Contracts, lines 401-437; field names exact, omitempty for
//         last_run + last_verify so an uninitialized USB does not surface
//         empty sub-objects).
//       - default: tabular plain-text summary so an operator can read it
//         without piping through jq.
//
// Exit code table:
//
//	0  success
//	2  usage error (no path, unknown flag, --help, nonexistent path)
//	1  reserved for runtime failure (e.g. drives.Query exec failure on a
//	   readable mountpoint); status aborts and prints the wrapped error
//
// AC: the status surface is informational; no AC number applies to v0.1
// status. The contract is the locked --json schema verified by
// TestStatus_JSONSchema_Locked.

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/user"
	"path/filepath"

	"github.com/maheshmirchandani/Backup-Pro/internal/drives"
	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
)

// statusExitCode* mirror the binary's exit-code contract in doc.go. Declared
// as named constants so the runStatus pipeline reads as a table rather than
// a wall of literals; matches the convention used by init / backup / verify.
const (
	statusExitCodeOK      = 0
	statusExitCodeRuntime = 1
	statusExitCodeUsage   = 2
)

// statusSchemaVersion is the v field of the locked --json schema (API
// Contracts, line 401-437). Bumped only on a schema break; treat as a
// release artifact downstream tooling reads.
const statusSchemaVersion = 1

// lockBasename is the on-disk name of the runner's exclusive lock file. Must
// stay in sync with internal/preflight/preflight.go's gate 7 wiring (which
// joins DotDir+"lock"). A drift here would cause status to report "free"
// while a backup holds the lock, which is the exact false-positive we are
// designing to avoid.
const lockBasename = "lock"

// statusLastRun is the on-the-wire shape of the last_run sub-object in the
// locked --json schema (API Contracts, line 413-423). Field names + types
// match the schema literally; a future schema bump must edit the schema doc
// AND this struct (and bump statusSchemaVersion).
type statusLastRun struct {
	RunID          string `json:"run_id"`
	StartedAt      string `json:"started_at"`
	FinishedAt     string `json:"finished_at"`
	Mode           string `json:"mode"`
	Profile        string `json:"profile,omitempty"`
	ExitStatus     string `json:"exit_status"`
	FilesTotal     int    `json:"files_total"`
	FilesSucceeded int    `json:"files_succeeded"`
	FilesFailed    int    `json:"files_failed"`
	BytesTotal     int64  `json:"bytes_total"`
}

// statusLastVerify is the on-the-wire shape of the last_verify sub-object
// in the locked --json schema (API Contracts, line 424-432). See statusLastRun
// for the schema-coupling rules.
type statusLastVerify struct {
	VerifyID             string `json:"verify_id"`
	VerifiedAt           string `json:"verified_at"`
	ForRunID             string `json:"for_run_id"`
	ExitStatus           string `json:"exit_status"`
	FilesVerified        int    `json:"files_verified"`
	FilesIntegrityFailed int    `json:"files_integrity_failed"`
	FilesHashMismatch    int    `json:"files_hash_mismatch"`
}

// statusRecord is the locked JSON schema for `flashbackup status --json`
// (API Contracts, lines 401-437). Field order in this struct matches the
// schema example for readability when the JSON is pretty-printed; encoding
// preserves field order from struct declaration.
//
// LastRun + LastVerify are pointer-typed with omitempty so an uninitialized
// or fresh USB does not surface empty sub-objects in the output (the schema
// example shows full sub-objects; absence is signalled by the key being
// missing).
type statusRecord struct {
	V                  int               `json:"v"`
	FlashbackupVersion string            `json:"flashbackup_version"`
	RsyncVersion       string            `json:"rsync_version"`
	USBPath            string            `json:"usb_path"`
	USBVolumeUUID      string            `json:"usb_volume_uuid"`
	USBFilesystem      string            `json:"usb_filesystem"`
	USBBytesFree       int64             `json:"usb_bytes_free"`
	USBBytesTotal      int64             `json:"usb_bytes_total"`
	NamespacePrefix    string            `json:"namespace_prefix"`
	LockStatus         string            `json:"lock_status"`
	LastRun            *statusLastRun    `json:"last_run,omitempty"`
	LastVerify         *statusLastVerify `json:"last_verify,omitempty"`
	RetainedRuns       int               `json:"retained_runs"`
	RetentionLimit     int               `json:"retention_limit"`
}

// runStatus is the testable entry point for the `status` subcommand. argv is
// the trailing args after "status" (so argv[0] is the positional path or a
// flag, NOT the subcommand name). stdout receives the JSON or the tabular
// summary; stderr receives usage errors and any wrapped error from the
// disk read.
//
// stdin is accepted for handler-signature symmetry; status has no interactive
// prompts today. ctx is the signal-aware ctx from main; drives.Query
// respects ctx for its diskutil exec, and the disk reads are short-lived
// enough that a Ctrl-C during the read returns promptly.
func runStatus(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin // accepted for handler-signature symmetry; status has no prompts

	// Local FlagSet so we don't pollute flag.CommandLine. ContinueOnError so
	// a bad flag prints our usage block on stderr rather than calling
	// os.Exit inside the flag package (which would bypass cmd-level cleanup).
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonMode := fs.Bool("json", false,
		"emit the locked status schema as JSON (suitable for scripts)")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "Usage: flashbackup status [--json] <USB-path>")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Shows the current state of a FlashBackup-initialized USB drive.")
		fmt.Fprintln(stderr, "  <USB-path>  mountpoint of an initialized FlashBackup USB")
		fmt.Fprintln(stderr, "")
		fmt.Fprintln(stderr, "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(argv); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return statusExitCodeOK
		}
		return statusExitCodeUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "flashbackup status: missing <USB-path> argument")
		fs.Usage()
		return statusExitCodeUsage
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "flashbackup status: unexpected extra arguments after path: %v\n", rest[1:])
		fs.Usage()
		return statusExitCodeUsage
	}
	usbPath := rest[0]

	// Resolve the USB path to an absolute, symlink-free mountpoint. Same
	// EvalSymlinks discipline as init.go: a missing path fails here with a
	// clear error rather than producing a confusing "diskutil exec failed"
	// further down the pipeline.
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup status: resolve %q: %v\n", usbPath, err)
		return statusExitCodeUsage
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup status: %q: %v\n", abs, err)
		return statusExitCodeUsage
	}
	mountpoint := resolved
	mpInfo, err := os.Stat(mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup status: stat %q: %v\n", mountpoint, err)
		return statusExitCodeUsage
	}
	if !mpInfo.IsDir() {
		fmt.Fprintf(stderr, "flashbackup status: %q is not a directory\n", mountpoint)
		return statusExitCodeUsage
	}

	rec, err := buildStatusRecord(ctx, mountpoint)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup status: %v\n", err)
		return statusExitCodeRuntime
	}

	if *jsonMode {
		if err := emitStatusJSON(stdout, rec); err != nil {
			fmt.Fprintf(stderr, "flashbackup status: emit json: %v\n", err)
			return statusExitCodeRuntime
		}
		return statusExitCodeOK
	}

	if err := emitStatusPlain(stdout, rec); err != nil {
		fmt.Fprintf(stderr, "flashbackup status: emit plain: %v\n", err)
		return statusExitCodeRuntime
	}
	return statusExitCodeOK
}

// buildStatusRecord composes the on-the-wire statusRecord from disk reads.
// Each helper is split out to status_helpers.go so the orchestration here
// stays scannable. A read failure on drives.Query is fatal (no point
// reporting a status with no volume identity); read failures on the
// optional surfaces (last_run, last_verify) are downgraded to "absent" so
// a partially-populated USB still surfaces a usable record.
func buildStatusRecord(ctx context.Context, mountpoint string) (*statusRecord, error) {
	dotDir := filepath.Join(mountpoint, ".flashbackup")

	// drives.Query is the canonical capacity + filesystem + volume_uuid
	// surface; mirrors what init / preflight / volume_uuid use. A failure
	// here aborts status because every field of the record is unreliable
	// without it (e.g. usb_filesystem would be empty; the operator would
	// see misleading defaults).
	vol, err := drives.Query(ctx, mountpoint)
	if err != nil {
		return nil, fmt.Errorf("query volume: %w", err)
	}

	host, err := os.Hostname()
	if err != nil {
		return nil, fmt.Errorf("get hostname: %w", err)
	}
	u, err := user.Current()
	if err != nil {
		return nil, fmt.Errorf("get current user: %w", err)
	}

	// Lock status: stat-based (do NOT Acquire; see file-header comment).
	// Absence -> free; presence (even of a stale lock left after a crash)
	// -> held. The operator who suspects a stale lock can inspect the
	// lock file directly; status surfaces the on-disk truth, not the
	// liveness-checked truth.
	lockStatus := readLockStatus(filepath.Join(dotDir, lockBasename))

	rec := &statusRecord{
		V:                  statusSchemaVersion,
		FlashbackupVersion: Version,
		RsyncVersion:       RsyncVersion,
		USBPath:            mountpoint,
		USBVolumeUUID:      vol.VolumeUUID,
		USBFilesystem:      vol.FilesystemType,
		USBBytesFree:       vol.BytesFree,
		USBBytesTotal:      vol.BytesTotal,
		NamespacePrefix:    paths.Prefix(host, u.Username),
		LockStatus:         lockStatus,
		RetainedRuns:       countRetainedRuns(dotDir),
		RetentionLimit:     defaultStatusRetentionLimit,
	}

	// last_run: tolerate a missing or unreadable runs.ndjson by leaving
	// the field nil (omitempty drops it from the JSON output). A read
	// error during the per-line parse is preserved at the pipeline level
	// by readLastRun (it returns the latest successfully-parsed finished
	// line). An empty USB with no runs yet legitimately has no last_run.
	if lr := readLastRun(filepath.Join(dotDir, "runs.ndjson")); lr != nil {
		rec.LastRun = lr
	}

	// last_verify: walk runs/<runID>/verifications/<verifyID>/summary.json
	// and pick the one with the newest verified_at. Absent -> nil ->
	// omitempty drops the field. A walk failure is non-fatal; we treat
	// "no verify found" identically to "the disk read partially failed".
	if lv := readLastVerify(filepath.Join(dotDir, "runs")); lv != nil {
		rec.LastVerify = lv
	}

	return rec, nil
}

// emitStatusJSON marshals the record to stdout. json.MarshalIndent for
// readability; the schema doc shows pretty-printed output and a downstream
// jq probe handles either form identically. A trailing newline matches
// the convention from `flashbackup init` success output.
func emitStatusJSON(w io.Writer, rec *statusRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal status: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write status json: %w", err)
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return fmt.Errorf("write status newline: %w", err)
	}
	return nil
}

// defaultStatusRetentionLimit is the retention_limit reported in --json. Hard-
// coded to match runner.DefaultRetentionLimit (=10); avoiding the import
// keeps status's surface area minimal. A future config-driven retention
// would require both this and runner.DefaultRetentionLimit to read from
// the same source.
const defaultStatusRetentionLimit = 10
