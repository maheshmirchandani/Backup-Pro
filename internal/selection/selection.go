package selection

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"golang.org/x/text/unicode/norm"
)

// Candidate is one file selected for backup, captured during T1 enumeration.
// (Size, MtimeNS) form the source-side mutation-detection signature consumed
// at T3 per invariant #2; a re-stat at T3 that disagrees flips the file to
// `source_mutated`. RelativePath is NFC-normalized per invariant #32 and uses
// forward slashes for cross-platform readability of the manifest.
//
// AbsolutePath is preserved in its raw (non-canonicalized) byte form so I/O
// against the source filesystem works regardless of which Unicode form the
// kernel actually stored on disk. The two-track design (raw on disk, NFC in
// the manifest) is deliberate: invariant #32 binds manifest identity, not
// filesystem identity.
type Candidate struct {
	RelativePath string // NFC-normalized; forward slashes
	AbsolutePath string // raw bytes for I/O (do not normalize)
	Size         int64
	MtimeNS      int64
	Mode         uint32 // os.FileMode bits incl. ModeSymlink (preserves type)
}

// Options configures Walk.
//
// Includes/Excludes use `filepath.Match` glob syntax against the BASENAME
// (no `**`). An empty Includes means "include everything not excluded";
// patterns are validated upstream by `internal/profiles` (Task 9's strict
// allowlist) so the walker assumes well-formed patterns.
//
// FollowSymlinks is typically false: the spec preserves symlinks via rsync,
// so the walker captures the symlink itself rather than its target. The
// option exists for future tooling (e.g., a `--dereference` flag) but is
// not exercised by v0.1.
type Options struct {
	SourceRoot     string
	Includes       []string
	Excludes       []string
	FollowSymlinks bool
}

// Result is what Walk returns. CollidingPaths records pairs of source paths
// whose NFC normalization collides (e.g., one stored NFC, one stored NFD);
// per invariant #32 BOTH are reported and NEITHER is included in Candidates
// (the walker cannot safely choose between them). Skipped records the raw
// relative paths excluded by an Excludes pattern, for operator visibility.
type Result struct {
	Candidates     []Candidate
	Skipped        []string
	CollidingPaths []string
}

// Walk traverses opts.SourceRoot, applies the include/exclude filters,
// canonicalizes each selected path to NFC, detects duplicate normalized
// paths, and returns the set. Honors ctx cancellation between entries.
//
// Errors:
//   - Source root does not exist or is not a directory: returned wrapped.
//   - Context cancelled mid-walk: returned wrapped (errors.Is checks pass).
//   - Per-entry Lstat failures: returned wrapped (the walker is conservative
//     here; a partial enumeration on transient I/O errors would corrupt the
//     T3 mutation gate).
func Walk(ctx context.Context, opts Options) (*Result, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("selection walk: %w", err)
	}

	// Resolve the source root (defense vs. caller passing a symlink as the
	// root). EvalSymlinks here resolves the ROOT only; symlinks WITHIN the
	// walk are still not followed unless opts.FollowSymlinks is true.
	rootAbs, err := filepath.Abs(opts.SourceRoot)
	if err != nil {
		return nil, fmt.Errorf("resolve source root %q: %w", opts.SourceRoot, err)
	}
	resolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("resolve source root %q: %w", opts.SourceRoot, err)
	}
	rootInfo, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat source root %q: %w", opts.SourceRoot, err)
	}
	if !rootInfo.IsDir() {
		return nil, fmt.Errorf("source root %q is not a directory", opts.SourceRoot)
	}

	// collector tracks accepted candidates by normalized rel path and detects
	// duplicates per invariant #32. Extracted into a small struct so the
	// collision logic is unit-testable independently of filesystem behavior
	// (some macOS volume roles collapse NFC/NFD twins at write time, which
	// makes an end-to-end test of the collision branch dependent on volume
	// configuration outside our control).
	collector := newCandidateCollector()

	result := &Result{
		Candidates:     make([]Candidate, 0, 64),
		Skipped:        make([]string, 0),
		CollidingPaths: make([]string, 0),
	}

	walkErr := filepath.WalkDir(resolved, func(path string, d fs.DirEntry, walkInErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkInErr != nil {
			return fmt.Errorf("walk %q: %w", path, walkInErr)
		}

		// Compute the raw relative path. The root entry itself has rel=="."
		// (or "" after our trim); skip it per invariant: Candidates are
		// FILES under the root, not the root.
		relRaw, err := filepath.Rel(resolved, path)
		if err != nil {
			return fmt.Errorf("relpath %q: %w", path, err)
		}
		if relRaw == "." {
			return nil
		}

		// Use Lstat so symlinks are captured as symlinks (not their target).
		// DirEntry.Info() would also work and Go's WalkDir already lstat'd
		// the entry, but Info() returns a cached value; calling Lstat keeps
		// the source-of-truth single (we control when the syscall happens).
		info, err := os.Lstat(path)
		if err != nil {
			return fmt.Errorf("lstat %q: %w", path, err)
		}

		mode := info.Mode()

		// Skip directory entries: they appear implicitly via their children.
		if mode.IsDir() {
			return nil
		}

		// Symlink handling: when FollowSymlinks=false (the v0.1 default),
		// the symlink itself becomes a Candidate; Size/MtimeNS come from
		// Lstat (the link's own metadata, not the target). WalkDir does
		// not descend through symlinked directories without help, so the
		// no-follow branch needs no additional control flow here.
		//
		// Other irregular file types (pipes, sockets, devices) are out of
		// scope for a user-data backup. Skip silently; the runner-level
		// event log surfaces "skipped non-regular" if a consumer cares.
		if !mode.IsRegular() && mode&os.ModeSymlink == 0 {
			return nil
		}

		// Apply Excludes against the basename. A match means the file is
		// excluded; record in Skipped and continue.
		base := filepath.Base(path)
		excluded, err := matchAny(opts.Excludes, base)
		if err != nil {
			return fmt.Errorf("match excludes for %q: %w", base, err)
		}
		if excluded {
			result.Skipped = append(result.Skipped, toForwardSlash(relRaw))
			return nil
		}

		// Apply Includes if non-empty: file must match at least one include.
		if len(opts.Includes) > 0 {
			included, err := matchAny(opts.Includes, base)
			if err != nil {
				return fmt.Errorf("match includes for %q: %w", base, err)
			}
			if !included {
				return nil
			}
		}

		// Build relative path with forward slashes for the manifest.
		relFwd := toForwardSlash(relRaw)

		// NFC canonicalization (invariant #32). Apply to the full relative
		// path; the raw path stays in AbsolutePath for actual I/O.
		relNFC := norm.NFC.String(relFwd)

		cand := Candidate{
			RelativePath: relNFC,
			AbsolutePath: path,
			Size:         info.Size(),
			MtimeNS:      info.ModTime().UnixNano(),
			Mode:         uint32(mode),
		}

		collector.add(relNFC, relFwd, cand)
		return nil
	})
	if walkErr != nil {
		// Surface context cancellation with the standard error chain so
		// errors.Is(err, context.Canceled) works at the caller.
		if errors.Is(walkErr, context.Canceled) || errors.Is(walkErr, context.DeadlineExceeded) {
			return nil, fmt.Errorf("selection walk: %w", walkErr)
		}
		return nil, walkErr
	}

	result.Candidates = collector.candidates()
	result.CollidingPaths = collector.collisions()

	sort.Slice(result.Candidates, func(i, j int) bool {
		return result.Candidates[i].RelativePath < result.Candidates[j].RelativePath
	})
	sort.Strings(result.Skipped)
	sort.Strings(result.CollidingPaths)

	return result, nil
}

// candidateCollector accumulates candidates indexed by NFC-normalized rel
// path, demoting a normalized key to the "colliding" set if a second raw
// form maps to the same normalized key. Its existence as a separate type
// is purely to make the collision branch testable without depending on
// filesystem normalization behavior.
type candidateCollector struct {
	// rawForms tracks, per normalized key, the set of distinct raw rel
	// paths seen. >1 entry means the key is in collision.
	rawForms map[string]map[string]struct{}
	// accepted holds Candidates not yet known to be in collision. On the
	// first observed collision the entry is removed.
	accepted map[string]Candidate
}

func newCandidateCollector() *candidateCollector {
	return &candidateCollector{
		rawForms: make(map[string]map[string]struct{}),
		accepted: make(map[string]Candidate),
	}
}

// add records a new entry. The first raw form seen for a normalized key
// is accepted as a Candidate; a second distinct raw form for the same key
// demotes BOTH raw forms to the colliding set and removes the accepted
// Candidate. Subsequent entries with the same normalized key continue to
// accumulate raw forms (so three-way NFC/NFD/etc. collisions are reported
// fully).
func (c *candidateCollector) add(normKey, rawRel string, cand Candidate) {
	forms, exists := c.rawForms[normKey]
	if !exists {
		forms = make(map[string]struct{}, 1)
		c.rawForms[normKey] = forms
	}
	forms[rawRel] = struct{}{}
	if len(forms) == 1 {
		c.accepted[normKey] = cand
		return
	}
	// >=2 distinct raw forms: drop from accepted; reported via collisions().
	delete(c.accepted, normKey)
}

func (c *candidateCollector) candidates() []Candidate {
	out := make([]Candidate, 0, len(c.accepted))
	for _, cand := range c.accepted {
		out = append(out, cand)
	}
	return out
}

func (c *candidateCollector) collisions() []string {
	out := make([]string, 0)
	for _, forms := range c.rawForms {
		if len(forms) < 2 {
			continue
		}
		for raw := range forms {
			out = append(out, raw)
		}
	}
	return out
}

// matchAny returns true if `name` matches any of the glob patterns. Patterns
// are filepath.Match syntax; an invalid pattern returns a wrapped error.
func matchAny(patterns []string, name string) (bool, error) {
	for _, p := range patterns {
		ok, err := filepath.Match(p, name)
		if err != nil {
			return false, fmt.Errorf("bad pattern %q: %w", p, err)
		}
		if ok {
			return true, nil
		}
	}
	return false, nil
}

// toForwardSlash converts a filepath-separator path to forward-slash form.
// On macOS the separator is already '/', so this is a no-op there; the
// helper exists so the manifest stays portable for any future reader.
func toForwardSlash(p string) string {
	return filepath.ToSlash(p)
}
