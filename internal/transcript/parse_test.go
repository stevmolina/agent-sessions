package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "tests", "fixtures", name)
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}
func TestSharedFixtures(t *testing.T) {
	tests := []struct{ file, source, id, phrase string }{{"cursor.jsonl", "cursor", "cursor", "zebra-pluto-419"}, {"claude.jsonl", "claude", "claude-fixture-1", "mango-helix-882"}, {"codex.jsonl", "codex", "codex-fixture-1", "quartz-ember-553"}}
	for _, tt := range tests {
		t.Run(tt.source, func(t *testing.T) {
			s, err := Parse(strings.NewReader(fixture(t, tt.file)), tt.source, "/tmp/"+tt.id+".jsonl")
			if err != nil {
				t.Fatal(err)
			}
			if s.ID != tt.id {
				t.Fatalf("id=%q", s.ID)
			}
			if !strings.Contains(s.Body, tt.phrase) {
				t.Fatalf("missing phrase in %q", s.Body)
			}
			for _, forbidden := range []string{"secret-tool-dump", "tool_use", "function_call"} {
				if strings.Contains(s.Body, forbidden) {
					t.Fatalf("leaked %s", forbidden)
				}
			}
		})
	}
}

func TestCursorKeepsOnlyHumanTurns(t *testing.T) {
	s, err := Parse(strings.NewReader(fixture(t, "cursor.jsonl")), "cursor", "/tmp/cursor.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Cursor distinctive phrase zebra-pluto-419" {
		t.Fatalf("title=%q", s.Title)
	}
	if len(s.Turns) != 2 || s.Turns[0].Role != "user" || s.Turns[1].Role != "assistant" {
		t.Fatalf("turns=%#v", s.Turns)
	}
	if strings.Contains(Format(s, 200000), "tool_use") {
		t.Fatalf("formatted transcript leaked tool data: %q", Format(s, 200000))
	}
}

func TestCodexTitleUsesExplicitRequest(t *testing.T) {
	raw := `{"type":"session_meta","payload":{"id":"c1","cwd":"/tmp","timestamp":"t"}}` + "\n" +
		`{"type":"response_item","payload":{"type":"message","role":"user","content":[{"type":"input_text","text":"<in-app-browser-context source=\"x\">tabs</in-app-browser-context>\n## My request:\nReal question about widgets"}]}}` + "\n"
	s, err := Parse(strings.NewReader(raw), "codex", "/tmp/c1.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "Real question about widgets" {
		t.Fatalf("title=%q", s.Title)
	}
	if strings.Contains(s.Body, "in-app-browser-context") {
		t.Fatalf("body=%q", s.Body)
	}
}

func TestCursorCWDKeepsDashedSegments(t *testing.T) {
	actualCWD := filepath.Join(t.TempDir(), "project-with-dashes")
	if err := os.MkdirAll(actualCWD, 0o755); err != nil {
		t.Fatal(err)
	}
	cursorRoot := t.TempDir()
	t.Setenv("SESSIONS_CURSOR_ROOT", cursorRoot)
	slug := strings.ReplaceAll(strings.TrimPrefix(filepath.Clean(actualCWD), string(filepath.Separator)), string(filepath.Separator), "-")
	transcriptPath := filepath.Join(cursorRoot, slug, "agent-transcripts", "session", "session.jsonl")
	s, err := Parse(strings.NewReader(fixture(t, "cursor.jsonl")), "cursor", transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.CWD == nil || *s.CWD != actualCWD {
		t.Fatalf("cwd=%v want %q", s.CWD, actualCWD)
	}
}
func TestLargeLineAndUnicodeTitle(t *testing.T) {
	text := strings.Repeat("é", 200)
	raw := `{"role":"user","message":{"content":"<user_query>` + text + `</user_query>"}}` + "\n" + strings.Repeat(" ", 100000)
	s, err := Parse(strings.NewReader(raw), "cursor", "/tmp/x.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if len([]rune(s.Title)) != 160 || !strings.HasSuffix(s.Title, "…") {
		t.Fatalf("title runes=%d", len([]rune(s.Title)))
	}
}

func TestClaudeSubagentUsesAgentIDAndSessionIDAsParent(t *testing.T) {
	raw := `{"type":"user","agentId":"agent-42","sessionId":"parent-from-json","message":{"role":"user","content":"delegated work"}}` + "\n"
	s, err := Parse(strings.NewReader(raw), "claude", "/tmp/folder-parent/subagents/child.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "agent-42" {
		t.Fatalf("id=%q", s.ID)
	}
	if s.ParentID == nil || *s.ParentID != "parent-from-json" {
		t.Fatalf("parent_id=%v", s.ParentID)
	}
}
