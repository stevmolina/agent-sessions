package remote

import (
	"context"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/store"
)

func TestRemoteRoundTrip(t *testing.T) {
	if os.Getenv("SESSIONS_REMOTE_TEST") == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	row := store.ExportRow{
		ID: "remote-fixture", Source: "claude", Provider: "claude",
		Path: "remote-fixture-path", OriginPath: "remote-fixture-path",
		Title: "quartz remote title", Revision: "1", Body: "user: quartz-remote-marker",
		Turns: []model.Turn{{Role: "user", Text: "quartz-remote-marker"}},
	}
	t.Cleanup(func() {
		db, err := connect(context.Background(), "DATABASE_URL")
		if err != nil {
			return
		}
		defer db.Close(context.Background())
		_, _ = db.Exec(context.Background(), `DELETE FROM session_machines WHERE id = $1`, row.ID)
		_, _ = db.Exec(context.Background(), `DELETE FROM sessions WHERE id = $1`, row.ID)
	})
	if _, err := Push(ctx, []store.ExportRow{row}, "v1:test", "remote-test-machine", false); err != nil {
		t.Fatal(err)
	}
	hits, err := Search(ctx, "quartz-remote-marker", "claude", "", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].ID != row.ID || strings.Contains(hits[0].Snippet, "DATABASE_URL") {
		t.Fatalf("hits=%d", len(hits))
	}
	shown, err := Show(ctx, "claude", row.ID)
	if err != nil || len(shown) != 1 || shown[0].Turns[0].Text != "quartz-remote-marker" {
		t.Fatal("show failed")
	}
	db, err := connect(ctx, "DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	if _, err := db.Exec(ctx, `DELETE FROM session_machines WHERE id = $1`, row.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND machine = $2`, row.ID, "remote-test-machine"); err != nil {
		t.Fatal(err)
	}
}

// This opt-in test uses only synthetic rows and exercises PostgreSQL's transaction
// lock and restrictive ownership foreign key. Run against an isolated database.
func TestRemoteConcurrentOwnership(t *testing.T) {
	if os.Getenv("SESSIONS_REMOTE_TEST") == "" {
		t.Skip()
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	row := store.ExportRow{ID: "ownership-test-" + suffix, Source: "claude", Provider: "claude", Revision: "1", Body: "synthetic ownership fixture", Turns: []model.Turn{{Role: "user", Text: "synthetic ownership fixture"}}}
	machines := []string{"ownership-a-" + suffix, "ownership-b-" + suffix}
	t.Cleanup(func() {
		db, err := connect(context.Background(), "DATABASE_URL")
		if err != nil {
			return
		}
		defer db.Close(context.Background())
		db.Exec(context.Background(), `DELETE FROM session_machines WHERE id=$1`, row.ID)
		db.Exec(context.Background(), `DELETE FROM sessions WHERE id=$1`, row.ID)
	})
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, machine := range machines {
		wg.Add(1)
		go func(machine string) {
			defer wg.Done()
			_, err := Push(ctx, []store.ExportRow{row}, "test-policy", machine, true)
			errs <- err
		}(machine)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	db, err := connect(ctx, "DATABASE_URL")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close(ctx)
	var owners int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM session_machines WHERE source=$1 AND id=$2`, row.Source, row.ID).Scan(&owners); err != nil || owners != 2 {
		t.Fatalf("owners=%d err=%v", owners, err)
	}
	if _, err = db.Exec(ctx, `DELETE FROM sessions WHERE source=$1 AND id=$2`, row.Source, row.ID); err == nil {
		t.Fatal("legacy delete bypassed restrictive ownership")
	}
	stats, err := Push(ctx, nil, "test-policy", machines[0], true)
	if err != nil || stats.Deleted != 0 {
		t.Fatalf("first removal=%+v err=%v", stats, err)
	}
	var sessions int
	if err = db.QueryRow(ctx, `SELECT count(*) FROM sessions WHERE source=$1 AND id=$2`, row.Source, row.ID).Scan(&sessions); err != nil || sessions != 1 {
		t.Fatalf("shared sessions=%d err=%v", sessions, err)
	}
	stats, err = Push(ctx, nil, "test-policy", machines[1], true)
	if err != nil || stats.Deleted != 1 {
		t.Fatalf("last removal=%+v err=%v", stats, err)
	}
}
