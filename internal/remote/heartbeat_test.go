package remote

import (
	"context"
	"database/sql"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/usuario/sessions/internal/store"
)

// heartbeatDB applies the real CREATE TABLE from schema.sql to the SQLite double.
func heartbeatDB(t *testing.T) sqliteConn {
	t.Helper()
	db := ownershipDB(t)
	stmts, err := schemaStatements()
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range stmts {
		if strings.HasPrefix(stmt, "CREATE TABLE IF NOT EXISTS sessions_push_heartbeats") ||
			strings.Contains(stmt, "\nCREATE TABLE IF NOT EXISTS sessions_push_heartbeats") {
			if _, err := db.Exec(context.Background(), stmt); err != nil {
				t.Fatal(err)
			}
			return db
		}
	}
	t.Fatal("schema.sql has no heartbeat table")
	return db
}

type heartbeatState struct {
	attempts, successes, failures int
	lastError                     sql.NullString
	hasSuccess, hasErrorAt        bool
	uploaded, skipped, duration   int
	version                       string
}

func readHeartbeat(t *testing.T, db *sql.DB, machine string) heartbeatState {
	t.Helper()
	var s heartbeatState
	var success, errorAt sql.NullString
	err := db.QueryRow(`SELECT attempts, successes, failures, last_error, last_success_at, last_error_at,
 uploaded, skipped, duration_ms, version FROM sessions_push_heartbeats WHERE machine = ?`, machine).
		Scan(&s.attempts, &s.successes, &s.failures, &s.lastError, &success, &errorAt,
			&s.uploaded, &s.skipped, &s.duration, &s.version)
	if err != nil {
		t.Fatal(err)
	}
	s.hasSuccess, s.hasErrorAt = success.Valid, errorAt.Valid
	return s
}

func TestHeartbeatCountsAttemptsAndKeepsLastSuccess(t *testing.T) {
	db := heartbeatDB(t)
	ctx := context.Background()
	ok := Heartbeat{Machine: "mac", OK: true, Uploaded: 3, Skipped: 1, Duration: 1500 * time.Millisecond, Version: "abc123"}
	if err := recordHeartbeat(ctx, db, ok); err != nil {
		t.Fatal(err)
	}
	s := readHeartbeat(t, db.db, "mac")
	if s.attempts != 1 || s.successes != 1 || s.failures != 0 || !s.hasSuccess || s.lastError.Valid || s.hasErrorAt {
		t.Fatalf("first success recorded wrong: %+v", s)
	}
	if s.uploaded != 3 || s.skipped != 1 || s.duration != 1500 || s.version != "abc123" {
		t.Fatalf("counts recorded wrong: %+v", s)
	}

	failed := Heartbeat{Machine: "mac", Error: "remote database unavailable\nsecond line", Version: "abc123"}
	if err := recordHeartbeat(ctx, db, failed); err != nil {
		t.Fatal(err)
	}
	s = readHeartbeat(t, db.db, "mac")
	if s.attempts != 2 || s.successes != 1 || s.failures != 1 {
		t.Fatalf("failure counted wrong: %+v", s)
	}
	if !s.hasSuccess {
		t.Fatal("a failure erased the last success time")
	}
	if s.lastError.String != "remote database unavailable" || !s.hasErrorAt {
		t.Fatalf("error recorded wrong: %+v", s)
	}
	if err := recordHeartbeat(ctx, db, failed); err != nil {
		t.Fatal(err)
	}
	if n := consecutive(t, db.db, "mac"); n != 2 {
		t.Fatalf("want 2 failures in a row, got %d", n)
	}

	if err := recordHeartbeat(ctx, db, ok); err != nil {
		t.Fatal(err)
	}
	s = readHeartbeat(t, db.db, "mac")
	if s.attempts != 4 || s.successes != 2 || s.failures != 2 || s.lastError.Valid {
		t.Fatalf("recovery recorded wrong: %+v", s)
	}
	if n := consecutive(t, db.db, "mac"); n != 0 {
		t.Fatalf("success should reset the failure streak, got %d", n)
	}
	if !s.hasErrorAt {
		t.Fatal("recovery erased when the last error happened")
	}
}

func consecutive(t *testing.T, db *sql.DB, machine string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT consecutive_failures FROM sessions_push_heartbeats WHERE machine = ?`, machine).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestShortErrorKeepsOneShortLine(t *testing.T) {
	if got := ShortError("  upsert failed (23505)\nDETAIL: more"); got != "upsert failed (23505)" {
		t.Fatalf("got %q", got)
	}
	long := strings.Repeat("x", 500)
	if got := ShortError(long); len(got) != 200 {
		t.Fatalf("long error not cut: %d", len(got))
	}
}

// countingConn records how many statements reach the database.
type countingConn struct {
	sqliteConn
	execs int
}

func (c *countingConn) Exec(ctx context.Context, q string, args ...any) (pgconn.CommandTag, error) {
	c.execs++
	return c.sqliteConn.Exec(ctx, q, args...)
}

// Verification runs only on rows about to be uploaded. A failure skips that row
// and blocks deletes, so a bad clean can never remove a cloud copy.
func TestVerifyRunsOnUploadsOnlyAndFailureBlocksDeletes(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	kept := pushFixture()
	gone := pushFixture()
	gone.ID = "gone"
	if _, err := syncRows(ctx, db, []store.ExportRow{kept, gone}, "policy", "machine-a", true); err != nil {
		t.Fatal(err)
	}
	changed := kept
	changed.Revision = "2"
	changed.Path = "changed-path"
	fresh := pushFixture()
	fresh.ID = "fresh"
	var checked []string
	verify := func(r store.ExportRow) bool {
		checked = append(checked, r.ID)
		return r.ID != "fresh"
	}
	// "gone" disappeared locally; "fresh" fails verification.
	stats, err := syncRowsVerified(ctx, db, []store.ExportRow{changed, fresh}, "policy", "machine-a", true, verify)
	if err != nil {
		t.Fatal(err)
	}
	if len(checked) != 2 {
		t.Fatalf("verify should see only the two uploads, saw %v", checked)
	}
	if stats.Uploaded != 1 || stats.Failed != 1 || stats.Deleted != 0 || len(stats.SkippedPaths) != 1 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if sqlCount(t, db.db, "sessions") != 2 {
		t.Fatal("a verification failure must block deletes")
	}

	checked = nil
	if _, err := syncRowsVerified(ctx, db, []store.ExportRow{changed}, "policy", "machine-a", true, verify); err != nil {
		t.Fatal(err)
	}
	if len(checked) != 0 {
		t.Fatalf("an unchanged row was re-verified: %v", checked)
	}
	if sqlCount(t, db.db, "sessions") != 1 {
		t.Fatal("a clean push should remove the row this machine no longer has")
	}
}

// A push with nothing new must not write one ownership row per session. That was
// most of the ten minutes a no-change push took against Neon.
func TestUnchangedPushWritesNothing(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	rows := []store.ExportRow{pushFixture()}
	second := pushFixture()
	second.ID = "other"
	rows = append(rows, second)
	if _, err := syncRows(ctx, db, rows, "policy", "machine-a", true); err != nil {
		t.Fatal(err)
	}
	db.execs = 0
	stats, err := syncRows(ctx, db, rows, "policy", "machine-a", true)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 0 || stats.Unchanged != 2 {
		t.Fatalf("unexpected stats: %+v", stats)
	}
	if db.execs != 0 {
		t.Fatalf("no-change push ran %d writes", db.execs)
	}
}
