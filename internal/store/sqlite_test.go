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
	if len(hits) != 1 || hits[0].ID != "one" || hits[0].Snippet == "" {
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
CREATE VIRTUAL TABLE sessions_fts USING fts5(path UNINDEXED,body,title,tokenize='unicode61 remove_diacritics 2');`
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
	tx, _ := db.Begin()
	if err := Upsert(tx, &model.Session{ID: "go", Source: "codex", Path: "/go", Body: "cross runtime", MTime: 123.125, Size: 9}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	var id string
	var mtime float64
	if err := db.QueryRow("SELECT id,mtime FROM sessions").Scan(&id, &mtime); err != nil {
		t.Fatal(err)
	}
	if id != "go" || mtime != 123.125 {
		t.Fatalf("id=%q mtime=%v", id, mtime)
	}
	var check string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("quick_check=%q err=%v", check, err)
	}
}
