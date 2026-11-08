package importer

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
)

type entry struct{ name, body string }

func buildZip(t *testing.T, entries ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		w, err := zw.Create(e.name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(e.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func readTestZip(t *testing.T, data []byte, lim Limits) (Result, error) {
	t.Helper()
	return ReadZipFrom(bytes.NewReader(data), int64(len(data)), lim)
}

func mustRead(t *testing.T, data []byte) Result {
	t.Helper()
	res, err := readTestZip(t, data, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func turnsEqual(t *testing.T, got []model.Turn, want ...string) {
	t.Helper()
	var flat []string
	for _, turn := range got {
		flat = append(flat, turn.Role+": "+turn.Text)
	}
	if strings.Join(flat, "|") != strings.Join(want, "|") {
		t.Fatalf("turns:\n got  %q\n want %q", flat, want)
	}
}

// A ChatGPT conversation with an edited first question: the old branch (n2,
// n3) must be dropped, and system, hidden, tool and image-only messages too.
const chatgptExport = `[
 {
  "title": "Planting tomatoes",
  "create_time": 1767225600.25,
  "update_time": 1767229200,
  "conversation_id": "conv-gpt-1",
  "id": "conv-gpt-1",
  "current_node": "n7",
  "mapping": {
   "root": {"id": "root", "parent": null, "children": ["n1"], "message": null},
   "n1": {"id": "n1", "parent": "root", "children": ["n2", "n4"], "message": {"author": {"role": "system"}, "content": {"content_type": "text", "parts": ["You are helpful"]}, "metadata": {"is_visually_hidden_from_conversation": true}}},
   "n2": {"id": "n2", "parent": "n1", "children": ["n3"], "message": {"author": {"role": "user"}, "content": {"content_type": "text", "parts": ["old question"]}, "recipient": "all"}},
   "n3": {"id": "n3", "parent": "n2", "children": [], "message": {"author": {"role": "assistant"}, "content": {"content_type": "text", "parts": ["old answer"]}, "recipient": "all"}},
   "n4": {"id": "n4", "parent": "n1", "children": ["n5"], "message": {"author": {"role": "user"}, "content": {"content_type": "multimodal_text", "parts": [{"content_type": "image_asset_pointer"}, "When do I plant tomatoes?"]}, "recipient": "all"}},
   "n5": {"id": "n5", "parent": "n4", "children": ["n6"], "message": {"author": {"role": "assistant"}, "content": {"content_type": "code", "text": "search('tomatoes')"}, "recipient": "browser"}},
   "n6": {"id": "n6", "parent": "n5", "children": ["n7"], "message": {"author": {"role": "tool"}, "content": {"content_type": "text", "parts": ["tool output"]}}},
   "n7": {"id": "n7", "parent": "n6", "children": [], "message": {"author": {"role": "assistant"}, "content": {"content_type": "text", "parts": ["After the last frost."]}, "recipient": "all"}}
  }
 },
 {"title": "Empty", "conversation_id": "conv-gpt-empty", "current_node": "r", "mapping": {"r": {"id": "r", "parent": null, "children": [], "message": null}}},
 {"title": "No id", "current_node": "a", "mapping": {"a": {"id": "a", "parent": null, "children": [], "message": {"author": {"role": "user"}, "content": {"content_type": "text", "parts": ["hi"]}}}}}
]`

func TestChatGPTFollowsCurrentBranch(t *testing.T) {
	res := mustRead(t, buildZip(t,
		entry{"chat.html", "<html></html>"},
		entry{"user.json", `{"email":"someone@example.com"}`},
		entry{"conversations.json", chatgptExport},
	))
	if len(res.Conversations) != 1 || res.Skipped[source.SourceChatGPT] != 2 {
		t.Fatalf("conversations=%d skipped=%v", len(res.Conversations), res.Skipped)
	}
	c := res.Conversations[0]
	if c.Source != source.SourceChatGPT || c.ID != "conv-gpt-1" || c.Title != "Planting tomatoes" {
		t.Fatalf("%+v", c)
	}
	if c.StartedAt != "2026-01-01T00:00:00Z" || c.UpdatedAt != "2026-01-01T01:00:00Z" {
		t.Fatalf("times %s %s", c.StartedAt, c.UpdatedAt)
	}
	turnsEqual(t, c.Turns, "user: When do I plant tomatoes?", "assistant: After the last frost.")
}

func TestChatGPTWithoutCurrentNodeTakesNewestLeaf(t *testing.T) {
	body := `[{"conversation_id": "conv-gpt-2", "mapping": {
	 "a": {"parent": null, "children": ["b", "c"], "message": {"author": {"role": "user"}, "content": {"content_type": "text", "parts": ["q"]}, "create_time": 10}},
	 "b": {"parent": "a", "children": [], "message": {"author": {"role": "assistant"}, "content": {"content_type": "text", "parts": ["first try"]}, "create_time": 11}},
	 "c": {"parent": "a", "children": [], "message": {"author": {"role": "assistant"}, "content": {"content_type": "text", "parts": ["second try"]}, "create_time": 12}}
	}}]`
	res := mustRead(t, buildZip(t, entry{"conversations-000.json", body}))
	if len(res.Conversations) != 1 {
		t.Fatalf("%+v", res)
	}
	turnsEqual(t, res.Conversations[0].Turns, "user: q", "assistant: second try")
}

const claudeExport = `[
 {
  "uuid": "11111111-2222-3333-4444-555555555555",
  "name": "Sourdough schedule",
  "created_at": "2026-02-03T10:00:00.123456Z",
  "updated_at": "2026-02-03T11:30:00+01:00",
  "chat_messages": [
   {"uuid": "m1", "sender": "human", "text": "How long should it proof?", "content": [{"type": "text", "text": "How long should it proof?"}], "created_at": "2026-02-03T10:00:00Z"},
   {"uuid": "m2", "sender": "assistant", "text": "flat text", "content": [{"type": "thinking", "thinking": "hmm"}, {"type": "tool_use", "name": "x", "input": {}}, {"type": "text", "text": "About 4 hours"}, {"type": "text", "text": "at room temperature."}]},
   {"uuid": "m3", "sender": "human", "text": "Thanks", "content": []},
   {"uuid": "m4", "sender": "assistant", "text": "", "content": [{"type": "text", "text": "  "}]}
  ]
 },
 {"uuid": "no-messages", "name": "Nothing", "chat_messages": []}
]`

func TestClaudeAI(t *testing.T) {
	res := mustRead(t, buildZip(t,
		entry{"users.json", `[]`},
		entry{"projects.json", `[]`},
		entry{"conversations.json", claudeExport},
	))
	if len(res.Conversations) != 1 || res.Skipped[source.SourceClaudeAI] != 1 {
		t.Fatalf("conversations=%d skipped=%v", len(res.Conversations), res.Skipped)
	}
	c := res.Conversations[0]
	if c.Source != source.SourceClaudeAI || c.ID != "11111111-2222-3333-4444-555555555555" || c.Title != "Sourdough schedule" {
		t.Fatalf("%+v", c)
	}
	if c.StartedAt != "2026-02-03T10:00:00Z" || c.UpdatedAt != "2026-02-03T10:30:00Z" {
		t.Fatalf("times %s %s", c.StartedAt, c.UpdatedAt)
	}
	turnsEqual(t, c.Turns,
		"user: How long should it proof?",
		"assistant: About 4 hours\nat room temperature.",
		"user: Thanks")
}

// Grok wraps conversations and responses, stores response times as BSON
// milliseconds, and links regenerated replies through parent_response_id.
const grokExport = `{
 "projects": [{"id": "p1"}],
 "conversations": [
  {
   "conversation": {"id": "grok-conv-1", "title": "Bike gears", "create_time": "2026-03-01T08:00:00.000000Z", "modify_time": "2026-03-01T08:05:00Z"},
   "responses": [
    {"response": {"_id": "r3", "parent_response_id": "r1", "sender": "ASSISTANT", "message": "regenerated answer", "create_time": {"$date": {"$numberLong": "1772352180000"}}}, "share_link": null},
    {"response": {"_id": "r1", "sender": "human", "message": "Which gear for hills?", "create_time": {"$date": {"$numberLong": "1772352060000"}}}, "share_link": null},
    {"response": {"_id": "r2", "parent_response_id": "r1", "sender": "assistant", "message": "first answer", "create_time": {"$date": {"$numberLong": "1772352120000"}}}},
    {"response": {"_id": "r4", "parent_response_id": "r3", "sender": "human", "message": "And downhill?", "create_time": {"$date": {"$numberLong": "1772352240000"}}}},
    {"response": {"_id": "r5", "parent_response_id": "r4", "sender": "grok-3", "message": "The hardest one.", "create_time": {"$date": {"$numberLong": "1772352300000"}}}}
   ]
  },
  {
   "conversation": {"id": "grok-conv-2", "title": "", "create_time": "2026-03-02T09:00:00Z"},
   "responses": [
    {"sender": "assistant", "message": "second", "create_time": 1772442060},
    {"sender": "human", "message": "first", "create_time": 1772442000}
   ]
  },
  {"conversation": {"title": "no id"}, "responses": [{"response": {"sender": "human", "message": "x"}}]},
  "not an object"
 ],
 "tasks": []
}`

func TestGrok(t *testing.T) {
	res := mustRead(t, buildZip(t,
		entry{"ttl/30d/export_data/0000/prod-mc-billing.json", `{}`},
		entry{"ttl/30d/export_data/0000/prod-grok-backend.json", grokExport},
	))
	if len(res.Conversations) != 2 || res.Skipped[source.SourceGrok] != 2 {
		t.Fatalf("conversations=%d skipped=%v", len(res.Conversations), res.Skipped)
	}
	c := res.Conversations[0]
	if c.Source != source.SourceGrok || c.ID != "grok-conv-1" || c.Title != "Bike gears" || c.StartedAt != "2026-03-01T08:00:00Z" || c.UpdatedAt != "2026-03-01T08:05:00Z" {
		t.Fatalf("%+v", c)
	}
	turnsEqual(t, c.Turns, "user: Which gear for hills?", "assistant: regenerated answer", "user: And downhill?", "assistant: The hardest one.")
	// No parent links: plain time order.
	turnsEqual(t, res.Conversations[1].Turns, "user: first", "assistant: second")
}

func TestFlexTime(t *testing.T) {
	cases := map[string]any{
		"2026-03-01T08:03:00Z":  map[string]any{"$date": map[string]any{"$numberLong": "1772352180000"}},
		"2026-03-01T08:03:00Z ": map[string]any{"$date": "2026-03-01T08:03:00Z"},
		"2026-01-01T00:00:00Z":  float64(1767225600),
		"2026-01-01T00:00:00Z ": float64(1767225600000),
		"":                      nil,
	}
	for want, in := range cases {
		if got := flexTime(in); got != strings.TrimSpace(want) {
			t.Errorf("flexTime(%v) = %q, want %q", in, got, want)
		}
	}
}

func TestZipSafety(t *testing.T) {
	data := buildZip(t,
		entry{"../evil", "planted"},
		entry{"../../conversations.json", claudeExport},
		entry{"/abs/evil.sh", "#!/bin/sh"},
	)
	cwd := t.TempDir()
	t.Chdir(cwd)
	res, err := readTestZip(t, data, DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Conversations) != 1 {
		t.Fatalf("conversations=%d", len(res.Conversations))
	}
	for _, p := range []string{filepath.Join(cwd, "..", "evil"), filepath.Join(cwd, "evil"), "/abs/evil.sh"} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s was written", p)
		}
	}
	// Storing uses ids, never entry names.
	root := t.TempDir()
	if _, err := Store(root, res); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(filepath.Join(root, source.SourceClaudeAI))
	if len(entries) != 1 || entries[0].Name() != "11111111-2222-3333-4444-555555555555.json" {
		t.Fatalf("stored %v", entries)
	}
}

func TestOversizedEntryRefused(t *testing.T) {
	big := buildZip(t, entry{"conversations.json", claudeExport})
	_, err := readTestZip(t, big, Limits{MaxEntry: 64, MaxTotal: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("err=%v", err)
	}
	// A header that lies about the size is caught while reading.
	var limitErr *limitError
	_, err = parseConversationsLimited(claudeExport, 64)
	if !errors.As(err, &limitErr) {
		t.Fatalf("err=%v", err)
	}
	// The total budget spans entries.
	two := buildZip(t, entry{"conversations.json", claudeExport}, entry{"prod-grok-backend.json", grokExport})
	_, err = readTestZip(t, two, Limits{MaxEntry: 1 << 20, MaxTotal: int64(len(claudeExport)) + 10})
	if err == nil || !strings.Contains(err.Error(), "total limit") {
		t.Fatalf("err=%v", err)
	}
}

func parseConversationsLimited(body string, max int64) (Result, error) {
	c := &collector{emit: func(source.Imported) error { return nil }}
	r := &limitedReader{r: strings.NewReader(body), max: max, name: "conversations.json", budget: &budget{}}
	return Result{}, parseConversations(r, c)
}

func TestNotAnExport(t *testing.T) {
	_, err := readTestZip(t, buildZip(t, entry{"notes.txt", "hello"}), DefaultLimits)
	if !errors.Is(err, ErrNoExport) {
		t.Fatalf("err=%v", err)
	}
	if _, err := ReadZipFrom(bytes.NewReader([]byte("not a zip")), 9, DefaultLimits); err == nil {
		t.Fatal("garbage should fail")
	}
}

func TestStoreIsIdempotent(t *testing.T) {
	root := t.TempDir()
	res := mustRead(t, buildZip(t, entry{"conversations.json", chatgptExport}))
	first, err := Store(root, res)
	if err != nil || len(first) != 1 || first[0].Imported != 1 || first[0].Skipped != 2 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	path := source.ImportedPath(root, res.Conversations[0])
	before, _ := os.Stat(path)
	again, err := Store(root, res)
	if err != nil || again[0].Imported != 0 || again[0].Unchanged != 1 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	after, _ := os.Stat(path)
	if !before.ModTime().Equal(after.ModTime()) {
		t.Fatal("an unchanged re-import rewrote the file")
	}
	res.Conversations[0].Title = "Renamed"
	updated, err := Store(root, res)
	if err != nil || updated[0].Imported != 1 {
		t.Fatalf("updated=%+v err=%v", updated, err)
	}
	if entries, _ := os.ReadDir(filepath.Join(root, source.SourceChatGPT)); len(entries) != 1 {
		t.Fatalf("re-import should overwrite in place, found %d files", len(entries))
	}
}

func TestImportZipStoresWhileParsing(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "export.zip")
	if err := os.WriteFile(file, buildZip(t, entry{"conversations.json", chatgptExport}), 0o600); err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(dir, "imports")
	got, err := ImportZip(file, root, DefaultLimits)
	if err != nil || len(got) != 1 || got[0].Source != source.SourceChatGPT || got[0].Imported != 1 || got[0].Skipped != 2 {
		t.Fatalf("got=%+v err=%v", got, err)
	}
	again, err := ImportZip(file, root, DefaultLimits)
	if err != nil || again[0].Imported != 0 || again[0].Unchanged != 1 {
		t.Fatalf("again=%+v err=%v", again, err)
	}
	if DefaultLimits.MaxEntry != 512<<20 || DefaultLimits.MaxTotal != 2<<30 {
		t.Fatalf("limits %+v", DefaultLimits)
	}
}
