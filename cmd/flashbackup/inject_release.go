//go:build !faultinject

package main

// inject_release.go is the release-shape no-op stub for the fault-injection
// CLI seam. It is compiled when the `faultinject` build tag is NOT set
// (the default release build). Together with inject_faultinject.go it
// gates the --inject DSL behind the release-gate symbol scan: the release
// binary's symbol table never carries any reference to runner.Parse,
// runner.Activate, or the injectFlag type, so `make verify-release` stays
// clean.
//
// The signatures below mirror the faultinject-tagged variants exactly so
// backup.go can call them unconditionally. registerInjectFlag returns a
// non-nil sentinel pointer so backup.go does not have to nil-check; the
// pointer carries no fields and activateInjects ignores it. The flag
// itself is intentionally NOT registered: a release binary that receives
// `--inject=...` on the command line will reject it with the standard
// flag-package "flag provided but not defined" error, surfacing the
// release shape's refusal cleanly (exit 2 via the usage path in
// backup.go).
//
// The intentional omission of `runner.Parse` / `runner.Activate` calls
// here is what keeps the release binary clean of the runner.faultinject
// symbol set; do not add any import of those names to this file.

import (
	"flag"
	"io"
)

// injectFlag is the empty placeholder so the type name resolves under
// both build tags. Its zero value is the only value backup.go ever holds.
type injectFlag struct{}

// registerInjectFlag returns a non-nil placeholder. The release shape
// deliberately does NOT add the --inject flag to fs; any operator who
// passes --inject gets the flag-package "flag provided but not defined"
// rejection (exit 2) which is the correct release-time behaviour.
func registerInjectFlag(_ *flag.FlagSet) *injectFlag {
	return &injectFlag{}
}

// activateInjects is a no-op in the release shape: there is no fault
// list to install, and runner.Activate (a no-op stub in faultinject_release.go)
// would refuse the call anyway.
func activateInjects(_ io.Writer, _ *injectFlag) error {
	return nil
}
