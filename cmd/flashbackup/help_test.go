package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// callHelp is the test-local convenience wrapper around runHelp. Keeps each
// case in this file from having to spin up its own bytes.Buffer trio; the
// signature mirrors runCapture in main_test.go but targets runHelp
// directly rather than the dispatcher (the dispatcher path is exercised by
// TestRun_HelpSubcommand in main_test.go).
func callHelp(t *testing.T, argv []string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runHelp(context.Background(), argv, bytes.NewBufferString(""), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// TestHelp_NoArgs_PrintsTopLevel: runHelp with no argv prints the
// constants-table empty-key entry (the top-level usage screen) to stdout
// and returns exit 0. This is the same body that printUsage now emits, so a
// regression that breaks one will break the other (intentional coupling).
func TestHelp_NoArgs_PrintsTopLevel(t *testing.T) {
	code, stdout, stderr := callHelp(t, nil)
	if code != helpExitCodeOK {
		t.Errorf("exit code: got %d, want %d", code, helpExitCodeOK)
	}
	if stderr != "" {
		t.Errorf("stderr should be empty on top-level help, got %q", stderr)
	}
	if !strings.HasPrefix(stdout, "flashbackup - portable macOS backup utility") {
		t.Errorf("stdout should lead with the top-level banner, got %q", stdout)
	}
	if !strings.Contains(stdout, "Subcommands:") {
		t.Errorf("stdout should list Subcommands:, got %q", stdout)
	}
}

// TestHelp_KnownSubcommand: for every known subcommand name in the
// constants table, runHelp prints that entry verbatim to stdout and
// returns exit 0. Asserts the leading "Usage:" line is present (every
// entry must start with one per the Tech Writer convention) and that the
// body matches the table entry byte for byte (so a future refactor that
// adds a transform before write surfaces here).
func TestHelp_KnownSubcommand(t *testing.T) {
	for name, want := range subcommandHelpTexts {
		if name == "" {
			continue // top-level is exercised by TestHelp_NoArgs_PrintsTopLevel
		}
		t.Run(name, func(t *testing.T) {
			code, stdout, stderr := callHelp(t, []string{name})
			if code != helpExitCodeOK {
				t.Errorf("exit code: got %d, want %d", code, helpExitCodeOK)
			}
			if stderr != "" {
				t.Errorf("stderr should be empty for known subcommand, got %q", stderr)
			}
			if stdout != want {
				t.Errorf("stdout for %q did not match constants table entry\nwant:\n%s\ngot:\n%s",
					name, want, stdout)
			}
		})
	}
}

// TestHelp_UnknownSubcommand: an unknown name is exit 2 with a clear
// error message naming the offending token. The quoted-token assertion
// guards against a future regression where the error message drops the
// user-supplied value (mirrors the unknown-subcommand contract in
// main.go's dispatch).
func TestHelp_UnknownSubcommand(t *testing.T) {
	code, stdout, stderr := callHelp(t, []string{"xyzzy"})
	if code != helpExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, helpExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty for unknown subcommand, got %q", stdout)
	}
	if !strings.Contains(stderr, "unknown subcommand") {
		t.Errorf("stderr should mention 'unknown subcommand', got %q", stderr)
	}
	if !strings.Contains(stderr, "\"xyzzy\"") {
		t.Errorf("stderr should quote the offending token, got %q", stderr)
	}
}

// TestHelp_TooManyArgs: more than one positional is rejected with exit 2.
// The "ambiguous intent" error message lets the operator see what was
// dropped on the floor; they can then re-run with just the name they
// actually wanted help on.
func TestHelp_TooManyArgs(t *testing.T) {
	code, stdout, stderr := callHelp(t, []string{"init", "backup"})
	if code != helpExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, helpExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty on too-many-args, got %q", stdout)
	}
	if !strings.Contains(stderr, "unexpected extra arguments") {
		t.Errorf("stderr should mention extra arguments, got %q", stderr)
	}
}

// TestHelp_EmptyNameRejected: `runHelp(ctx, [""], ...)` is exit 2. The
// empty-string key in the constants table is reserved for the top-level
// usage screen and must not be reachable via argv (the surface for that is
// `flashbackup help` with no positional). A regression that let the empty
// key escape would have the operator typing `flashbackup help ""` and
// silently getting the top-level screen, which is confusing.
func TestHelp_EmptyNameRejected(t *testing.T) {
	code, stdout, stderr := callHelp(t, []string{""})
	if code != helpExitCodeUsage {
		t.Errorf("exit code: got %d, want %d", code, helpExitCodeUsage)
	}
	if stdout != "" {
		t.Errorf("stdout should be empty when name is empty, got %q", stdout)
	}
	if !strings.Contains(stderr, "must not be empty") {
		t.Errorf("stderr should mention empty name, got %q", stderr)
	}
}

// TestHelp_AllSubcommandsHaveText prevents drift between subcommandList
// (the dispatcher) and subcommandHelpTexts (the help text). Adding a new
// subcommand to the dispatcher without an accompanying help entry would
// surface here as a clear test failure, naming the missing key.
//
// This is the most important drift guard in this file: a missing
// dispatcher entry is caught at compile time (the handler field would be
// undefined), but a missing help entry would silently make `help <name>`
// print nothing without erroring (the comma-ok path returns "" + ok=false,
// which we test handles as exit 2, but only if the test actually probes
// for that name; this guard automates the probe).
func TestHelp_AllSubcommandsHaveText(t *testing.T) {
	for _, sc := range subcommandList {
		t.Run(sc.name, func(t *testing.T) {
			text, ok := subcommandHelpTexts[sc.name]
			if !ok {
				t.Errorf("subcommand %q has no entry in subcommandHelpTexts; add one to helptext.go", sc.name)
			}
			if text == "" {
				t.Errorf("subcommand %q has empty entry in subcommandHelpTexts; populate it in helptext.go", sc.name)
			}
		})
	}
}

// TestHelp_HelpTextHasUsageLine: every entry (including the top-level
// empty key) must contain the literal "Usage:" substring. This locks the
// Tech Writer convention's first section at content level so a future edit
// cannot drop the header without tripping a test.
func TestHelp_HelpTextHasUsageLine(t *testing.T) {
	for name, text := range subcommandHelpTexts {
		label := name
		if name == "" {
			label = "(top-level)"
		}
		t.Run(label, func(t *testing.T) {
			if !strings.Contains(text, "Usage:") {
				t.Errorf("entry %q must contain %q somewhere in its body", label, "Usage:")
			}
		})
	}
}

// TestHelp_HelpTextHasNoEmDashes: every entry (including the top-level
// empty key) must NOT contain U+2014 (em-dash) or U+2013 (en-dash). The
// CLAUDE.md em-dash discipline is enforced at code level by code review,
// but help text is the most operator-visible surface in the binary; a
// stray em-dash here would land in front of every user who reads --help.
// Table-driven on every entry so a future addition gets the same
// treatment without needing to remember to extend this test.
func TestHelp_HelpTextHasNoEmDashes(t *testing.T) {
	const forbidden = "—–" // em-dash + en-dash
	for name, text := range subcommandHelpTexts {
		label := name
		if name == "" {
			label = "(top-level)"
		}
		t.Run(label, func(t *testing.T) {
			if strings.ContainsAny(text, forbidden) {
				t.Errorf("entry %q contains a forbidden em-dash or en-dash glyph; rewrite per CLAUDE.md writing-style rules", label)
			}
		})
	}
}

// TestHelp_HelpTextFor_KnownName: the helpTextFor accessor returns the
// same body as the direct map lookup for a known name. Guards against a
// future refactor of the accessor (e.g. an injected transform) breaking
// the byte-identical contract with the table.
func TestHelp_HelpTextFor_KnownName(t *testing.T) {
	for _, name := range []string{"init", "backup", "verify", "status", "profiles", "help"} {
		t.Run(name, func(t *testing.T) {
			if got := helpTextFor(name); got != subcommandHelpTexts[name] {
				t.Errorf("helpTextFor(%q) returned text that differs from the table entry", name)
			}
		})
	}
}

// TestHelp_HelpTextFor_UnknownName: an unknown name yields the empty
// string from the accessor (matching map[string]string's zero-value
// behaviour). Documenting this in a test makes the contract explicit so a
// future caller does not assume the accessor panics or errors.
func TestHelp_HelpTextFor_UnknownName(t *testing.T) {
	if got := helpTextFor("nope-not-a-subcommand"); got != "" {
		t.Errorf("helpTextFor(unknown) = %q, want empty string", got)
	}
}
