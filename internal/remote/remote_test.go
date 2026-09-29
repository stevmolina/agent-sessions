package remote

import (
	"context"
	"os"
	"strings"
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
	if _, err := db.Exec(ctx, `DELETE FROM sessions WHERE id = $1 AND machine = $2`, row.ID, "remote-test-machine"); err != nil {
		t.Fatal(err)
	}
}
