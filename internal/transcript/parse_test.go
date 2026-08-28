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
