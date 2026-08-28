package store

import (
	"os"
	"os/exec"
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

func TestPythonV2Compatibility(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 unavailable")
	}
	dbPath := filepath.Join(t.TempDir(), "compat.sqlite")
	script := `import sqlite3,sys
p=sys.argv[1]; c=sqlite3.connect(p)
c.executescript("""CREATE TABLE meta(key TEXT PRIMARY KEY,value TEXT NOT NULL); INSERT INTO meta VALUES('schema_version','2'); CREATE TABLE sessions(path TEXT PRIMARY KEY,id TEXT NOT NULL,source TEXT NOT NULL,cwd TEXT,started_at TEXT,updated_at TEXT,mtime REAL NOT NULL,size INTEGER NOT NULL,title TEXT,parent_id TEXT); CREATE INDEX idx_sessions_id ON sessions(id); CREATE INDEX idx_sessions_source ON sessions(source); CREATE VIRTUAL TABLE sessions_fts USING fts5(path UNINDEXED,body,title,tokenize='unicode61 remove_diacritics 2');""")
c.commit();c.execute("VACUUM INTO ?",(p+'.backup',));c.close()`
	cmd := exec.Command(python, "-c", script, dbPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python create: %v: %s", err, out)
	}
	db, err := Open(dbPath + ".backup")
	if err != nil {
		t.Fatal(err)
	}
	tx, _ := db.Begin()
	if err := Upsert(tx, &model.Session{ID: "go", Source: "codex", Path: "/go", Body: "cross runtime", MTime: 123.125, Size: 9}); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	db.Close()
	verify := `import sqlite3,sys
c=sqlite3.connect(sys.argv[1]); assert c.execute("select value from meta where key='schema_version'").fetchone()[0]=='2'; assert c.execute("select id,mtime from sessions").fetchone()==('go',123.125); assert c.execute('pragma quick_check').fetchone()[0]=='ok'`
	cmd = exec.Command(python, "-c", verify, dbPath+".backup")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("python reopen: %v: %s", err, out)
	}
	_ = os.Remove(dbPath)
}
