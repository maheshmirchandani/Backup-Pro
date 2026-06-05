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
//     real os.Stdin case, a SIGINT during the read would interrupt the
//     read syscall and the Scanner returns false with no token; we
//     surface that as io.ErrUnexpectedEOF (the operator hit Ctrl-C
//     before typing). For the bytes.Buffer case (tests), the Scanner
//     reads synchronously without blocking; ctx is checked once before
//     the read so a pre-cancelled ctx is honoured but the actual read
//     proceeds without ctx integration. This is a deliberate punt:
//     wrapping os.Stdin in a goroutine-driven cancellable reader is more
//     complexity than the cmd-layer interaction warrants, and the real
//     blast radius of "operator started typing then sent SIGINT" is the
//     read-syscall interruption path which already works correctly.

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner/types"
)

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
	// literal expected token as documentation (the renderer ignores it,
	// but the brief calls for Path:"DELETE" and a future renderer that
	// wants to highlight the token can read it from there).
	warning := "WARNING: move mode will PERMANENTLY DELETE source files after they verify on the USB.\n" +
		"Files that fail verification are NOT deleted (the atomic gate protects you).\n" +
		"\n" +
		"Type DELETE (exact case) to proceed, anything else to abort:"
	ev := types.UIEvent{
		Kind:   types.UIEvtPrompt,
		Path:   "DELETE",
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
	if got != ev.Path {
		// Decline path: anything other than exact "DELETE". Includes
		// empty (""), lowercase ("delete"), typos ("DELET"), trailing
		// whitespace ("DELETE "), leading whitespace (" DELETE"), and
		// any pasted garbage. The friction is the feature.
		return errDeleteAborted
	}
	return nil
}
