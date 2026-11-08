// Package brainmap files a session under a brain (a knowledge repo id such as
// owner/repo) by the folder it ran in. The map is a small JSON file each person
// keeps on their own machines; nothing in it is a secret.
package brainmap

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rule files every session whose cwd matches one of Match under Brain.
type Rule struct {
	Brain string   `json:"brain"`
	Match []string `json:"match"`
}

// Map is the parsed brains.json. The zero value and a nil *Map file nothing.
type Map struct {
	Default string `json:"default"`
	Rules   []Rule `json:"rules"`
}

// Path is $SESSIONS_BRAINS_FILE, else ~/.config/sessions/brains.json.
func Path() string {
	if value := strings.TrimSpace(os.Getenv("SESSIONS_BRAINS_FILE")); value != "" {
		if value == "~" || strings.HasPrefix(value, "~/") {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, strings.TrimPrefix(value, "~"))
		}
		return value
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "sessions", "brains.json")
}

// Load reads the map at Path. A missing file is an empty map, not an error.
func Load() (*Map, error) { return LoadFile(Path()) }

// LoadFile reads one map. A missing file is an empty map; a malformed one is an error.
func LoadFile(path string) (*Map, error) {
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return &Map{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("brain map %s: %w", path, err)
	}
	m, err := Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("brain map %s: %w", path, err)
	}
	return m, nil
}

// Parse decodes and validates a map. Unknown fields are refused so a typo
// does not silently file everything under the default.
func Parse(raw []byte) (*Map, error) {
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	var m Map
	if err := dec.Decode(&m); err != nil {
		return nil, errors.New("not valid JSON for a brain map")
	}
	if dec.More() {
		return nil, errors.New("trailing data after the brain map")
	}
	m.Default = strings.TrimSpace(m.Default)
	for i := range m.Rules {
		rule := &m.Rules[i]
		rule.Brain = strings.TrimSpace(rule.Brain)
		if rule.Brain == "" {
			return nil, fmt.Errorf("rule %d has no brain", i+1)
		}
		if len(rule.Match) == 0 {
			return nil, fmt.Errorf("rule %d (%s) has no match patterns", i+1, rule.Brain)
		}
		for j, pattern := range rule.Match {
			pattern = strings.ReplaceAll(strings.TrimSpace(pattern), `\`, "/")
			if pattern == "" || pattern == "*" {
				return nil, fmt.Errorf("rule %d (%s) has an empty pattern", i+1, rule.Brain)
			}
			if strings.HasPrefix(pattern, "/") || (len(pattern) > 1 && pattern[1] == ':') {
				return nil, fmt.Errorf("rule %d (%s) pattern %q must be relative", i+1, rule.Brain, pattern)
			}
			rule.Match[j] = strings.TrimSuffix(pattern, "/")
		}
	}
	return &m, nil
}

// Brain returns the brain for a working directory, or "" for none. The first
// rule with a matching pattern wins; no match, or no cwd, means Default.
//
// A pattern ending in * matches when cwd contains "/" + the pattern without the
// star, so "worktrees/app-*" matches ".../worktrees/app-fix/sub". Any other
// pattern must match whole path segments: "engagements/acme" matches
// ".../engagements/acme" and ".../engagements/acme/repo", not ".../engagements/acme2".
func (m *Map) Brain(cwd string) string {
	if m == nil {
		return ""
	}
	cwd = strings.ReplaceAll(strings.TrimSpace(cwd), `\`, "/")
	if cwd == "" {
		return m.Default
	}
	for _, rule := range m.Rules {
		for _, pattern := range rule.Match {
			if matches(cwd, pattern) {
				return rule.Brain
			}
		}
	}
	return m.Default
}

func matches(cwd, pattern string) bool {
	if prefix, ok := strings.CutSuffix(pattern, "*"); ok {
		return strings.Contains(cwd, "/"+prefix)
	}
	return strings.Contains(cwd+"/", "/"+pattern+"/")
}
