//go:build faultinject

package main

// inject_faultinject.go is the build-tagged seam that wires the runner's
// fault-injection DSL (--inject=<spec>) into the `backup` subcommand. It
// is compiled ONLY when the `faultinject` build tag is set; the
// release-shape build picks up the no-op stub in inject_release.go
// instead. The split is a release-gate invariant: `make verify-release`
// greps the binary's Go symbol table for any symbol containing
// "faultinject" and fails the build if one leaks in, so the wiring code
// itself (which calls runner.Parse + runner.Activate by name) lives
// behind the tag to keep the release binary clean.
//
// The cmd-side seam has two responsibilities:
//
//  1. registerInjectFlag attaches a --inject=<spec> flag to the backup
//     subcommand's FlagSet. The flag is repeatable: each --inject occurrence
//     appends one spec string. We use a custom flag.Value (injectFlag)
//     because flag.StringSlice does not exist in the standard library.
//
//  2. activateInjects parses every collected --inject spec via runner.Parse
//     and calls runner.Activate to install the fault list before
//     runner.Run starts. Parse errors are surfaced to stderr with a
//     non-zero exit code so a bad spec is loud at the cmd level rather
//     than producing a confusing mid-run failure.
//
// Both helpers have a sibling no-op pair in inject_release.go; backup.go
// calls into them by name and is build-tag-agnostic.

import (
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/maheshmirchandani/Backup-Pro/internal/runner"
)

// injectFlag implements flag.Value. Each --inject occurrence appends one
// raw spec string. The slice is parsed later by activateInjects (after
// fs.Parse returns) so any per-spec validation failure can name the
// exact spec string in the error message.
type injectFlag struct {
	specs []string
}

// String returns the comma-joined spec list. The flag package calls this
// when printing the default value in fs.PrintDefaults; the value is
// returned even if no --inject was supplied (in which case it is empty).
func (f *injectFlag) String() string {
	if f == nil {
		return ""
	}
	return strings.Join(f.specs, ",")
}

// Set appends one spec. Called by the flag package once per --inject
// occurrence. Empty values are rejected here (rather than later in Parse)
// so the error names the right CLI token.
func (f *injectFlag) Set(spec string) error {
	if spec == "" {
		return fmt.Errorf("--inject: empty spec")
	}
	f.specs = append(f.specs, spec)
	return nil
}

// registerInjectFlag attaches --inject to fs and returns the receiving
// injectFlag value. The returned pointer is consumed later by
// activateInjects. Callers must call activateInjects AFTER fs.Parse and
// BEFORE runner.Run so the fault list is installed in time for T0.
func registerInjectFlag(fs *flag.FlagSet) *injectFlag {
	f := &injectFlag{}
	fs.Var(f, "inject", "fault-injection spec (repeatable); "+
		"format: action:phase=<P>[:file=<F>][:after_pct=N][:after_count=N]")
	return f
}

// activateInjects parses every collected spec via runner.Parse and calls
// runner.Activate to install the resulting fault list. Returns a non-nil
// error if any spec is invalid; the caller surfaces it to stderr and
// returns the runtime exit code (1) per the cmd-level error contract.
//
// stderr is plumbed for future "warn on N>0 active faults" output; today
// we keep activation silent so the backup command's stdout/stderr layout
// stays identical to a no-inject invocation.
func activateInjects(stderr io.Writer, f *injectFlag) error {
	_ = stderr
	if f == nil || len(f.specs) == 0 {
		runner.Activate(nil)
		return nil
	}
	faults, err := runner.Parse(f.specs)
	if err != nil {
		return fmt.Errorf("--inject parse: %w", err)
	}
	runner.Activate(faults)
	return nil
}
