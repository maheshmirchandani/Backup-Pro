package e2e

// verify_tampered_manifest_test.go covers AC-19 end-to-end: tampering a
// manifest entry's sha256_source field post-backup (while leaving the
// persisted HMAC intact) must be caught by `flashbackup verify` as a
// keyed integrity check failure (FilesIntegrityFailed >= 1, exit code 1,
// exit_status="integrity_failed").
//
// The HMAC field of each ManifestEntry is a keyed checksum over the
// canonical (V, Path, Size, MtimeNS, SHA256Source, CopiedAt, Status)
// tuple using a per-USB key from version.json (invariant #33). Any byte
// flip in the JSON-encoded value of one of those fields breaks the HMAC
// recomputation at load time without breaking the JSON structure.
//
// Test mechanic:
//
//   1. Mount + init a fresh APFS DMG, seed the tiny fixture, store a
//      profile.
//   2. Run backup, assert exit 0 and that manifest.ndjson.gz lands.
//   3. Decompress manifest.ndjson.gz, parse the first JSON line into a
//      state.ManifestEntry, overwrite SHA256Source with a different
//      well-formed hex string (the HMAC field is left untouched, so the
//      keyed checksum no longer matches the new canonical tuple),
//      re-marshal, recompress, write back over the manifest file.
//   4. Run verify; assert exit code 1 and that summary.json reports
//      FilesIntegrityFailed >= 1 and exit_status="integrity_failed".
//
// The cmd-layer counterpart (cmd/flashbackup/verify_test.go's
// TestVerify_TamperedManifest) covers the same path against a planted
// manifest with no real backup; this e2e test pins the full pipeline
// (real init + real backup + real verify) end-to-end so a regression in
// the runner's HMAC write path or in verify's HMAC recompute path
// surfaces here rather than only in unit-level coverage.
//
// Tagged into the e2e-safety Makefile gate via the "TamperedManifest"
// run-name pattern. NOT build-tagged faultinject: the test does NOT use
// the faultinject DSL; it tampers the manifest directly on disk.

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
)

// TestE2E_VerifyTamperedManifest_AC19 backs up the tiny fixture, flips
// the first manifest entry's sha256_source field while leaving the
// persisted HMAC untouched, then runs verify and asserts the AC-19
// contract: exit code 1, summary.json.exit_status="integrity_failed",
// summary.json.files_integrity_failed >= 1.
func TestE2E_VerifyTamperedManifest_AC19(t *testing.T) {
	testutil.RequireE2E(t)
	testutil.RequireMacOS(t)
	testutil.RequireHdiutil(t)
	testutil.RequireDiskutil(t)

	// Real GNU rsync is required: the embedded placeholder stub at this
	// stage of the plan exits 0 without copying bytes, which would make
	// T2 see every file as not_transferred and no manifest entries would
	// be written for verify to integrity-check. Skip cleanly if not on
	// the box.
	gnuRsync := findGNURsync()
	if gnuRsync == "" {
		t.Skip("real GNU rsync not found at /opt/homebrew/bin/rsync or /usr/local/bin/rsync; install via brew install rsync")
	}
	t.Setenv("FLASHBACKUP_RSYNC_PATH_FOR_TEST", gnuRsync)

	// Mount + init the USB.
	usb := SetupUSB(t, 64)

	// Seed source + profile. Includes=["*"] matches every tiny-fixture
	// file (a.txt, b.md, c.json); no excludes.
	source := SeedSource(t, "tiny")
	SeedProfile(t, usb, "tamper-test", source, []string{"*"}, nil)

	// Backup.
	exitCode, stdout, stderr := RunBackup(t, "tamper-test", usb)
	if exitCode != 0 {
		t.Fatalf("backup exit code: got %d want 0\nstdout: %s\nstderr: %s",
			exitCode, stdout, stderr)
	}
	runID := AssertRunsNDJSONHasFinishedLine(t, usb)
	if runID == "" {
		t.Fatalf("AssertRunsNDJSONHasFinishedLine returned empty runID")
	}
	AssertManifestExists(t, usb, runID)

	// Tamper: flip the first manifest entry's SHA256Source while leaving
	// the persisted HMAC untouched. verify's load path will recompute
	// HMAC over the (now-tampered) tuple and surface the mismatch as an
	// IntegrityError.
	manifestPath := filepath.Join(usb, ".flashbackup", "runs", runID, "manifest.ndjson.gz")
	tamperFirstManifestEntry(t, manifestPath)

	// Verify. Run with an explicit run-id so the resolver does not have
	// to consult version.json or scan runs/; the test path under
	// investigation is the manifest load / HMAC recompute, not run
	// selection.
	exitCode, vstdout, vstderr := RunVerify(t, usb, runID)
	if exitCode != 1 {
		t.Errorf("verify exit code: got %d want 1 (integrity_failed)\nstdout: %s\nstderr: %s",
			exitCode, vstdout, vstderr)
	}

	// summary.json lands under <usb>/.flashbackup/runs/<runID>/verifications/<verifyID>/.
	summaryPath := AssertVerifySummaryExists(t, usb, runID)
	rec := readSummaryRecord(t, summaryPath)

	if rec.ExitStatus != "integrity_failed" {
		t.Errorf("exit_status: got %q want %q", rec.ExitStatus, "integrity_failed")
	}
	if rec.FilesIntegrityFailed < 1 {
		t.Errorf("files_integrity_failed: got %d want >=1 (AC-19)", rec.FilesIntegrityFailed)
	}
	if rec.ForRunID != runID {
		t.Errorf("for_run_id: got %q want %q", rec.ForRunID, runID)
	}
}

// tamperFirstManifestEntry decompresses the gzipped NDJSON manifest at
// manifestPath, rewrites the first entry's SHA256Source field to a
// well-formed hex string that is GUARANTEED to differ from the real
// source hash (all-zero hex), leaves the persisted HMAC untouched so the
// keyed checksum no longer matches the new canonical tuple, then
// recompresses and writes back.
//
// We use a typed unmarshal into state.ManifestEntry (rather than a raw
// byte-flip in the JSON text) so a future field rename surfaces here as
// a compile error rather than as a silent "wrote-back unchanged bytes"
// pass. Mirrors cmd/flashbackup/verify_test.go's verifyCmdTamperManifest
// helper; duplicated because Go test files in package e2e cannot import
// helpers from package main.
//
// The replacement hex value is all-zeros. SHA256 of the real source bytes
// is cryptographically guaranteed not to be the all-zero hash (would
// imply a preimage attack against SHA-256), so the rewritten field
// always differs from the legitimate one.
func tamperFirstManifestEntry(t *testing.T, manifestPath string) {
	t.Helper()

	// Decompress.
	f, err := os.Open(manifestPath)
	if err != nil {
		t.Fatalf("open %s: %v", manifestPath, err)
	}
	gr, err := gzip.NewReader(f)
	if err != nil {
		f.Close()
		t.Fatalf("gzip reader %s: %v", manifestPath, err)
	}
	raw, err := io.ReadAll(gr)
	gr.Close()
	f.Close()
	if err != nil {
		t.Fatalf("read manifest body: %v", err)
	}

	// Split into NDJSON lines. TrimRight strips the trailing newline so
	// Split does not produce a phantom empty trailing line.
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatalf("manifest %s empty; nothing to tamper", manifestPath)
	}

	// Parse the first entry, rewrite SHA256Source, leave HMAC alone.
	var e state.ManifestEntry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatalf("unmarshal first manifest entry: %v\nline: %s", err, lines[0])
	}
	// 64 hex chars of '0'. SHA256 collision against the real source bytes
	// is cryptographically negligible, so this is guaranteed to differ
	// from the legitimate hash recorded at T2-pre-hash time.
	e.SHA256Source = "0000000000000000000000000000000000000000000000000000000000000000"
	rewritten, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal rewritten entry: %v", err)
	}
	lines[0] = rewritten

	// Re-join with newline separator + trailing newline (mirrors the
	// runner's NDJSON convention).
	out := bytes.Join(lines, []byte("\n"))
	out = append(out, '\n')

	// Recompress.
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(out); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}

	// Write back over the manifest. Use 0o600 to match the runner's mode.
	if err := os.WriteFile(manifestPath, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write tampered manifest %s: %v", manifestPath, err)
	}
}
