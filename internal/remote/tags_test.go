package remote

import (
	"context"
	"database/sql"
	"testing"

	"github.com/usuario/sessions/internal/brainmap"
	"github.com/usuario/sessions/internal/store"
)

func testBrains(t *testing.T, raw string) *brainmap.Map {
	t.Helper()
	m, err := brainmap.Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func readTags(t *testing.T, db *sql.DB, id string) (owner, brain sql.NullString) {
	t.Helper()
	if err := db.QueryRow(`SELECT owner, brain FROM sessions WHERE id = ?`, id).Scan(&owner, &brain); err != nil {
		t.Fatal(err)
	}
	return owner, brain
}

func withCWD(row store.ExportRow, id, cwd string) store.ExportRow {
	row.ID = id
	row.CWD = &cwd
	return row
}

const acmeMap = `{"default":"someone/personal-brain","rules":[{"brain":"someone/acme-knowledge","match":["engagements/acme"]}]}`

func TestNewRowsCarryOwnerAndBrain(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	opts := testOpts("machine-a", true)
	opts.Brains = testBrains(t, acmeMap)
	rows := []store.ExportRow{
		withCWD(pushFixture(), "client", "/home/u/engagements/acme/repo"),
		withCWD(pushFixture(), "personal", "/home/u/dev/tool"),
		pushFixture(), // no cwd: the default brain
	}
	stats, err := syncRows(ctx, db, rows, opts)
	if err != nil || stats.Uploaded != 3 || stats.Retagged != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	for id, want := range map[string]string{"client": "someone/acme-knowledge", "personal": "someone/personal-brain", "shared": "someone/personal-brain"} {
		owner, brain := readTags(t, db.db, id)
		if owner.String != testOwner || brain.String != want {
			t.Errorf("%s: owner=%v brain=%v, want %s", id, owner, brain, want)
		}
	}
}

func TestNoMapMeansNullBrain(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	row := withCWD(pushFixture(), "plain", "/home/u/engagements/acme")
	if _, err := syncRows(ctx, db, []store.ExportRow{row}, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	owner, brain := readTags(t, db.db, "plain")
	if owner.String != testOwner || brain.Valid {
		t.Fatalf("owner=%v brain=%v", owner, brain)
	}
}

func TestChangedMapRetagsUnchangedRow(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	row := withCWD(pushFixture(), "client", "/home/u/engagements/acme/repo")
	other := withCWD(pushFixture(), "other", "/home/u/dev/tool")
	rows := []store.ExportRow{row, other}
	if _, err := syncRows(ctx, db, rows, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	opts := testOpts("machine-a", true)
	opts.Brains = testBrains(t, `{"rules":[{"brain":"someone/acme-knowledge","match":["engagements/acme"]}]}`)
	db.execs = 0
	stats, err := syncRows(ctx, db, rows, opts)
	if err != nil {
		t.Fatal(err)
	}
	// Only the acme row changed brain; "other" stays NULL and is not touched.
	if stats.Uploaded != 0 || stats.Unchanged != 2 || stats.Retagged != 1 || db.execs != 1 {
		t.Fatalf("stats=%+v execs=%d", stats, db.execs)
	}
	if _, brain := readTags(t, db.db, "client"); brain.String != "someone/acme-knowledge" {
		t.Fatalf("brain=%v", brain)
	}
	// A row an older client wrote with no owner is claimed by the next push.
	if _, err := db.Exec(ctx, `UPDATE sessions SET owner = NULL WHERE id = 'other'`); err != nil {
		t.Fatal(err)
	}
	stats, err = syncRows(ctx, db, rows, opts)
	if err != nil || stats.Retagged != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if owner, _ := readTags(t, db.db, "other"); owner.String != testOwner {
		t.Fatalf("owner=%v", owner)
	}
}

func TestCorrectTagsWriteNothing(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	opts := testOpts("machine-a", true)
	opts.Brains = testBrains(t, acmeMap)
	rows := []store.ExportRow{
		withCWD(pushFixture(), "client", "/home/u/engagements/acme"),
		pushFixture(),
	}
	if _, err := syncRows(ctx, db, rows, opts); err != nil {
		t.Fatal(err)
	}
	db.execs = 0
	stats, err := syncRows(ctx, db, rows, opts)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Uploaded != 0 || stats.Retagged != 0 || db.execs != 0 {
		t.Fatalf("stats=%+v execs=%d", stats, db.execs)
	}
}

func TestPushRefusesWithoutOwner(t *testing.T) {
	db := ownershipDB(t)
	opts := testOpts("machine-a", true)
	opts.Owner = ""
	if _, err := syncRows(context.Background(), db, []store.ExportRow{pushFixture()}, opts); err == nil {
		t.Fatal("a push with no owner must fail")
	}
	if _, err := Push(context.Background(), nil, Options{Fingerprint: "policy"}); err == nil {
		t.Fatal("Push with no owner must fail before connecting")
	}
	if sqlCount(t, db.db, "sessions") != 0 {
		t.Fatal("nothing should be written")
	}
}

// Two machines, one with a map and one without, must not undo each other.
func TestEmptyBrainNeverClearsAnotherMachinesBrain(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	row := withCWD(pushFixture(), "client", "/home/u/engagements/acme")
	mapped := testOpts("machine-a", true)
	mapped.Brains = testBrains(t, acmeMap)
	if _, err := syncRows(ctx, db, []store.ExportRow{row}, mapped); err != nil {
		t.Fatal(err)
	}
	// Machine b has no map: unchanged row, nothing to write.
	db.execs = 0
	stats, err := syncRows(ctx, db, []store.ExportRow{row}, testOpts("machine-b", true))
	if err != nil || stats.Retagged != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if _, brain := readTags(t, db.db, "client"); brain.String != "someone/acme-knowledge" {
		t.Fatalf("brain cleared: %v", brain)
	}
	// Machine b uploads new content: the upsert keeps the brain too.
	changed := row
	changed.Revision = "2"
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, testOpts("machine-b", true)); err != nil || stats.Uploaded != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if _, brain := readTags(t, db.db, "client"); brain.String != "someone/acme-knowledge" {
		t.Fatalf("upsert cleared brain: %v", brain)
	}
	// A wrong owner is still fixed, and the brain survives that too.
	if _, err := db.Exec(ctx, `UPDATE sessions SET owner = 'someone-else'`); err != nil {
		t.Fatal(err)
	}
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, testOpts("machine-b", true)); err != nil || stats.Retagged != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	owner, brain := readTags(t, db.db, "client")
	if owner.String != testOwner || brain.String != "someone/acme-knowledge" {
		t.Fatalf("owner=%v brain=%v", owner, brain)
	}
	// Machine a pushes again: everything already matches.
	db.execs = 0
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, mapped); err != nil || stats.Retagged != 0 || db.execs != 0 {
		t.Fatalf("stats=%+v execs=%d err=%v", stats, db.execs, err)
	}
}
