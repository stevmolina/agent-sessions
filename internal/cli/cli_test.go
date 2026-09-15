package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if code != 0 || !strings.Contains(stdout, "# id=codex-fixture-1 source=codex cwd=/tmp/codex-proj") {
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
}

func installFixtures(t *testing.T) fixtureRoots {
	t.Helper()
	root := t.TempDir()
	cursor := filepath.Join(root, "cursor")
	claude := filepath.Join(root, "claude")
	codex := filepath.Join(root, "codex")
	archive := filepath.Join(root, "archive")
	copyFixture(t, "cursor.jsonl", filepath.Join(cursor, "Users-tmp-proj", "agent-transcripts", "aaaa", "aaaa.jsonl"))
	copyFixture(t, "claude.jsonl", filepath.Join(claude, "-tmp-proj", "claude-fixture-1.jsonl"))
	copyFixture(t, "codex.jsonl", filepath.Join(codex, "2026", "08", "26", "rollout-codex-fixture-1.jsonl"))
	if err := os.MkdirAll(archive, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_INDEX", filepath.Join(root, "index.sqlite"))
	t.Setenv("SESSIONS_CURSOR_ROOT", cursor)
	t.Setenv("SESSIONS_CLAUDE_ROOT", claude)
	t.Setenv("SESSIONS_CODEX_ROOT", codex)
	t.Setenv("SESSIONS_CODEX_ARCHIVE_ROOT", archive)
	return fixtureRoots{cursor: cursor, claude: claude, codex: codex}
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
