package main

// profiles.go implements the `flashbackup profiles <action> [args] <USB-path>`
// subcommand (Task 40). It is a thin CRUD wrapper around the
// internal/profiles.Store, surfacing list / new / edit / delete / validate to
// the operator without forcing them to hand-edit the canonical single-document
// store at <USB>/.flashbackup/profiles.json.
//
// Action shape:
//
//	flashbackup profiles list     <USB-path> [--json]
//	flashbackup profiles new      <profile-name> <USB-path>
//	flashbackup profiles edit     <profile-name> <USB-path>
//	flashbackup profiles delete   <profile-name> <USB-path>
//	flashbackup profiles validate <profile-name> <USB-path>
//	flashbackup profiles --help
//
// The new + edit actions launch $EDITOR (vim fallback) on a temp JSON file;
// on save the result is parsed, validated, and Upserted via the store. The
// edit action additionally rejects an attempt to rename the profile in the
// editor (rename is a separate concern outside the v0.1 surface).
//
// Exit code table (matches doc.go contract):
//
//	0  success
//	1  validate found integrity issues, or a runtime failure (editor crash,
//	   disk read/write error after argv parse succeeded)
//	2  usage error (no action, unknown action, missing positional, --help,
//	   non-existent USB path, profile-not-found on edit / delete, validation
//	   failure on new (after the editor closes), name collision on new)
//
// AC: none of the v0.1 acceptance criteria map directly to the profiles
// CRUD surface; this subcommand exists so the operator can author the
// profiles that backup + verify subsequently consume. The contract is the
// argv shape above and the editor-override seam below.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
)

// profilesExitCode* mirror the binary's exit-code contract in doc.go. Declared
// as named constants so each action arm reads as a table rather than a wall of
// literals; matches the convention used by init / backup / verify / status.
const (
	profilesExitCodeOK      = 0
	profilesExitCodeRuntime = 1
	profilesExitCodeUsage   = 2
)

// editorRunOverrideForTest is the test seam for the editor exec. When nil,
// runProfiles invokes the real editor via exec.Command. When non-nil, tests
// substitute a Go callback that reads + rewrites the temp file in-process so
// no TTY is required.
//
// Tests MUST reset this via t.Cleanup to nil; a leaked override would silently
// hide editor-exec regressions from any subsequent test. The variable is
// package-private so a future move to a separate _test.go init does not
// require exposing internals through the public API.
var editorRunOverrideForTest func(path string) error

// runProfiles is the testable entry point for the `profiles` subcommand. argv
// is the trailing args after "profiles" (so argv[0] is the action verb or
// "--help"). stdout receives the list / validate output; stderr receives
// usage errors and any wrapped error from the store / editor.
//
// stdin is accepted for handler-signature symmetry; profiles has no
// interactive stdin reads in v0.1 (the editor owns its own TTY). ctx is the
// signal-aware ctx from main; a SIGINT during editor exec propagates to the
// editor via the shared process group, and a SIGINT during a store load /
// save is observed at the next syscall boundary.
func runProfiles(ctx context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) int {
	_ = stdin // accepted for handler-signature symmetry; profiles has no prompts

	// Top-level --help is handled here (before action dispatch) so the
	// operator can ask for help without naming an action. Each action arm
	// owns its own per-action FlagSet for action-specific flags (only
	// `list` has one today: --json).
	if len(argv) == 0 {
		fmt.Fprintln(stderr, "flashbackup profiles: missing <action> argument")
		printProfilesUsage(stderr)
		return profilesExitCodeUsage
	}
	switch argv[0] {
	case "--help", "-h":
		printProfilesUsage(stdout)
		return profilesExitCodeOK
	}

	action := argv[0]
	rest := argv[1:]

	switch action {
	case "list":
		return runProfilesList(ctx, rest, stdout, stderr)
	case "new":
		return runProfilesNew(ctx, rest, stdout, stderr)
	case "edit":
		return runProfilesEdit(ctx, rest, stdout, stderr)
	case "delete":
		return runProfilesDelete(ctx, rest, stdout, stderr)
	case "validate":
		return runProfilesValidate(ctx, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "flashbackup profiles: unknown action %q\n", action)
		printProfilesUsage(stderr)
		return profilesExitCodeUsage
	}
}

// printProfilesUsage writes the action-list + flags block. Each action lists
// its positional shape so the operator does not have to consult the design
// spec to remember the argument order. Kept under 25 lines so it fits in a
// typical terminal.
func printProfilesUsage(w io.Writer) {
	fmt.Fprintln(w, "Usage:")
	fmt.Fprintln(w, "  flashbackup profiles list     <USB-path> [--json]")
	fmt.Fprintln(w, "  flashbackup profiles new      <profile-name> <USB-path>")
	fmt.Fprintln(w, "  flashbackup profiles edit     <profile-name> <USB-path>")
	fmt.Fprintln(w, "  flashbackup profiles delete   <profile-name> <USB-path>")
	fmt.Fprintln(w, "  flashbackup profiles validate <profile-name> <USB-path>")
	fmt.Fprintln(w, "")
	fmt.Fprintln(w, "Manage backup profiles stored at <USB>/.flashbackup/profiles.json.")
	fmt.Fprintln(w, "  new + edit launch $EDITOR (vim fallback) on a temp JSON file;")
	fmt.Fprintln(w, "  on save the profile is validated and persisted.")
}

// runProfilesList implements the `list` action. Supports --json for scripted
// consumers; plain text mode emits one profile per line with a one-line
// summary (source path + include/exclude counts).
func runProfilesList(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	_ = ctx
	// The documented argv shape is `list <USB-path> [--json]`, with
	// --json after the positional. The stdlib `flag` package stops at
	// the first non-flag token, so a literal `list /Volumes/X --json`
	// would treat `--json` as an extra positional. We accept --json in
	// either position by pre-scanning argv for the flag-or-help tokens
	// and stripping them before handing the remainder to the
	// positional parser.
	jsonMode := false
	rest := make([]string, 0, len(argv))
	for _, a := range argv {
		switch a {
		case "--json", "-json":
			jsonMode = true
		case "--help", "-h":
			fmt.Fprintln(stderr, "Usage: flashbackup profiles list <USB-path> [--json]")
			return profilesExitCodeOK
		default:
			rest = append(rest, a)
		}
	}
	if len(rest) == 0 {
		fmt.Fprintln(stderr, "flashbackup profiles list: missing <USB-path> argument")
		fmt.Fprintln(stderr, "Usage: flashbackup profiles list <USB-path> [--json]")
		return profilesExitCodeUsage
	}
	if len(rest) > 1 {
		fmt.Fprintf(stderr, "flashbackup profiles list: unexpected extra arguments: %v\n", rest[1:])
		fmt.Fprintln(stderr, "Usage: flashbackup profiles list <USB-path> [--json]")
		return profilesExitCodeUsage
	}

	store, code := openProfilesStore(rest[0], "list", stderr)
	if store == nil {
		return code
	}

	all, err := store.List()
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles list: %v\n", err)
		return profilesExitCodeRuntime
	}

	if jsonMode {
		if err := emitProfilesJSON(stdout, all); err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles list: emit json: %v\n", err)
			return profilesExitCodeRuntime
		}
		return profilesExitCodeOK
	}

	if len(all) == 0 {
		fmt.Fprintln(stdout, "no profiles yet")
		return profilesExitCodeOK
	}
	for _, p := range all {
		fmt.Fprintf(stdout, "%s\tsource=%s\tincludes=%d\texcludes=%d\n",
			p.Name, p.Source, len(p.Includes), len(p.Excludes))
	}
	return profilesExitCodeOK
}

// runProfilesNew implements the `new` action. Refuses to overwrite an
// existing profile; launches the editor on a fresh skeleton; parses +
// validates the saved JSON; Upserts the result. A validation failure after
// the editor closes exits 2 (operator-fixable: re-run and retype the JSON).
func runProfilesNew(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	if code, name, usbPath, ok := parseNamedAction(argv, "new", stderr); !ok {
		return code
	} else {
		store, code := openProfilesStore(usbPath, "new", stderr)
		if store == nil {
			return code
		}

		// Collision check: refuse to clobber an existing profile via `new`.
		// The operator who wants to overwrite uses `edit` instead; the split
		// surfaces accidental name reuse loudly.
		if _, err := store.Get(name); err == nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: profile %q already exists\n", name)
			return profilesExitCodeUsage
		}

		skeleton := profiles.Profile{
			Name:     name,
			Source:   "/path/to/source",
			Includes: []string{"*"},
			Excludes: []string{".DS_Store"},
		}
		data, err := json.MarshalIndent(skeleton, "", "  ")
		if err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: marshal skeleton: %v\n", err)
			return profilesExitCodeRuntime
		}

		edited, err := editJSONInEditor(ctx, data, stderr)
		if err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: %v\n", err)
			return profilesExitCodeRuntime
		}

		var p profiles.Profile
		if err := json.Unmarshal(edited, &p); err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: parse edited JSON: %v\n", err)
			return profilesExitCodeUsage
		}
		if p.Name != name {
			fmt.Fprintf(stderr,
				"flashbackup profiles new: editor changed name from %q to %q (rename not supported)\n",
				name, p.Name)
			return profilesExitCodeUsage
		}
		if err := profiles.ValidateProfile(p); err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: %v\n", err)
			return profilesExitCodeUsage
		}
		if err := store.Upsert(p); err != nil {
			fmt.Fprintf(stderr, "flashbackup profiles new: %v\n", err)
			return profilesExitCodeRuntime
		}
		fmt.Fprintf(stdout, "created profile %q\n", p.Name)
		return profilesExitCodeOK
	}
}

// runProfilesEdit implements the `edit` action. Loads the existing profile,
// marshals it into the editor temp file, and on save parses + validates +
// Upserts. A name change in the edited JSON is rejected (rename is a
// separate concern that would require delete + recreate semantics).
func runProfilesEdit(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	code, name, usbPath, ok := parseNamedAction(argv, "edit", stderr)
	if !ok {
		return code
	}
	store, code := openProfilesStore(usbPath, "edit", stderr)
	if store == nil {
		return code
	}

	existing, err := store.Get(name)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: %v\n", err)
		return profilesExitCodeUsage
	}

	data, err := json.MarshalIndent(existing, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: marshal existing: %v\n", err)
		return profilesExitCodeRuntime
	}

	edited, err := editJSONInEditor(ctx, data, stderr)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: %v\n", err)
		return profilesExitCodeRuntime
	}

	var p profiles.Profile
	if err := json.Unmarshal(edited, &p); err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: parse edited JSON: %v\n", err)
		return profilesExitCodeUsage
	}
	if p.Name != name {
		fmt.Fprintf(stderr,
			"flashbackup profiles edit: editor changed name from %q to %q (rename not supported)\n",
			name, p.Name)
		return profilesExitCodeUsage
	}
	if err := profiles.ValidateProfile(p); err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: %v\n", err)
		return profilesExitCodeUsage
	}
	if err := store.Upsert(p); err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles edit: %v\n", err)
		return profilesExitCodeRuntime
	}
	fmt.Fprintf(stdout, "updated profile %q\n", p.Name)
	return profilesExitCodeOK
}

// runProfilesDelete implements the `delete` action. A missing profile is an
// operator-fixable usage error (likely a typo on the name); we surface the
// store's "not found" message directly.
func runProfilesDelete(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	_ = ctx
	code, name, usbPath, ok := parseNamedAction(argv, "delete", stderr)
	if !ok {
		return code
	}
	store, code := openProfilesStore(usbPath, "delete", stderr)
	if store == nil {
		return code
	}
	if err := store.Delete(name); err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles delete: %v\n", err)
		// store.Delete returns "not found" for a missing profile; treat
		// that as operator-fixable. A future disk-read failure would also
		// pass through here as exit 2, which is acceptable: the operator
		// either fixes the path or fixes the profile name.
		return profilesExitCodeUsage
	}
	fmt.Fprintf(stdout, "deleted profile %q\n", name)
	return profilesExitCodeOK
}

// runProfilesValidate implements the `validate` action. Loads the profile
// and re-runs ValidateProfile. Exit 0 + "OK" on success; exit 1 + the
// validation error on failure (treated as a runtime integrity signal, not
// a usage error, because the validation already passed at Upsert time and
// a re-check that fails means on-disk state diverged from what Upsert
// accepted).
func runProfilesValidate(ctx context.Context, argv []string, stdout, stderr io.Writer) int {
	_ = ctx
	code, name, usbPath, ok := parseNamedAction(argv, "validate", stderr)
	if !ok {
		return code
	}
	store, code := openProfilesStore(usbPath, "validate", stderr)
	if store == nil {
		return code
	}
	p, err := store.Get(name)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles validate: %v\n", err)
		return profilesExitCodeUsage
	}
	if err := profiles.ValidateProfile(p); err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles validate: %v\n", err)
		return profilesExitCodeRuntime
	}
	fmt.Fprintln(stdout, "OK")
	return profilesExitCodeOK
}

// editJSONInEditor writes data to a temp file, runs the editor (real or
// test override), reads the file back, and returns the post-edit bytes. The
// temp file is removed before return on every path so a crashed editor does
// not leave dangling state in /tmp.
//
// When editorRunOverrideForTest is non-nil it is called instead of forking
// $EDITOR; tests inject a Go callback to mutate the file in-process.
func editJSONInEditor(ctx context.Context, initial []byte, stderr io.Writer) ([]byte, error) {
	tmp, err := os.CreateTemp("", "flashbackup-profile-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Defer removal before any error returns; the tmp file should never
	// outlive this function.
	defer os.Remove(tmpPath)

	if _, err := tmp.Write(initial); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("write initial JSON: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close temp file: %w", err)
	}

	if editorRunOverrideForTest != nil {
		if err := editorRunOverrideForTest(tmpPath); err != nil {
			return nil, fmt.Errorf("editor override: %w", err)
		}
	} else {
		if err := runEditorSubprocess(ctx, tmpPath, stderr); err != nil {
			return nil, err
		}
	}

	out, err := os.ReadFile(tmpPath)
	if err != nil {
		return nil, fmt.Errorf("read edited file: %w", err)
	}
	return out, nil
}

// runEditorSubprocess invokes $EDITOR (or vim fallback) against the given
// path with stdin / stdout / stderr wired to the controlling terminal.
// The editor needs the real TTY to be interactive; we deliberately do NOT
// pipe through bytes.Buffer because that would prevent vim from drawing.
//
// A non-zero exit from the editor is reported as an error so the caller
// can short-circuit (we cannot trust the file contents if the editor
// aborted).
func runEditorSubprocess(ctx context.Context, path string, stderr io.Writer) error {
	editor := os.Getenv("EDITOR")
	if editor == "" {
		editor = "vim"
	}
	// Split on whitespace so EDITOR="code --wait" works; the first token
	// is the program, the rest are forwarded args before the path.
	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return fmt.Errorf("EDITOR is empty after whitespace split")
	}
	args := append(parts[1:], path)
	// G204 false-positive: the EDITOR env var IS operator-controlled by
	// design (this is the established UNIX convention for $EDITOR), and the
	// only argument we append is a temp file path we created ourselves via
	// os.CreateTemp. There is no way to honor $EDITOR without exec'ing what
	// the operator set; refusing would defeat the feature.
	cmd := exec.CommandContext(ctx, parts[0], args...) //nolint:gosec // bounded: operator-controlled EDITOR + our own tempfile path
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("editor %q: %w", editor, err)
	}
	return nil
}

// openProfilesStore wraps the USB-path resolution + Store construction for
// every action arm. Returns (nil, exit-code) on any failure so the caller
// can return directly; returns (store, 0) on success.
//
// USB path resolution mirrors init / backup / verify / status: Abs +
// EvalSymlinks + stat for is-dir.
func openProfilesStore(usbPath, action string, stderr io.Writer) (*profiles.Store, int) {
	abs, err := filepath.Abs(usbPath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles %s: resolve %q: %v\n", action, usbPath, err)
		return nil, profilesExitCodeUsage
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles %s: %q: %v\n", action, abs, err)
		return nil, profilesExitCodeUsage
	}
	mpInfo, err := os.Stat(resolved)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles %s: stat %q: %v\n", action, resolved, err)
		return nil, profilesExitCodeUsage
	}
	if !mpInfo.IsDir() {
		fmt.Fprintf(stderr, "flashbackup profiles %s: %q is not a directory\n", action, resolved)
		return nil, profilesExitCodeUsage
	}

	storePath := filepath.Join(resolved, ".flashbackup", "profiles.json")
	store, err := profiles.NewStore(storePath)
	if err != nil {
		fmt.Fprintf(stderr, "flashbackup profiles %s: open store: %v\n", action, err)
		return nil, profilesExitCodeRuntime
	}
	return store, profilesExitCodeOK
}

// parseNamedAction parses the shared `<profile-name> <USB-path>` positional
// shape used by new / edit / delete / validate. Returns
// (exit-code, name, usb-path, ok); when ok is false the caller must return
// exit-code directly without further action.
//
// The action string is interpolated into error messages so each arm
// surfaces "flashbackup profiles edit:" rather than a generic prefix; the
// operator scanning a logfile then knows exactly which action failed.
func parseNamedAction(argv []string, action string, stderr io.Writer) (int, string, string, bool) {
	if len(argv) > 0 {
		switch argv[0] {
		case "--help", "-h":
			fmt.Fprintf(stderr, "Usage: flashbackup profiles %s <profile-name> <USB-path>\n", action)
			return profilesExitCodeOK, "", "", false
		}
	}
	if len(argv) < 2 {
		fmt.Fprintf(stderr,
			"flashbackup profiles %s: requires <profile-name> <USB-path>\n", action)
		fmt.Fprintf(stderr,
			"Usage: flashbackup profiles %s <profile-name> <USB-path>\n", action)
		return profilesExitCodeUsage, "", "", false
	}
	if len(argv) > 2 {
		fmt.Fprintf(stderr,
			"flashbackup profiles %s: unexpected extra arguments: %v\n", action, argv[2:])
		return profilesExitCodeUsage, "", "", false
	}
	name := argv[0]
	if name == "" {
		fmt.Fprintf(stderr, "flashbackup profiles %s: <profile-name> must not be empty\n", action)
		return profilesExitCodeUsage, "", "", false
	}
	return profilesExitCodeOK, name, argv[1], true
}
