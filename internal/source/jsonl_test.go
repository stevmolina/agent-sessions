package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJSONLDiscoverAndLoadFixtures(t *testing.T) {
	root := t.TempDir()
	cursor := filepath.Join(root, "cursor")
	claude := filepath.Join(root, "claude")
	codex := filepath.Join(root, "codex")
	copyJSONL(t, "cursor.jsonl", filepath.Join(cursor, "Users-tmp-proj", "agent-transcripts", "aaaa", "aaaa.jsonl"))
	copyJSONL(t, "claude.jsonl", filepath.Join(claude, "-tmp-proj", "claude-fixture-1.jsonl"))
	copyJSONL(t, "codex.jsonl", filepath.Join(codex, "2026", "08", "26", "rollout-codex-fixture-1.jsonl"))
	t.Setenv("SESSIONS_CURSOR_ROOT", cursor)
	t.Setenv("SESSIONS_CLAUDE_ROOT", claude)
	t.Setenv("SESSIONS_CODEX_ROOT", codex)
	t.Setenv("SESSIONS_CODEX_ARCHIVE_ROOT", filepath.Join(root, "archive"))
	_ = os.MkdirAll(filepath.Join(root, "archive"), 0o755)

	cands, err := JSONL{}.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("candidates=%d", len(cands))
	}
	bySource := map[string]Candidate{}
	for _, c := range cands {
		bySource[c.Source] = c
		if c.Locator != c.OriginPath || c.Revision == "" {
			t.Fatalf("bad candidate %#v", c)
		}
	}
	for _, sourceName := range []string{"cursor", "claude", "codex"} {
		c, ok := bySource[sourceName]
		if !ok {
			t.Fatalf("missing %s", sourceName)
		}
		s, err := JSONL{}.Load(c)
		if err != nil {
			t.Fatal(err)
		}
		if s.Source != sourceName || s.Provider != sourceName {
			t.Fatalf("%s source=%s provider=%s", sourceName, s.Source, s.Provider)
		}
		if s.Path != c.Locator {
			t.Fatalf("path=%q", s.Path)
		}
	}
}

func copyJSONL(t *testing.T, name, dest string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestJSONLUnknownSource(t *testing.T) {
	_, err := JSONL{}.Load(Candidate{Source: "nope", OriginPath: "/tmp/x.jsonl"})
	if err == nil || !strings.Contains(err.Error(), "unknown source") {
		t.Fatalf("err=%v", err)
	}
}
