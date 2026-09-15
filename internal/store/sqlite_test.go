package store

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/usuario/sessions/internal/model"
)

func TestFTS5Smoke(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, _ := db.Begin()
	title := "Café title"
	if err := Upsert(tx, &model.Session{ID: "one", Source: "codex", Path: "/one", Title: title, Body: "distinctive café phrase", MTime: 1.25, Size: 12}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(db, "cafe phrase", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "one" || hits[0].Snippet == "" || hits[0].Provider != "codex" {
		t.Fatalf("unexpected hits: %#v", hits)
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check=%q err=%v", check, err)
	}
}

func TestExistingV2DatabaseCompatibility(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "compat.sqlite")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	legacySchema := `
CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL);
INSERT INTO meta VALUES('schema_version','2');
CREATE TABLE sessions(path TEXT PRIMARY KEY,id TEXT NOT NULL,source TEXT NOT NULL,cwd TEXT,started_at TEXT,updated_at TEXT,mtime REAL NOT NULL,size INTEGER NOT NULL,title TEXT,parent_id TEXT);
CREATE INDEX idx_sessions_id ON sessions(id);
CREATE INDEX idx_sessions_source ON sessions(source);
CREATE VIRTUAL TABLE sessions_fts USING fts5(path UNINDEXED,body,title,tokenize='unicode61 remove_diacritics 2');
INSERT INTO sessions(path,id,source,cwd,started_at,updated_at,mtime,size,title,parent_id) VALUES('/old','old-id','codex','/tmp','2026-01-01T00:00:00Z','2026-01-01T00:00:00Z',123.125,9,'old title',NULL);
INSERT INTO sessions_fts(path,body,title) VALUES('/old','preserved distinctive phrase quartz-legacy-001','old title');`
	if _, err := legacy.Exec(legacySchema); err != nil {
		legacy.Close()
		t.Fatal(err)
	}
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var version string
	if err := db.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != SchemaVersion {
		t.Fatalf("version=%q", version)
	}
	hits, err := Search(db, "quartz-legacy-001", "", "", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != "old-id" || hits[0].Provider != "codex" {
		t.Fatalf("fts after migrate: %#v", hits)
	}
	tx, _ := db.Begin()
	if err := Upsert(tx, &model.Session{ID: "go", Source: "codex", Path: "/go", Body: "cross runtime", MTime: 123.125, Size: 9}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var id string
	var mtime float64
	if err := db.QueryRow("SELECT id,mtime FROM sessions WHERE path='/go'").Scan(&id, &mtime); err != nil {
		t.Fatal(err)
	}
	if id != "go" || mtime != 123.125 {
		t.Fatalf("id=%q mtime=%v", id, mtime)
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check=%q err=%v", check, err)
	}
	db.Close()
	again, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	again.Close()
}

func TestUnsupportedSchemaVersionIsNonDestructive(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "bad.sqlite")
	legacy, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.Exec(`CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('schema_version','99'); CREATE TABLE sessions(path TEXT PRIMARY KEY);`); err != nil {
		t.Fatal(err)
	}
	legacy.Close()
	_, err = Open(dbPath)
	if err == nil {
		t.Fatal("expected error")
	}
	check, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer check.Close()
	var version, dummy string
	if err := check.QueryRow(`SELECT value FROM meta WHERE key='schema_version'`).Scan(&version); err != nil {
		t.Fatal(err)
	}
	if version != "99" {
		t.Fatalf("rewrote schema version to %q", version)
	}
	if err := check.QueryRow(`SELECT name FROM sqlite_master WHERE name='sessions'`).Scan(&dummy); err != nil {
		t.Fatal(err)
	}
}

func TestExactAliasCollapseAndFilters(t *testing.T) {
	db, err := Open(filepath.Join(t.TempDir(), "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tx, _ := db.Begin()
	native := &model.Session{ID: "abc", Source: "codex", Provider: "codex", Path: "/native/abc.jsonl", OriginPath: "/native/abc.jsonl", Body: "shared phrase quartz-link-001 native extra", Title: "shared phrase quartz-link-001", CWD: str("/tmp/p")}
	t3 := &model.Session{ID: "thread-1", Source: "t3code", Provider: "codex", Path: "t3code://env/thread-1", OriginPath: "/t3/state.sqlite", RecordID: "thread-1", ProviderSessionID: str("abc"), Body: "shared phrase quartz-link-001 t3 extra", Title: "shared phrase quartz-link-001", CWD: str("/tmp/p")}
	ambiguousNative := &model.Session{ID: "dup-id", Source: "codex", Provider: "codex", Path: "/native/dup1.jsonl", Body: "ambiguous phrase quartz-amb-001 a", Title: "a"}
	ambiguousNative2 := &model.Session{ID: "dup-id", Source: "codex", Provider: "codex", Path: "/native/dup2.jsonl", Body: "ambiguous phrase quartz-amb-001 b", Title: "b"}
	t3Amb := &model.Session{ID: "thread-amb", Source: "t3code", Provider: "codex", Path: "t3code://env/thread-amb", ProviderSessionID: str("dup-id"), Body: "ambiguous phrase quartz-amb-001 t3", Title: "c"}
	inferredNative := &model.Session{ID: "other", Source: "codex", Provider: "codex", Path: "/native/other.jsonl", Body: "probable phrase quartz-inf-001 native", Title: "probable phrase quartz-inf-001", CWD: str("/tmp/inf"), StartedAt: str("2026-08-26T02:00:00.000Z"), UpdatedAt: str("2026-08-26T02:10:00.000Z")}
	inferredT3 := &model.Session{ID: "thread-inf", Source: "t3code", Provider: "codex", Path: "t3code://env/thread-inf", Body: "probable phrase quartz-inf-001 t3", Title: "probable phrase quartz-inf-001", CWD: str("/tmp/inf"), StartedAt: str("2026-08-26T02:01:00.000Z"), UpdatedAt: str("2026-08-26T02:11:00.000Z")}
	for _, s := range []*model.Session{native, t3, ambiguousNative, ambiguousNative2, t3Amb, inferredNative, inferredT3} {
		if err := Upsert(tx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := RebuildAliases(tx); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	hits, err := SearchOptsQuery(db, SearchOpts{Query: "quartz-link-001", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "t3code" {
		t.Fatalf("default collapse: %#v", hits)
	}
	hits, err = SearchOptsQuery(db, SearchOpts{Query: "quartz-link-001", Source: "codex", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "codex" {
		t.Fatalf("source=codex should stay native: %#v", hits)
	}
	hits, err = SearchOptsQuery(db, SearchOpts{Query: "quartz-link-001", AllCopies: true, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("all-copies=%d", len(hits))
	}
	hits, err = SearchOptsQuery(db, SearchOpts{Query: "quartz-amb-001", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 3 {
		t.Fatalf("ambiguous should not collapse, got %d %#v", len(hits), hits)
	}
	hits, err = SearchOptsQuery(db, SearchOpts{Query: "quartz-inf-001", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 2 {
		t.Fatalf("inferred should remain visible, got %d", len(hits))
	}
	hits, err = SearchOptsQuery(db, SearchOpts{Query: "quartz-link-001", Provider: "codex", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].Source != "t3code" {
		t.Fatalf("provider=codex collapsed: %#v", hits)
	}
	var exact, inferred int
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_aliases WHERE match_kind='exact'`).Scan(&exact); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COUNT(*) FROM session_aliases WHERE match_kind='inferred'`).Scan(&inferred); err != nil {
		t.Fatal(err)
	}
	if exact != 1 {
		t.Fatalf("exact aliases=%d", exact)
	}
}

func str(v string) *string { return &v }
