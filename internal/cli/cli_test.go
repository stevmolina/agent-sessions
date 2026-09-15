package cli

import (
	"bytes"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/source"
)

func TestIndexSearchShowLifecycle(t *testing.T) {
	roots := installFixtures(t)

	code, stdout, stderr := run("index")
	if code != 0 || stdout != "" || stderr != "3 upserted, 0 unchanged, 0 deleted, 0 failed\n" {
		t.Fatalf("first index: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run("search", "mango-helix-882", "--source", "claude", "--cwd", "/tmp/claude-proj", "--limit", "1")
	if code != 0 || !strings.Contains(stdout, "claude-fixture-1\tclaude\t") || !strings.Contains(stdout, "mango-helix-882") {
		t.Fatalf("filtered search: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, stdout, stderr = run("show", "codex:codex-fixture-1")
	if code != 0 || !strings.Contains(stdout, "# id=codex-fixture-1 source=codex provider=codex cwd=/tmp/codex-proj") {
		t.Fatalf("show: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	for _, forbidden := range []string{"secret-tool-dump", "function_call", "tool_use"} {
		if strings.Contains(stdout, forbidden) {
			t.Fatalf("show leaked %q in %q", forbidden, stdout)
		}
	}

	code, _, stderr = run("index")
	if code != 0 || stderr != "0 upserted, 3 unchanged, 0 deleted, 0 failed\n" {
		t.Fatalf("unchanged index: code=%d stderr=%q", code, stderr)
	}
	cursor := filepath.Join(roots.cursor, "Users-tmp-proj", "agent-transcripts", "aaaa", "aaaa.jsonl")
	if err := os.Chmod(cursor, 0); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(cursor, 0o644) })
	code, _, stderr = run("index")
	if code != 0 || stderr != "0 upserted, 3 unchanged, 0 deleted, 0 failed\n" {
		t.Fatalf("unchanged unreadable file: code=%d stderr=%q", code, stderr)
	}

	claude := filepath.Join(roots.claude, "-tmp-proj", "claude-fixture-1.jsonl")
	f, err := os.OpenFile(claude, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("\n"); err != nil {
		f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err := os.Chtimes(claude, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run("index")
	if code != 0 || stderr != "1 upserted, 2 unchanged, 0 deleted, 0 failed\n" {
		t.Fatalf("changed index: code=%d stderr=%q", code, stderr)
	}

	codex := filepath.Join(roots.codex, "2026", "08", "26", "rollout-codex-fixture-1.jsonl")
	if err := os.Remove(codex); err != nil {
		t.Fatal(err)
	}
	code, _, stderr = run("index")
	if code != 0 || stderr != "0 upserted, 2 unchanged, 1 deleted, 0 failed\n" {
		t.Fatalf("deleted index: code=%d stderr=%q", code, stderr)
	}
	code, stdout, _ = run("search", "quartz-ember-553")
	if code != 0 || stdout != "no matches\n" {
		t.Fatalf("deleted search: code=%d stdout=%q", code, stdout)
	}
}

type fixtureRoots struct {
	cursor string
	claude string
	codex  string
	t3     string
}

func installFixtures(t *testing.T) fixtureRoots {
	t.Helper()
	root := t.TempDir()
	cursor := filepath.Join(root, "cursor")
	claude := filepath.Join(root, "claude")
	codex := filepath.Join(root, "codex")
	archive := filepath.Join(root, "archive")
	t3root := filepath.Join(root, "t3")
	copyFixture(t, "cursor.jsonl", filepath.Join(cursor, "Users-tmp-proj", "agent-transcripts", "aaaa", "aaaa.jsonl"))
	copyFixture(t, "claude.jsonl", filepath.Join(claude, "-tmp-proj", "claude-fixture-1.jsonl"))
	copyFixture(t, "codex.jsonl", filepath.Join(codex, "2026", "08", "26", "rollout-codex-fixture-1.jsonl"))
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(t3root, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_INDEX", filepath.Join(root, "index.sqlite"))
	t.Setenv("SESSIONS_CURSOR_ROOT", cursor)
	t.Setenv("SESSIONS_CLAUDE_ROOT", claude)
	t.Setenv("SESSIONS_CODEX_ROOT", codex)
	t.Setenv("SESSIONS_CODEX_ARCHIVE_ROOT", archive)
	t.Setenv("SESSIONS_T3_ROOT", t3root)
	t.Setenv("SESSIONS_T3_INDEXEDDB", filepath.Join(root, "missing-indexeddb"))
	return fixtureRoots{cursor: cursor, claude: claude, codex: codex, t3: t3root}
}

func TestT3ProviderSearchAndDedup(t *testing.T) {
	roots := installFixtures(t)
	if _, err := source.WriteT3Fixture(roots.t3, "env-test", []source.T3ThreadFixture{
		{
			ID: "thread-codex", Title: "T3 via Codex quartz-ember-553",
			Provider: "codex", ProviderSession: "codex-fixture-1", CWD: "/tmp/codex-proj",
			Messages: []source.T3MessageFixture{
				{Role: "user", Text: "Codex distinctive phrase quartz-ember-553 from t3"},
				{Role: "assistant", Text: "working"},
			},
		},
		{
			ID: "thread-claude", Title: "T3 via Claude t3-claude-222",
			Provider: "claudeAgent", ProviderInstance: "acct-claude", CWD: "/tmp/claude-proj",
			Messages: []source.T3MessageFixture{{Role: "user", Text: "T3 Claude distinctive phrase t3-claude-222"}},
		},
		{
			ID: "thread-cursor", Title: "T3 via Cursor t3-cursor-333",
			Provider: "cursor", CWD: "/tmp/cursor-proj",
			Messages: []source.T3MessageFixture{{Role: "user", Text: "T3 Cursor distinctive phrase t3-cursor-333"}},
		},
	}); err != nil {
		t.Fatal(err)
	}

	code, _, stderr := run("index")
	if code != 0 || !strings.Contains(stderr, "6 upserted") {
		t.Fatalf("index: code=%d stderr=%q", code, stderr)
	}

	code, stdout, stderr := run("search", "quartz-ember-553")
	if code != 0 {
		t.Fatalf("search: %q %q", stdout, stderr)
	}
	if strings.Count(stdout, "quartz-ember-553") < 1 || strings.Contains(stdout, "codex-fixture-1\tcodex\t") {
		t.Fatalf("default search should collapse to t3: %q", stdout)
	}
	if !strings.Contains(stdout, "thread-codex\tt3code\t") || !strings.Contains(stdout, "provider: codex") {
		t.Fatalf("t3 result missing provider: %q", stdout)
	}

	code, stdout, _ = run("search", "quartz-ember-553", "--source", "codex")
	if code != 0 || !strings.Contains(stdout, "codex-fixture-1\tcodex\t") || strings.Contains(stdout, "thread-codex") {
		t.Fatalf("source=codex should stay native: %q", stdout)
	}

	code, stdout, _ = run("search", "quartz-ember-553", "--source", "t3code")
	if code != 0 || !strings.Contains(stdout, "thread-codex\tt3code\t") || strings.Contains(stdout, "codex-fixture-1\tcodex\t") {
		t.Fatalf("source=t3code: %q", stdout)
	}

	code, stdout, _ = run("search", "quartz-ember-553", "--provider", "codex")
	if code != 0 || !strings.Contains(stdout, "thread-codex\tt3code\t") || strings.Contains(stdout, "codex-fixture-1\tcodex\t") {
		t.Fatalf("provider=codex collapsed: %q", stdout)
	}

	code, stdout, _ = run("search", "quartz-ember-553", "--all-copies")
	if code != 0 || !strings.Contains(stdout, "thread-codex\tt3code\t") || !strings.Contains(stdout, "codex-fixture-1\tcodex\t") {
		t.Fatalf("all-copies: %q", stdout)
	}

	code, stdout, _ = run("search", "t3-claude-222", "--source", "t3code", "--provider", "claude")
	if code != 0 || !strings.Contains(stdout, "thread-claude\tt3code\t") {
		t.Fatalf("combined filters: %q", stdout)
	}

	code, stdout, stderr = run("show", "t3code:thread-codex")
	if code != 0 || !strings.Contains(stdout, "# id=thread-codex source=t3code provider=codex") {
		t.Fatalf("show t3: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run("show", "t3code://env-test/thread-codex")
	if code != 0 || !strings.Contains(stdout, "# id=thread-codex source=t3code provider=codex") {
		t.Fatalf("show locator: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, stderr = run("show", "codex:codex-fixture-1")
	if code != 0 || !strings.Contains(stdout, "# id=codex-fixture-1 source=codex provider=codex") {
		t.Fatalf("show native: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}

	code, _, stderr = run("index")
	if code != 0 || !strings.Contains(stderr, "0 upserted, 6 unchanged") {
		t.Fatalf("unchanged t3: %q", stderr)
	}

	db, err := sql.Open("sqlite", filepath.Join(roots.t3, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projection_threads SET updated_at='2026-08-26T05:00:00.000Z', title='T3 via Codex quartz-ember-553 changed' WHERE thread_id='thread-codex'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projection_thread_messages SET text='changed quartz-ember-553' WHERE thread_id='thread-codex' AND role='user'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	code, _, stderr = run("index")
	if code != 0 || !strings.Contains(stderr, "1 upserted, 5 unchanged") {
		t.Fatalf("changed t3: %q", stderr)
	}

	db, err = sql.Open("sqlite", filepath.Join(roots.t3, "state.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE projection_threads SET deleted_at='2026-08-26T06:00:00.000Z' WHERE thread_id='thread-cursor'`); err != nil {
		t.Fatal(err)
	}
	db.Close()
	code, _, stderr = run("index")
	if code != 0 || !strings.Contains(stderr, "0 upserted, 5 unchanged, 1 deleted") {
		t.Fatalf("deleted t3: %q", stderr)
	}
	code, stdout, _ = run("search", "t3-cursor-333")
	if code != 0 || stdout != "no matches\n" {
		t.Fatalf("deleted t3 search: %q", stdout)
	}
}

func TestInvalidSourceAndProvider(t *testing.T) {
	code, _, stderr := run("search", "x", "--source", "nope")
	if code != 2 || !strings.Contains(stderr, "t3code") {
		t.Fatalf("invalid source: code=%d stderr=%q", code, stderr)
	}
	code, _, stderr = run("search", "x", "--provider", "1bad")
	if code != 2 || !strings.Contains(stderr, "provider") {
		t.Fatalf("invalid provider: code=%d stderr=%q", code, stderr)
	}
}

func copyFixture(t *testing.T, name, destination string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "tests", "fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func run(args ...string) (int, string, string) {
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := Main(args, &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}
