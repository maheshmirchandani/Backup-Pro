package plain

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// progressThrottle caps TTY-mode UIEvtProgress emissions to one line per
// 100ms (10 Hz). Below this rate the terminal would flicker on each rsync
// tick (the spec targets one tick per 200ms during T1, plus rehash emits
// per-file which can be much faster for many small files). The cap is per
// renderer, not per phase, so a transition from T1 to verify reuses the
// same pacing budget.
const progressThrottle = 100 * time.Millisecond

// phaseLabel maps a wire-string Phase to a human-readable label. The wire
// strings are part of the on-disk contract (invariant #13 / runner/types
// doc); the label is renderer-local and may evolve without a schema bump.
// "verify" is included even though it is not a runner Phase constant
// because internal/verify/rehash emits UIEvent with Phase="verify" (per
// rehash.go phaseWire comment / Task 31 review minor #3).
//
// An unknown phase string passes through unchanged so a future phase added
// to the wire vocabulary is visible to the operator before this map is
// updated.
func phaseLabel(p types.Phase) string {
	switch string(p) {
	case "T0":
		return "T0 preflight"
	case "T0+":
		return "T0+ enumerate"
	case "T1":
		return "T1 transfer"
	case "T2":
		return "T2 hash compare"
	case "T3":
		return "T3 delete source"
	case "T4":
		return "T4 finalize"
	case "verify":
		return "verify"
	case "":
		return "(unknown phase)"
	default:
		return string(p)
	}
}

// plainRenderer is the runner.Renderer implementation. All fields after
// construction are read-only EXCEPT lastProgressAt, which is guarded by mu.
// out writes are also guarded by mu (the renderer is contractually safe to
// call from multiple goroutines).
type plainRenderer struct {
	out   io.Writer
	isTTY bool

	mu             sync.Mutex
	lastProgressAt time.Time
	// inProgressLine is true when the last write to out was a TTY-mode
	// progress line (no trailing newline; uses leading \r to overwrite).
	// The next non-progress event prepends \n so it lands on its own line
	// instead of being concatenated to the dangling progress line.
	inProgressLine bool
}

// NewPlainRenderer returns a runner.Renderer that writes UIEvents to out as
// plain text. Pass isTTY=true for interactive terminals (rate-limited
// progress that overwrites the prior line, file_started/file_completed
// suppressed for volume) and isTTY=false for pipes/log files (every event
// becomes one line; progress is dropped to avoid log spam).
//
// A nil out panics here at construction time rather than later inside
// OnEvent. A nil writer is always a caller bug; panicking at construction
// surfaces it in the line that built the wrong renderer rather than the
// first event later in the run.
func NewPlainRenderer(out io.Writer, isTTY bool) types.Renderer {
	if out == nil {
		panic("plain.NewPlainRenderer: nil io.Writer")
	}
	return &plainRenderer{out: out, isTTY: isTTY}
}

// OnEvent dispatches a UIEvent to the appropriate handler. Per PS3 the
// renderer is fail-open on unknown event kinds (writes a fallback line,
// returns nil). io.Writer errors ARE returned to the caller wrapped: the
// runner swallows them via emitUI but cmd may want to surface a broken-
// terminal condition to the operator.
//
// Concurrency: all writes are serialized through r.mu so per-line atomicity
// holds even when emit sites race (the rsync parser callback in T1 runs in
// parallel with the orchestrator's phase events).
func (r *plainRenderer) OnEvent(_ context.Context, ev types.UIEvent) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	switch ev.Kind {
	case types.UIEvtPhaseStarted:
		return r.writePhaseStarted(ev)
	case types.UIEvtPhaseCompleted:
		return r.writePhaseCompleted(ev)
	case types.UIEvtFileStarted:
		return r.writeFileStarted(ev)
	case types.UIEvtFileCompleted:
		return r.writeFileCompleted(ev)
	case types.UIEvtFileFailed:
		return r.writeFileFailed(ev)
	case types.UIEvtProgress:
		return r.writeProgress(ev)
	case types.UIEvtPrompt:
		return r.writePrompt(ev)
	case types.UIEvtSummary:
		return r.writeSummary(ev)
	default:
		return r.writeUnknown(ev)
	}
}

// writeLine emits a complete line, breaking out of any dangling progress
// line first. Caller must hold r.mu.
func (r *plainRenderer) writeLine(s string) error {
	prefix := ""
	if r.inProgressLine {
		// Terminate the dangling progress line with a newline so the
		// upcoming event doesn't concatenate onto it.
		prefix = "\n"
		r.inProgressLine = false
	}
	if _, err := fmt.Fprint(r.out, prefix+s+"\n"); err != nil {
		return fmt.Errorf("plain renderer: write: %w", err)
	}
	return nil
}

func (r *plainRenderer) writePhaseStarted(ev types.UIEvent) error {
	return r.writeLine(fmt.Sprintf("=> %s starting", phaseLabel(ev.Phase)))
}

func (r *plainRenderer) writePhaseCompleted(ev types.UIEvent) error {
	label := phaseLabel(ev.Phase)
	switch ev.Status {
	case "ok":
		// duration_ms is not on UIEvent today; the audit log captures
		// duration. Keep the renderer line shape stable so the future
		// addition is non-breaking ("OK <phase> (<ms>ms)" extension point).
		return r.writeLine(fmt.Sprintf("OK %s", label))
	case "aborted":
		errStr := ""
		if ev.Err != nil {
			errStr = ev.Err.Error()
		}
		if errStr == "" {
			return r.writeLine(fmt.Sprintf("!! %s aborted", label))
		}
		return r.writeLine(fmt.Sprintf("!! %s aborted: %s", label, errStr))
	case "skipped":
		return r.writeLine(fmt.Sprintf("-- %s skipped", label))
	default:
		// Any other Status string flows through with the raw value; the
		// caller has emitted a phase-completion shape the renderer does
		// not classify (forward-compat with future Status values).
		return r.writeLine(fmt.Sprintf("** %s %s", label, ev.Status))
	}
}

func (r *plainRenderer) writeFileStarted(ev types.UIEvent) error {
	// TTY mode suppresses per-file starts; the progress line is the
	// operator's signal that work is happening, and per-file lines at
	// 50/sec scroll faster than the terminal can render.
	if r.isTTY {
		return nil
	}
	return r.writeLine(fmt.Sprintf("   start %s", ev.Path))
}

func (r *plainRenderer) writeFileCompleted(ev types.UIEvent) error {
	if r.isTTY {
		return nil
	}
	// Status is one of the FileStatus / DeletionStatus wire strings the
	// runner classifies (verified, hash_mismatch, source_mutated, deleted,
	// etc.). Surface it so the operator can grep a non-TTY log for
	// non-verified outcomes.
	if ev.Status != "" {
		return r.writeLine(fmt.Sprintf("   OK %s (%s)", ev.Path, ev.Status))
	}
	return r.writeLine(fmt.Sprintf("   OK %s", ev.Path))
}

func (r *plainRenderer) writeFileFailed(ev types.UIEvent) error {
	// Failures always emit (both modes). A silent failure here would mean
	// the operator only learns about per-file problems from the final
	// summary, which is too late for cancel-on-first-error workflows.
	errStr := ""
	if ev.Err != nil {
		errStr = ev.Err.Error()
	}
	if errStr == "" {
		return r.writeLine(fmt.Sprintf("   !! %s", ev.Path))
	}
	return r.writeLine(fmt.Sprintf("   !! %s: %s", ev.Path, errStr))
}

func (r *plainRenderer) writeProgress(ev types.UIEvent) error {
	// Non-TTY: drop. The audit log is the durable record; pipe-spamming
	// progress at 50 ticks/sec would balloon log files for no UX benefit.
	if !r.isTTY {
		return nil
	}
	if ev.Progress == nil {
		// Defensive: a progress event without ProgressInfo is a caller
		// bug. Drop silently rather than printing an empty bar.
		return nil
	}
	// Throttle. The runner's emit sites can fire hundreds of times per
	// second on small-file workloads; rendering each one makes the
	// terminal flicker and burns CPU on string formatting.
	now := time.Now()
	if !r.lastProgressAt.IsZero() && now.Sub(r.lastProgressAt) < progressThrottle {
		return nil
	}
	r.lastProgressAt = now

	pct := percent(ev.Progress.BytesDone, ev.Progress.BytesTotal)
	speed := formatSpeed(ev.Progress.BytesPerSec)
	// \r returns the cursor to column zero so the next progress line
	// overwrites this one. No \n: the line stays "open" until a non-
	// progress event flushes it (writeLine prepends \n in that case).
	line := fmt.Sprintf("\r%3d%% (%d/%d) %s %s",
		pct, ev.Progress.FilesDone, ev.Progress.FilesTotal,
		ev.Progress.CurrentFile, speed)
	if _, err := fmt.Fprint(r.out, line); err != nil {
		return fmt.Errorf("plain renderer: write progress: %w", err)
	}
	r.inProgressLine = true
	return nil
}

func (r *plainRenderer) writePrompt(ev types.UIEvent) error {
	// Prompts have no trailing newline: cmd reads stdin immediately after.
	// Status carries the prompt text per the UIEvent contract in types.go.
	prefix := ""
	if r.inProgressLine {
		prefix = "\n"
		r.inProgressLine = false
	}
	if _, err := fmt.Fprint(r.out, prefix+ev.Status+" "); err != nil {
		return fmt.Errorf("plain renderer: write prompt: %w", err)
	}
	return nil
}

func (r *plainRenderer) writeSummary(ev types.UIEvent) error {
	// Three-part block per spec section 6 (what / where / next step). The
	// "what" is the exit status, "where" is the run dir (carried via cmd
	// in v0.1; for now Status alone is the durable identifier), "next
	// step" depends on exit status. Keep this block stable: external
	// tooling may grep for the "exit status:" prefix to classify runs.
	prefix := ""
	if r.inProgressLine {
		prefix = "\n"
		r.inProgressLine = false
	}
	// Single fmt.Fprintf so the whole block lands as one write call;
	// holding mu prevents interleaving with other emitters even if the
	// underlying Writer is itself unbuffered.
	block := prefix +
		"\n" +
		"Run complete.\n" +
		fmt.Sprintf("  exit status: %s\n", statusOrUnknown(ev.Status)) +
		fmt.Sprintf("  finished at: %s\n", ev.Timestamp.Format(time.RFC3339)) +
		"  details: see <USB>/.flashbackup/runs/<RunID>/events.ndjson\n"
	if _, err := fmt.Fprint(r.out, block); err != nil {
		return fmt.Errorf("plain renderer: write summary: %w", err)
	}
	return nil
}

func (r *plainRenderer) writeUnknown(ev types.UIEvent) error {
	// PS3 fail-open: an unknown kind must not crash or abort the run.
	// Surface enough detail that a future maintainer can find the missing
	// case statement above (Kind + Phase + Path).
	return r.writeLine(fmt.Sprintf("?? %s phase=%s path=%s status=%s",
		ev.Kind, ev.Phase, ev.Path, ev.Status))
}

// percent returns the integer percentage of done/total, clamped to [0, 100].
// total == 0 yields 0 (avoids divide-by-zero and a "100%" report for an
// empty run, which would be misleading).
func percent(done, total int64) int {
	if total <= 0 {
		return 0
	}
	p := (done * 100) / total
	switch {
	case p < 0:
		return 0
	case p > 100:
		return 100
	default:
		return int(p)
	}
}

// formatSpeed renders a bytes-per-second rate in human-readable units. The
// units cap at GiB/s; anything faster is unlikely and a TiB/s line would
// suggest a counter bug rather than real throughput.
func formatSpeed(bps int64) string {
	const (
		kib = 1024
		mib = 1024 * 1024
		gib = 1024 * 1024 * 1024
	)
	switch {
	case bps <= 0:
		return ""
	case bps >= gib:
		return fmt.Sprintf("%.1f GiB/s", float64(bps)/float64(gib))
	case bps >= mib:
		return fmt.Sprintf("%.1f MiB/s", float64(bps)/float64(mib))
	case bps >= kib:
		return fmt.Sprintf("%.1f KiB/s", float64(bps)/float64(kib))
	default:
		return fmt.Sprintf("%d B/s", bps)
	}
}

// statusOrUnknown returns s, substituting a placeholder for the empty
// string. UIEvtSummary always carries an ExitStatus when emitted by the
// runner, but defensive code here keeps the summary block readable if a
// future caller forgets to populate it.
func statusOrUnknown(s string) string {
	if s == "" {
		return "(unknown)"
	}
	return s
}
