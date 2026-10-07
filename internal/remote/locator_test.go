package remote

import "testing"

func TestParseLocator(t *testing.T) {
	for _, tc := range []struct{ input, source, id string }{
		{"t3code:thread", "t3code", "thread"},
		{"codex:session", "codex", "session"},
		{"t3code://environment/thread", "", "t3code://environment/thread"},
		{"/transcripts/session.jsonl", "", "/transcripts/session.jsonl"},
		{"session", "", "session"},
	} {
		src, id := ParseLocator(tc.input)
		if src != tc.source || id != tc.id {
			t.Errorf("ParseLocator(%q) = %q, %q; want %q, %q", tc.input, src, id, tc.source, tc.id)
		}
	}
}
