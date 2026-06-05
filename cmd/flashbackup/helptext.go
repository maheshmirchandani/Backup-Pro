package main

// helptext.go is the single source of truth for human-readable help text
// surfaced by the binary: the top-level usage screen and every subcommand's
// detailed help body. It is the constants table the Task 41 Tech Writer hat
// asked for so future edits land in one place rather than scattering across
// every subcommand's fs.Usage block.
//
// File length budget exemption: this file is constants-only (no logic) and
// intentionally exceeds the project's 200-line guideline so each entry can
// carry full Flags / Examples / See also sections without abbreviation. The
// surface is wide because the help text IS the contract; trimming it for
// length would create the exact "consult the spec" friction the convention
// is designed to remove. Future entries should keep the same shape:
//
//	Usage: <one-line invocation>
//
//	<short description, 1 to 3 sentences>
//
//	Flags:           (or Actions: or Subcommands: as appropriate for shape)
//	  <flag>      <one-line description>
//	  ...
//
//	Examples:
//	  <concrete invocation 1>
//	  <concrete invocation 2>
//
//	See also: <comma-separated related subcommands>
//
// Section-header convention (Task 41 review M3): substitute "Actions:"
// when the subcommand has no flags but takes a verb (e.g. profiles); use
// "Subcommands:" for the help subcommand's own list. The shape follows
// the surface, not the literal label.
//
// Em-dash / en-dash discipline applies to this file as much as any other:
// help text is user-visible, so the CLAUDE.md rule is enforced both in code
// (gofmt + go vet) and at content level (TestHelp_HelpTextHasNoEmDashes in
// help_test.go scans every entry for the forbidden glyphs).
//
// The "" (empty string) key is the top-level usage screen. It is not
// reachable via `flashbackup help <name>` (runHelp rejects an empty name)
// because the top-level surface is `flashbackup help` with no positional;
// reserving the empty key for that purpose keeps the table self-describing
// instead of needing a parallel "topLevelHelp" constant.

// subcommandHelpTexts maps a subcommand name to its detailed --help body.
// The empty-string key holds the top-level usage block. helpTextFor(name)
// is the canonical accessor; help.go uses the map directly to distinguish
// "no entry" from "empty entry" via the map-comma-ok form.
//
// Adding a new subcommand requires adding an entry here AND wiring the
// dispatcher in main.go's subcommandList. TestHelp_AllSubcommandsHaveText
// catches a missing entry as a unit-test failure so the drift cannot
// silently ship.
var subcommandHelpTexts = map[string]string{
	"": `flashbackup - portable macOS backup utility

Usage:
  flashbackup <subcommand> [args...]
  flashbackup --version
  flashbackup --help
  flashbackup help [<subcommand>]

Subcommands:
  init      initialize a USB volume for FlashBackup
  backup    run a backup using a profile
  verify    verify the integrity of a prior run
  status    show recent run history and current state
  profiles  list, create, edit, or delete backup profiles
  help      show help for the binary or a subcommand

Exit codes:
  0    success, or --version / --help
  1    subcommand-level failure (runner / verify returned a non-recoverable error)
  2    usage error, or a preflight failure that aborted before the run state machine started
  130  second SIGINT or SIGTERM within 5 seconds; force exit (128 + signo)

Run 'flashbackup help <subcommand>' for detailed help on any subcommand.
See https://github.com/maheshmirchandani/Backup-Pro for documentation.
`,

	"init": `Usage: flashbackup init <USB-path> [--reset-keys]

Initialize a USB volume for FlashBackup use. Verifies the filesystem is APFS
or HFS+, creates the <USB>/.flashbackup/ directory, suppresses Spotlight
indexing on the volume, extracts the embedded rsync binary (SHA256-verified),
and writes a fresh version.json with a random 32-byte HMAC key.

The volume must be mounted and writable. exFAT and msdos are refused with a
reformat recipe; init does not modify a refused volume.

Flags:
  --reset-keys    overwrite an existing version.json with a fresh HMAC key
                  (WARNING: invalidates every prior manifest on this USB)

Examples:
  flashbackup init /Volumes/FLASHBKP
  flashbackup init /Volumes/FLASHBKP --reset-keys

See also: status, profiles, help.
`,

	"backup": `Usage: flashbackup backup <profile-name> <USB-path> [--move]

Run a backup using a saved profile. The profile is loaded from
<USB-path>/.flashbackup/profiles.json. The copy phase hashes each source file
during read, copies it to the destination, re-hashes the destination, and
records both hashes in the run manifest. A mismatch fails the run.

In move mode, the source files are deleted ONLY after the verified copy
completes, AND only if (size, mtime_ns) of each source file is unchanged
between the start of the run and the moment of deletion. Any source mutation
during the run blocks the unlink for that file. Move mode requires the
operator to type the literal token DELETE at an upfront confirmation prompt.

Flags:
  --move    move mode: delete source files after verified copy
            (requires typing the literal DELETE token at the prompt)

Examples:
  flashbackup backup my-docs /Volumes/FLASHBKP
  flashbackup backup photos /Volumes/FLASHBKP --move

See also: verify, profiles, status, help.
`,

	"verify": `Usage: flashbackup verify [--all | <run-id>] [--check-extras] <USB-path>

Re-hash the manifest entries from a prior run and confirm the destination
files still match. With no run-id, the latest run is verified. With --all,
every run on the USB is verified in turn. The manifest is HMAC-authenticated
before any rehash, so a tampered manifest fails fast.

A hash mismatch, missing file, or unreadable file is an integrity failure
(exit 1). A manifest signature failure is also an integrity failure (exit 1).

Flags:
  --all              verify every run on the USB
                     (mutually exclusive with a positional <run-id>)
  --check-extras     additionally count files in destination that are NOT in
                     any manifest (informational; never an integrity error)

Examples:
  flashbackup verify /Volumes/FLASHBKP
  flashbackup verify --all /Volumes/FLASHBKP
  flashbackup verify 2026-06-04T1430Z-a7f2 /Volumes/FLASHBKP
  flashbackup verify --all --check-extras /Volumes/FLASHBKP

See also: backup, status, help.
`,

	"status": `Usage: flashbackup status [--json] <USB-path>

Show the current state of a FlashBackup-initialized USB drive. Reports the
volume identity (filesystem, capacity, free space), the namespace prefix
(<hostname>-<username>), the runner lock status (held or free), the count
of retained runs, the last run summary, and the last verify summary.

Status is read-only. It does not acquire the runner lock and does not modify
any on-disk state. The --json flag emits a stable schema for scripted use
(documented at API Contracts in the design spec).

Flags:
  --json    emit the locked status schema as JSON
            (suitable for piping through jq or other tooling)

Examples:
  flashbackup status /Volumes/FLASHBKP
  flashbackup status --json /Volumes/FLASHBKP

See also: backup, verify, help.
`,

	"profiles": `Usage:
  flashbackup profiles list     <USB-path> [--json]
  flashbackup profiles new      <profile-name> <USB-path>
  flashbackup profiles edit     <profile-name> <USB-path>
  flashbackup profiles delete   <profile-name> <USB-path>
  flashbackup profiles validate <profile-name> <USB-path>

Manage backup profiles stored at <USB-path>/.flashbackup/profiles.json.

The new and edit actions launch $EDITOR (vim fallback) on a temporary JSON
file; on save the result is parsed, validated, and persisted. Renaming a
profile via the editor is rejected; use delete plus new to rename.

The validate action re-runs the profile schema check against the on-disk
record and exits 0 if the profile is still valid, exit 1 if a stored
profile has drifted out of compliance with the current schema.

Actions:
  list        list every profile on the USB (plain text or JSON)
  new         author a new profile by editing a JSON skeleton in $EDITOR
  edit        edit an existing profile in $EDITOR
  delete      remove a profile by name
  validate    re-check an on-disk profile against the schema

Examples:
  flashbackup profiles list /Volumes/FLASHBKP
  flashbackup profiles list /Volumes/FLASHBKP --json
  flashbackup profiles new my-docs /Volumes/FLASHBKP
  flashbackup profiles edit my-docs /Volumes/FLASHBKP
  flashbackup profiles delete my-docs /Volumes/FLASHBKP
  flashbackup profiles validate my-docs /Volumes/FLASHBKP

See also: backup, help.
`,

	"help": `Usage:
  flashbackup help              show the top-level usage screen
  flashbackup help <subcommand> show detailed help for a subcommand

Print help text for the binary or for a named subcommand. Equivalent to
'flashbackup --help' (no args) and 'flashbackup <subcommand> --help'
(with one argument). The text is sourced from a constants table so the
top-level usage, the per-subcommand --help body, and 'flashbackup help
<subcommand>' always agree.

Subcommands:
  init, backup, verify, status, profiles, help

Examples:
  flashbackup help
  flashbackup help init
  flashbackup help backup
  flashbackup help verify
  flashbackup help status
  flashbackup help profiles

See also: every other subcommand. 'flashbackup help help' is a deliberate
self-reference and prints this text.
`,
}

// helpTextFor returns the detailed help text for the named subcommand, or
// the empty string if no entry exists. Callers that need to distinguish
// "no entry" from "empty entry" should use the map directly with the
// comma-ok form; runHelp does so to reject unknown names with a clear
// error rather than silently printing an empty block.
//
// This accessor exists so future refactors of the storage layer (e.g.
// splitting top-level vs subcommand into two maps, or sourcing from a
// `go embed` directive) do not break callers; the map is left exported
// within the package because the test suite walks it for the
// drift-prevention check.
func helpTextFor(name string) string {
	return subcommandHelpTexts[name]
}
