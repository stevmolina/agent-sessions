package indexer

import (
	"database/sql"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/clean"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
)

// A secret in a message must not reach the index from Antigravity, Grok or Muse,
// the same as for Claude, Codex and Cursor. The title is built from the same
// text, so it must be clean too.
func TestNativeSourcesAreRedactedBeforeIndexing(t *testing.T) {
	root := t.TempDir()
	for _, key := range []string{"SESSIONS_CURSOR_ROOT", "SESSIONS_CLAUDE_ROOT", "SESSIONS_CODEX_ROOT", "SESSIONS_CODEX_ARCHIVE_ROOT", "SESSIONS_IMPORTS_ROOT"} {
		t.Setenv(key, filepath.Join(root, key))
	}
	t.Setenv("SESSIONS_T3_ROOT", filepath.Join(root, "t3"))
	t.Setenv("SESSIONS_T3_INDEXEDDB", filepath.Join(root, "missing-indexeddb"))
	ag, grok, muse := filepath.Join(root, "ag"), filepath.Join(root, "grok"), filepath.Join(root, "muse")
	t.Setenv("SESSIONS_ANTIGRAVITY_ROOT", ag)
	t.Setenv("SESSIONS_GROK_ROOT", grok)
	t.Setenv("SESSIONS_MUSE_ROOT", muse)

	token := "ghp_" + mixedToken(36)
	msgs := []source.NativeMsg{
		{Role: "user", Text: "Use this token " + token + " to push the branch."},
		{Role: "assistant", Text: "Pushed with " + token + "."},
	}
	if _, err := source.WriteAntigravityFixture(ag, "antigravity-cli", "/tmp/p", "ag-1", msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := source.WriteGrokFixture(grok, "/Users/u/dev/p", "grok-1", "", msgs); err != nil {
		t.Fatal(err)
	}
	if _, err := source.WriteMuseFixture(muse, "/Users/u/dev/p", "muse-1", msgs); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	c, err := clean.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Refresh(db, c)
	if err != nil || stats.Failed != 0 || stats.Upserted != 3 {
		t.Fatalf("refresh: %+v %v", stats, err)
	}
	rows, err := store.List(db)
	if err != nil || len(rows) != 3 {
		t.Fatalf("rows=%d err=%v", len(rows), err)
	}
	seen := map[string]bool{}
	for _, row := range rows {
		seen[row.Source] = true
		all := row.Title + "\n" + row.Body
		for _, turn := range row.Turns {
			all += "\n" + turn.Text
		}
		if strings.Contains(all, token) {
			t.Errorf("%s: secret survived in the index", row.Source)
		}
		if !strings.Contains(row.Body, "[REDACTED:github-pat]") {
			t.Errorf("%s: missing placeholder in body %q", row.Source, row.Body)
		}
	}
	for _, src := range []string{source.SourceAntigravity, source.SourceGrokCLI, source.SourceMuse} {
		if !seen[src] {
			t.Errorf("no %s row", src)
		}
	}
	var raw sql.NullString
	if err := db.QueryRow(`SELECT group_concat(body, ' ') FROM sessions_fts`).Scan(&raw); err != nil || strings.Contains(raw.String, token) {
		t.Fatalf("full-text index kept the secret: %v", err)
	}
}
