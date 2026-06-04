package rsync

import (
	"io"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

// ProgressKind enumerates the categories of events the Parser emits.
type ProgressKind int

// ProgressKind values.
const (
	// ProgressUnknown is the zero value; never emitted intentionally.
	ProgressUnknown ProgressKind = iota
	// ProgressFileStarted means rsync announced the next file via a filename
	// line. Path is set, byte counters are zero.
	ProgressFileStarted
	// ProgressTransferring is an in-flight percent update. The event reflects
	// the latest \r-overwritten state of the file currently being transferred.
	ProgressTransferring
	// ProgressFileCompleted is the final 100% line for a file, carrying the
	// (xfr#N, to-chk=A/B) tail.
	ProgressFileCompleted
	// ProgressSummary is the "sent X bytes  received Y bytes ..." footer.
	ProgressSummary
)

// ProgressEvent is one event produced by Parser as it consumes rsync's
// --progress output. Fields not relevant to a given Kind are left zero.
type ProgressEvent struct {
	Kind ProgressKind

	// Path is the source-relative file path for File* events, or the raw
	// summary text for ProgressSummary.
	Path string

	// BytesTransferred is the byte count rsync reported on the line.
	BytesTransferred int64
	// Percent is 0..100.
	Percent int
	// SpeedBytesPerSec is best-effort parsed from the speed unit ("MB/s",
	// "kB/s", etc.). Zero when unparseable.
	SpeedBytesPerSec int64
	// ETASeconds is best-effort parsed from H:MM:SS; zero when unparseable.
	ETASeconds int

	// FileNumber is the rsync xfr#N transfer sequence number (1-based).
	FileNumber int
	// ToCheck is the first half of to-chk=A/B (files still queued, excluding
	// the one just completed).
	ToCheck int
	// TotalFiles is the second half of to-chk=A/B (total files in the run).
	TotalFiles int
}

// Parser consumes rsync --progress output and emits ProgressEvents to its
// OnEvent handler. Designed to be slotted into Wrapper.Options.Stdout so a
// single rsync stdout stream feeds both the parser and the run log.
//
// Concurrency: Write is safe to call from one goroutine at a time. The
// mu sync.Mutex defends against accidental concurrent Writes from misuse;
// it does NOT make the parser safe for parallel goroutines feeding rsync
// chunks out of order (that would corrupt the line buffer regardless of
// locking, since rsync's stream is order-dependent).
type Parser struct {
	// PassThrough receives raw rsync stdout bytes verbatim. Typically the
	// run-log buffer. If nil, raw bytes are discarded by the parser (events
	// are still emitted). Parsing is non-destructive: every byte handed to
	// Write is forwarded to PassThrough before line classification.
	PassThrough io.Writer

	// OnEvent is invoked for each parsed event. Called from the goroutine
	// that drives Write (typically the rsync stdout-reader inside
	// exec.Cmd.Run). If nil, events are dropped.
	OnEvent func(ProgressEvent)

	mu          sync.Mutex
	buf         strings.Builder
	currentFile string
}

// progressLineRE matches one in-flight or final progress line. Examples:
//
//	"       524,288 100%  500.00MB/s    0:00:00 (xfr#1, to-chk=2/3)"
//	"        32,768  20%   30.00MB/s    0:00:01"
//
// Groups: 1=bytes (with commas), 2=percent, 3=speed (with unit/s suffix),
// 4=H:MM:SS ETA, 5=xfr#N (optional), 6=to-chk numerator (optional),
// 7=to-chk denominator (optional).
var progressLineRE = regexp.MustCompile(
	`^\s+([\d,]+)\s+(\d+)%\s+([\d.]+(?:[kMG]?B)?/s)\s+(\d+:\d+:\d+)(?:\s+\(xfr#(\d+),\s*to-chk=(\d+)/(\d+)\))?\s*$`,
)

// summaryLineRE matches the non-filename, non-progress noise lines we should
// classify either as ProgressSummary or skip outright. We treat the entire
// match prefix as a single classifier.
var summaryLineRE = regexp.MustCompile(`^(sent |total size |sending |receiving )`)

// Write implements io.Writer. Each call forwards the raw bytes to
// PassThrough (if non-nil) and then scans the accumulated buffer for
// complete lines terminated by '\r' or '\n', classifying each.
//
// Returns the number of bytes consumed (always len(b) on success) and the
// PassThrough writer's error (if any). A PassThrough error does NOT stop
// parsing; the bytes are still consumed and events still emitted, since
// failing to log raw progress shouldn't break the runner's progress UI.
func (p *Parser) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()

	var passErr error
	if p.PassThrough != nil {
		_, passErr = p.PassThrough.Write(b)
	}

	// Append to the line buffer, then drain every complete line.
	p.buf.Write(b)
	p.drainLines(false)

	return len(b), passErr
}

// Flush emits a final event for any partially-buffered tail line (one that
// ended without \r or \n). Call after the rsync subprocess exits to make
// sure a trailing summary line that lacks a newline still produces an event.
func (p *Parser) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.drainLines(true)
}

// drainLines consumes complete lines from p.buf, calling handleLine for
// each. If final is true, any remaining tail (not terminated by \r or \n)
// is also classified.
//
// Must be called with p.mu held.
func (p *Parser) drainLines(final bool) {
	s := p.buf.String()
	p.buf.Reset()

	// Walk the string, splitting on either \r or \n. We intentionally accept
	// EITHER terminator because rsync rewrites the progress line in place
	// with \r and only emits \n at file boundaries. Both terminators are
	// "end of one logical line" for our purposes.
	start := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c != '\r' && c != '\n' {
			continue
		}
		line := s[start:i]
		p.handleLine(line)
		start = i + 1
	}

	// Whatever's left after the last terminator is an incomplete line. Keep
	// it buffered for the next Write, unless this is the final flush, in
	// which case we classify it too.
	tail := s[start:]
	if final {
		if tail != "" {
			p.handleLine(tail)
		}
	} else {
		p.buf.WriteString(tail)
	}
}

// handleLine classifies one decoded line and emits zero or one event.
//
// Order matters: we try progressLineRE first (it has the most specific
// shape and would falsely match summary regex if checked second), then
// summary, then fallback to filename / blank / unknown.
func (p *Parser) handleLine(line string) {
	if line == "" {
		return
	}

	if m := progressLineRE.FindStringSubmatch(line); m != nil {
		ev := ProgressEvent{
			Path:             p.currentFile,
			BytesTransferred: parseBytes(m[1]),
			Percent:          atoi(m[2]),
			SpeedBytesPerSec: parseSpeed(m[3]),
			ETASeconds:       parseETA(m[4]),
		}

		// (xfr#N, to-chk=A/B) tail is the file-complete signal. rsync only
		// emits it on the final 100% line per file.
		if m[5] != "" {
			ev.Kind = ProgressFileCompleted
			ev.FileNumber = atoi(m[5])
			ev.ToCheck = atoi(m[6])
			ev.TotalFiles = atoi(m[7])
			p.emit(ev)
			// Clear currentFile so a later stray progress line (shouldn't
			// happen, but be defensive) doesn't attach to a finished path.
			p.currentFile = ""
			return
		}

		ev.Kind = ProgressTransferring
		p.emit(ev)
		return
	}

	if summaryLineRE.MatchString(line) {
		// "sending incremental file list" is informational, not summary; drop it.
		if strings.HasPrefix(line, "sending ") || strings.HasPrefix(line, "receiving ") {
			return
		}
		p.emit(ProgressEvent{Kind: ProgressSummary, Path: line})
		return
	}

	// Anything else: a filename. rsync writes the name flush-left with no
	// trailing whitespace. Anything with leading whitespace that didn't
	// match the progress regex is malformed; ignore it rather than treat as
	// a filename.
	if len(line) > 0 && (line[0] == ' ' || line[0] == '\t') {
		return
	}

	p.currentFile = line
	p.emit(ProgressEvent{Kind: ProgressFileStarted, Path: line})
}

// emit delivers an event to OnEvent if set.
func (p *Parser) emit(ev ProgressEvent) {
	if p.OnEvent != nil {
		p.OnEvent(ev)
	}
}

// parseBytes strips commas from "524,288" and parses to int64. Returns 0
// on parse error.
func parseBytes(s string) int64 {
	clean := strings.ReplaceAll(s, ",", "")
	n, err := strconv.ParseInt(clean, 10, 64)
	if err != nil {
		return 0
	}
	return n
}

// atoi is a non-erroring strconv.Atoi wrapper (zero on failure).
func atoi(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// parseSpeed converts an rsync speed token (e.g. "30.00MB/s", "10.00kB/s",
// "500B/s", "1.50GB/s") to bytes-per-second. Returns 0 on parse error.
//
// Units are interpreted as rsync does: power-of-2 (kB = 1024, MB = 1024^2,
// GB = 1024^3). This matches GNU rsync's --progress output convention.
func parseSpeed(s string) int64 {
	// Expected suffixes (longest first to match correctly):
	//   "kB/s", "MB/s", "GB/s", "B/s"
	// rsync 3.x always writes "/s"; if the suffix is missing, give up.
	if !strings.HasSuffix(s, "/s") {
		return 0
	}
	body := strings.TrimSuffix(s, "/s")

	var mult int64 = 1
	switch {
	case strings.HasSuffix(body, "kB"):
		mult = 1024
		body = strings.TrimSuffix(body, "kB")
	case strings.HasSuffix(body, "MB"):
		mult = 1024 * 1024
		body = strings.TrimSuffix(body, "MB")
	case strings.HasSuffix(body, "GB"):
		mult = 1024 * 1024 * 1024
		body = strings.TrimSuffix(body, "GB")
	case strings.HasSuffix(body, "B"):
		mult = 1
		body = strings.TrimSuffix(body, "B")
	}

	f, err := strconv.ParseFloat(strings.TrimSpace(body), 64)
	if err != nil {
		return 0
	}
	return int64(f * float64(mult))
}

// parseETA converts an "H:MM:SS" string to total seconds. Returns 0 on
// parse error.
func parseETA(s string) int {
	parts := strings.Split(s, ":")
	if len(parts) != 3 {
		return 0
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	sec, err3 := strconv.Atoi(parts[2])
	if err1 != nil || err2 != nil || err3 != nil {
		return 0
	}
	return h*3600 + m*60 + sec
}
