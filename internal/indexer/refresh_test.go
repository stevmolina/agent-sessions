package indexer

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/clean"
	"github.com/usuario/sessions/internal/model"
	"github.com/usuario/sessions/internal/source"
	"github.com/usuario/sessions/internal/store"
)

type stubCleaner struct {
	fp    string
	fail  bool
	calls int
}

func (s *stubCleaner) Fingerprint() string { return s.fp }
func (s *stubCleaner) Apply(sess *model.Session) error {
	s.calls++
	if s.fail {
		return errors.New("redaction failed")
	}
	return nil
}

func TestRefreshSkipsSessionWhenRedactionFails(t *testing.T) {
	db, path := openFixture(t, "keep this sentence")
	defer db.Close()
	ok := &stubCleaner{fp: "v1"}
	stats, err := Refresh(db, ok)
	if err != nil || stats.Upserted != 1 || stats.Failed != 0 {
		t.Fatalf("first refresh: %+v %v", stats, err)
	}
	bad := &stubCleaner{fp: "v2", fail: true}
	stats, err = Refresh(db, bad)
	if err != nil || stats.Failed != 1 || stats.Upserted != 0 {
		t.Fatalf("failed redaction: %+v %v", stats, err)
	}
	var n int
	if err := db.QueryRow("SELECT COUNT(*) FROM sessions").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("raw session remained indexed: %d", n)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "keep this sentence") {
		t.Fatal("raw transcript changed")
	}
}

func TestRefreshReindexesWhenFingerprintChanges(t *testing.T) {
	db, _ := openFixture(t, "stable sentence")
	defer db.Close()
	first := &stubCleaner{fp: "one"}
	if _, err := Refresh(db, first); err != nil {
		t.Fatal(err)
	}
	if first.calls != 1 {
		t.Fatalf("calls=%d", first.calls)
	}
	again := &stubCleaner{fp: "one"}
	stats, err := Refresh(db, again)
	if err != nil || stats.Unchanged != 1 || stats.Upserted != 0 || again.calls != 0 {
		t.Fatalf("unchanged: %+v calls=%d err=%v", stats, again.calls, err)
	}
	rotated := &stubCleaner{fp: "two"}
	stats, err = Refresh(db, rotated)
	if err != nil || stats.Upserted != 1 || rotated.calls != 1 {
		t.Fatalf("rotated: %+v calls=%d err=%v", stats, rotated.calls, err)
	}
}

func TestRefreshStoresCleanTextAndLeavesTheFile(t *testing.T) {
	pat := "ghp_" + mixedToken(36)
	db, path := openFixture(t, "token "+pat)
	defer db.Close()
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	c, err := clean.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	stats, err := Refresh(db, c)
	if err != nil || stats.Failed != 0 || stats.Upserted != 1 {
		t.Fatalf("refresh: %+v %v", stats, err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(original) {
		t.Fatal("raw transcript was modified")
	}
	var body string
	if err := db.QueryRow("SELECT body FROM sessions_fts").Scan(&body); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(body, pat) {
		t.Fatal("secret survived in index")
	}
	if !strings.Contains(body, "[REDACTED:github-pat]") {
		t.Fatal("missing github-pat placeholder")
	}
}

func mixedToken(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = alphabet[(i*7+3)%len(alphabet)]
	}
	return string(b)
}

func openFixture(t *testing.T, text string) (*sql.DB, string) {
	t.Helper()
	root := t.TempDir()
	claude := filepath.Join(root, "claude")
	for _, key := range []string{"SESSIONS_CURSOR_ROOT", "SESSIONS_CODEX_ROOT", "SESSIONS_CODEX_ARCHIVE_ROOT"} {
		dir := filepath.Join(root, key)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, dir)
	}
	t.Setenv("SESSIONS_CLAUDE_ROOT", claude)
	t.Setenv("SESSIONS_T3_ROOT", filepath.Join(root, "t3"))
	t.Setenv("SESSIONS_T3_INDEXEDDB", filepath.Join(root, "missing-indexeddb"))
	path := filepath.Join(claude, "proj", "sess.jsonl")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	line := `{"type":"user","message":{"role":"user","content":"` + text + `"},"cwd":"/tmp/p","sessionId":"sess","timestamp":"2026-08-26T01:00:01.000Z"}` + "\n"
	if err := os.WriteFile(path, []byte(line), 0o644); err != nil {
		t.Fatal(err)
	}
	db, err := store.Open(filepath.Join(root, "index.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	return db, path
}

type failingLoadAdapter struct{ candidates []source.Candidate }

func (a failingLoadAdapter) Discover() ([]source.Candidate, error) { return a.candidates, nil }
func (a failingLoadAdapter) Load(source.Candidate) (*model.Session, error) {
	return nil, errors.New("unreadable transcript")
}
func TestRefreshRemovesPreviousTextWhenLoadFails(t *testing.T) {
	db, path := openFixture(t, "previous searchable text")
	defer db.Close()
	if _, err := Refresh(db, &stubCleaner{fp: "old"}); err != nil {
		t.Fatal(err)
	}
	candidates, err := (source.JSONL{}).Discover()
	if err != nil {
		t.Fatal(err)
	}
	stats, err := refresh(db, &stubCleaner{fp: "new"}, []source.SourceAdapter{failingLoadAdapter{candidates}})
	if err != nil || stats.Failed != 1 || len(stats.Warnings) != 1 || !strings.HasPrefix(stats.Warnings[0], "redaction failed") {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sessions_fts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("previous text survived failed load")
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal("raw transcript was removed")
	}
	stats, err = Refresh(db, &stubCleaner{fp: "new"})
	if err != nil || stats.Upserted != 1 {
		t.Fatalf("retry stats=%+v err=%v", stats, err)
	}
}

func TestRefreshRetainsRowsWhenPreviouslyIndexedRootDisappears(t *testing.T) {
	db, path := openFixture(t, "keep remote copy")
	defer db.Close()
	if _, err := Refresh(db, &stubCleaner{fp: "old"}); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(path))
	if err := os.Rename(root, root+"-offline"); err != nil {
		t.Fatal(err)
	}
	stats, err := Refresh(db, &stubCleaner{fp: "new"})
	if err != nil || stats.Failed != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	var count int
	if err := db.QueryRow("SELECT count(*) FROM sessions").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("unavailable root removed indexed rows")
	}
	fp, err := store.Meta(db, "clean_fingerprint")
	if err != nil || fp != "old" {
		t.Fatalf("fingerprint=%q err=%v", fp, err)
	}
}
func TestRefreshAllowsCompleteEmptySnapshot(t *testing.T) {
	db, path := openFixture(t, "remove deliberately")
	defer db.Close()
	if _, err := Refresh(db, &stubCleaner{fp: "policy"}); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	stats, err := Refresh(db, &stubCleaner{fp: "policy"})
	if err != nil || stats.Failed != 0 || stats.Deleted != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestRefreshRetainsPreviousT3Environment(t *testing.T) {
	db, _ := openFixture(t, "ordinary transcript")
	defer db.Close()
	envA := filepath.Join(t.TempDir(), "a")
	envB := filepath.Join(t.TempDir(), "b")
	thread := []source.T3ThreadFixture{{ID: "thread", Title: "first environment", Provider: "codex", Messages: []source.T3MessageFixture{{Role: "user", Text: "retain this T3 chat"}}}}
	if _, err := source.WriteT3Fixture(envA, "env-a", thread); err != nil {
		t.Fatal(err)
	}
	if _, err := source.WriteT3Fixture(envB, "env-b", nil); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", envA)
	if _, err := Refresh(db, &stubCleaner{fp: "policy"}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_T3_ROOT", envB)
	stats, err := Refresh(db, &stubCleaner{fp: "policy"})
	if err != nil || stats.Failed != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	var count int
	if err := db.QueryRow(`SELECT count(*) FROM sessions WHERE path='t3code://env-a/thread'`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatal("changing T3 environment removed the previous chat")
	}
}
