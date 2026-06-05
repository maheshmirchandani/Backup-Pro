package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/testutil"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify"
)

// ----------------------------------------------------------------------------
// Unit tests (no DMG; exercise argv parsing + error paths)
//
// These cover the cmd-side seam: argv parsing, mutually-exclusive flag
// combinations, USB-path resolution. The verify pipeline itself (load +
// rehash + summary write) is covered by internal/verify/verify_test.go.
// ----------------------------------------------------------------------------

// TestVerify_MissingDestArg: bare `flashbackup verify` (no positionals)
// must reject with exit 2 + a usage block on stderr. This is the most
// common operator typo (forgot the path) so a regression here would surface
// as silent zero exit, which the dispatcher must not allow.
func TestVerify_MissingDestArg(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "verify"})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on usage error, got %q", stdout)
	}
	if !strings.Contains(stderr, "missing <USB-path>") {
		t.Errorf("stderr should explain missing arg, got %q", stderr)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestVerify_HelpFlag: `flashbackup verify --help` must exit 0 (deliberate
// user request for help; not an error) and print the usage block to
// stderr. Matches the init / backup subcommand convention.
func TestVerify_HelpFlag(t *testing.T) {
	code, stdout, stderr := runCapture(t, []string{"flashbackup", "verify", "--help"})
	if code != verifyExitCodeOK {
		t.Errorf("exit code: got %d, want 0", code)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on --help, got %q", stdout)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should contain usage block, got %q", stderr)
	}
	if !strings.Contains(stderr, "--all") {
		t.Errorf("stderr should mention --all flag, got %q", stderr)
	}
	if !strings.Contains(stderr, "--check-extras") {
		t.Errorf("stderr should mention --check-extras flag, got %q", stderr)
	}
}

// TestVerify_UnknownFlag: an unknown flag (typo of --all) must exit 2 with
// the offending flag named in stderr. The flag.FlagSet writes its own error
// line before our Usage runs; we only assert exit code + that the usage
// block is present.
func TestVerify_UnknownFlag(t *testing.T) {
	code, _, stderr := runCapture(t, []string{"flashbackup", "verify", "--alll", "/tmp"})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if !strings.Contains(stderr, "Usage:") {
		t.Errorf("stderr should include usage block, got %q", stderr)
	}
}

// TestVerify_AllAndRunIDMutuallyExclusive: passing both --all AND a
// positional run-id must exit 2 with a clear error. Matches the
// verify.Verify guard (which would also reject), but rejecting at the cmd
// level gives a better error message and prevents a confusing "preflight
// failed" wrap of the underlying mutual-exclusion error.
func TestVerify_AllAndRunIDMutuallyExclusive(t *testing.T) {
	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "verify", "--all", "2026-06-04T1200Z-aaaa", "/tmp"})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if !strings.Contains(stderr, "mutually exclusive") {
		t.Errorf("stderr should mention mutual exclusivity, got %q", stderr)
	}
}

// TestVerify_TooManyArgs: three positionals (no --all) must exit 2. The
// guard against an operator who typed `verify run-id USB extra-junk`.
func TestVerify_TooManyArgs(t *testing.T) {
	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "verify", "2026-06-04T1200Z-aaaa", "/tmp", "extra"})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if !strings.Contains(stderr, "unexpected extra arguments") {
		t.Errorf("stderr should mention extra args, got %q", stderr)
	}
}

// TestVerify_NonexistentPath: a USB path that does not exist on disk must
// exit 2 from the EvalSymlinks step, not from the verify pipeline. Matches
// the init / backup subcommand contract: usage errors surface at the cmd
// layer with a clear "no such file" message.
func TestVerify_NonexistentPath(t *testing.T) {
	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "verify", "/nonexistent/never/will-exist-verify-test"})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if !strings.Contains(stderr, "flashbackup verify:") {
		t.Errorf("stderr should be prefixed with 'flashbackup verify:', got %q", stderr)
	}
}

// TestVerify_PathIsFile: USB path that resolves to a regular file (not a
// directory) must exit 2. Catches the operator who passed a single file
// path by mistake.
func TestVerify_PathIsFile(t *testing.T) {
	tmpFile := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(tmpFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := runCapture(t, []string{"flashbackup", "verify", tmpFile})
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, verifyExitCodeUsage)
	}
	if !strings.Contains(stderr, "not a directory") {
		t.Errorf("stderr should mention 'not a directory', got %q", stderr)
	}
}

// TestVerifyExitCode covers the translator table for every documented
// ExitStatus value plus the nil / empty / unknown defensive arms. This is
// the cmd-side contract surface; a future ExitStatus addition would require
// a new arm here and a doc.go update.
func TestVerifyExitCode(t *testing.T) {
	cases := []struct {
		name   string
		result *verify.VerifyResult
		want   int
	}{
		{"nil result", nil, verifyExitCodeRuntime},
		{"empty exit status", &verify.VerifyResult{}, verifyExitCodeRuntime},
		{"ok", &verify.VerifyResult{ExitStatus: verify.ExitStatusOK}, verifyExitCodeOK},
		{"integrity_failed", &verify.VerifyResult{ExitStatus: verify.ExitStatusIntegrityFailed}, verifyExitCodeRuntime},
		{"preflight_failed", &verify.VerifyResult{ExitStatus: verify.ExitStatusPreflightFailed}, verifyExitCodeUsage},
		{"unknown status", &verify.VerifyResult{ExitStatus: "future_value"}, verifyExitCodeRuntime},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := verifyExitCode(tc.result)
			if got != tc.want {
				t.Errorf("verifyExitCode(%+v) = %d, want %d", tc.result, got, tc.want)
			}
		})
	}
}

// TestRunVerify_DirectCall_NoStdin: exercise runVerify directly with a
// nil stdin to confirm the stdin parameter is accepted but not consumed.
// This is the regression guard for the handler-signature symmetry: if a
// future change made runVerify hard-require stdin, this test would crash
// rather than silently changing behaviour.
func TestRunVerify_DirectCall_NoStdin(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runVerify(context.Background(), []string{}, nil, &stdout, &stderr)
	if code != verifyExitCodeUsage {
		t.Errorf("exit code: got %d, want %d (missing USB path)", code, verifyExitCodeUsage)
	}
}

// ----------------------------------------------------------------------------
// E2E tests (mount a DMG, plant a manifest, run verify)
//
// These cover AC-9, AC-10, and AC-19 end-to-end through the cmd layer.
// The fixture pattern mirrors internal/verify.verify_test.go's plantRun:
// init + version.json + a manifest with HMAC-signed entries + namespaced
// dest files. We re-implement plantRun locally rather than depend on
// internal/verify's test helpers because Go test files cannot import each
// other's helpers.
// ----------------------------------------------------------------------------

// verifyCmdPlantRun seeds one run on the USB: writes manifest.ndjson.gz +
// dest files. The runID format matches the canonical runIDPattern so the
// verify resolver picks it up. host/user must match the values returned by
// os.Hostname() + os.Getenv("USER") so paths.Namespaced derives the right
// dest path.
//
// Returns the manifest path so tamper tests can rewrite it in place.
func verifyCmdPlantRun(t *testing.T, dest, runID string, files map[string][]byte) string {
	t.Helper()

	dotDir := filepath.Join(dest, ".flashbackup")
	runDir := filepath.Join(dotDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir runDir: %v", err)
	}

	versionPath := filepath.Join(dotDir, "version.json")
	vf, err := state.ReadVersionFile(versionPath)
	if err != nil {
		t.Fatalf("ReadVersionFile: %v", err)
	}
	hmacKey := verifyCmdDecodeHex(t, vf.HMACKey)

	manifestBase := filepath.Join(runDir, "manifest.ndjson")
	store, err := state.NewNDJSONManifestStore(manifestBase, hmacKey)
	if err != nil {
		t.Fatalf("NewNDJSONManifestStore: %v", err)
	}
	ctx := context.Background()
	for rel, payload := range files {
		e := state.ManifestEntry{
			V:            1,
			Path:         rel,
			Size:         int64(len(payload)),
			MtimeNS:      1700000000000000000,
			SHA256Source: verifyCmdSha256Hex(payload),
			Status:       state.StatusVerified,
		}
		if err := store.AppendEntry(ctx, e); err != nil {
			t.Fatalf("AppendEntry %q: %v", rel, err)
		}
	}
	if err := store.Gzip(ctx); err != nil {
		t.Fatalf("Gzip: %v", err)
	}

	host, err := os.Hostname()
	if err != nil {
		t.Fatalf("os.Hostname: %v", err)
	}
	user := os.Getenv("USER")
	if user == "" {
		t.Fatal("USER env var empty; cannot derive namespace prefix")
	}

	for rel, payload := range files {
		// Use the canonical paths.Namespaced layout so the rehash loop
		// finds files at the path the manifest entry resolves to.
		// paths.Prefix replaces dots with hyphens (macbook.local ->
		// macbook-local) so we cannot simply concatenate host+"-"+user.
		full := paths.Namespaced(dest, host, user, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatalf("mkdir dest %q: %v", full, err)
		}
		if err := os.WriteFile(full, payload, 0o600); err != nil {
			t.Fatalf("write dest %q: %v", full, err)
		}
	}

	return manifestBase + ".gz"
}

// verifyCmdDecodeHex wraps hex.DecodeString with a t.Fatal on parse error.
// Kept as a helper so the call sites stay one-liners.
func verifyCmdDecodeHex(t *testing.T, s string) []byte {
	t.Helper()
	out, err := hex.DecodeString(s)
	if err != nil {
		t.Fatalf("decode hex %q: %v", s, err)
	}
	return out
}

// verifyCmdSha256Hex returns the hex-encoded sha256 of b.
func verifyCmdSha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// verifyCmdInitUSB initializes a USB at the given dest path via the init
// subcommand. Returns when init succeeds; t.Fatal on failure so callers do
// not have to inspect the exit code.
func verifyCmdInitUSB(t *testing.T, dest string) {
	t.Helper()
	code, _, stderr := runCapture(t, []string{"flashbackup", "init", dest})
	if code != 0 {
		t.Fatalf("init failed: code=%d stderr=%s", code, stderr)
	}
}

// TestVerify_HappyPath: init + plant a manifest + dest files + run verify.
// Exit 0; summary.json lands under verifications/. AC-9.
func TestVerify_HappyPath(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	verifyCmdInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	runID := "2026-06-04T1200Z-aaaa"
	verifyCmdPlantRun(t, dest, runID, map[string][]byte{
		"a.txt":     []byte("alpha content"),
		"sub/b.txt": []byte("bravo content longer"),
	})

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "verify", dest})
	if code != verifyExitCodeOK {
		t.Errorf("exit code: got %d, want %d\nstdout=%s\nstderr=%s",
			code, verifyExitCodeOK, stdout, stderr)
	}

	// summary.json lands at the canonical path.
	verifyDirs, err := os.ReadDir(filepath.Join(dest, ".flashbackup", "runs", runID, "verifications"))
	if err != nil {
		t.Fatalf("read verifications dir: %v", err)
	}
	if len(verifyDirs) != 1 {
		t.Fatalf("expected 1 verifications subdir, got %d", len(verifyDirs))
	}
	summaryPath := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications",
		verifyDirs[0].Name(), "summary.json")
	if _, err := os.Stat(summaryPath); err != nil {
		t.Errorf("summary.json missing at %s: %v", summaryPath, err)
	}
}

// TestVerify_TamperedManifest: AC-19. Tampering one entry's SHA256Source
// while leaving the HMAC intact must surface as an integrity failure.
// Exit 1; FilesIntegrityFailed > 0 reflected in summary.json.
func TestVerify_TamperedManifest(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	verifyCmdInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	runID := "2026-06-04T1300Z-bbbb"
	manifestPath := verifyCmdPlantRun(t, dest, runID, map[string][]byte{
		"a.txt": []byte("alpha"),
		"b.txt": []byte("bravo"),
	})

	verifyCmdTamperManifest(t, manifestPath)

	code, stdout, stderr := runCapture(t, []string{"flashbackup", "verify", dest})
	if code != verifyExitCodeRuntime {
		t.Errorf("exit code: got %d, want %d (AC-19 tamper)\nstdout=%s\nstderr=%s",
			code, verifyExitCodeRuntime, stdout, stderr)
	}

	// Read summary.json and assert FilesIntegrityFailed >= 1.
	verifyDirs, err := os.ReadDir(filepath.Join(dest, ".flashbackup", "runs", runID, "verifications"))
	if err != nil {
		t.Fatalf("read verifications dir: %v", err)
	}
	if len(verifyDirs) != 1 {
		t.Fatalf("expected 1 verifications subdir, got %d", len(verifyDirs))
	}
	summaryPath := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications",
		verifyDirs[0].Name(), "summary.json")
	rec := verifyCmdReadSummary(t, summaryPath)
	got, _ := rec["files_integrity_failed"].(float64)
	if got < 1 {
		t.Errorf("files_integrity_failed: got %v, want >=1 (AC-19)", got)
	}
	if status, _ := rec["exit_status"].(string); status != string(verify.ExitStatusIntegrityFailed) {
		t.Errorf("exit_status: got %q, want %q", status, verify.ExitStatusIntegrityFailed)
	}
}

// TestVerify_ExplicitRunID: planting two runs, then verifying the older
// run by explicit run-id positional must verify only that run (the latest
// is NOT touched). The summary.json lands under the older run's
// verifications dir and the newer one is untouched.
func TestVerify_ExplicitRunID(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	verifyCmdInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	older := "2026-06-04T1000Z-aaaa"
	newer := "2026-06-04T1100Z-bbbb"
	verifyCmdPlantRun(t, dest, older, map[string][]byte{"old.txt": []byte("o")})
	verifyCmdPlantRun(t, dest, newer, map[string][]byte{"new.txt": []byte("n")})

	code, _, stderr := runCapture(t, []string{"flashbackup", "verify", older, dest})
	if code != verifyExitCodeOK {
		t.Errorf("exit code: got %d, want %d\nstderr=%s", code, verifyExitCodeOK, stderr)
	}

	// Older run got a verifications subdir, newer did not.
	olderVer := filepath.Join(dest, ".flashbackup", "runs", older, "verifications")
	if entries, err := os.ReadDir(olderVer); err != nil || len(entries) != 1 {
		t.Errorf("older run should have 1 verifications subdir; got entries=%v err=%v",
			entries, err)
	}
	newerVer := filepath.Join(dest, ".flashbackup", "runs", newer, "verifications")
	if entries, err := os.ReadDir(newerVer); err == nil && len(entries) > 0 {
		t.Errorf("newer run should NOT have a verifications subdir; got %v", entries)
	}
}

// TestVerify_All: planting two runs, then `flashbackup verify --all <USB>`
// must verify both runs. Each run gets its own verifications/summary.json.
func TestVerify_All(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	verifyCmdInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	r1 := "2026-06-04T1000Z-aaaa"
	r2 := "2026-06-04T1100Z-bbbb"
	verifyCmdPlantRun(t, dest, r1, map[string][]byte{"x.txt": []byte("x")})
	verifyCmdPlantRun(t, dest, r2, map[string][]byte{"y.txt": []byte("y")})

	code, _, stderr := runCapture(t, []string{"flashbackup", "verify", "--all", dest})
	if code != verifyExitCodeOK {
		t.Errorf("exit code: got %d, want %d\nstderr=%s", code, verifyExitCodeOK, stderr)
	}

	for _, r := range []string{r1, r2} {
		verDir := filepath.Join(dest, ".flashbackup", "runs", r, "verifications")
		entries, err := os.ReadDir(verDir)
		if err != nil {
			t.Errorf("read verifications for %s: %v", r, err)
			continue
		}
		if len(entries) != 1 {
			t.Errorf("run %s: expected 1 verifications subdir, got %d", r, len(entries))
		}
	}
}

// TestVerify_CheckExtras: plant a run, drop an additional file under the
// namespaced dest that is NOT in the manifest, run verify --check-extras.
// FilesExtraInDest must reflect the extra file; exit_status stays ok
// (extras are informational, never an integrity failure).
func TestVerify_CheckExtras(t *testing.T) {
	testutil.RequireMacOS(t)
	testutil.RequireE2E(t)
	testutil.RequireDiskutil(t)

	dest := testutil.MountTempVolume(t, "APFS")
	verifyCmdInitUSB(t, dest)
	defer clearImmutableRsync(dest)

	runID := "2026-06-04T1200Z-aaaa"
	verifyCmdPlantRun(t, dest, runID, map[string][]byte{
		"a.txt": []byte("alpha"),
	})

	// Add an extra file to the namespaced dest that is NOT in the manifest.
	host, _ := os.Hostname()
	user := os.Getenv("USER")
	extra := paths.Namespaced(dest, host, user, "extra.txt")
	if err := os.WriteFile(extra, []byte("uninvited"), 0o600); err != nil {
		t.Fatalf("write extra: %v", err)
	}

	code, _, stderr := runCapture(t,
		[]string{"flashbackup", "verify", "--check-extras", dest})
	if code != verifyExitCodeOK {
		t.Errorf("exit code: got %d, want %d (extras are informational)\nstderr=%s",
			code, verifyExitCodeOK, stderr)
	}

	verifyDirs, _ := os.ReadDir(filepath.Join(dest, ".flashbackup", "runs", runID, "verifications"))
	if len(verifyDirs) != 1 {
		t.Fatalf("expected 1 verifications subdir, got %d", len(verifyDirs))
	}
	summaryPath := filepath.Join(dest, ".flashbackup", "runs", runID, "verifications",
		verifyDirs[0].Name(), "summary.json")
	rec := verifyCmdReadSummary(t, summaryPath)
	if got, _ := rec["files_extra_in_dest"].(float64); got != 1 {
		t.Errorf("files_extra_in_dest: got %v, want 1", got)
	}
}

// verifyCmdTamperManifest rewrites the SHA256Source field of the first
// entry in a gzipped NDJSON manifest in place, keeping the persisted HMAC
// intact so verify.Load surfaces it as an IntegrityError (AC-19 path).
// Mirrors internal/verify.tamperManifestEntry but lives here because Go
// test files cannot import each other's helpers.
func verifyCmdTamperManifest(t *testing.T, manifestPath string) {
	t.Helper()
	raw := verifyCmdGunzipFile(t, manifestPath)
	lines := bytes.Split(bytes.TrimRight(raw, "\n"), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("manifest empty; nothing to tamper")
	}
	var e state.ManifestEntry
	if err := json.Unmarshal(lines[0], &e); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	e.SHA256Source = "f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0f0"
	rewritten, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	lines[0] = rewritten
	out := bytes.Join(lines, []byte("\n"))
	out = append(out, '\n')
	verifyCmdWriteGzipFile(t, manifestPath, out)
}

func verifyCmdGunzipFile(t *testing.T, path string) []byte {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer f.Close()
	gr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer gr.Close()
	data, err := io.ReadAll(gr)
	if err != nil {
		t.Fatalf("read all: %v", err)
	}
	return data
}

func verifyCmdWriteGzipFile(t *testing.T, path string, data []byte) {
	t.Helper()
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	if _, err := gw.Write(data); err != nil {
		t.Fatalf("gzip write: %v", err)
	}
	if err := gw.Close(); err != nil {
		t.Fatalf("gzip close: %v", err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o600); err != nil {
		t.Fatalf("write file: %v", err)
	}
}

// verifyCmdReadSummary reads + parses a summary.json from disk. Returns
// the raw map so callers can assert on counter fields without coupling to
// the on-disk record type.
func verifyCmdReadSummary(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var rec map[string]any
	if err := json.Unmarshal(data, &rec); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return rec
}
