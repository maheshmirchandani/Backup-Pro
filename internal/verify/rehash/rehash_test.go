package rehash_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/paths"
	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
	"github.com/maheshmirchandani/Backup-Pro/internal/state"
	"github.com/maheshmirchandani/Backup-Pro/internal/verify/rehash"
)

// ----------------------------------------------------------------------------
// Fixture helpers
// ----------------------------------------------------------------------------

const (
	testHost = "macbook-local"
	testUser = "alice"
)

// sha256Hex returns the hex-encoded sha256 of b.
func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// writeNamespacedFile writes payload to the namespaced dest path that
// rehash will look at: <destRoot>/<paths.Prefix(host, user)>/<relPath>.
// Returns the absolute on-disk path for cleanup or chmod.
func writeNamespacedFile(t *testing.T, destRoot, relPath string, payload []byte) string {
	t.Helper()
	full := paths.Namespaced(destRoot, testHost, testUser, relPath)
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(full, payload, 0o600); err != nil {
		t.Fatalf("write %s: %v", full, err)
	}
	return full
}

// entry constructs a manifest entry whose size + sha256 match payload.
// The other fields are filled with stable placeholders so tests need not
// compare them explicitly.
func entry(relPath string, payload []byte) state.ManifestEntry {
	return state.ManifestEntry{
		V:            1,
		Path:         relPath,
		Size:         int64(len(payload)),
		MtimeNS:      1700000000000000000,
		SHA256Source: sha256Hex(payload),
		CopiedAt:     time.Date(2026, 6, 3, 14, 30, 0, 0, time.UTC),
		Status:       state.StatusVerified,
	}
}

// recordingRenderer captures every UIEvent it receives. Used by tests that
// assert on the progress event stream.
type recordingRenderer struct {
	mu     sync.Mutex
	events []types.UIEvent
}

func (r *recordingRenderer) OnEvent(_ context.Context, ev types.UIEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, ev)
	return nil
}

func (r *recordingRenderer) snapshot() []types.UIEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]types.UIEvent, len(r.events))
	copy(out, r.events)
	return out
}

// erroringRenderer always returns an error from OnEvent; used to confirm
// rehash swallows renderer errors per PS3.
type erroringRenderer struct{}

func (erroringRenderer) OnEvent(_ context.Context, _ types.UIEvent) error {
	return errors.New("renderer down")
}

// ----------------------------------------------------------------------------
// Tests
// ----------------------------------------------------------------------------

func TestRehash_HappyPath(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	files := map[string][]byte{
		"a.txt":            []byte("alpha content"),
		"sub/b.txt":        []byte("bravo bravo bravo"),
		"deep/nested/c.go": []byte("package main\n"),
	}
	var entries []state.ManifestEntry
	for rel, payload := range files {
		writeNamespacedFile(t, dest, rel, payload)
		entries = append(entries, entry(rel, payload))
	}

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  entries,
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if got, want := res.FilesChecked, len(entries); got != want {
		t.Errorf("FilesChecked: got %d want %d", got, want)
	}
	if got, want := res.FilesVerified, len(entries); got != want {
		t.Errorf("FilesVerified: got %d want %d", got, want)
	}
	if res.FilesHashMismatch != 0 || res.FilesSizeMismatch != 0 ||
		res.FilesMissing != 0 || res.FilesUnreadable != 0 {
		t.Errorf("expected only Verified counts; got %+v", res)
	}
	var wantBytes int64
	for _, p := range files {
		wantBytes += int64(len(p))
	}
	if res.BytesRead != wantBytes {
		t.Errorf("BytesRead: got %d want %d", res.BytesRead, wantBytes)
	}
	for _, fr := range res.PerFile {
		if fr.Status != rehash.StatusVerified {
			t.Errorf("file %q: got Status=%s want verified", fr.Entry.Path, fr.Status)
		}
		if fr.Err != nil {
			t.Errorf("file %q: unexpected Err=%v", fr.Entry.Path, fr.Err)
		}
		if fr.ActualSHA256 == "" {
			t.Errorf("file %q: ActualSHA256 empty", fr.Entry.Path)
		}
	}
}

func TestRehash_SizeMismatch(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	payload := []byte("on-disk bytes are 22b")
	writeNamespacedFile(t, dest, "a.txt", payload)

	// Manifest claims size=100 (and a sha256 of the imagined 100-byte
	// file); on-disk size is len(payload). Cheap fail-fast must NOT hash.
	e := entry("a.txt", payload)
	e.Size = 100
	e.SHA256Source = sha256Hex([]byte("imagined 100-byte original"))

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesSizeMismatch != 1 {
		t.Errorf("FilesSizeMismatch: got %d want 1", res.FilesSizeMismatch)
	}
	if len(res.PerFile) != 1 || res.PerFile[0].Status != rehash.StatusSizeMismatch {
		t.Fatalf("PerFile: %+v", res.PerFile)
	}
	if res.PerFile[0].ActualSHA256 != "" {
		t.Errorf("ActualSHA256 should be empty (no hash should have run); got %q",
			res.PerFile[0].ActualSHA256)
	}
	if res.PerFile[0].ActualSize != int64(len(payload)) {
		t.Errorf("ActualSize: got %d want %d", res.PerFile[0].ActualSize, len(payload))
	}
	if res.BytesRead != 0 {
		t.Errorf("BytesRead: got %d want 0 (size_mismatch should not contribute)", res.BytesRead)
	}
}

func TestRehash_HashMismatch(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	onDisk := []byte("THIS IS THE WRONG CONTNT.")
	original := []byte("this was the original ctt")
	if len(onDisk) != len(original) {
		t.Fatalf("test setup error: payloads must be same length (got %d vs %d)",
			len(onDisk), len(original))
	}
	writeNamespacedFile(t, dest, "a.txt", onDisk)

	// Manifest entry's sha256 matches `original`, but on-disk bytes are
	// `onDisk`. Both same size so the size check passes and we reach the
	// hash compare.
	e := entry("a.txt", original)
	if e.Size != int64(len(onDisk)) {
		t.Fatalf("setup: size mismatch")
	}

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesHashMismatch != 1 {
		t.Errorf("FilesHashMismatch: got %d want 1", res.FilesHashMismatch)
	}
	if res.PerFile[0].Status != rehash.StatusHashMismatch {
		t.Errorf("Status: got %s want hash_mismatch", res.PerFile[0].Status)
	}
	if res.PerFile[0].ActualSHA256 != sha256Hex(onDisk) {
		t.Errorf("ActualSHA256: got %q want %q",
			res.PerFile[0].ActualSHA256, sha256Hex(onDisk))
	}
	if res.BytesRead != int64(len(onDisk)) {
		t.Errorf("BytesRead: got %d want %d", res.BytesRead, len(onDisk))
	}
}

func TestRehash_Missing(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	// Entry exists in manifest; file does NOT exist on disk.
	e := entry("phantom.txt", []byte("never written"))

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesMissing != 1 {
		t.Errorf("FilesMissing: got %d want 1", res.FilesMissing)
	}
	fr := res.PerFile[0]
	if fr.Status != rehash.StatusMissing {
		t.Errorf("Status: got %s want missing", fr.Status)
	}
	if fr.ActualSize != -1 {
		t.Errorf("ActualSize: got %d want -1 (sentinel for missing)", fr.ActualSize)
	}
	if fr.Err != nil {
		// Missing is NOT an error; it's a valid verify outcome. The
		// underlying ENOENT must not leak into Err.
		t.Errorf("Err should be nil for missing; got %v", fr.Err)
	}
	if fr.ActualSHA256 != "" {
		t.Errorf("ActualSHA256: got %q want empty", fr.ActualSHA256)
	}
}

func TestRehash_Unreadable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("chmod-based permission test is POSIX only")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permission bits; cannot construct an unreadable file")
	}
	ctx := context.Background()
	dest := t.TempDir()

	payload := []byte("readable bytes")
	full := writeNamespacedFile(t, dest, "locked.txt", payload)

	// chmod 0o000 to deny open. Cleanup must restore perms or t.TempDir's
	// rm-rf will fail on macOS for the parent dir.
	if err := os.Chmod(full, 0o000); err != nil {
		t.Fatalf("chmod 000: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(full, 0o600) })

	e := entry("locked.txt", payload)

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesUnreadable != 1 {
		t.Fatalf("FilesUnreadable: got %d want 1 (perhaps lstat saw size and open failed): %+v",
			res.FilesUnreadable, res)
	}
	fr := res.PerFile[0]
	if fr.Status != rehash.StatusUnreadable {
		t.Errorf("Status: got %s want unreadable", fr.Status)
	}
	if fr.Err == nil {
		t.Errorf("Err should be populated for unreadable")
	}
}

func TestRehash_AggregateCounters(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses chmod for one of the five outcomes")
	}
	if os.Geteuid() == 0 {
		t.Skip("root bypasses POSIX permission bits")
	}
	ctx := context.Background()
	dest := t.TempDir()

	// 1. Verified
	verifiedPayload := []byte("verified content")
	writeNamespacedFile(t, dest, "verified.txt", verifiedPayload)
	verifiedEntry := entry("verified.txt", verifiedPayload)

	// 2. SizeMismatch
	sizeMisPayload := []byte("on-disk truncated")
	writeNamespacedFile(t, dest, "sizemis.txt", sizeMisPayload)
	sizeMisEntry := entry("sizemis.txt", sizeMisPayload)
	sizeMisEntry.Size = 999

	// 3. HashMismatch
	hashOnDisk := []byte("xxxxxxxxxxxxxxxxxxxx")
	hashOriginal := []byte("yyyyyyyyyyyyyyyyyyyy")
	writeNamespacedFile(t, dest, "hashmis.txt", hashOnDisk)
	hashMisEntry := entry("hashmis.txt", hashOriginal)

	// 4. Missing
	missingEntry := entry("missing.txt", []byte("never written to disk"))

	// 5. Unreadable
	unreadablePayload := []byte("locked content")
	unreadableFull := writeNamespacedFile(t, dest, "unreadable.txt", unreadablePayload)
	if err := os.Chmod(unreadableFull, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadableFull, 0o600) })
	unreadableEntry := entry("unreadable.txt", unreadablePayload)

	entries := []state.ManifestEntry{
		verifiedEntry, sizeMisEntry, hashMisEntry, missingEntry, unreadableEntry,
	}

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  entries,
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}

	if got, want := res.FilesChecked, 5; got != want {
		t.Errorf("FilesChecked: got %d want %d", got, want)
	}
	sum := res.FilesVerified + res.FilesSizeMismatch + res.FilesHashMismatch +
		res.FilesMissing + res.FilesUnreadable
	if sum != res.FilesChecked {
		t.Errorf("counter sum %d != FilesChecked %d: %+v", sum, res.FilesChecked, res)
	}
	if res.FilesVerified != 1 {
		t.Errorf("FilesVerified: got %d want 1", res.FilesVerified)
	}
	if res.FilesSizeMismatch != 1 {
		t.Errorf("FilesSizeMismatch: got %d want 1", res.FilesSizeMismatch)
	}
	if res.FilesHashMismatch != 1 {
		t.Errorf("FilesHashMismatch: got %d want 1", res.FilesHashMismatch)
	}
	if res.FilesMissing != 1 {
		t.Errorf("FilesMissing: got %d want 1", res.FilesMissing)
	}
	if res.FilesUnreadable != 1 {
		t.Errorf("FilesUnreadable: got %d want 1", res.FilesUnreadable)
	}
}

func TestRehash_EmptyEntries(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  nil,
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesChecked != 0 {
		t.Errorf("FilesChecked: got %d want 0", res.FilesChecked)
	}
	if len(res.PerFile) != 0 {
		t.Errorf("PerFile: got %d entries want 0", len(res.PerFile))
	}
	if res.BytesRead != 0 {
		t.Errorf("BytesRead: got %d want 0", res.BytesRead)
	}
}

func TestRehash_EmitsProgressUIEvents(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	files := map[string][]byte{
		"f1.txt": []byte("one"),
		"f2.txt": []byte("two two"),
		"f3.txt": []byte("three three three"),
	}
	var entries []state.ManifestEntry
	for rel, payload := range files {
		writeNamespacedFile(t, dest, rel, payload)
		entries = append(entries, entry(rel, payload))
	}

	rec := &recordingRenderer{}
	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:    entries,
		DestRoot:   dest,
		Hostname:   testHost,
		Username:   testUser,
		UIRenderer: rec,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}

	events := rec.snapshot()
	// Contract: one UIEvtProgress per file (no throttling). See rehash.go
	// emitProgress doc comment.
	if got, want := len(events), len(entries); got != want {
		t.Fatalf("events: got %d want %d (one per file): %+v", got, want, events)
	}

	var lastBytesDone int64
	var lastFilesDone int
	for i, ev := range events {
		if ev.Kind != types.UIEvtProgress {
			t.Errorf("events[%d].Kind: got %s want %s", i, ev.Kind, types.UIEvtProgress)
		}
		if string(ev.Phase) != "verify" {
			t.Errorf("events[%d].Phase: got %q want %q", i, ev.Phase, "verify")
		}
		if ev.Progress == nil {
			t.Fatalf("events[%d].Progress is nil", i)
		}
		// Counters must be monotonic non-decreasing.
		if ev.Progress.FilesDone < lastFilesDone {
			t.Errorf("events[%d].FilesDone regressed: %d < %d",
				i, ev.Progress.FilesDone, lastFilesDone)
		}
		if ev.Progress.BytesDone < lastBytesDone {
			t.Errorf("events[%d].BytesDone regressed: %d < %d",
				i, ev.Progress.BytesDone, lastBytesDone)
		}
		lastBytesDone = ev.Progress.BytesDone
		lastFilesDone = ev.Progress.FilesDone

		if ev.Progress.FilesTotal != len(entries) {
			t.Errorf("events[%d].FilesTotal: got %d want %d",
				i, ev.Progress.FilesTotal, len(entries))
		}
		if ev.Timestamp.IsZero() {
			t.Errorf("events[%d].Timestamp is zero", i)
		}
	}

	// Final event must match the run's final totals.
	last := events[len(events)-1]
	if last.Progress.FilesDone != res.FilesChecked {
		t.Errorf("final FilesDone %d != FilesChecked %d",
			last.Progress.FilesDone, res.FilesChecked)
	}
	if last.Progress.BytesDone != res.BytesRead {
		t.Errorf("final BytesDone %d != BytesRead %d",
			last.Progress.BytesDone, res.BytesRead)
	}
}

func TestRehash_CancelledMidStream(t *testing.T) {
	// Cancel mid-loop via a renderer that cancels its own ctx after the
	// first file. The next iteration's between-files ctx.Err() check
	// surfaces context.Canceled; partial Result must be returned alongside.
	parent := context.Background()
	dest := t.TempDir()

	const n = 50
	var entries []state.ManifestEntry
	for i := 0; i < n; i++ {
		payload := []byte(fmt.Sprintf("payload %d", i))
		rel := fmt.Sprintf("f%d.txt", i)
		writeNamespacedFile(t, dest, rel, payload)
		entries = append(entries, entry(rel, payload))
	}

	cctx, cancel := context.WithCancel(parent)
	defer cancel()

	cancelAfter := &cancelAfterFirstRenderer{cancel: cancel}

	res, err := rehash.Rehash(cctx, rehash.Options{
		Entries:    entries,
		DestRoot:   dest,
		Hostname:   testHost,
		Username:   testUser,
		UIRenderer: cancelAfter,
	})
	if err == nil {
		t.Fatal("expected cancellation error")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
	// Mid-loop cancel must return the partial Result so the caller does
	// not lose work already done (the alternative of returning nil would
	// silently discard the verified files already processed).
	if res == nil {
		t.Fatal("expected non-nil partial Result on mid-loop cancellation")
	}
	if res.FilesChecked == 0 {
		t.Error("FilesChecked: expected at least one file processed before cancellation")
	}
	if res.FilesChecked >= n {
		t.Errorf("FilesChecked: got %d (expected < %d; cancellation should stop the loop)",
			res.FilesChecked, n)
	}
}

// cancelAfterFirstRenderer cancels the parent ctx the first time OnEvent
// is invoked. Used by TestRehash_CancelledMidStream to construct a real
// mid-loop cancellation (as opposed to an entry-time cancel, which is
// already covered by TestRehash_CancelledAtEntry).
type cancelAfterFirstRenderer struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	tripped bool
}

func (c *cancelAfterFirstRenderer) OnEvent(_ context.Context, _ types.UIEvent) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.tripped {
		c.tripped = true
		c.cancel()
	}
	return nil
}

func TestRehash_RespectsNamespace(t *testing.T) {
	// Write files to <dest>/<paths.Prefix(host, user)>/<rel> via the same
	// helper rehash uses; confirm rehash finds them. Also write a decoy
	// at <dest>/<rel> (no namespace) and confirm rehash does NOT find it.
	ctx := context.Background()
	dest := t.TempDir()

	payload := []byte("namespaced content")
	rel := "subdir/a.txt"

	// Decoy at unnamespaced location; rehash should ignore.
	decoy := filepath.Join(dest, rel)
	if err := os.MkdirAll(filepath.Dir(decoy), 0o700); err != nil {
		t.Fatalf("mkdir decoy: %v", err)
	}
	if err := os.WriteFile(decoy, payload, 0o600); err != nil {
		t.Fatalf("write decoy: %v", err)
	}

	// Real file under namespace.
	writeNamespacedFile(t, dest, rel, payload)
	e := entry(rel, payload)

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if res.FilesVerified != 1 {
		t.Fatalf("FilesVerified: got %d want 1; result: %+v", res.FilesVerified, res)
	}

	// Now mismatch the hostname to confirm the namespace is consulted.
	resWrong, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  []state.ManifestEntry{e},
		DestRoot: dest,
		Hostname: "other-host",
		Username: "other-user",
	})
	if err != nil {
		t.Fatalf("Rehash (wrong namespace): %v", err)
	}
	if resWrong.FilesMissing != 1 {
		t.Errorf("wrong namespace: FilesMissing got %d want 1 (decoy must not satisfy verify)",
			resWrong.FilesMissing)
	}
}

func TestRehash_NilRenderer(t *testing.T) {
	ctx := context.Background()
	dest := t.TempDir()

	files := map[string][]byte{
		"a.txt": []byte("a"),
		"b.txt": []byte("bb"),
	}
	var entries []state.ManifestEntry
	for rel, payload := range files {
		writeNamespacedFile(t, dest, rel, payload)
		entries = append(entries, entry(rel, payload))
	}

	// Reference run with a recorder (counters baseline).
	rec := &recordingRenderer{}
	resRec, err := rehash.Rehash(ctx, rehash.Options{
		Entries:    entries,
		DestRoot:   dest,
		Hostname:   testHost,
		Username:   testUser,
		UIRenderer: rec,
	})
	if err != nil {
		t.Fatalf("Rehash (recorder): %v", err)
	}

	// Run with nil renderer; must not panic; counters identical.
	resNil, err := rehash.Rehash(ctx, rehash.Options{
		Entries:    entries,
		DestRoot:   dest,
		Hostname:   testHost,
		Username:   testUser,
		UIRenderer: nil,
	})
	if err != nil {
		t.Fatalf("Rehash (nil): %v", err)
	}
	if resNil.FilesChecked != resRec.FilesChecked ||
		resNil.FilesVerified != resRec.FilesVerified ||
		resNil.BytesRead != resRec.BytesRead {
		t.Errorf("nil-renderer counters diverge from recorder: nil=%+v rec=%+v",
			resNil, resRec)
	}
}

func TestRehash_EmptyDestRoot(t *testing.T) {
	_, err := rehash.Rehash(context.Background(), rehash.Options{
		Entries:  nil,
		DestRoot: "",
		Hostname: testHost,
		Username: testUser,
	})
	if err == nil {
		t.Fatal("expected error for empty DestRoot")
	}
	if !strings.Contains(err.Error(), "DestRoot") {
		t.Errorf("error: got %q want mentions DestRoot", err.Error())
	}
}

func TestRehash_CancelledAtEntry(t *testing.T) {
	cctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := rehash.Rehash(cctx, rehash.Options{
		Entries:  nil,
		DestRoot: t.TempDir(),
		Hostname: testHost,
		Username: testUser,
	})
	if err == nil {
		t.Fatal("expected cancellation at entry")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected wrapped context.Canceled, got %v", err)
	}
}

func TestRehash_RendererErrorsSwallowed(t *testing.T) {
	// PS3: a renderer that returns an error must NOT abort the run. All
	// files should still classify; the returned error must be nil.
	ctx := context.Background()
	dest := t.TempDir()
	payload := []byte("content")
	writeNamespacedFile(t, dest, "a.txt", payload)

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:    []state.ManifestEntry{entry("a.txt", payload)},
		DestRoot:   dest,
		Hostname:   testHost,
		Username:   testUser,
		UIRenderer: erroringRenderer{},
	})
	if err != nil {
		t.Fatalf("Rehash should not propagate renderer errors; got %v", err)
	}
	if res.FilesVerified != 1 {
		t.Errorf("FilesVerified: got %d want 1", res.FilesVerified)
	}
}

func TestRehash_PerFileOrder(t *testing.T) {
	// PerFile is documented as parallel to opts.Entries. Confirm the order
	// is preserved (a future map-based iteration would silently break this).
	ctx := context.Background()
	dest := t.TempDir()

	rels := []string{"z.txt", "m.txt", "a.txt"}
	var entries []state.ManifestEntry
	for _, rel := range rels {
		payload := []byte(rel) // each file's content == its rel name
		writeNamespacedFile(t, dest, rel, payload)
		entries = append(entries, entry(rel, payload))
	}

	res, err := rehash.Rehash(ctx, rehash.Options{
		Entries:  entries,
		DestRoot: dest,
		Hostname: testHost,
		Username: testUser,
	})
	if err != nil {
		t.Fatalf("Rehash: %v", err)
	}
	if len(res.PerFile) != len(rels) {
		t.Fatalf("PerFile length: got %d want %d", len(res.PerFile), len(rels))
	}
	for i, fr := range res.PerFile {
		if fr.Entry.Path != rels[i] {
			t.Errorf("PerFile[%d].Entry.Path: got %q want %q",
				i, fr.Entry.Path, rels[i])
		}
	}
}
