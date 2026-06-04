package rsync

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// goldenPath is the pinned --progress capture for invariant #43. Upgrading
// the embedded rsync to a version whose --progress format changes MUST
// break TestParser_ContractGoldenFile loudly so we notice.
const goldenPath = "testdata/rsync-3.4.1-progress.golden"

// collect runs the parser against input (fed as one chunk) and returns the
// emitted events in order.
func collect(t *testing.T, input []byte) []ProgressEvent {
	t.Helper()
	var events []ProgressEvent
	p := &Parser{
		OnEvent: func(ev ProgressEvent) { events = append(events, ev) },
	}
	if _, err := p.Write(input); err != nil {
		t.Fatalf("Write returned err: %v", err)
	}
	p.Flush()
	return events
}

// collectByteByByte feeds input one byte at a time to prove buffering works
// across arbitrary chunk boundaries.
func collectByteByByte(t *testing.T, input []byte) []ProgressEvent {
	t.Helper()
	var events []ProgressEvent
	p := &Parser{
		OnEvent: func(ev ProgressEvent) { events = append(events, ev) },
	}
	for i := 0; i < len(input); i++ {
		if _, err := p.Write(input[i : i+1]); err != nil {
			t.Fatalf("Write byte %d returned err: %v", i, err)
		}
	}
	p.Flush()
	return events
}

func loadGolden(t *testing.T) []byte {
	t.Helper()
	abs, err := filepath.Abs(goldenPath)
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		t.Fatalf("read golden %s: %v", abs, err)
	}
	return data
}

// expectedFromGolden returns the canonical event sequence we expect from
// the golden file. Kept as a separate function so byte-by-byte and
// happy-path tests can compare against the same source of truth.
func expectedFromGolden() []ProgressEvent {
	return []ProgressEvent{
		{Kind: ProgressFileStarted, Path: "Documents/foo.pdf"},
		{
			Kind:             ProgressFileCompleted,
			Path:             "Documents/foo.pdf",
			BytesTransferred: 524288,
			Percent:          100,
			SpeedBytesPerSec: int64(500.00 * 1024 * 1024),
			ETASeconds:       0,
			FileNumber:       1,
			ToCheck:          2,
			TotalFiles:       3,
		},
		{Kind: ProgressFileStarted, Path: "Documents/bar.docx"},
		{
			Kind:             ProgressTransferring,
			Path:             "Documents/bar.docx",
			BytesTransferred: 32768,
			Percent:          20,
			SpeedBytesPerSec: int64(30.00 * 1024 * 1024),
			ETASeconds:       1,
		},
		{
			Kind:             ProgressFileCompleted,
			Path:             "Documents/bar.docx",
			BytesTransferred: 163840,
			Percent:          100,
			SpeedBytesPerSec: int64(150.00 * 1024 * 1024),
			ETASeconds:       0,
			FileNumber:       2,
			ToCheck:          1,
			TotalFiles:       3,
		},
		{Kind: ProgressFileStarted, Path: "Documents/baz.md"},
		{
			Kind:             ProgressFileCompleted,
			Path:             "Documents/baz.md",
			BytesTransferred: 512,
			Percent:          100,
			SpeedBytesPerSec: int64(10.00 * 1024),
			ETASeconds:       0,
			FileNumber:       3,
			ToCheck:          0,
			TotalFiles:       3,
		},
		{Kind: ProgressSummary, Path: "sent 720,896 bytes  received 67 bytes  720,963.00 bytes/sec"},
		{Kind: ProgressSummary, Path: "total size is 720,640  speedup is 1.00"},
	}
}

func eventsEqual(a, b ProgressEvent) bool {
	return a == b
}

func compareEventSlices(t *testing.T, got, want []ProgressEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("event count: got %d want %d\n got=%+v\nwant=%+v", len(got), len(want), got, want)
	}
	for i := range got {
		if !eventsEqual(got[i], want[i]) {
			t.Errorf("event %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestParser_HappyPath(t *testing.T) {
	input := loadGolden(t)
	got := collect(t, input)
	compareEventSlices(t, got, expectedFromGolden())
}

func TestParser_ByteByByte(t *testing.T) {
	input := loadGolden(t)
	got := collectByteByByte(t, input)
	compareEventSlices(t, got, expectedFromGolden())
}

func TestParser_CarriageReturnOverwrites(t *testing.T) {
	// One filename, one \r-terminated mid-transfer line, then a \n-terminated
	// 100% line with the xfr tail. Expect 3 events in order:
	//   FileStarted("file.txt"), Transferring(50%), FileCompleted(100%, xfr#1).
	// The trailing \n after the 100% line is just a terminator; no extra event.
	input := []byte("file.txt\n" +
		"       1000  50% 1MB/s 0:00:00\r" +
		"       2000 100% 2MB/s 0:00:00 (xfr#1, to-chk=0/1)\n")
	got := collect(t, input)

	if len(got) != 3 {
		t.Fatalf("expected 3 events, got %d: %+v", len(got), got)
	}
	if got[0].Kind != ProgressFileStarted || got[0].Path != "file.txt" {
		t.Errorf("event 0: want FileStarted file.txt, got %+v", got[0])
	}
	if got[1].Kind != ProgressTransferring || got[1].Percent != 50 {
		t.Errorf("event 1: want Transferring 50%%, got %+v", got[1])
	}
	if got[2].Kind != ProgressFileCompleted || got[2].Percent != 100 || got[2].FileNumber != 1 {
		t.Errorf("event 2: want FileCompleted 100%% xfr#1, got %+v", got[2])
	}
}

func TestParser_SpeedUnits(t *testing.T) {
	cases := []struct {
		line string
		want int64
	}{
		{"        1000 100%  500B/s    0:00:00", 500},
		{"        1000 100%   10.00kB/s    0:00:00", int64(10.00 * 1024)},
		{"        1000 100%   30.00MB/s    0:00:00", int64(30.00 * 1024 * 1024)},
		{"        1000 100%    1.50GB/s    0:00:00", int64(1.50 * 1024 * 1024 * 1024)},
	}
	for _, tc := range cases {
		t.Run(tc.line, func(t *testing.T) {
			// Prepend a filename so the parser has currentFile and the
			// 100% (no xfr tail) line classifies as Transferring.
			input := []byte("dummy.bin\n" + tc.line + "\n")
			events := collect(t, input)
			// Expect FileStarted + Transferring (no xfr tail = not completed).
			if len(events) != 2 {
				t.Fatalf("expected 2 events, got %d: %+v", len(events), events)
			}
			ev := events[1]
			if ev.Kind != ProgressTransferring {
				t.Fatalf("expected Transferring, got kind=%d %+v", ev.Kind, ev)
			}
			if ev.SpeedBytesPerSec != tc.want {
				t.Errorf("speed: got %d want %d (line %q)", ev.SpeedBytesPerSec, tc.want, tc.line)
			}
		})
	}
}

func TestParser_PassThrough(t *testing.T) {
	input := loadGolden(t)
	var sink bytes.Buffer
	p := &Parser{PassThrough: &sink}
	if _, err := p.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.Flush()
	if !bytes.Equal(sink.Bytes(), input) {
		t.Errorf("PassThrough did not receive identical bytes:\n got %q\nwant %q", sink.Bytes(), input)
	}
}

func TestParser_PassThrough_ByteByByte(t *testing.T) {
	input := loadGolden(t)
	var sink bytes.Buffer
	p := &Parser{PassThrough: &sink}
	for i := 0; i < len(input); i++ {
		if _, err := p.Write(input[i : i+1]); err != nil {
			t.Fatalf("Write byte %d: %v", i, err)
		}
	}
	p.Flush()
	if !bytes.Equal(sink.Bytes(), input) {
		t.Errorf("PassThrough byte-by-byte mismatch")
	}
}

func TestParser_Flush(t *testing.T) {
	// Feed a complete filename + a partial summary line without trailing
	// newline. Without Flush, the summary should NOT be emitted yet.
	input := []byte("README.md\nsent 100 bytes  received 5 bytes  105 bytes/sec")
	var events []ProgressEvent
	p := &Parser{
		OnEvent: func(ev ProgressEvent) { events = append(events, ev) },
	}
	if _, err := p.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("pre-flush: expected 1 event (just FileStarted), got %d: %+v", len(events), events)
	}
	p.Flush()
	if len(events) != 2 {
		t.Fatalf("post-flush: expected 2 events, got %d: %+v", len(events), events)
	}
	if events[1].Kind != ProgressSummary {
		t.Errorf("flushed event: want Summary, got %+v", events[1])
	}
}

func TestParser_ContractGoldenFile(t *testing.T) {
	// Invariant #43: pinned-format contract test. Upgrading the embedded
	// rsync to a version that changes --progress output MUST break this
	// test loudly. The expected counts below are pinned to GNU rsync 3.4.1.
	input := loadGolden(t)
	got := collect(t, input)

	counts := map[ProgressKind]int{}
	for _, ev := range got {
		counts[ev.Kind]++
	}

	type kc struct {
		kind ProgressKind
		want int
		name string
	}
	want := []kc{
		{ProgressFileStarted, 3, "FileStarted"},
		{ProgressTransferring, 1, "Transferring"},
		{ProgressFileCompleted, 3, "FileCompleted"},
		{ProgressSummary, 2, "Summary"},
	}
	for _, w := range want {
		if counts[w.kind] != w.want {
			t.Errorf("kind %s: got %d want %d (full counts: %+v)", w.name, counts[w.kind], w.want, counts)
		}
	}
	if counts[ProgressUnknown] != 0 {
		t.Errorf("ProgressUnknown should never be emitted, got %d", counts[ProgressUnknown])
	}
}

func TestParser_NoOnEvent(t *testing.T) {
	// Sanity: a Parser with neither OnEvent nor PassThrough should accept
	// input and not panic.
	p := &Parser{}
	input := loadGolden(t)
	if _, err := p.Write(input); err != nil {
		t.Fatalf("Write: %v", err)
	}
	p.Flush()
}

func TestParser_PassThroughError(t *testing.T) {
	// A PassThrough that errors should still let parsing complete and
	// events fire. We use a writer that always errors after the first
	// byte to simulate a partially-broken downstream.
	var events []ProgressEvent
	p := &Parser{
		PassThrough: errWriter{},
		OnEvent:     func(ev ProgressEvent) { events = append(events, ev) },
	}
	input := loadGolden(t)
	n, err := p.Write(input)
	if n != len(input) {
		t.Errorf("Write n: got %d want %d", n, len(input))
	}
	if err == nil {
		t.Errorf("Write err: want non-nil from PassThrough, got nil")
	}
	p.Flush()
	// Parsing should still have produced its full event sequence.
	compareEventSlices(t, events, expectedFromGolden())
}

type errWriter struct{}

func (errWriter) Write(p []byte) (int, error) {
	return 0, errSentinel
}

var errSentinel = errors.New("synthetic passthrough failure")

func TestParseSpeed_Edges(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"", 0},
		{"30.00MB/s", 30 * 1024 * 1024},
		{"30.00MB", 0},    // missing /s suffix => unparseable
		{"abc/s", 0},      // garbage body
		{"1024B/s", 1024}, // plain B/s
		{"0.5GB/s", 1024 * 1024 * 512},
	}
	for _, tc := range cases {
		if got := parseSpeed(tc.in); got != tc.want {
			t.Errorf("parseSpeed(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseETA_Edges(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"", 0},
		{"0:00:00", 0},
		{"0:00:30", 30},
		{"0:02:00", 120},
		{"1:00:00", 3600},
		{"2:30:15", 2*3600 + 30*60 + 15},
		{"junk", 0},
		{"1:2", 0}, // wrong number of parts
	}
	for _, tc := range cases {
		if got := parseETA(tc.in); got != tc.want {
			t.Errorf("parseETA(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}

func TestParseBytes_Edges(t *testing.T) {
	cases := []struct {
		in   string
		want int64
	}{
		{"0", 0},
		{"100", 100},
		{"1,000", 1000},
		{"1,234,567", 1234567},
		{"", 0},
		{"abc", 0},
	}
	for _, tc := range cases {
		if got := parseBytes(tc.in); got != tc.want {
			t.Errorf("parseBytes(%q) = %d, want %d", tc.in, got, tc.want)
		}
	}
}
