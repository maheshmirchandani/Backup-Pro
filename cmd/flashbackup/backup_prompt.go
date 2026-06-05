package main

// backup_prompt.go owns the move-mode "Type DELETE" confirmation gate
// (Task 37, AC-7 + AC-8). Lives in its own file because:
//
//   1. backup.go is already at the 200-line file budget and the prompt
//      flow (warning composition, stdin read, exact-match compare, error
//      taxonomy) is its own concern from the argv-parse / runner-invoke
//      pipeline.
//   2. The decision-lock from the implementer brief is that the prompt
//      text lives in the cmd layer, NOT in the renderer. A separate file
//      makes that ownership visible to any future task author who might
//      otherwise add another prompt-style UX in the renderer.
//
// Design decisions baked in here (locked):
//
//   - UIEvent path: the prompt warning + prompt-token text is composed in
//     cmd and emitted via a single UIEvent{Kind:UIEvtPrompt} through the
//     renderer. ev.Status carries the operator-visible text; ev.Path
//     carries the literal token ("DELETE") the operator must type. The
//     renderer's writePrompt already writes ev.Status + a trailing space
//     with no newline, so the operator's typed line begins immediately
//     after the prompt. This matches the brief's literal reading
//     ("UIRenderer emits UIEvent{Kind:UIEvtPrompt,Path:"DELETE"}") while
//     leaving the wording in cmd-side control.
//
//   - Stdin read: bufio.Scanner with default newline split. One line read,
//     no peek-ahead, no retry. The Scanner's buffer is the stdlib default
//     (64 KiB) which is multiple orders of magnitude beyond what a typo
//     could plausibly produce; a pathological pasted blob just gets
//     truncated and fails the exact-match compare, which is the desired
//     "anything other than DELETE aborts" behaviour.
//
//   - Exact-match: case-sensitive byte-equality against "DELETE". No
//     trim, no normalise, no tolerance. "delete", "DELETE ", " DELETE",
//     "DELETE\t", "Delete" all decline. The friction is the feature
//     (design spec section 4: "Against muscle-memory acceptance").
//
//   - Ctx awareness: bufio.Scanner does not honour context cancellation
//     directly (it blocks on the underlying io.Reader's Read). For the
//     bytes.Buffer case (tests), ctx is checked once before the read so a
//     pre-cancelled ctx is honoured. For the real os.Stdin TTY case, a
//     first SIGINT does NOT interrupt the read syscall (macOS restarts
//     the read via ERESTART); the operator must hit Enter to exit the
//     prompt, or send a second SIGINT within 5s to trigger the
//     installSignalHandlers force-exit in main. The first-SIGINT escape
//     valve is the second-signal handler, not the prompt itself.
//     Refined 2026-06-05 per Task 37 review M2.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

// deleteToken is the exact literal the operator must type to confirm move
// mode. Case-sensitive byte equality; no trim, no normalize. Defined here
// (not on the UIEvent) so the cmd-side comparison is the single source of
// truth; the renderer never reads it.
const deleteToken = "DELETE"

// errDeleteAborted is returned by promptDeleteConfirm when the operator
// declines the move-mode prompt (typed anything other than the literal
// "DELETE" token, including empty input, lowercase, or any surrounding
// whitespace). Sentinel so callers can distinguish "declined" (exit 2,
// operator-fixable) from "read error" (exit 1, runtime failure).
var errDeleteAborted = errors.New("DELETE confirmation declined")

// promptDeleteConfirm renders the move-mode warning + prompt through the
// supplied Renderer, then reads exactly one line from in and returns nil
// iff the line equals "DELETE" byte-for-byte (no trim, no case-fold, no
// trailing whitespace tolerance).
//
// Returns:
//   - nil                       on exact "DELETE" match (proceed with ModeMove).
//   - errDeleteAborted          on any non-matching line (exit 2, operator
//     declined; verified copies stay at destination
//     per design spec section 4
//     "copy_only_aborted_delete").
//   - io.ErrUnexpectedEOF       on EOF before any line is read (scripted
//     invocation piped nothing; fail loud, not
//     silently proceed).
//   - ctx.Err()                 if ctx is already done when called.
//   - the renderer's error      if the UIEvent fan-out fails (rare; the
//     plain renderer writes to its Writer which
//     is typically a buffered stdout).
//
// ctx is checked once before the render+read so a pre-cancelled ctx
// short-circuits without printing the warning. The read itself is not
// cancellable (see file-header decision on ctx awareness); a SIGINT
// mid-read interrupts the syscall and surfaces as io.ErrUnexpectedEOF
// which the caller maps to exit 1.
func promptDeleteConfirm(ctx context.Context, renderer types.Renderer, in io.Reader) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	// Compose the warning + prompt in ev.Status; ev.Path carries the
	// Status carries the multi-line warning; the renderer's writePrompt
	// handler writes ev.Status + " " with no trailing newline so the
	// operator types right after the prompt. The expected token is the
	// deleteToken constant below (was previously stashed in ev.Path; per
	// Task 37 review M4 we promoted it to a named const so the
	// cmd-side comparison has a clear single source of truth that does
	// not rely on the UIEvent contract.).
	warning := "WARNING: move mode will PERMANENTLY DELETE source files after they verify on the USB.\n" +
		"Files that fail verification are NOT deleted (the atomic gate protects you).\n" +
		"\n" +
		"Type DELETE (exact case) to proceed, anything else to abort:"
	ev := types.UIEvent{
		Kind:   types.UIEvtPrompt,
		Status: warning,
	}
	if err := renderer.OnEvent(ctx, ev); err != nil {
		return fmt.Errorf("render move-mode prompt: %w", err)
	}

	// Read a single line. bufio.Scanner's default split is ScanLines which
	// strips the trailing "\n" or "\r\n" from the returned token; we
	// compare the post-strip token against "DELETE" so a literal
	// "DELETE\n" on stdin is the accept path.
	scanner := bufio.NewScanner(in)
	if !scanner.Scan() {
		// EOF before any line was read. Could also be a Scanner.Err() if
		// the underlying reader returned a non-EOF error; we surface that
		// distinctly so the caller can log the real cause.
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("read DELETE confirmation: %w", err)
		}
		return io.ErrUnexpectedEOF
	}

	got := scanner.Text()
	if got != deleteToken {
		// Decline path: anything other than exact "DELETE". Includes
		// empty (""), lowercase ("delete"), typos ("DELET"), trailing
		// whitespace ("DELETE "), leading whitespace (" DELETE"), and
		// any pasted garbage. The friction is the feature.
		return errDeleteAborted
	}
	return nil
}
