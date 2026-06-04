package profiles

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// SchemaVersion is the profile JSON schema version (invariant #13).
const SchemaVersion = 1

// MaxProfilesFileBytes caps how much of profiles.json will be parsed.
// Defends against DoS via a maliciously huge profile file. 1 MB is
// generously above any realistic use case (hundreds of profiles).
const MaxProfilesFileBytes = 1 << 20 // 1 MB

// MaxPatternLen is the per-pattern character cap.
const MaxPatternLen = 256

// allowedGlobChars defines the strict allowlist for include/exclude
// patterns. Replaces the original over-permissive filepath.Match(pat, "")
// check. Per multi-hat security review: filepath.Match accepts patterns
// (e.g., "../../*") that pass to rsync with different semantics, enabling
// confused-deputy attacks. The allowlist explicitly forbids:
//   - `..` (path traversal)
//   - leading `/` (absolute paths)
//   - NUL bytes
//   - `**` (Go stdlib doesn't support it, rsync does, semantic mismatch)
//   - anything outside [a-zA-Z0-9 . _ * ? / -]
var allowedGlobChars = regexp.MustCompile(`^[a-zA-Z0-9._*?/\-]+$`)

// Store provides CRUD over the profiles.json file at a fixed path.
type Store struct {
	path string
}

// NewStore returns a Store at the given path. Creates the parent directory
// with mode 0700 if missing (the .flashbackup dir contains an HMAC key in
// version.json; restrictive mode protects siblings).
func NewStore(path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create profiles dir: %w", err)
	}
	return &Store{path: path}, nil
}

func (s *Store) load() (*ProfilesDoc, error) {
	f, err := os.Open(s.path)
	if os.IsNotExist(err) {
		return &ProfilesDoc{V: SchemaVersion, Profiles: nil}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open profiles.json: %w", err)
	}
	defer f.Close()
	// Cap reads at MaxProfilesFileBytes to defend against DoS.
	lr := io.LimitReader(f, MaxProfilesFileBytes+1)
	data, err := io.ReadAll(lr)
	if err != nil {
		return nil, fmt.Errorf("read profiles.json: %w", err)
	}
	if len(data) > MaxProfilesFileBytes {
		return nil, fmt.Errorf("profiles.json exceeds %d bytes (cap)", MaxProfilesFileBytes)
	}
	var doc ProfilesDoc
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parse profiles.json: %w", err)
	}
	return &doc, nil
}

func (s *Store) save(doc *ProfilesDoc) error {
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profiles.json: %w", err)
	}
	// Local atomic write helper (write-then-rename + parent-dir fsync) keeps
	// this package a leaf without importing internal/state.
	return writeAtomic(s.path, data, 0644)
}

// writeAtomic mirrors state.WriteTmpThenRename. Kept local to avoid
// importing internal/state into a leaf package.
//
// TODO(plan2): consolidate this duplicate with state.WriteTmpThenRename
// by moving the helper into a new internal/atomic (or internal/fsutil)
// leaf package that both internal/state and internal/profiles can import.
func writeAtomic(path string, data []byte, mode os.FileMode) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return fmt.Errorf("create tmp: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return fmt.Errorf("write tmp: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return fmt.Errorf("fsync tmp: %w", err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("close tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return fmt.Errorf("open parent dir for fsync: %w", err)
	}
	defer dir.Close()
	if err := dir.Sync(); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	return nil
}

// Upsert inserts or replaces a profile by name. Patterns validated first.
func (s *Store) Upsert(p Profile) error {
	if err := ValidateProfile(p); err != nil {
		return err
	}
	p.V = SchemaVersion
	doc, err := s.load()
	if err != nil {
		return err
	}
	replaced := false
	for i, ex := range doc.Profiles {
		if ex.Name == p.Name {
			doc.Profiles[i] = p
			replaced = true
			break
		}
	}
	if !replaced {
		doc.Profiles = append(doc.Profiles, p)
	}
	sort.Slice(doc.Profiles, func(i, j int) bool { return doc.Profiles[i].Name < doc.Profiles[j].Name })
	if doc.V == 0 {
		doc.V = SchemaVersion
	}
	return s.save(doc)
}

// Get returns the profile with the given name, or an error if not found.
func (s *Store) Get(name string) (Profile, error) {
	doc, err := s.load()
	if err != nil {
		return Profile{}, err
	}
	for _, p := range doc.Profiles {
		if p.Name == name {
			return p, nil
		}
	}
	return Profile{}, fmt.Errorf("profile %q not found", name)
}

// List returns all stored profiles (alphabetical by name, as persisted).
func (s *Store) List() ([]Profile, error) {
	doc, err := s.load()
	if err != nil {
		return nil, err
	}
	return doc.Profiles, nil
}

// Delete removes the profile with the given name. Returns an error if not found.
func (s *Store) Delete(name string) error {
	doc, err := s.load()
	if err != nil {
		return err
	}
	out := make([]Profile, 0, len(doc.Profiles))
	found := false
	for _, p := range doc.Profiles {
		if p.Name == name {
			found = true
			continue
		}
		out = append(out, p)
	}
	if !found {
		return fmt.Errorf("delete profile %q: not found", name)
	}
	doc.Profiles = out
	return s.save(doc)
}

// ValidateProfile checks the profile's name, source, and pattern syntax.
// Patterns are validated strictly via the allowlist (NOT via the
// over-permissive filepath.Match(pat, "") check from the original design).
func ValidateProfile(p Profile) error {
	if p.Name == "" {
		return fmt.Errorf("profile name is empty")
	}
	if p.Source == "" {
		return fmt.Errorf("profile %q: source is empty", p.Name)
	}
	for i, pat := range p.Includes {
		if err := validatePattern(pat); err != nil {
			return fmt.Errorf("profile %q: include[%d]=%q invalid: %w", p.Name, i, pat, err)
		}
	}
	for i, pat := range p.Excludes {
		if err := validatePattern(pat); err != nil {
			return fmt.Errorf("profile %q: exclude[%d]=%q invalid: %w", p.Name, i, pat, err)
		}
	}
	return nil
}

func validatePattern(pat string) error {
	if pat == "" {
		return fmt.Errorf("empty pattern")
	}
	if len(pat) > MaxPatternLen {
		return fmt.Errorf("pattern exceeds %d chars", MaxPatternLen)
	}
	if strings.Contains(pat, "\x00") {
		return fmt.Errorf("pattern contains NUL byte")
	}
	if strings.HasPrefix(pat, "/") {
		return fmt.Errorf("pattern must not start with /")
	}
	if strings.Contains(pat, "..") {
		return fmt.Errorf("pattern must not contain ..")
	}
	if strings.Contains(pat, "**") {
		return fmt.Errorf("pattern must not contain ** (use multiple lines instead)")
	}
	if !allowedGlobChars.MatchString(pat) {
		return fmt.Errorf("pattern contains disallowed characters; allowed: a-z A-Z 0-9 . _ * ? / -")
	}
	if _, err := filepath.Match(pat, ""); err != nil {
		return fmt.Errorf("pattern syntax invalid: %w", err)
	}
	return nil
}
