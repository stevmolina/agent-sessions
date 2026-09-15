package source

import (
	"regexp"
	"strings"
)

var providerSlug = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]*$`)

func NormalizeProvider(name string) string {
	n := strings.TrimSpace(name)
	if n == "" {
		return ""
	}
	switch strings.ToLower(n) {
	case "claude", "claudeagent", "claude-agent", "claude_agent":
		return "claude"
	case "codex":
		return "codex"
	case "cursor":
		return "cursor"
	case "grok":
		return "grok"
	case "opencode":
		return "opencode"
	default:
		return n
	}
}

func KnownSources() []string {
	return []string{"cursor", "claude", "codex", "t3code"}
}

func ValidSource(value string) bool {
	for _, s := range KnownSources() {
		if value == s {
			return true
		}
	}
	return false
}

func KnownProviders() []string {
	return []string{"cursor", "claude", "codex", "grok", "opencode"}
}

func ParseProvider(value string) (string, bool) {
	if !providerSlug.MatchString(value) {
		return "", false
	}
	return NormalizeProvider(value), true
}
