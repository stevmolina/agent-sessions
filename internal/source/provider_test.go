package source

import "testing"

func TestNormalizeProvider(t *testing.T) {
	tests := map[string]string{
		"claudeAgent": "claude",
		"ClaudeAgent": "claude",
		"claude":      "claude",
		"codex":       "codex",
		"cursor":      "cursor",
		"grok":        "grok",
		"opencode":    "opencode",
		"ollama":      "ollama",
	}
	for in, want := range tests {
		if got := NormalizeProvider(in); got != want {
			t.Fatalf("NormalizeProvider(%q)=%q want %q", in, got, want)
		}
	}
}

func TestParseProvider(t *testing.T) {
	got, ok := ParseProvider("claudeAgent")
	if !ok || got != "claude" {
		t.Fatalf("got=%q ok=%v", got, ok)
	}
	if _, ok := ParseProvider("not a provider"); ok {
		t.Fatal("expected invalid")
	}
}
