package remote

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/store"
	_ "modernc.org/sqlite"
)

// sqliteConn exercises the ownership SQL and synchronization against a real,
// isolated SQL database. Only PostgreSQL's JSON cast is removed for SQLite.
type sqliteConn struct{ db *sql.DB }

func (c sqliteConn) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	r, err := c.db.ExecContext(ctx, strings.ReplaceAll(strings.ReplaceAll(q, "::jsonb", ""), "now()", "CURRENT_TIMESTAMP"), args...)
	if err != nil {
		return pgconn.CommandTag{}, err
	}
	n, err := r.RowsAffected()
	return pgconn.NewCommandTag(fmt.Sprintf("UPDATE %d", n)), err
}
func (c sqliteConn) Query(ctx context.Context, q string, args ...any) (pgx.Rows, error) {
	r, err := c.db.QueryContext(ctx, strings.ReplaceAll(strings.ReplaceAll(q, "::jsonb", ""), "now()", "CURRENT_TIMESTAMP"), args...)
	if err != nil {
		return nil, err
	}
	return &sqliteRows{rows: r}, nil
}

type sqliteRows struct{ rows *sql.Rows }

func (r *sqliteRows) Close()                                       { r.rows.Close() }
func (r *sqliteRows) Err() error                                   { return r.rows.Err() }
func (r *sqliteRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *sqliteRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *sqliteRows) Next() bool                                   { return r.rows.Next() }
func (r *sqliteRows) Scan(dest ...any) error                       { return r.rows.Scan(dest...) }
func (r *sqliteRows) Values() ([]any, error)                       { return nil, fmt.Errorf("not used") }
func (r *sqliteRows) RawValues() [][]byte                          { return nil }
func (r *sqliteRows) Conn() *pgx.Conn                              { return nil }

func ownershipDB(t *testing.T) sqliteConn {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "remote.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	db.SetMaxOpenConns(1)
	_, err = db.Exec(`PRAGMA foreign_keys=ON;
 CREATE TABLE sessions(source text,id text,machine text,provider text,provider_session_id text,cwd text,title text,path text,origin_path text,revision text,clean_fingerprint text,started_at text,updated_at text,body text,turns text,pushed_at text DEFAULT CURRENT_TIMESTAMP,PRIMARY KEY(source,id));
 CREATE TABLE session_machines(source text,id text,machine text,PRIMARY KEY(source,id,machine),FOREIGN KEY(source,id) REFERENCES sessions(source,id) ON DELETE RESTRICT);`)
	if err != nil {
		t.Fatal(err)
	}
	return sqliteConn{db}
}
func pushFixture() store.ExportRow {
	return store.ExportRow{Source: "claude", ID: "shared", Provider: "claude", Revision: "1", Body: "cleaned text", Turns: []model.Turn{{Role: "user", Text: "cleaned text"}}}
}
func sqlCount(t *testing.T, db *sql.DB, table string) int {
	t.Helper()
	var n int
	if err := db.QueryRow("SELECT count(*) FROM " + table).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}
func TestSharedOwnershipAndEmptySuccessfulSnapshots(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	row := pushFixture()
	first, err := syncRows(ctx, db, []store.ExportRow{row}, "policy", "machine-a", true)
	if err != nil || first.Uploaded != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	second, err := syncRows(ctx, db, []store.ExportRow{row}, "policy", "machine-b", true)
	if err != nil || second.Uploaded != 0 || second.Unchanged != 1 {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	if sqlCount(t, db.db, "session_machines") != 2 {
		t.Fatal("unchanged second owner was not registered")
	}
	// Old clients cannot delete a row owned by either machine.
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE source=$1 AND id=$2`, row.Source, row.ID); err == nil {
		t.Fatal("legacy deletion bypassed ownership")
	}
	removed, err := syncRows(ctx, db, nil, "policy", "machine-a", true)
	if err != nil || removed.Deleted != 0 {
		t.Fatalf("remove a=%+v err=%v", removed, err)
	}
	if sqlCount(t, db.db, "sessions") != 1 || sqlCount(t, db.db, "session_machines") != 1 {
		t.Fatal("removing one owner deleted shared content")
	}
	kept, err := syncRows(ctx, db, nil, "policy", "machine-b", false)
	if err != nil || kept.Deleted != 0 || sqlCount(t, db.db, "session_machines") != 1 {
		t.Fatalf("incomplete scan=%+v err=%v", kept, err)
	}
	removed, err = syncRows(ctx, db, nil, "policy", "machine-b", true)
	if err != nil || removed.Deleted != 1 || sqlCount(t, db.db, "sessions") != 0 {
		t.Fatalf("last owner=%+v err=%v", removed, err)
	}
}
func TestPreviouslyEmptyRemoteTurnsAreUploadedAgain(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	row := pushFixture()
	if _, err := syncRows(ctx, db, []store.ExportRow{row}, "policy", "machine-a", true); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `UPDATE sessions SET turns='[]'`); err != nil {
		t.Fatal(err)
	}
	stats, err := syncRows(ctx, db, []store.ExportRow{row}, "policy", "machine-a", true)
	if err != nil || stats.Uploaded != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	var turns string
	if err := db.db.QueryRow("SELECT turns FROM sessions").Scan(&turns); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(turns, "cleaned text") {
		t.Fatal("turns were not repaired")
	}
}
func TestOwnershipBackfillRunsOnlyOnce(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	row := pushFixture()
	if err := upsert(ctx, db, row, "legacy-machine", "policy"); err != nil {
		t.Fatal(err)
	}
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	ownershipSchema := string(raw)[strings.Index(string(raw), "-- Separate machine ownership"):]
	apply := func() {
		t.Helper()
		for _, stmt := range strings.Split(ownershipSchema, ";") {
			if strings.TrimSpace(stmt) == "" {
				continue
			}
			if _, err := db.Exec(ctx, stmt); err != nil {
				t.Fatal(err)
			}
		}
	}
	apply()
	if sqlCount(t, db.db, "session_machines") != 1 {
		t.Fatal("legacy owner not backfilled")
	}
	if _, err := db.Exec(ctx, `DELETE FROM session_machines`); err != nil {
		t.Fatal(err)
	}
	apply()
	if sqlCount(t, db.db, "session_machines") != 0 {
		t.Fatal("stale legacy owner resurrected")
	}
}
