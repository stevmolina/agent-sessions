package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
	_ "modernc.org/sqlite"
)

const SchemaVersion = "3"

const schema = `
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE IF NOT EXISTS sessions (
 path TEXT PRIMARY KEY, id TEXT NOT NULL, source TEXT NOT NULL, provider TEXT NOT NULL,
 provider_instance_id TEXT, provider_session_id TEXT, project_id TEXT,
 origin_path TEXT, record_id TEXT, cwd TEXT, model TEXT, branch TEXT, worktree_path TEXT,
 archived INTEGER NOT NULL DEFAULT 0, started_at TEXT, updated_at TEXT,
 mtime REAL NOT NULL, size INTEGER NOT NULL, revision TEXT, title TEXT, parent_id TEXT
);
CREATE INDEX IF NOT EXISTS idx_sessions_id ON sessions(id);
CREATE INDEX IF NOT EXISTS idx_sessions_source ON sessions(source);
CREATE INDEX IF NOT EXISTS idx_sessions_provider ON sessions(provider);
CREATE INDEX IF NOT EXISTS idx_sessions_provider_session ON sessions(provider, provider_session_id);
CREATE TABLE IF NOT EXISTS session_aliases (
 canonical TEXT NOT NULL, alias TEXT NOT NULL, match_kind TEXT NOT NULL,
 PRIMARY KEY (canonical, alias)
);
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
	version, err := schemaVersion(db)
	if err != nil {
		db.Close()
		return nil, err
	}
	if version != "" && version != "2" && version != SchemaVersion {
		db.Close()
		return nil, fmt.Errorf("unsupported sessions schema version %q; remove the index and rebuild it", version)
	}
	if version == "2" {
		if err := migrateV2toV3(db); err != nil {
			db.Close()
			return nil, err
		}
		version = SchemaVersion
	}
	if _, err = db.Exec(schema); err != nil {
		db.Close()
		return nil, err
	}
	if err := ensureV3Columns(db); err != nil {
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

func schemaVersion(db *sql.DB) (string, error) {
	var version string
	err := db.QueryRow("SELECT value FROM meta WHERE key='schema_version'").Scan(&version)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil && strings.Contains(err.Error(), "no such table") {
		return "", nil
	}
	return version, err
}

func migrateV2toV3(db *sql.DB) error {
	if err := ensureV3Columns(db); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sessions SET provider=source WHERE provider IS NULL OR provider=''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sessions SET origin_path=path WHERE origin_path IS NULL OR origin_path=''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sessions SET record_id=id WHERE record_id IS NULL OR record_id=''`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE sessions SET revision=printf('%s:%s', mtime, size) WHERE revision IS NULL OR revision=''`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS session_aliases (canonical TEXT NOT NULL, alias TEXT NOT NULL, match_kind TEXT NOT NULL, PRIMARY KEY (canonical, alias))`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_provider ON sessions(provider)`); err != nil {
		return err
	}
	if _, err := db.Exec(`CREATE INDEX IF NOT EXISTS idx_sessions_provider_session ON sessions(provider, provider_session_id)`); err != nil {
		return err
	}
	if _, err := db.Exec(`UPDATE meta SET value=? WHERE key='schema_version'`, SchemaVersion); err != nil {
		return err
	}
	return nil
}

func ensureV3Columns(db *sql.DB) error {
	cols, err := sessionColumns(db)
	if err != nil {
		if strings.Contains(err.Error(), "no such table") {
			return nil
		}
		return err
	}
	if len(cols) == 0 {
		return nil
	}
	for _, col := range []struct{ name, def string }{
		{"provider", "TEXT"},
		{"provider_instance_id", "TEXT"},
		{"provider_session_id", "TEXT"},
		{"project_id", "TEXT"},
		{"origin_path", "TEXT"},
		{"record_id", "TEXT"},
		{"model", "TEXT"},
		{"branch", "TEXT"},
		{"worktree_path", "TEXT"},
		{"archived", "INTEGER NOT NULL DEFAULT 0"},
		{"revision", "TEXT"},
	} {
		if !cols[col.name] {
			if _, err := db.Exec("ALTER TABLE sessions ADD COLUMN " + col.name + " " + col.def); err != nil {
				return err
			}
		}
	}
	return nil
}

func sessionColumns(db *sql.DB) (map[string]bool, error) {
	rows, err := db.Query(`PRAGMA table_info(sessions)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt sql.NullString
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return nil, err
		}
		out[name] = true
	}
	return out, rows.Err()
}

type IndexedMeta struct {
	MTime    float64
	Size     int64
	Revision string
}

func Indexed(db queryer) (map[string]IndexedMeta, error) {
	rows, err := db.Query("SELECT path,mtime,size,revision FROM sessions")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]IndexedMeta{}
	for rows.Next() {
		var p string
		var m float64
		var z int64
		var rev sql.NullString
		if err := rows.Scan(&p, &m, &z, &rev); err != nil {
			return nil, err
		}
		out[p] = IndexedMeta{MTime: m, Size: z, Revision: rev.String}
	}
	return out, rows.Err()
}

type queryer interface {
	Query(string, ...any) (*sql.Rows, error)
}

func Upsert(tx *sql.Tx, s *model.Session) error {
	provider := s.Provider
	if provider == "" {
		provider = s.Source
	}
	origin := s.OriginPath
	if origin == "" {
		origin = s.Path
	}
	record := s.RecordID
	if record == "" {
		record = s.ID
	}
	archived := 0
	if s.Archived {
		archived = 1
	}
	_, err := tx.Exec(`INSERT INTO sessions(path,id,source,provider,provider_instance_id,provider_session_id,project_id,origin_path,record_id,cwd,model,branch,worktree_path,archived,started_at,updated_at,mtime,size,revision,title,parent_id) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(path) DO UPDATE SET id=excluded.id,source=excluded.source,provider=excluded.provider,provider_instance_id=excluded.provider_instance_id,provider_session_id=excluded.provider_session_id,project_id=excluded.project_id,origin_path=excluded.origin_path,record_id=excluded.record_id,cwd=excluded.cwd,model=excluded.model,branch=excluded.branch,worktree_path=excluded.worktree_path,archived=excluded.archived,started_at=excluded.started_at,updated_at=excluded.updated_at,mtime=excluded.mtime,size=excluded.size,revision=excluded.revision,title=excluded.title,parent_id=excluded.parent_id`,
		s.Path, s.ID, s.Source, provider, s.ProviderInstanceID, s.ProviderSessionID, nullEmpty(s.ProjectID), origin, record, s.CWD, s.Model, s.Branch, s.WorktreePath, archived, s.StartedAt, s.UpdatedAt, s.MTime, s.Size, s.Revision, s.Title, s.ParentID)
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
	if _, err := tx.Exec("DELETE FROM session_aliases WHERE canonical=? OR alias=?", path, path); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM sessions WHERE path=?", path); err != nil {
		return err
	}
	_, err := tx.Exec("DELETE FROM sessions_fts WHERE path=?", path)
	return err
}

func Lookup(db *sql.DB, id string) ([]model.Row, error) {
	if strings.HasPrefix(id, "t3code://") || strings.Contains(id, "/") {
		rows, err := queryRows(db, "SELECT "+rowColumns+" FROM sessions WHERE path=?", id)
		if err != nil {
			return nil, err
		}
		if len(rows) > 0 {
			return rows, nil
		}
	}
	src := ""
	lookup := id
	if a, b, ok := strings.Cut(id, ":"); ok && source.ValidSource(a) {
		src = a
		lookup = b
	}
	q := "SELECT " + rowColumns + " FROM sessions WHERE id=?"
	args := []any{lookup}
	if src != "" {
		q = "SELECT " + rowColumns + " FROM sessions WHERE source=? AND id=?"
		args = []any{src, lookup}
	}
	return queryRows(db, q, args...)
}

const rowColumns = `id,source,provider,cwd,started_at,updated_at,path,title,parent_id,provider_instance_id,provider_session_id,origin_path,record_id,model,branch,worktree_path,archived,revision`

func queryRows(db *sql.DB, q string, args ...any) ([]model.Row, error) {
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.Row
	for rows.Next() {
		r, err := scanRow(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func scanRow(rows *sql.Rows) (model.Row, error) {
	var r model.Row
	var archived int
	err := rows.Scan(&r.ID, &r.Source, &r.Provider, &r.CWD, &r.StartedAt, &r.UpdatedAt, &r.Path, &r.Title, &r.ParentID, &r.ProviderInstanceID, &r.ProviderSessionID, &r.OriginPath, &r.RecordID, &r.Model, &r.Branch, &r.WorktreePath, &archived, &r.Revision)
	r.Archived = archived != 0
	return r, err
}

type Hit struct {
	model.Row
	Snippet string
}

type SearchOpts struct {
	Query     string
	Source    string
	Provider  string
	CWD       string
	Limit     int
	AllCopies bool
}

func Search(db *sql.DB, query, sourceFilter, cwd string, limit int) ([]Hit, error) {
	return SearchOptsQuery(db, SearchOpts{Query: query, Source: sourceFilter, CWD: cwd, Limit: limit})
}

func SearchOptsQuery(db *sql.DB, opts SearchOpts) ([]Hit, error) {
	text := strings.Join(strings.Fields(opts.Query), " ")
	fts := `"` + strings.ReplaceAll(text, `"`, `""`) + `"`
	q := `SELECT s.id,s.source,s.provider,s.cwd,s.started_at,s.updated_at,s.path,s.title,s.parent_id,s.provider_instance_id,s.provider_session_id,s.origin_path,s.record_id,s.model,s.branch,s.worktree_path,s.archived,s.revision,snippet(sessions_fts,1,'','',' … ',24) FROM sessions_fts JOIN sessions s ON s.path=sessions_fts.path WHERE sessions_fts MATCH ?`
	args := []any{fts}
	if opts.Source != "" {
		q += " AND s.source=?"
		args = append(args, opts.Source)
	}
	if opts.Provider != "" {
		q += " AND s.provider=?"
		args = append(args, opts.Provider)
	}
	if opts.CWD != "" {
		q += " AND (s.cwd=? OR s.cwd LIKE ?)"
		args = append(args, opts.CWD, strings.TrimRight(opts.CWD, "/")+"/%")
	}
	fetch := opts.Limit
	if fetch <= 0 {
		fetch = 20
	}
	collapse := !opts.AllCopies && opts.Source == ""
	if collapse {
		fetch = 10000
	}
	q += " ORDER BY rank LIMIT ?"
	args = append(args, fetch)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Hit
	for rows.Next() {
		var h Hit
		var archived int
		if err := rows.Scan(&h.ID, &h.Source, &h.Provider, &h.CWD, &h.StartedAt, &h.UpdatedAt, &h.Path, &h.Title, &h.ParentID, &h.ProviderInstanceID, &h.ProviderSessionID, &h.OriginPath, &h.RecordID, &h.Model, &h.Branch, &h.WorktreePath, &archived, &h.Revision, &h.Snippet); err != nil {
			return nil, err
		}
		h.Archived = archived != 0
		out = append(out, h)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if collapse {
		aliases, err := exactAliasMap(db)
		if err != nil {
			return nil, err
		}
		out = collapseHits(out, aliases)
	}
	if opts.Limit > 0 && len(out) > opts.Limit {
		out = out[:opts.Limit]
	}
	return out, nil
}

func exactAliasMap(db *sql.DB) (map[string]string, error) {
	rows, err := db.Query(`SELECT canonical, alias FROM session_aliases WHERE match_kind='exact'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var canonical, alias string
		if err := rows.Scan(&canonical, &alias); err != nil {
			return nil, err
		}
		out[alias] = canonical
	}
	return out, rows.Err()
}

func collapseHits(hits []Hit, aliases map[string]string) []Hit {
	present := map[string]bool{}
	for _, h := range hits {
		present[h.Path] = true
	}
	var out []Hit
	for _, h := range hits {
		canonical, ok := aliases[h.Path]
		if ok && present[canonical] {
			continue
		}
		out = append(out, h)
	}
	return out
}

func RebuildAliases(tx *sql.Tx) error {
	if _, err := tx.Exec(`DELETE FROM session_aliases`); err != nil {
		return err
	}
	_, err := tx.Exec(`
INSERT INTO session_aliases(canonical, alias, match_kind)
SELECT t.path, n.path, 'exact'
FROM sessions t
JOIN sessions n ON n.source = t.provider AND n.id = t.provider_session_id AND n.source != 't3code'
WHERE t.source='t3code'
  AND t.provider_session_id IS NOT NULL AND t.provider_session_id != ''
  AND (t.provider, t.provider_session_id) IN (
    SELECT provider, provider_session_id FROM sessions
    WHERE source='t3code' AND provider_session_id IS NOT NULL AND provider_session_id != ''
    GROUP BY provider, provider_session_id
    HAVING COUNT(*) = 1
  )
  AND (n.source, n.id) IN (
    SELECT source, id FROM sessions
    WHERE source != 't3code'
    GROUP BY source, id
    HAVING COUNT(*) = 1
  )`)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`
INSERT INTO session_aliases(canonical, alias, match_kind)
SELECT t.path, n.path, 'inferred'
FROM sessions t
JOIN sessions n ON n.source = t.provider AND n.source != 't3code'
WHERE t.source='t3code'
  AND t.cwd IS NOT NULL AND n.cwd IS NOT NULL AND t.cwd = n.cwd
  AND t.title IS NOT NULL AND n.title IS NOT NULL AND t.title != '' AND t.title = n.title
  AND NOT EXISTS (SELECT 1 FROM session_aliases a WHERE a.canonical=t.path AND a.alias=n.path)
  AND (
    (t.started_at IS NOT NULL AND n.started_at IS NOT NULL AND n.started_at BETWEEN datetime(t.started_at, '-2 hours') AND datetime(t.updated_at, '+2 hours'))
    OR (t.updated_at IS NOT NULL AND n.updated_at IS NOT NULL AND n.updated_at BETWEEN datetime(t.started_at, '-2 hours') AND datetime(t.updated_at, '+2 hours'))
  )`)
	return err
}

func nullEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}
