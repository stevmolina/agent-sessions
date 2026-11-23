package remote

import (
	"strings"
	"testing"
)

// ALTER TABLE takes an ACCESS EXCLUSIVE lock even when IF NOT EXISTS skips the
// change, and schema.sql runs on every push. Column changes must sit inside a
// DO block that checks the catalog first.
func TestSchemaHasNoUnguardedAlter(t *testing.T) {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	for i, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "ALTER TABLE ") {
			t.Errorf("schema.sql line %d alters a table outside a guarded block: %s", i+1, line)
		}
	}
	text := string(raw)
	for _, want := range []string{"column_name = 'owner'", "column_name = 'brain'", "indexname = 'sessions_owner_brain_idx'", "column_name = 'repos'", "ADD COLUMN repos text[]", "indexname = 'sessions_repos_idx'", "USING GIN (repos)", "WHEN insufficient_privilege THEN NULL"} {
		if !strings.Contains(text, want) {
			t.Errorf("schema.sql lacks %q", want)
		}
	}
}

// Binaries from before the repos column keep pushing to the same database, so
// the column must stay nullable and the old upsert must not need it.
func TestReposColumnIsOptionalForOldPushers(t *testing.T) {
	raw, err := schemaFS.ReadFile("schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if strings.Contains(text, "repos text[] NOT NULL") || strings.Contains(text, "repos text[] DEFAULT") {
		t.Error("repos must be a plain nullable column")
	}
	if strings.Contains(text, "ALTER COLUMN repos") {
		t.Error("schema must not change repos after it exists")
	}
	if !strings.Contains(upsertSQL, "repos = CASE") || !strings.Contains(retagSQL, "repos=COALESCE") {
		t.Error("upsert and retag must both write repos")
	}
}
