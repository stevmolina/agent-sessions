package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usuario/sessions/internal/model"
	_ "modernc.org/sqlite"
)

const SchemaVersion = "2"
const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
 path TEXT PRIMARY KEY, id TEXT NOT NULL, source TEXT NOT NULL, cwd TEXT,
 started_at TEXT, updated_at TEXT, mtime REAL NOT NULL, size INTEGER NOT NULL,
 title TEXT, parent_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_id ON sessions(id);
CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source);
CREATE VIRTUAL TABLE IF NOT EXISTS sessions_fts USING fts5(
 path UNINDEXED, body, title, tokenize = 'unicode61 remove_diacritics 2'
);`

func Open(path string) (*sql.DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	ctx := context.Background()
	for _, pragma := range []string{"PRAGMA journal_mode=WAL", "PRAGMA foreign_keys=ON", "PRAGMA busy_timeout=5000"} {
		if _, err = db.ExecContext(ctx, pragma); err != nil {
			db.Close()
			return nil, err
		}
	}
	var version string
	err = db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version)
	if err == sql.ErrNoRows {
		version = ""
	} else if err != nil && !strings.Contains(err.Error(), "no such table") {
		db.Close()
		return nil, err
	}
	if version != "" && version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported sessions schema version %q; rebuild the index or use the Python CLI", version)
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if version == "" {
		if _, err = db.Exec("INSERT OR REPLACE INTO meta(key,value) VALUES('schema_version',?)", SchemaVersion); err != nil {
			db.Close()
			return nil, err
		}
	}
	return db, nil
}

func Indexed(db queryer) (map[string][2]float64, error) {
	rows, err := db.Query("SELECT path,mtime,size FROM sessions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string][2]float64{}
	for rows.Next() {
		var p string
		var m float64
		var z int64
		if err := rows.Scan(&p, &m, &z); err != nil {
			return nil, err
		}
		out[p] = [2]float64{m, float64(z)}
	}
	return out, rows.Err()
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func Upsert(tx *sql.Tx, s *model.Session) error {
	_, err := tx.Exec(`INSERT INTO sessions(path,id,source,cwd,started_at,updated_at,mtime,size,title,parent_id) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET id=excluded.id,source=excluded.source,cwd=excluded.cwd,started_at=excluded.started_at,updated_at=excluded.updated_at,mtime=excluded.mtime,size=excluded.size,title=excluded.title,parent_id=excluded.parent_id`, s.Path, s.ID, s.Source, s.CWD, s.StartedAt, s.UpdatedAt, s.MTime, s.Size, s.Title, s.ParentID)
	if err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions_fts WHERE path=?", s.Path); err != nil {
		return err
	}
	_, err = tx.Exec("INSERT INTO sessions_fts(path,body,title) VALUES(?,?,?)", s.Path, s.Body, s.Title)
	return err
}
func Delete(tx *sql.Tx, path string) error {
	if _, err := tx.Exec("DELETE FROM sessions WHERE path=?", path); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM sessions_fts WHERE path=?", path)
	return err
}

func Lookup(db *sql.DB, id string) ([]model.Row, error) {
	source := ""
	lookup := id
	if a, b, ok := strings.Cut(id, ":"); ok && (a == "cursor" || a == "claude" || a == "codex") {
		source = a
		lookup = b
	}
	q := "SELECT id,source,cwd,started_at,updated_at,path,title,parent_id FROM sessions WHERE id=?"
	args := []any{lookup}
	if source != "" {
		q = "SELECT id,source,cwd,started_at,updated_at,path,title,parent_id FROM sessions WHERE source=? AND id=?"
		args = []any{source, lookup}
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Row
	for rows.Next() {
		var r model.Row
		if err := rows.Scan(&r.ID, &r.Source, &r.CWD, &r.StartedAt, &r.UpdatedAt, &r.Path, &r.Title, &r.ParentID); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type Hit struct {
	model.Row
	Snippet string
}

func Search(db *sql.DB, query, source, cwd string, limit int) ([]Hit, error) {
	text := strings.Join(strings.Fields(query), " ")
	fts := `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	q := `SELECT s.id,s.source,s.cwd,s.started_at,s.updated_at,s.path,s.title,s.parent_id,snippet(sessions_fts,1,'','',' … ',24) FROM sessions_fts JOIN sessions s ON s.path=sessions_fts.path WHERE sessions_fts MATCH ?`
	args := []any{fts}
	if source != "" {
		q += " AND s.source=?"
		args = append(args, source)
	}
	if cwd != "" {
		q += " AND (s.cwd=? OR s.cwd LIKE ?)"
		args = append(args, cwd, strings.TrimRight(cwd, "/")+"/%")
	}
	q += " ORDER BY rank LIMIT ?"
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		if err := rows.Scan(&h.ID, &h.Source, &h.CWD, &h.StartedAt, &h.UpdatedAt, &h.Path, &h.Title, &h.ParentID, &h.Snippet); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
