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
	// Machine a pushes again: everything already matches.
	db.execs = 0
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, mapped); err != nil || stats.Retagged != 0 || db.execs != 0 {
		t.Fatalf("stats=%+v execs=%d err=%v", stats, db.execs, err)
	}
}

// An owner, once set, never changes: a second person pushing the same chat
// must not take it over, by upload or by retag, and must not refile it.
func TestOwnerNeverChanges(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	row := withCWD(pushFixture(), "client", "/home/u/engagements/acme")
	first := testOpts("machine-a", true)
	first.Brains = testBrains(t, acmeMap)
	if _, err := syncRows(ctx, db, []store.ExportRow{row}, first); err != nil {
		t.Fatal(err)
	}
	other := testOpts("machine-b", true)
	other.Owner = "user_someone_else"
	other.Brains = testBrains(t, `{"default":"someone-else/notes"}`)

	// Unchanged row: no retag, no writes beyond claiming machine ownership.
	db.execs = 0
	stats, err := syncRows(ctx, db, []store.ExportRow{row}, other)
	if err != nil || stats.Retagged != 0 || db.execs != 1 {
		t.Fatalf("stats=%+v execs=%d err=%v", stats, db.execs, err)
	}
	// Changed row: the upload keeps the first owner and its brain.
	changed := row
	changed.Revision = "2"
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, other); err != nil || stats.Uploaded != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	owner, brain := readTags(t, db.db, "client")
	if owner.String != testOwner || brain.String != "someone/acme-knowledge" {
		t.Fatalf("taken over: owner=%v brain=%v", owner, brain)
	}
	// The retag statement itself refuses another owner's row.
	if _, err := db.Exec(ctx, retagSQL, "user_someone_else", "x/y", row.Source, row.ID, nil); err != nil {
		t.Fatal(err)
	}
	if owner, brain = readTags(t, db.db, "client"); owner.String != testOwner || brain.String != "someone/acme-knowledge" {
		t.Fatalf("retag took over: owner=%v brain=%v", owner, brain)
	}
	// A row with no owner yet is claimed by whoever pushes it next.
	if _, err := db.Exec(ctx, `UPDATE sessions SET owner = NULL`); err != nil {
		t.Fatal(err)
	}
	if stats, err = syncRows(ctx, db, []store.ExportRow{changed}, other); err != nil || stats.Retagged != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if owner, brain = readTags(t, db.db, "client"); owner.String != "user_someone_else" || brain.String != "someone-else/notes" {
		t.Fatalf("unowned row not claimed: owner=%v brain=%v", owner, brain)
	}
}

func readRepos(t *testing.T, db *sql.DB, id string) ([]string, bool) {
	t.Helper()
	var raw sql.NullString
	if err := db.QueryRow(`SELECT repos FROM sessions WHERE id = ?`, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if !raw.Valid {
		return nil, false
	}
	list, _ := parseReposJSON([]byte(raw.String))
	return list, true
}

// fakeRepos tags a row by its cwd for the cheap pass, and adds a transcript tag
// on the full pass. calls counts the full passes.
func fakeRepos(calls *int) func(store.ExportRow, bool) []string {
	return func(row store.ExportRow, full bool) []string {
		var out []string
		if row.CWD != nil && *row.CWD == "/data/OpenBot/Agents/agent-1" {
			out = append(out, "stevmolina/pokebots")
		}
		if full {
			*calls++
			out = append(out, "acme/touched")
		}
		return out
	}
}

func TestNewRowsCarryRepos(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	calls := 0
	opts := testOpts("machine-a", true)
	opts.Repos = fakeRepos(&calls)
	rows := []store.ExportRow{withCWD(pushFixture(), "agent", "/data/OpenBot/Agents/agent-1")}
	if _, err := syncRows(ctx, db, rows, opts); err != nil {
		t.Fatal(err)
	}
	got, ok := readRepos(t, db.db, "agent")
	if !ok || len(got) != 2 || got[0] != "stevmolina/pokebots" || got[1] != "acme/touched" {
		t.Fatalf("repos=%v ok=%v", got, ok)
	}
}

func TestOldRowsAreTaggedOnNextPushOnceAndWithoutRescan(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	ctx := context.Background()
	rows := []store.ExportRow{withCWD(pushFixture(), "old", "/data/OpenBot/Agents/agent-1")}
	// An older binary pushed it: no repos column value.
	if _, err := syncRows(ctx, db, rows, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRepos(t, db.db, "old"); ok {
		t.Fatal("old binary should leave repos NULL")
	}
	calls := 0
	opts := testOpts("machine-a", true)
	opts.Repos = fakeRepos(&calls)
	stats, err := syncRows(ctx, db, rows, opts)
	if err != nil || stats.Uploaded != 0 || stats.Retagged != 1 || calls != 1 {
		t.Fatalf("stats=%+v err=%v calls=%d", stats, err, calls)
	}
	if got, ok := readRepos(t, db.db, "old"); !ok || len(got) != 2 {
		t.Fatalf("repos=%v ok=%v", got, ok)
	}
	// Tagged now: the next push does no transcript pass and writes nothing.
	db.execs = 0
	stats, err = syncRows(ctx, db, rows, opts)
	if err != nil || stats.Retagged != 0 || calls != 1 || db.execs != 0 {
		t.Fatalf("stats=%+v err=%v calls=%d execs=%d", stats, err, calls, db.execs)
	}
}

func TestRowWithNoReposIsMarkedCheckedNotRescanned(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	rows := []store.ExportRow{pushFixture()}
	if _, err := syncRows(ctx, db, rows, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	opts := testOpts("machine-a", true)
	opts.Repos = func(store.ExportRow, bool) []string { return nil }
	if _, err := syncRows(ctx, db, rows, opts); err != nil {
		t.Fatal(err)
	}
	got, ok := readRepos(t, db.db, "shared")
	if !ok || len(got) != 0 {
		t.Fatalf("want an empty array, got %v ok=%v", got, ok)
	}
}

func TestAnotherMachineNeverRemovesRepos(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	rows := []store.ExportRow{withCWD(pushFixture(), "shared-chat", "/data/OpenBot/Agents/agent-1")}
	calls := 0
	a := testOpts("machine-a", true)
	a.Repos = fakeRepos(&calls)
	if _, err := syncRows(ctx, db, rows, a); err != nil {
		t.Fatal(err)
	}
	// Machine b has no checkout and finds nothing, and the content is unchanged.
	b := testOpts("machine-b", true)
	b.Repos = func(store.ExportRow, bool) []string { return nil }
	if _, err := syncRows(ctx, db, rows, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := readRepos(t, db.db, "shared-chat"); len(got) != 2 {
		t.Fatalf("repos=%v", got)
	}
	// A changed row re-uploaded by b keeps the tags too.
	rows[0].Revision = "2"
	if _, err := syncRows(ctx, db, rows, b); err != nil {
		t.Fatal(err)
	}
	if got, _ := readRepos(t, db.db, "shared-chat"); len(got) != 2 {
		t.Fatalf("after upload repos=%v", got)
	}
}

func TestOtherPersonsRowKeepsItsRepos(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	rows := []store.ExportRow{withCWD(pushFixture(), "theirs", "/data/OpenBot/Agents/agent-1")}
	if _, err := syncRows(ctx, db, rows, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	mine := testOpts("machine-b", true)
	mine.Owner = "user_someone_else"
	mine.Repos = fakeRepos(&calls)
	if _, err := syncRows(ctx, db, rows, mine); err != nil {
		t.Fatal(err)
	}
	if _, ok := readRepos(t, db.db, "theirs"); ok {
		t.Fatal("a second person's push must not tag the first person's row")
	}
}
