// Package profiles persists user-defined backup filter configurations.
// A profile names a source path + include/exclude glob patterns.
//
// Profile patterns use stdlib filepath.Match syntax (`*`, `?`, `[seq]`).
// `**` is NOT supported (would behave differently in our walker vs rsync).
// Patterns are validated strictly at Upsert time; bad patterns are rejected
// before they reach rsync.
package profiles

// Profile is one saved filter configuration.
type Profile struct {
	V        int      `json:"v"` // schema version; always 1 in v0.1
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	Includes []string `json:"includes,omitempty"`
	Excludes []string `json:"excludes,omitempty"`
}

// ProfilesDoc is the on-disk shape of profiles.json. Named to avoid
// collision with os.File / io/fs.File / state.VersionFile.
type ProfilesDoc struct {
	V        int       `json:"v"`
	Profiles []Profile `json:"profiles"`
}
