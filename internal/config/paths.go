package config

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type Transcript struct{ Source, Path string }

func envPath(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return expand(value)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, fallback)
}
func expand(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return path
}
func IndexPath() string  { return envPath("SESSIONS_INDEX", ".cache/sessions/index.sqlite") }
func CursorRoot() string { return envPath("SESSIONS_CURSOR_ROOT", ".cursor/projects") }
func ClaudeRoot() string { return envPath("SESSIONS_CLAUDE_ROOT", ".claude/projects") }
func CodexRoot() string  { return envPath("SESSIONS_CODEX_ROOT", ".codex/sessions") }
func CodexArchiveRoot() string {
	return envPath("SESSIONS_CODEX_ARCHIVE_ROOT", ".codex/archived_sessions")
}

func Discover() []Transcript {
	var out []Transcript
	walk := func(root, source string, accept func(string) bool) {
		_ = filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			if entry.IsDir() {
				if source == "claude" && path != root && (entry.Name() == "memory" || entry.Name() == "debug" || entry.Name() == "tool-results") {
					return filepath.SkipDir
				}
				return nil
			}
			if filepath.Ext(path) == ".jsonl" && accept(path) {
				out = append(out, Transcript{source, path})
			}
			return nil
		})
	}
	walk(CursorRoot(), "cursor", func(path string) bool { return strings.Contains(filepath.ToSlash(path), "/agent-transcripts/") })
	walk(ClaudeRoot(), "claude", func(string) bool { return true })
	walk(CodexRoot(), "codex", func(string) bool { return true })
	walk(CodexArchiveRoot(), "codex", func(path string) bool { return filepath.Dir(path) == CodexArchiveRoot() })
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}
