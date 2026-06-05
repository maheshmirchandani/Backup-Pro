package main

// status_helpers.go holds the per-surface read helpers and the plain-text
// formatter for the `status` subcommand. Split from status.go for file-length
// hygiene (matches backup + verify pattern); each helper is a pure function
// over disk reads with no overlapping state.

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
)

// statusRunIDPattern matches the canonical RunID format. Duplicated from
// verify.runIDPattern + runner.runIDPattern for the same reason: avoid
// importing the runner package from cmd just for one regex. The three
// regexes MUST stay identical; documented in verify.go's runIDPattern.
var statusRunIDPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{4}Z-[0-9a-fA-F]{4}$`)

// readLockStatus reports the on-disk lock state without acquiring the lock.
// stat-only: a present file (regardless of liveness of the owning PID)
// surfaces as "held"; an absent file surfaces as "free".
//
// We deliberately do NOT call lock.Acquire here even with an immediate
// release: an Acquire race against a concurrent backup would clobber the
// real lock, and the operator wants to know "is the file there" not "is
// the PID alive" (the lock file is the cross-process signal; PID liveness
// is the runner's concern at Acquire-time).
//
// A stat error other than ErrNotExist is treated as "free" defensively;
// a permission error on a private dir would lie about the real state,
// but the dir mode is 0o700 and status runs as the same user as init.
func readLockStatus(lockPath string) string {
	if _, err := os.Stat(lockPath); err == nil {
		return "held"
	}
	return "free"
}

// countRetainedRuns enumerates <dotDir>/runs/ and returns the count of
// entries that match the canonical RunID pattern. Skips non-dir entries
// and non-canonical names so an operator-added stray dir does not inflate
// the count.
//
// A missing runs dir returns 0 (the freshly-initialized USB case); any
// other read error also returns 0 because status cannot meaningfully
// surface "I read the file system and got a number minus context" in
// the JSON schema.
func countRetainedRuns(dotDir string) int {
	runsDir := filepath.Join(dotDir, "runs")
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return 0
	}
	count := 0
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		if !statusRunIDPattern.MatchString(e.Name()) {
			continue
		}
		count++
	}
	return count
}

// readLastRun scans runs.ndjson and returns the LAST `finished` line
// projected to the locked schema shape (statusLastRun). Returns nil for:
//
//   - a missing runs.ndjson (fresh USB; no run ever started)
//   - a runs.ndjson with no `finished` line (every run crashed; legitimate
//     state — invariant #10 says crashed runs are observable by absence)
//   - any per-line parse error during the scan (the entries up to the
//     error are still considered; state.ReadRunLog accumulates the parse
//     errors but still returns the entries it could parse)
//
// We pick the LAST `finished` line, not the latest by RunID, because the
// runs.ndjson is append-only and the latest written line IS chronologically
// last; the canonical RunID pattern is also timestamp-prefixed so the
// two orderings agree by construction (defended against drift by the
// canonical RunID pattern guard in runner.newRunID).
func readLastRun(runsNDJSONPath string) *statusLastRun {
	entries, _ := state.ReadRunLog(runsNDJSONPath)
	// Ignore the joined error here: per the helper contract, partially-
	// parsed entries are still usable. The caller (buildStatusRecord)
	// would have nowhere to surface a multi-line error chain in the
	// locked schema anyway.

	var latest *state.FinishedRun
	for i := range entries {
		if entries[i].Event != "finished" || entries[i].Finished == nil {
			continue
		}
		latest = entries[i].Finished
	}
	if latest == nil {
		return nil
	}
	return &statusLastRun{
		RunID:          latest.RunID,
		StartedAt:      latest.StartedAt.UTC().Format(time.RFC3339),
		FinishedAt:     latest.FinishedAt.UTC().Format(time.RFC3339),
		Mode:           latest.Mode,
		Profile:        latest.Profile,
		ExitStatus:     latest.ExitStatus,
		FilesTotal:     latest.FilesTotal,
		FilesSucceeded: latest.FilesSucceeded,
		FilesFailed:    latest.FilesFailed,
		BytesTotal:     latest.BytesTotal,
	}
}

// readLastVerify walks the on-disk verify summary tree and returns the
// most-recent summary projected to the locked schema shape (statusLastVerify).
// Returns nil if no summary.json exists anywhere under <runsDir>/<runID>/
// verifications/, or if every candidate summary.json fails to parse.
//
// Discovery strategy: scan <runsDir> for canonical RunID dirs, then under
// each one scan verifications/ for canonical VerifyID dirs (which share
// the runIDPattern by design — verify.newVerifyID returns the same shape),
// then attempt to parse summary.json. Pick the candidate with the
// chronologically-latest VerifiedAt; ties are broken by lexical VerifyID
// (deterministic but unimportant — a tie within a single second is a
// race that never happens in practice).
//
// Why we re-parse summary.json rather than reading a maintained index:
// there is no maintained index. summary.json is the canonical per-verify
// record; status reads it lazily. The cost is one open + json.Unmarshal
// per VerifyID dir on the USB, which is O(N runs * M verifies-per-run)
// and bounded by the retention limit. A future status performance
// regression here would be the trigger for a maintained index.
func readLastVerify(runsDir string) *statusLastVerify {
	runEntries, err := os.ReadDir(runsDir)
	if err != nil {
		return nil
	}

	// Sort runs deterministically so the tie-break (lexical) is reproducible.
	// We only need this for the tie-break; the actual selection key is
	// VerifiedAt below.
	runNames := make([]string, 0, len(runEntries))
	for _, e := range runEntries {
		if !e.IsDir() {
			continue
		}
		if !statusRunIDPattern.MatchString(e.Name()) {
			continue
		}
		runNames = append(runNames, e.Name())
	}
	sort.Strings(runNames)

	var (
		bestAt  time.Time
		bestRec *statusLastVerifyRecord
	)
	for _, runID := range runNames {
		verifDir := filepath.Join(runsDir, runID, "verifications")
		verEntries, err := os.ReadDir(verifDir)
		if err != nil {
			continue
		}
		for _, ve := range verEntries {
			if !ve.IsDir() {
				continue
			}
			if !statusRunIDPattern.MatchString(ve.Name()) {
				continue
			}
			summaryPath := filepath.Join(verifDir, ve.Name(), "summary.json")
			rec, err := readVerifySummaryFile(summaryPath)
			if err != nil {
				continue
			}
			if bestRec == nil || rec.VerifiedAt.After(bestAt) {
				bestAt = rec.VerifiedAt
				bestRec = rec
			}
		}
	}
	if bestRec == nil {
		return nil
	}
	return &statusLastVerify{
		VerifyID:             bestRec.VerifyID,
		VerifiedAt:           bestRec.VerifiedAt.UTC().Format(time.RFC3339),
		ForRunID:             bestRec.ForRunID,
		ExitStatus:           bestRec.ExitStatus,
		FilesVerified:        bestRec.FilesVerified,
		FilesIntegrityFailed: bestRec.FilesIntegrityFailed,
		FilesHashMismatch:    bestRec.FilesHashMismatch,
	}
}

// statusLastVerifyRecord is the subset of the on-disk verify summary.json
// that status surfaces in the locked schema. Kept as a local struct (not
// importing verify.summaryRecord) so a verify-internal schema bump does
// not silently shift the status output; if a future verify field needs
// surfacing in status, the explicit json tags here document the contract.
type statusLastVerifyRecord struct {
	VerifyID             string    `json:"verify_id"`
	ForRunID             string    `json:"for_run_id"`
	VerifiedAt           time.Time `json:"verified_at"`
	ExitStatus           string    `json:"exit_status"`
	FilesVerified        int       `json:"files_verified"`
	FilesIntegrityFailed int       `json:"files_integrity_failed"`
	FilesHashMismatch    int       `json:"files_hash_mismatch"`
}

// readVerifySummaryFile reads one summary.json and parses the subset of
// fields status surfaces. Returns a typed record on success, or an error
// (which the caller silently skips because a corrupt summary.json is a
// per-verify failure that should not block reporting on the rest).
func readVerifySummaryFile(path string) (*statusLastVerifyRecord, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var rec statusLastVerifyRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	return &rec, nil
}

// emitStatusPlain writes the tabular plain-text summary to w. The layout is
// the operator-facing surface (terminal default); no contract test asserts
// on the exact bytes, but the field labels are stable so a future tweak
// is a notice-worthy event. Times render in UTC for cross-timezone
// consistency (the JSON schema also uses RFC3339 UTC).
//
// Bytes are formatted via humanizeBytes (decimal SI: GB / MB / KB) per the
// design-spec convention; the operator wants "132 GB free", not the raw
// 132000000000.
func emitStatusPlain(w io.Writer, rec *statusRecord) error {
	if _, err := fmt.Fprintf(w, "USB path:            %s\n", rec.USBPath); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "USB volume UUID:     %s\n", rec.USBVolumeUUID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "USB filesystem:      %s\n", rec.USBFilesystem); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Free / total:        %s / %s\n",
		humanizeBytes(rec.USBBytesFree), humanizeBytes(rec.USBBytesTotal)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Namespace prefix:    %s\n", rec.NamespacePrefix); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Lock status:         %s\n", rec.LockStatus); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "FlashBackup version: %s\n", rec.FlashbackupVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "rsync version:       %s\n", rec.RsyncVersion); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "Retained runs:       %d / %d\n",
		rec.RetainedRuns, rec.RetentionLimit); err != nil {
		return err
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if rec.LastRun == nil {
		if _, err := fmt.Fprintln(w, "Last run: (none yet)"); err != nil {
			return err
		}
	} else {
		lr := rec.LastRun
		if _, err := fmt.Fprintln(w, "Last run:"); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  RunID:           %s\n", lr.RunID); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Started:         %s\n", lr.StartedAt); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Finished:        %s\n", lr.FinishedAt); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Mode:            %s\n", lr.Mode); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Profile:         %s\n", lr.Profile); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Exit status:     %s\n", lr.ExitStatus); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Files:           %d / %d succeeded (%d failed)\n",
			lr.FilesSucceeded, lr.FilesTotal, lr.FilesFailed); err != nil {
			return err
		}
		if _, err := fmt.Fprintf(w, "  Bytes:           %s\n", humanizeBytes(lr.BytesTotal)); err != nil {
			return err
		}
	}

	if _, err := fmt.Fprintln(w); err != nil {
		return err
	}
	if rec.LastVerify == nil {
		if _, err := fmt.Fprintln(w, "Last verify: (none yet)"); err != nil {
			return err
		}
		return nil
	}
	lv := rec.LastVerify
	if _, err := fmt.Fprintln(w, "Last verify:"); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  VerifyID:        %s\n", lv.VerifyID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  For RunID:       %s\n", lv.ForRunID); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Verified at:     %s\n", lv.VerifiedAt); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Exit status:     %s\n", lv.ExitStatus); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Files verified:  %d\n", lv.FilesVerified); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Integrity fails: %d\n", lv.FilesIntegrityFailed); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "  Hash mismatches: %d\n", lv.FilesHashMismatch); err != nil {
		return err
	}
	return nil
}

// humanizeBytes renders an integer byte count in SI decimal units (1 GB =
// 1e9 bytes). The schema example uses raw bytes; the plain-text rendering
// uses human-friendly units because a 9-digit number is unreadable on a
// terminal. Choice of decimal SI (vs binary IEC) matches macOS Finder's
// convention, which is what most operators see daily.
//
// Returns one decimal place for GB/MB/KB; bare bytes for sub-1KB values.
// Negative inputs (cannot happen on a sane volume but defensive) render
// as "0 B" rather than emitting "-X" which would look like a parse error
// to a downstream eye.
func humanizeBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	switch {
	case b >= 1_000_000_000:
		return fmt.Sprintf("%.1f GB", float64(b)/1_000_000_000)
	case b >= 1_000_000:
		return fmt.Sprintf("%.1f MB", float64(b)/1_000_000)
	case b >= 1_000:
		return fmt.Sprintf("%.1f KB", float64(b)/1_000)
	default:
		return fmt.Sprintf("%d B", b)
	}
}
