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
	for _, want := range []string{"column_name = 'owner'", "column_name = 'brain'", "indexname = 'sessions_owner_brain_idx'", "WHEN insufficient_privilege THEN NULL"} {
		if !strings.Contains(text, want) {
			t.Errorf("schema.sql lacks %q", want)
		}
	}
}
