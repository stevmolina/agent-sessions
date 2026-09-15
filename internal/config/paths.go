package config

import (
	"os"
	"path/filepath"
	"strings"
)

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
func T3Root() string { return envPath("SESSIONS_T3_ROOT", ".t3/userdata") }

func T3RootExplicit() bool { return os.Getenv("SESSIONS_T3_ROOT") != "" }

func IndexedDBPath() string {
	return envPath("SESSIONS_T3_INDEXEDDB", "Library/Application Support/t3code/IndexedDB/t3code_app_0.indexeddb.leveldb")
}
