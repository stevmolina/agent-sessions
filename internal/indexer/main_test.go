package indexer

import (
	"os"
	"testing"
)

// The native agent roots default to folders in the real home. Tests never read
// them: point each at a folder that does not exist unless a test sets its own.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "sessions-test-roots")
	if err != nil {
		panic(err)
	}
	defer os.RemoveAll(dir)
	for _, key := range []string{"SESSIONS_ANTIGRAVITY_ROOT", "SESSIONS_GROK_ROOT", "SESSIONS_MUSE_ROOT"} {
		os.Setenv(key, dir+"/"+key)
	}
	os.Exit(m.Run())
}
