package remote

import (
	"context"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/store"
)

func cardRow(id, text string) store.ExportRow {
	row := pushFixture()
	row.ID = id
	row.Title = "<openbot_saved_memories>junk</openbot_saved_memories>" + text
	row.Turns = []model.Turn{
		{Role: "user", Text: "<openbot_saved_memories>junk</openbot_saved_memories>\n" + text},
		{Role: "assistant", Text: "Done: " + text + " Opened https://github.com/a/b/pull/7 and it merged."},
	}
	row.Body = "user: " + text
	return row
}

func readCard(t *testing.T, db sqliteConn, id string) (title, card, digest string, at *string) {
	t.Helper()
	var ti, ca, di, up *string
	if err := db.db.QueryRow(`SELECT title, card, digest, card_updated_at FROM sessions WHERE id=?`, id).Scan(&ti, &ca, &di, &up); err != nil {
		t.Fatal(err)
	}
	s := func(p *string) string {
		if p == nil {
			return ""
		}
		return *p
	}
	return s(ti), s(ca), s(di), up
}

func TestUploadWritesCleanTitleAndCard(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	rows := []store.ExportRow{cardRow("c1", "Add a card column to the sessions table.")}
	Annotate(rows, "machine-a", nil)
	if stats, err := syncRows(ctx, db, rows, testOpts("machine-a", true)); err != nil || stats.Uploaded != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	title, card, digest, at := readCard(t, db, "c1")
	if title != "Add a card column to the sessions table." {
		t.Fatalf("title %q", title)
	}
	if digest == "" || at == nil || card == "" {
		t.Fatalf("card=%q digest=%q at=%v", card, digest, at)
	}
	for _, want := range []string{"Where: no brain · machine-a", "Asked: Add a card column", "https://github.com/a/b/pull/7"} {
		if !contains(card, want) {
			t.Errorf("card misses %q:\n%s", want, card)
		}
	}
}

// A row pushed before cards existed has the same revision, so it is not uploaded
// again; it gets its card in place, once. A second push writes nothing.
func TestUnchangedRowGetsItsCardOnceInPlace(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	old := cardRow("c2", "Fix the sidebar.")
	if _, err := syncRows(ctx, db, []store.ExportRow{old}, testOpts("machine-a", true)); err != nil {
		t.Fatal(err)
	}
	if _, card, _, _ := readCard(t, db, "c2"); card != "" {
		t.Fatal("the old push should not have a card")
	}
	rows := []store.ExportRow{cardRow("c2", "Fix the sidebar.")}
	Annotate(rows, "machine-a", nil)
	stats, err := syncRows(ctx, db, rows, testOpts("machine-a", true))
	if err != nil || stats.Uploaded != 0 || stats.CardsUpdated != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if title, card, _, _ := readCard(t, db, "c2"); card == "" || title != "Fix the sidebar." {
		t.Fatalf("title=%q card=%q", title, card)
	}
	again, err := syncRows(ctx, db, rows, testOpts("machine-a", true))
	if err != nil || again.CardsUpdated != 0 || again.Uploaded != 0 {
		t.Fatalf("second push wrote: %+v err=%v", again, err)
	}
}

func TestPendingSeesAMissingCard(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	opts := testOpts("machine-a", true)
	if _, err := syncRows(ctx, db, []store.ExportRow{cardRow("c3", "Ship it.")}, opts); err != nil {
		t.Fatal(err)
	}
	after, err := loadRemote(ctx, db, afterStateSQL, "machine-a")
	if err != nil {
		t.Fatal(err)
	}
	state := &PushState{Format: stateFormat, Binary: "b", Database: "d", Machine: "machine-a", Owner: testOwner, Fingerprint: "policy", PushedAt: time.Now(), Remote: after}
	rows := []store.ExportRow{cardRow("c3", "Ship it.")}
	if got := state.Pending(rows, opts, "b", "d"); got != "" {
		t.Fatalf("unannotated rows should not need a card, got %q", got)
	}
	Annotate(rows, "machine-a", nil)
	if got := state.Pending(rows, opts, "b", "d"); got != "cards changed" {
		t.Fatalf("pending = %q", got)
	}
}

func TestAnotherOwnersRowKeepsItsCard(t *testing.T) {
	db := ownershipDB(t)
	ctx := context.Background()
	if _, err := syncRows(ctx, db, []store.ExportRow{cardRow("c4", "Theirs.")}, Options{Fingerprint: "policy", Machine: "machine-a", AllowDelete: true, Owner: "someone-else"}); err != nil {
		t.Fatal(err)
	}
	rows := []store.ExportRow{cardRow("c4", "Theirs.")}
	Annotate(rows, "machine-a", nil)
	stats, err := syncRows(ctx, db, rows, testOpts("machine-b", true))
	if err != nil || stats.CardsUpdated != 0 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
