package source

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestT3DiscoverLoadAndFilters(t *testing.T) {
	dir := t.TempDir()
	_, err := WriteT3Fixture(dir, "env-a", []T3ThreadFixture{
		{
			ID: "thread-codex", Title: "T3 Codex distinctive phrase t3-codex-111",
			Provider: "codex", ProviderSession: "codex-fixture-1", CWD: "/tmp/codex-proj",
			Messages: []T3MessageFixture{
				{ID: "z-later", Role: "assistant", Text: "later", CreatedAt: "2026-08-26T03:00:02.000Z"},
				{ID: "a-earlier", Role: "user", Text: "T3 Codex distinctive phrase t3-codex-111", CreatedAt: "2026-08-26T03:00:01.000Z"},
			},
		},
		{
			ID: "thread-claude", Title: "T3 Claude distinctive phrase t3-claude-222",
			Provider: "claudeAgent", ProviderInstance: "claudeAgent", CWD: "/tmp/claude-proj",
			Messages: []T3MessageFixture{
				{Role: "user", Text: "T3 Claude distinctive phrase t3-claude-222"},
				{Role: "assistant", Text: "ok"},
				{Role: "system", Text: "ignore me"},
			},
		},
		{
			ID: "thread-cursor", Title: "T3 Cursor distinctive phrase t3-cursor-333",
			Provider: "cursor", CWD: "/tmp/cursor-proj",
			Messages: []T3MessageFixture{{Role: "user", Text: "T3 Cursor distinctive phrase t3-cursor-333"}},
		},
		{
			ID: "thread-archived", Title: "archived thread", Provider: "codex", ArchivedAt: "2026-08-26T04:00:00.000Z",
			Messages: []T3MessageFixture{{Role: "user", Text: "archived body"}},
		},
		{
			ID: "thread-deleted", Title: "deleted thread", Provider: "codex", DeletedAt: "2026-08-26T04:00:00.000Z",
			Messages: []T3MessageFixture{{Role: "user", Text: "deleted body"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", dir)
	cands, err := T3{}.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 4 {
		t.Fatalf("candidates=%d want 4 (deleted omitted)", len(cands))
	}
	byID := map[string]Candidate{}
	for _, c := range cands {
		byID[c.RecordID] = c
		if !strings.HasPrefix(c.Locator, "t3code://env-a/") {
			t.Fatalf("locator=%q", c.Locator)
		}
		if c.Source != "t3code" {
			t.Fatalf("source=%q", c.Source)
		}
	}
	if _, ok := byID["thread-deleted"]; ok {
		t.Fatal("deleted thread should not be discovered")
	}
	codex, err := T3{}.Load(byID["thread-codex"])
	if err != nil {
		t.Fatal(err)
	}
	if codex.Source != "t3code" || codex.Provider != "codex" {
		t.Fatalf("source=%s provider=%s", codex.Source, codex.Provider)
	}
	if codex.ProviderSessionID == nil || *codex.ProviderSessionID != "codex-fixture-1" {
		t.Fatalf("provider_session_id=%v", codex.ProviderSessionID)
	}
	if len(codex.Turns) < 2 || codex.Turns[0].Role != "user" || codex.Turns[1].Role != "assistant" {
		t.Fatalf("order=%#v", codex.Turns)
	}
	if strings.Contains(codex.Body, "ignore me") {
		t.Fatalf("system text leaked: %q", codex.Body)
	}
	claude, err := T3{}.Load(byID["thread-claude"])
	if err != nil {
		t.Fatal(err)
	}
	if claude.Provider != "claude" {
		t.Fatalf("claudeAgent should normalize to claude, got %q", claude.Provider)
	}
	if claude.ProviderInstanceID == nil || *claude.ProviderInstanceID != "claudeAgent" {
		t.Fatalf("instance=%v", claude.ProviderInstanceID)
	}
	archived, err := T3{}.Load(byID["thread-archived"])
	if err != nil {
		t.Fatal(err)
	}
	if !archived.Archived {
		t.Fatal("expected archived")
	}
}

func TestT3SameThreadIDTwoEnvironments(t *testing.T) {
	a := t.TempDir()
	b := t.TempDir()
	if _, err := WriteT3Fixture(a, "env-1", []T3ThreadFixture{{
		ID: "shared", Title: "one", Provider: "codex",
		Messages: []T3MessageFixture{{Role: "user", Text: "env one"}},
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteT3Fixture(b, "env-2", []T3ThreadFixture{{
		ID: "shared", Title: "two", Provider: "cursor",
		Messages: []T3MessageFixture{{Role: "user", Text: "env two"}},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", a)
	ca, err := T3{}.Discover()
	if err != nil || len(ca) != 1 {
		t.Fatalf("a: %v %#v", err, ca)
	}
	t.Setenv("SESSIONS_T3_ROOT", b)
	cb, err := T3{}.Discover()
	if err != nil || len(cb) != 1 {
		t.Fatalf("b: %v %#v", err, cb)
	}
	if ca[0].Locator == cb[0].Locator {
		t.Fatalf("locators collided: %s", ca[0].Locator)
	}
	if !strings.Contains(ca[0].Locator, "env-1") || !strings.Contains(cb[0].Locator, "env-2") {
		t.Fatalf("locators=%s %s", ca[0].Locator, cb[0].Locator)
	}
}

func TestT3UnsupportedSchema(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "state.sqlite")
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`CREATE TABLE not_threads(id TEXT)`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("SESSIONS_T3_ROOT", dir)
	_, err = T3{}.Discover()
	if err == nil || !strings.Contains(err.Error(), "unsupported t3code schema") {
		t.Fatalf("err=%v", err)
	}
}

func TestT3MissingColumn(t *testing.T) {
	dir := t.TempDir()
	db, err := sql.Open("sqlite", filepath.Join(dir, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`
CREATE TABLE projection_threads(thread_id TEXT PRIMARY KEY, title TEXT, created_at TEXT, updated_at TEXT, deleted_at TEXT);
CREATE TABLE projection_thread_messages(message_id TEXT PRIMARY KEY, thread_id TEXT, role TEXT, text TEXT, created_at TEXT);
CREATE TABLE projection_thread_sessions(thread_id TEXT PRIMARY KEY);
`)
	if err != nil {
		t.Fatal(err)
	}
	db.Close()
	t.Setenv("SESSIONS_T3_ROOT", dir)
	_, err = T3{}.Discover()
	if err == nil || !strings.Contains(err.Error(), "unsupported t3code schema") {
		t.Fatalf("err=%v", err)
	}
}

func TestT3WALRead(t *testing.T) {
	dir := t.TempDir()
	dbPath, err := WriteT3Fixture(dir, "wal-env", []T3ThreadFixture{{
		ID: "wal-thread", Title: "wal phrase", Provider: "codex",
		Messages: []T3MessageFixture{{Role: "user", Text: "wal phrase"}},
	}})
	if err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	if _, err := writer.Exec(`PRAGMA journal_mode=WAL`); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec(`UPDATE projection_threads SET title='wal phrase live' WHERE thread_id='wal-thread'`); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", dir)
	cands, err := T3{}.Discover()
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("cands=%d", len(cands))
	}
	s, err := T3{}.Load(cands[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.Title != "wal phrase live" {
		t.Fatalf("title=%q", s.Title)
	}
}

func TestT3IndexedDBDiagnostic(t *testing.T) {
	home := t.TempDir()
	idb := filepath.Join(home, "Library", "Application Support", "t3code", "IndexedDB", "t3code_app_0.indexeddb.leveldb")
	if err := os.MkdirAll(idb, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SESSIONS_T3_ROOT", "")
	t.Setenv("SESSIONS_T3_INDEXEDDB", idb)
	_, err := T3{}.Discover()
	if err == nil || !strings.Contains(err.Error(), "IndexedDB") || !strings.Contains(err.Error(), "Raw LevelDB is not indexed") {
		t.Fatalf("err=%v", err)
	}
}

func TestT3DeterministicMessageOrderSameTimestamp(t *testing.T) {
	dir := t.TempDir()
	ts := "2026-08-26T03:00:01.000Z"
	if _, err := WriteT3Fixture(dir, "ord", []T3ThreadFixture{{
		ID: "ord", Title: "ord", Provider: "codex",
		Messages: []T3MessageFixture{
			{ID: "m-b", Role: "assistant", Text: "second", CreatedAt: ts},
			{ID: "m-a", Role: "user", Text: "first", CreatedAt: ts},
		},
	}}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", dir)
	cands, err := T3{}.Discover()
	if err != nil {
		t.Fatal(err)
	}
	s, err := T3{}.Load(cands[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Turns) != 2 || s.Turns[0].Text != "first" || s.Turns[1].Text != "second" {
		t.Fatalf("turns=%#v", s.Turns)
	}
}
