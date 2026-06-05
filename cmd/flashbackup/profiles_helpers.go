package main

// profiles_helpers.go carries the JSON list emitter for the `profiles list
// --json` arm. Split from profiles.go for file-length hygiene (matches the
// backup / verify / status convention).

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/maheshmirchandani/Backup-Pro/internal/profiles"
)

// profileListItem is the on-the-wire shape of one entry in the
// `profiles list --json` output. The schema is intentionally a subset of
// profiles.Profile: we drop the schema-version field `v` because it is
// store-internal plumbing, and we replace omitempty on the slice fields with
// always-present (possibly empty) arrays so downstream JSON consumers can
// uniformly call .includes / .excludes without nil-checking.
type profileListItem struct {
	Name     string   `json:"name"`
	Source   string   `json:"source"`
	Includes []string `json:"includes"`
	Excludes []string `json:"excludes"`
}

// emitProfilesJSON marshals the profile list as a JSON array of
// profileListItem to w. An empty list emits `[]` (not `null`) so a
// downstream `jq length` always returns a number.
func emitProfilesJSON(w io.Writer, all []profiles.Profile) error {
	out := make([]profileListItem, 0, len(all))
	for _, p := range all {
		item := profileListItem{
			Name:     p.Name,
			Source:   p.Source,
			Includes: p.Includes,
			Excludes: p.Excludes,
		}
		if item.Includes == nil {
			item.Includes = []string{}
		}
		if item.Excludes == nil {
			item.Excludes = []string{}
		}
		out = append(out, item)
	}
	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal profiles: %w", err)
	}
	if _, err := w.Write(data); err != nil {
		return fmt.Errorf("write json: %w", err)
	}
	if _, err := fmt.Fprintln(w); err != nil {
		return fmt.Errorf("write newline: %w", err)
	}
	return nil
}
