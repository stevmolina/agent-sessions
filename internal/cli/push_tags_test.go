package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPushTagsNeedsOwner(t *testing.T) {
	t.Setenv("SESSIONS_OWNER", "")
	t.Setenv("PATH", t.TempDir()) // no doppler binary, so the fallback fails
	_, _, err := pushTags(context.Background())
	if err == nil || !strings.Contains(err.Error(), "SESSIONS_OWNER") {
		t.Fatalf("err=%v", err)
	}
}

func TestPushTagsReadsOwnerAndMap(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "brains.json")
	t.Setenv("SESSIONS_OWNER", "user_invented")
	t.Setenv("SESSIONS_BRAINS_FILE", filepath.Join(dir, "missing.json"))
	owner, brains, err := pushTags(context.Background())
	if err != nil || owner != "user_invented" || brains.Brain("/x") != "" {
		t.Fatalf("owner=%q err=%v", owner, err)
	}
	if err := os.WriteFile(path, []byte(`{"default":"someone/notes"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_BRAINS_FILE", path)
	if _, brains, err = pushTags(context.Background()); err != nil || brains.Brain("/x") != "someone/notes" {
		t.Fatalf("err=%v", err)
	}
	if err := os.WriteFile(path, []byte(`{"default":`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = pushTags(context.Background()); err == nil {
		t.Fatal("a malformed map must fail the push")
	}
}
