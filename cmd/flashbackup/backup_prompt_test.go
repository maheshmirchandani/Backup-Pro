package main

// backup_prompt_test.go covers the move-mode DELETE confirmation gate
// (Task 37, AC-7 + AC-8). The unit tests below drive promptDeleteConfirm
// directly with a captured renderer + a bytes.Buffer stdin so we can
// exhaustively cover the case-sensitivity / whitespace / EOF / declined
// branches without standing up a USB volume.
//
// E2E coverage (move actually deletes source files, decline leaves them
// alone) lives in TestBackup_MoveMode_HappyPath and
// TestBackup_MoveMode_Declined in backup_test.go where a real DMG is
// mounted and the runner is exercised end-to-end.

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/maheshmirchandani/Backup-Pro/internal/plain"
)

// TestPromptDeleteConfirm_AcceptsExactDELETE: the happy path. stdin
// carries exactly "DELETE\n"; the function returns nil and the renderer
// has written the warning + prompt block.
func TestPromptDeleteConfirm_AcceptsExactDELETE(t *testing.T) {
	var out bytes.Buffer
	renderer := plain.NewPlainRenderer(&out, false)
	in := bytes.NewBufferString("DELETE\n")

	if err := promptDeleteConfirm(context.Background(), renderer, in); err != nil {
		t.Errorf("expected nil error on exact DELETE, got %v", err)
	}
	if !strings.Contains(out.String(), "Type DELETE") {
		t.Errorf("renderer output should include the prompt line, got %q", out.String())
	}
	if !strings.Contains(out.String(), "PERMANENTLY DELETE") {
		t.Errorf("renderer output should include the warning, got %q", out.String())
	}
}

// TestPromptDeleteConfirm_TableDecline: every variant that is NOT the
// exact literal "DELETE" must return errDeleteAborted. The cases include
// the four common operator near-misses (lowercase, typo, empty, trailing
// whitespace) plus the more pathological ones (leading whitespace, mixed
// case, the word "yes", a paste accident).
func TestPromptDeleteConfirm_TableDecline(t *testing.T) {
	cases := []struct {
		name  string
		stdin string
	}{
		{"lowercase", "delete\n"},
		{"typo missing E", "DELET\n"},
		{"typo extra E", "DELETEE\n"},
		{"empty line", "\n"},
		{"trailing space", "DELETE \n"},
		{"trailing tab", "DELETE\t\n"},
		{"leading space", " DELETE\n"},
		{"mixed case", "Delete\n"},
		{"the word yes", "yes\n"},
		{"the word y", "y\n"},
		{"paste accident", "pbpaste DELETE\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var out bytes.Buffer
			renderer := plain.NewPlainRenderer(&out, false)
			in := bytes.NewBufferString(tc.stdin)

			err := promptDeleteConfirm(context.Background(), renderer, in)
			if !errors.Is(err, errDeleteAborted) {
				t.Errorf("stdin %q: expected errDeleteAborted, got %v", tc.stdin, err)
			}
		})
	}
}

// TestPromptDeleteConfirm_EOFBeforeLine: scripted invocations that pipe
// nothing to stdin must fail loud (io.ErrUnexpectedEOF), not silently
// proceed. Maps to runtime exit (1) in the cmd-level translator.
func TestPromptDeleteConfirm_EOFBeforeLine(t *testing.T) {
	var out bytes.Buffer
	renderer := plain.NewPlainRenderer(&out, false)
	in := bytes.NewBufferString("") // EOF immediately

	err := promptDeleteConfirm(context.Background(), renderer, in)
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Errorf("expected io.ErrUnexpectedEOF on empty stdin, got %v", err)
	}
}

// TestPromptDeleteConfirm_PreCancelledCtx: a ctx that is already done
// when the function is called must return ctx.Err() WITHOUT printing the
// warning. Guards against a SIGINT during argv parse from spawning a
// confirmation prompt the operator no longer wants to answer.
func TestPromptDeleteConfirm_PreCancelledCtx(t *testing.T) {
	var out bytes.Buffer
	renderer := plain.NewPlainRenderer(&out, false)
	in := bytes.NewBufferString("DELETE\n")

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // pre-cancelled
	err := promptDeleteConfirm(ctx, renderer, in)
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled from pre-cancelled ctx, got %v", err)
	}
	if out.Len() != 0 {
		t.Errorf("pre-cancelled ctx should not print the prompt, got %q", out.String())
	}
}

// TestPromptDeleteConfirm_PrintsFullWarning: assert the warning block
// contains the three key signals the operator needs (1) permanent
// deletion warning, (2) atomic-gate reassurance, (3) the exact token to
// type. Exact-byte comparison would be fragile across cosmetic wording
// edits; the three substring checks are the durable contract.
func TestPromptDeleteConfirm_PrintsFullWarning(t *testing.T) {
	var out bytes.Buffer
	renderer := plain.NewPlainRenderer(&out, false)
	in := bytes.NewBufferString("DELETE\n")

	if err := promptDeleteConfirm(context.Background(), renderer, in); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	got := out.String()
	for _, want := range []string{
		"WARNING",
		"PERMANENTLY DELETE",
		"atomic gate",
		"Type DELETE",
		"exact case",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("warning missing %q substring; got %q", want, got)
		}
	}
}

// TestPromptDeleteConfirm_NoTrailingNewlineOnPrompt: the renderer's
// writePrompt contract is that the prompt ends without a newline so the
// operator's typed line begins immediately after the prompt. Confirm the
// last character written is a space (the renderer appends " " after
// ev.Status), not a newline.
func TestPromptDeleteConfirm_NoTrailingNewlineOnPrompt(t *testing.T) {
	var out bytes.Buffer
	renderer := plain.NewPlainRenderer(&out, false)
	in := bytes.NewBufferString("DELETE\n")

	if err := promptDeleteConfirm(context.Background(), renderer, in); err != nil {
		t.Fatalf("happy path failed: %v", err)
	}
	got := out.String()
	if got == "" {
		t.Fatal("renderer wrote nothing")
	}
	if last := got[len(got)-1]; last != ' ' {
		t.Errorf("last byte should be a space (renderer appends one after ev.Status), got %q", last)
	}
}
