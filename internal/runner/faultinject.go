//go:build faultinject

package runner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

// Action is one of the six fault kinds the DSL accepts. Values are the wire
// strings used in --inject specs and in the master plan API Contracts (line
// 376).
type Action string

const (
	ActionCorrupt          Action = "corrupt"
	ActionKill             Action = "kill"
	ActionMutateSource     Action = "mutate-source"
	ActionUnmount          Action = "unmount"
	ActionDiskFull         Action = "disk-full"
	ActionPermissionDenied Action = "permission-denied"
)

// Point names a documented Hook call site inside the runner phase code. The
// wire string equals the phase string passed in HookArgs.Phase at that site;
// Hook's matching predicate is plain string equality (see hookMatches).
type Point string

const (
	PointT1PreRsync  Point = "T1-pre"
	PointT1Progress  Point = "T1"
	PointT1Post      Point = "T1-post"
	PointT2PreHash   Point = "T2-pre"
	PointT2PerFile   Point = "T2"
	PointT3PreUnlink Point = "T3-pre"
	PointT3PerFile   Point = "T3"
)

// Fault is one parsed --inject spec.
//
// AfterPct in [1,100] triggers when (BytesDone*100)/BytesTotal >= AfterPct.
// AfterCount > 0 triggers when FilesDone >= AfterCount. Both are one-shot:
// once the fault fires the active-list entry's armed bit clears and Hook
// stops firing for it. AfterPct and AfterCount are mutually exclusive at
// Parse time.
type Fault struct {
	Action     Action
	Phase      string
	File       string
	AfterPct   int
	AfterCount int
}

// HookArgs is what each instrumented site passes to Hook so the predicates
// in the active fault list can be evaluated.
//
// Path-discovery scheme (design decision, see Task 28 brief): HookArgs
// carries DestRoot and SourceRoot. Action helpers that touch files build
// the absolute path via filepath.Join(<root>, CurrentFile). This keeps
// Fault parse-time-pure and lets the runner thread roots through once at
// Activate time / per-phase. Both roots are optional; if an action that
// requires a root is asked to fire with an empty root, the helper returns
// a descriptive error.
type HookArgs struct {
	Phase       string
	CurrentFile string
	FilesDone   int
	FilesTotal  int
	BytesDone   int64
	BytesTotal  int64
	DestRoot    string
	SourceRoot  string
}

var (
	// ErrFaultinjectStripped is the loud refusal the release stub returns
	// when callers pass --inject specs to a release binary. Defined in both
	// build variants so callers compile either way.
	ErrFaultinjectStripped = errors.New("faultinject stripped from release build")

	// ErrFaultKill is returned by Hook when a kill fault matches. The runner
	// treats this as a fatal-process simulation: the test process does NOT
	// call os.Exit so tests can assert the would-have-killed code path
	// without crashing themselves. The default killAction below returns
	// this sentinel; tests may replace killAction via SetKillActionForTest.
	ErrFaultKill = errors.New("faultinject: kill fired")

	// ErrFaultDiskFull is returned after the disk-full helper has filled the
	// destination volume's free space with a sentinel file. Test code uses
	// it to confirm the simulation completed before invoking cleanup.
	ErrFaultDiskFull = errors.New("faultinject: disk-full simulated")
)

// ErrInvalidSpec wraps Parse rejections so callers can switch on the typed
// error and surface the offending spec verbatim.
type ErrInvalidSpec struct {
	Spec   string
	Reason string
}

func (e *ErrInvalidSpec) Error() string {
	return fmt.Sprintf("faultinject: invalid spec %q: %s", e.Spec, e.Reason)
}

// faultinjectBuildTagPresent is the sentinel symbol the release-gate grep
// (`make verify-release` -> `go tool nm | grep -E '(^|[._/])faultinject'`)
// keys on. Its identifier deliberately contains the substring "faultinject"
// in lowercase so the gate can prove this file was linked. Reassigned at
// init time to defeat dead-code elimination; otherwise the linker could
// drop an unused exported var on -s -w builds.
var faultinjectBuildTagPresent = "active" //nolint:unused

func init() { faultinjectBuildTagPresent = "armed" }

var knownActions = map[string]Action{
	"corrupt":           ActionCorrupt,
	"kill":              ActionKill,
	"mutate-source":     ActionMutateSource,
	"unmount":           ActionUnmount,
	"disk-full":         ActionDiskFull,
	"permission-denied": ActionPermissionDenied,
}

// Parse parses one or more --inject spec strings into Faults. Validation
// rules per master plan API Contracts (line 376) and the Task 28 brief:
// known action, known keywords, after_pct in [1,100], after_count > 0,
// after_pct and after_count mutually exclusive, non-empty phase.
func Parse(specs []string) ([]Fault, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]Fault, 0, len(specs))
	for _, spec := range specs {
		f, err := parseOne(spec)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func parseOne(spec string) (Fault, error) {
	// Split on ':'. macOS APFS/HFS+ paths cannot contain ':' (it is the
	// legacy HFS path separator and Finder rejects it), so the file=<path>
	// value is safe under this split. If FlashBackup ever supports
	// alternate filesystems where ':' is legal in filenames, this parser
	// needs a more careful tokenizer.
	parts := strings.Split(spec, ":")
	if len(parts) < 2 {
		return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: "must be action:key=value[:key=value...]"}
	}
	actionStr := parts[0]
	action, ok := knownActions[actionStr]
	if !ok {
		return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("unknown action %q", actionStr)}
	}
	f := Fault{Action: action}
	pctSet := false
	countSet := false
	phaseSet := false
	for _, kv := range parts[1:] {
		eq := strings.IndexByte(kv, '=')
		if eq < 0 {
			return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("missing '=' in %q", kv)}
		}
		k := kv[:eq]
		v := kv[eq+1:]
		switch k {
		case "phase":
			if v == "" {
				return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: "phase cannot be empty"}
			}
			f.Phase = v
			phaseSet = true
		case "file":
			f.File = v
		case "after_pct":
			n, err := strconv.Atoi(v)
			if err != nil {
				return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("after_pct %q not an int", v)}
			}
			if n < 1 || n > 100 {
				return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("after_pct %d out of range [1,100]", n)}
			}
			f.AfterPct = n
			pctSet = true
		case "after_count":
			n, err := strconv.Atoi(v)
			if err != nil {
				return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("after_count %q not an int", v)}
			}
			if n <= 0 {
				return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("after_count %d must be > 0", n)}
			}
			f.AfterCount = n
			countSet = true
		default:
			return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: fmt.Sprintf("unknown keyword %q", k)}
		}
	}
	if !phaseSet {
		return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: "phase=<...> is required"}
	}
	if pctSet && countSet {
		return Fault{}, &ErrInvalidSpec{Spec: spec, Reason: "after_pct and after_count are mutually exclusive"}
	}
	return f, nil
}

// armedFault is the package-private active-list entry. The armed bit is
// what makes AfterPct / AfterCount one-shot. armed is read and written
// under activeMu.
type armedFault struct {
	Fault
	armed bool
}

var (
	activeMu sync.Mutex
	active   []*armedFault

	// killActionMu / killAction are split out so tests can swap the
	// terminating behaviour with a non-terminating stub. Default returns
	// ErrFaultKill so production-shaped tests can assert the sentinel.
	killActionMu sync.Mutex
	killAction   = defaultKillAction

	cleanupMu sync.Mutex
	cleanups  []func() error
)

func defaultKillAction(_ context.Context, _ HookArgs) error {
	return ErrFaultKill
}

// SetKillActionForTest swaps the kill helper. Returns the previous helper so
// tests can restore it via t.Cleanup. Not part of the stable API; only the
// faultinject-tagged test files call this.
func SetKillActionForTest(fn func(context.Context, HookArgs) error) func(context.Context, HookArgs) error {
	killActionMu.Lock()
	prev := killAction
	killAction = fn
	killActionMu.Unlock()
	return prev
}

// RegisterCleanup queues a cleanup function. permission-denied and disk-full
// use this so test fixtures (chmod 0000 files, fillers on the test volume)
// can be undone in t.Cleanup. Cleanups run LIFO via RunCleanups.
func RegisterCleanup(fn func() error) {
	cleanupMu.Lock()
	cleanups = append(cleanups, fn)
	cleanupMu.Unlock()
}

// RunCleanups invokes every registered cleanup function in LIFO order and
// clears the list. The first error is returned but every cleanup is still
// attempted (best-effort), matching the semantics tests need to restore
// fixture state regardless of partial failure.
func RunCleanups() error {
	cleanupMu.Lock()
	fns := cleanups
	cleanups = nil
	cleanupMu.Unlock()
	var firstErr error
	for i := len(fns) - 1; i >= 0; i-- {
		if err := fns[i](); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Activate replaces the active fault list. Calling Activate(nil) disables
// all faults. Each fault enters the active list armed; firing clears the
// armed bit (one-shot per Hook brief).
func Activate(faults []Fault) {
	activeMu.Lock()
	defer activeMu.Unlock()
	if len(faults) == 0 {
		active = nil
		return
	}
	active = make([]*armedFault, len(faults))
	for i := range faults {
		active[i] = &armedFault{Fault: faults[i], armed: true}
	}
}

// Hook is called at each instrumented site. It walks the active fault list
// in input order, runs the predicates, and executes the first matching
// fault's action helper. Ctx cancellation is honoured before any work.
func Hook(ctx context.Context, _ Point, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	activeMu.Lock()
	// snapshot the matching armed fault so the action runs outside the lock
	var fired *armedFault
	for _, af := range active {
		if !af.armed {
			continue
		}
		if !hookMatches(af.Fault, args) {
			continue
		}
		af.armed = false
		fired = af
		break
	}
	activeMu.Unlock()
	if fired == nil {
		return nil
	}
	return dispatch(ctx, fired.Fault, args)
}

func hookMatches(f Fault, args HookArgs) bool {
	if f.Phase != args.Phase {
		return false
	}
	if f.File != "" && f.File != args.CurrentFile {
		return false
	}
	if f.AfterPct > 0 {
		if args.BytesTotal <= 0 {
			return false
		}
		if (args.BytesDone*100)/args.BytesTotal < int64(f.AfterPct) {
			return false
		}
	}
	if f.AfterCount > 0 {
		if args.FilesDone < f.AfterCount {
			return false
		}
	}
	return true
}

func dispatch(ctx context.Context, f Fault, args HookArgs) error {
	switch f.Action {
	case ActionKill:
		killActionMu.Lock()
		fn := killAction
		killActionMu.Unlock()
		return fn(ctx, args)
	case ActionCorrupt:
		return corruptAction(ctx, f, args)
	case ActionMutateSource:
		return mutateSourceAction(ctx, f, args)
	case ActionPermissionDenied:
		return permissionDeniedAction(ctx, f, args)
	case ActionUnmount:
		return unmountAction(ctx, args)
	case ActionDiskFull:
		return diskFullAction(ctx, args)
	default:
		return fmt.Errorf("faultinject: dispatch missing action %q", f.Action)
	}
}

func targetDestPath(args HookArgs, file string) (string, error) {
	if args.DestRoot == "" {
		return "", errors.New("faultinject: HookArgs.DestRoot empty; cannot resolve dest path")
	}
	if file == "" {
		return "", errors.New("faultinject: file selector empty; cannot resolve dest path")
	}
	return filepath.Join(args.DestRoot, file), nil
}

func targetSourcePath(args HookArgs, file string) (string, error) {
	if args.SourceRoot == "" {
		return "", errors.New("faultinject: HookArgs.SourceRoot empty; cannot resolve source path")
	}
	if file == "" {
		return "", errors.New("faultinject: file selector empty; cannot resolve source path")
	}
	return filepath.Join(args.SourceRoot, file), nil
}

func corruptAction(ctx context.Context, f Fault, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file := f.File
	if file == "" {
		file = args.CurrentFile
	}
	p, err := targetDestPath(args, file)
	if err != nil {
		return err
	}
	fh, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("faultinject corrupt open: %w", err)
	}
	defer fh.Close()
	var b [1]byte
	if _, err := fh.ReadAt(b[:], 0); err != nil {
		return fmt.Errorf("faultinject corrupt read: %w", err)
	}
	b[0] ^= 0xFF
	if _, err := fh.WriteAt(b[:], 0); err != nil {
		return fmt.Errorf("faultinject corrupt write: %w", err)
	}
	return nil
}

func mutateSourceAction(ctx context.Context, f Fault, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file := f.File
	if file == "" {
		file = args.CurrentFile
	}
	p, err := targetSourcePath(args, file)
	if err != nil {
		return err
	}
	// Append a byte so size and mtime both change; the T3 source-mutation
	// gate (invariant #8) keys on (size, mtime_ns) so either delta is enough.
	fh, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return fmt.Errorf("faultinject mutate-source open: %w", err)
	}
	defer fh.Close()
	if _, err := fh.Write([]byte{0xAB}); err != nil {
		return fmt.Errorf("faultinject mutate-source write: %w", err)
	}
	return nil
}

func permissionDeniedAction(ctx context.Context, f Fault, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	file := f.File
	if file == "" {
		file = args.CurrentFile
	}
	p, err := targetDestPath(args, file)
	if err != nil {
		return err
	}
	info, err := os.Stat(p)
	if err != nil {
		return fmt.Errorf("faultinject permission-denied stat: %w", err)
	}
	prevMode := info.Mode().Perm()
	if err := os.Chmod(p, 0); err != nil {
		return fmt.Errorf("faultinject permission-denied chmod: %w", err)
	}
	RegisterCleanup(func() error { return os.Chmod(p, prevMode) })
	return nil
}

func unmountAction(ctx context.Context, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if args.DestRoot == "" {
		return errors.New("faultinject unmount: HookArgs.DestRoot empty")
	}
	// /sbin/umount is the documented OS X path; avoid $PATH lookup so a
	// hostile PATH on the test volume can't redirect us. -f skips the busy
	// check, which is what we need to simulate yanked-USB mid-run.
	cmd := exec.CommandContext(ctx, "/sbin/umount", "-f", args.DestRoot)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("faultinject unmount: %w (output: %s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func diskFullAction(ctx context.Context, args HookArgs) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if args.DestRoot == "" {
		return errors.New("faultinject disk-full: HookArgs.DestRoot empty")
	}
	var st syscall.Statfs_t
	if err := syscall.Statfs(args.DestRoot, &st); err != nil {
		return fmt.Errorf("faultinject disk-full statfs: %w", err)
	}
	free := int64(st.Bavail) * int64(st.Bsize)
	if free < 0 {
		free = 0
	}
	sentinel := filepath.Join(args.DestRoot, ".faultinject_disk_full")
	fh, err := os.OpenFile(sentinel, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("faultinject disk-full create: %w", err)
	}
	RegisterCleanup(func() error { return os.Remove(sentinel) })
	if free > 0 {
		if err := fh.Truncate(free); err != nil {
			fh.Close()
			return fmt.Errorf("faultinject disk-full truncate: %w", err)
		}
	}
	if err := fh.Close(); err != nil {
		return fmt.Errorf("faultinject disk-full close: %w", err)
	}
	return ErrFaultDiskFull
}
