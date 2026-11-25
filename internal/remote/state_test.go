package remote

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/store"
)

const (
	testBinary   = "abc123"
	testDatabase = "db1"
)

// pushAndRecord runs a real sync against the SQLite double and returns the
// state a successful push would save.
func pushAndRecord(t *testing.T, db conn, rows []store.ExportRow, opts Options) *PushState {
	t.Helper()
	stats, err := syncRows(context.Background(), db, rows, opts)
	if err != nil {
		t.Fatal(err)
	}
	return NewPushState(opts, testBinary, testDatabase, stats, time.Now())
}

func twoRows() []store.ExportRow {
	a := pushFixture()
	b := pushFixture()
	b.ID = "other"
	return []store.ExportRow{a, b}
}

// The core promise: after a push, the same rows need no connection, and a real
// push of them would indeed write nothing.
func TestPendingIsEmptyAfterPushAndPushAgreesItWritesNothing(t *testing.T) {
	db := &countingConn{sqliteConn: ownershipDB(t)}
	opts := testOpts("machine-a", true)
	opts.Brains = testBrains(t, acmeMap)
	opts.Repos = func(store.ExportRow, bool) []string { return []string{"acme/app"} }
	rows := twoRows()
	state := pushAndRecord(t, db, rows, opts)
	if reason := state.Pending(rows, opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("nothing changed, but Pending says %q", reason)
	}
	db.execs = 0
	if _, err := syncRows(context.Background(), db, rows, opts); err != nil {
		t.Fatal(err)
	}
	if db.execs != 0 {
		t.Fatalf("Pending said no change but a push wrote %d statements", db.execs)
	}
}

func TestPendingSeesEveryKindOfChange(t *testing.T) {
	cases := []struct {
		name   string
		change func(rows []store.ExportRow, opts *Options) []store.ExportRow
		want   string
	}{
		{"edited session", func(rows []store.ExportRow, _ *Options) []store.ExportRow {
			rows[0].Revision = "2"
			return rows
		}, "new or changed"},
		{"new session", func(rows []store.ExportRow, _ *Options) []store.ExportRow {
			extra := pushFixture()
			extra.ID = "new"
			return append(rows, extra)
		}, "new or changed"},
		{"deleted session", func(rows []store.ExportRow, _ *Options) []store.ExportRow {
			return rows[:1]
		}, "removed"},
		{"all sessions gone releases ownership", func([]store.ExportRow, *Options) []store.ExportRow {
			return nil
		}, "removed"},
		{"brain map changed", func(rows []store.ExportRow, opts *Options) []store.ExportRow {
			cwd := "/home/u/engagements/acme/repo"
			rows[0].CWD = &cwd
			opts.Brains = testBrains(t, acmeMap)
			return rows
		}, "tags changed"},
		{"new repo tag", func(rows []store.ExportRow, opts *Options) []store.ExportRow {
			opts.Repos = func(store.ExportRow, bool) []string { return []string{"acme/app", "acme/other"} }
			return rows
		}, "tags changed"},
		{"redaction secrets changed", func(rows []store.ExportRow, opts *Options) []store.ExportRow {
			opts.Fingerprint = "policy-2"
			return rows
		}, "redaction secrets"},
		{"owner changed", func(rows []store.ExportRow, opts *Options) []store.ExportRow {
			opts.Owner = "user_someone_else"
			return rows
		}, "owner"},
		{"host renamed", func(rows []store.ExportRow, opts *Options) []store.ExportRow {
			opts.Machine = "machine-renamed"
			return rows
		}, "host name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := ownershipDB(t)
			opts := testOpts("machine-a", true)
			opts.Repos = func(store.ExportRow, bool) []string { return []string{"acme/app"} }
			state := pushAndRecord(t, db, twoRows(), opts)
			rows := tc.change(twoRows(), &opts)
			if reason := state.Pending(rows, opts, testBinary, testDatabase); !strings.Contains(reason, tc.want) {
				t.Fatalf("Pending = %q, want it to mention %q", reason, tc.want)
			}
		})
	}
}

// A clean failure elsewhere in the refresh blocks deletes, so a missing row is
// not a change: the push would keep the cloud copy anyway.
func TestPendingIgnoresDeletesWhenDeletesAreBlocked(t *testing.T) {
	db := ownershipDB(t)
	opts := testOpts("machine-a", true)
	state := pushAndRecord(t, db, twoRows(), opts)
	opts.AllowDelete = false
	if reason := state.Pending(twoRows()[:1], opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("deletes are blocked, Pending = %q", reason)
	}
}

// A row that failed verification fails the same way at the same revision, so it
// alone must not wake the database on every run. A new revision is a change.
func TestPendingSkipsRowsThatFailedVerificationAtTheSameRevision(t *testing.T) {
	db := ownershipDB(t)
	bad := pushFixture()
	bad.ID = "bad"
	rows := append(twoRows(), bad)
	opts := verifiedOpts("machine-a", true, func(r store.ExportRow) bool { return r.ID != "bad" })
	state := pushAndRecord(t, db, rows, opts)
	if len(state.Failed) != 1 || state.Failed[0].ID != "bad" {
		t.Fatalf("failed rows not recorded: %+v", state.Failed)
	}
	if n := state.StillFailing(rows); n != 1 {
		t.Fatalf("StillFailing = %d, want 1", n)
	}
	if n := state.StillFailing(rows[:2]); n != 0 {
		t.Fatalf("the failing row was deleted, StillFailing = %d", n)
	}
	if reason := state.Pending(rows, opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("same failing row, Pending = %q", reason)
	}
	// While a row fails, deletes stay blocked, so a missing row is no change either.
	if reason := state.Pending([]store.ExportRow{rows[0], bad}, opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("deletes are blocked by the failing row, Pending = %q", reason)
	}
	rows[2].Revision = "2"
	if reason := state.Pending(rows, opts, testBinary, testDatabase); reason == "" {
		t.Fatal("the failing row changed: that must be pushed")
	}
}

// remoteStateSQL reports no revision for a row with no turns, which forces a
// re-upload. The saved state must not turn that into a change on every run.
func TestPendingRowWithNoTurnsIsNotAlwaysChanged(t *testing.T) {
	db := ownershipDB(t)
	empty := pushFixture()
	empty.Turns = []model.Turn{}
	opts := testOpts("machine-a", true)
	state := pushAndRecord(t, db, []store.ExportRow{empty}, opts)
	if reason := state.Pending([]store.ExportRow{empty}, opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("Pending = %q", reason)
	}
}

func TestPendingClaimsAndNeverTaggedRows(t *testing.T) {
	db := ownershipDB(t)
	opts := testOpts("machine-a", true)
	rows := twoRows()
	state := pushAndRecord(t, db, rows, opts)

	unowned := *state
	unowned.Remote = append([]Row(nil), state.Remote...)
	unowned.Remote[0].Owned = false
	if reason := unowned.Pending(rows, opts, testBinary, testDatabase); !strings.Contains(reason, "claim") {
		t.Fatalf("unowned row, Pending = %q", reason)
	}

	// The tagger is on now, and the rows have never been tagged: the full scan
	// is due, and Pending must not run it itself.
	scans := 0
	opts.Repos = func(_ store.ExportRow, full bool) []string {
		if full {
			scans++
		}
		return nil
	}
	if reason := state.Pending(rows, opts, testBinary, testDatabase); !strings.Contains(reason, "repo tags") {
		t.Fatalf("untagged rows, Pending = %q", reason)
	}
	if scans != 0 {
		t.Fatalf("Pending ran %d full transcript scans", scans)
	}
}

func TestPendingWithoutRecordOrAfterUpgrade(t *testing.T) {
	var none *PushState
	opts := testOpts("machine-a", true)
	if none.Pending(twoRows(), opts, testBinary, testDatabase) == "" {
		t.Fatal("no record must mean a normal push")
	}
	state := pushAndRecord(t, ownershipDB(t), twoRows(), opts)
	if !strings.Contains(state.Pending(twoRows(), opts, "newer", testDatabase), "new binary") {
		t.Fatal("a new binary must push once (it may migrate the schema)")
	}
	for _, db := range []string{"db2", ""} {
		if !strings.Contains(state.Pending(twoRows(), opts, testBinary, db), "database changed") {
			t.Fatalf("database %q: a new or unknown database must get a full push", db)
		}
	}
}

func TestDatabaseIDIgnoresPasswordAndNamesTheDatabase(t *testing.T) {
	ctx := context.Background()
	id := func(url string) string {
		t.Setenv("DATABASE_URL", url)
		got, err := DatabaseID(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	a := id("postgres://u:one@db.example:5432/neondb?sslmode=require")
	if a != id("postgres://u:two@db.example:5432/neondb?sslmode=require") {
		t.Fatal("a password rotation must not look like a new database")
	}
	if strings.Contains(a, "one") || len(a) != 16 {
		t.Fatalf("id %q", a)
	}
	if a == id("postgres://u:one@other.example:5432/neondb") || a == id("postgres://u:one@db.example:5432/otherdb") {
		t.Fatal("another host or database must get another id")
	}
}

func TestStateRoundTripAndFallbacks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "push-state.json")
	if _, err := LoadState(path); err == nil {
		t.Fatal("missing file must be an error, so the caller pushes")
	}
	opts := testOpts("machine-a", true)
	opts.Repos = func(store.ExportRow, bool) []string { return []string{"acme/app"} }
	state := pushAndRecord(t, ownershipDB(t), twoRows(), opts)
	state.HeartbeatAt = time.Date(2026, 11, 24, 10, 0, 0, 0, time.UTC)
	if err := SaveState(path, state); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("state file mode: %v %v", info, err)
	}
	loaded, err := LoadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if reason := loaded.Pending(twoRows(), opts, testBinary, testDatabase); reason != "" {
		t.Fatalf("reloaded state lost detail, Pending = %q", reason)
	}
	if !loaded.HeartbeatAt.Equal(state.HeartbeatAt) {
		t.Fatalf("heartbeat time %v, want %v", loaded.HeartbeatAt, state.HeartbeatAt)
	}
	for _, bad := range []string{"{not json", `{"format":99}`} {
		if err := os.WriteFile(path, []byte(bad), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadState(path); err == nil {
			t.Fatalf("%q loaded without error", bad)
		}
	}
}

func TestHeartbeatDueEverySixHours(t *testing.T) {
	if HeartbeatInterval != 6*time.Hour {
		t.Fatalf("HeartbeatInterval = %s; sessions health and the docs assume 6h", HeartbeatInterval)
	}
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	var none *PushState
	if !none.HeartbeatDue(now) || !(&PushState{}).HeartbeatDue(now) {
		t.Fatal("no heartbeat on record must be due")
	}
	s := &PushState{HeartbeatAt: now}
	for _, d := range []time.Duration{time.Minute, 15 * time.Minute, HeartbeatInterval - time.Minute} {
		if s.HeartbeatDue(now.Add(d)) {
			t.Fatalf("heartbeat due %s after the last one", d)
		}
	}
	if !s.HeartbeatDue(now.Add(HeartbeatInterval)) {
		t.Fatal("heartbeat not due after the interval")
	}
	if !s.HeartbeatDue(now.Add(-time.Hour)) {
		t.Fatal("a heartbeat time in the future (clock moved back) must count as due")
	}
}
