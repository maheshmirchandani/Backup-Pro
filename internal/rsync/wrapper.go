package rsync

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
)

// Options configure one rsync invocation. See Wrapper.Run for semantics.
type Options struct {
	// ExecPath is the absolute path to the rsync binary to run. Typically the
	// result of EnsureExtracted(). Required.
	ExecPath string

	// SourceRoot is the source directory to back up (absolute path). A
	// trailing slash is appended (and any existing trailing slashes are
	// stripped first) so rsync transfers the contents of SourceRoot rather
	// than SourceRoot itself.
	SourceRoot string

	// DestRoot is the destination directory (e.g. <USB>/.../<hostname-username>).
	// No trailing slash is added.
	DestRoot string

	// Files is the list of file paths relative to SourceRoot to transfer.
	// Passed to rsync via stdin (--files-from=-) with NUL terminators
	// (--from0) per the spec's security amendment: filenames are NEVER
	// expanded via shell or argv. If empty, --files-from is omitted entirely
	// and rsync recurses normally under -a.
	Files []string

	// Archive enables -a (preserve perms, mtimes, symlinks, devices, etc.).
	// Default true in production; only disable in tests.
	Archive bool

	// Partial enables --partial (keep partially-transferred files for resume).
	Partial bool

	// Xattrs enables --xattrs (preserve extended attributes; macOS Finder
	// tags, Spotlight metadata, quarantine bit).
	Xattrs bool

	// Sparse enables --sparse (preserve sparse-file holes).
	Sparse bool

	// HardLinks enables --hard-links.
	HardLinks bool

	// Delete enables --delete (mirror mode: remove destination files absent
	// from source). Caller must restrict to FB-written paths per spec
	// invariant #6; that is the caller's job, not this wrapper's.
	Delete bool

	// Stdout, Stderr receive the rsync subprocess's stdout/stderr. If nil,
	// those streams are discarded. Callers typically capture both for the
	// run log; Task 14's progress parser consumes Stdout.
	Stdout io.Writer
	Stderr io.Writer
}

// Wrapper invokes the embedded rsync binary as a subprocess. A type rather
// than a free function so callers can hold a single instance and so future
// fields (e.g. an injected exec.Command-style hook) have a home without
// breaking the API.
type Wrapper struct{}

// Run invokes rsync with the given options. Blocks until the subprocess
// exits. Returns the subprocess's exit status wrapped in an error on
// non-zero exit. Honors ctx cancellation via exec.CommandContext: cancelling
// the context sends SIGKILL to the subprocess (Go's default). If a softer
// SIGTERM-with-grace is required later, switch to cmd.Cancel.
//
// Callers that want a hard deadline should pass a context with a timeout.
func (w *Wrapper) Run(ctx context.Context, opts Options) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("rsync run: %w", err)
	}
	if opts.ExecPath == "" {
		return fmt.Errorf("rsync run: ExecPath is empty")
	}
	if opts.SourceRoot == "" {
		return fmt.Errorf("rsync run: SourceRoot is empty")
	}
	if opts.DestRoot == "" {
		return fmt.Errorf("rsync run: DestRoot is empty")
	}

	args := buildArgs(opts)

	cmd := exec.CommandContext(ctx, opts.ExecPath, args...)
	cmd.Stdout = opts.Stdout
	cmd.Stderr = opts.Stderr

	if len(opts.Files) > 0 {
		stdin, err := cmd.StdinPipe()
		if err != nil {
			return fmt.Errorf("rsync run: stdin pipe: %w", err)
		}
		// Write the NUL-terminated file list in a goroutine. If we wrote
		// inline before Wait, a large file list could block on the OS pipe
		// buffer while rsync's stdout/stderr also block on us (the writers
		// are synchronous). The goroutine pattern is the standard fix.
		go func() {
			defer stdin.Close()
			buf := bufio.NewWriter(stdin)
			for _, f := range opts.Files {
				_, _ = buf.WriteString(f)
				_ = buf.WriteByte(0)
			}
			_ = buf.Flush()
		}()
	}

	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return fmt.Errorf("rsync exited with status %d: %w", exitErr.ExitCode(), err)
		}
		return fmt.Errorf("rsync run: %w", err)
	}
	return nil
}

// ResolveExitCode returns the rsync subprocess exit code from a Run error,
// or -1 if the error was not an ExitError (subprocess failed to start, ctx
// cancelled before exec, validation error, etc.). Returns 0 for a nil err.
func ResolveExitCode(err error) int {
	if err == nil {
		return 0
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return -1
}

// buildArgs assembles the rsync argv (without the program path itself) from
// opts. Pure function; unit-testable without exec'ing anything.
func buildArgs(opts Options) []string {
	args := make([]string, 0, 12)
	if opts.Archive {
		args = append(args, "-a")
	}
	if opts.Partial {
		args = append(args, "--partial")
	}
	if opts.Xattrs {
		args = append(args, "--xattrs")
	}
	if opts.Sparse {
		args = append(args, "--sparse")
	}
	if opts.HardLinks {
		args = append(args, "--hard-links")
	}
	if opts.Delete {
		args = append(args, "--delete")
	}
	args = append(args, "--progress")
	if len(opts.Files) > 0 {
		args = append(args, "--from0", "--files-from=-")
	}
	src := strings.TrimRight(opts.SourceRoot, "/") + "/"
	args = append(args, src, opts.DestRoot)
	return args
}

// fileListBytes returns the NUL-terminated stdin payload that Run would
// write for opts.Files. Exposed unexported for tests only.
func fileListBytes(opts Options) []byte {
	var buf bytes.Buffer
	for _, f := range opts.Files {
		buf.WriteString(f)
		buf.WriteByte(0)
	}
	return buf.Bytes()
}
