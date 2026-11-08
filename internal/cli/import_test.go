package cli

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// An imported conversation is indexed, redacted and searchable like any other
// session, and importing the same export again changes nothing.
func TestImportIndexesAndRedacts(t *testing.T) {
	installFixtures(t)
	dir := t.TempDir()
	secrets := filepath.Join(dir, "secrets.json")
	if err := os.WriteFile(secrets, []byte(`{"INVENTED_TOKEN":"zq8-Kp2vLm9xTr4wBn7Yc"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("SESSIONS_SECRETS_JSON", secrets)
	export := filepath.Join(dir, "claude-export.zip")
	writeZip(t, export, map[string]string{
		"conversations.json": `[{"uuid": "conv-imported-1", "name": "Garden plan", "created_at": "2026-04-01T09:00:00Z", "updated_at": "2026-04-01T09:10:00Z",
		  "chat_messages": [
		    {"sender": "human", "text": "plan the lilac-orbit-417 garden, token zq8-Kp2vLm9xTr4wBn7Yc"},
		    {"sender": "assistant", "content": [{"type": "text", "text": "Start with the beds."}]}
		  ]}]`,
		"users.json": `[]`,
	})

	code, stdout, stderr := run("import", export)
	if code != 0 || !strings.Contains(stdout, "claude-ai: 1 imported, 0 unchanged, 0 skipped\n") {
		t.Fatalf("import: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	code, stdout, _ = run("import", export)
	if code != 0 || !strings.Contains(stdout, "claude-ai: 0 imported, 1 unchanged, 0 skipped\n") {
		t.Fatalf("re-import: code=%d stdout=%q", code, stdout)
	}

	code, stdout, stderr = run("search", "lilac-orbit-417", "--source", "claude-ai")
	if code != 0 || !strings.Contains(stdout, "conv-imported-1\tclaude-ai\t2026-04-01T09:00:00Z") || !strings.Contains(stdout, "title: Garden plan") {
		t.Fatalf("search: code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if strings.Contains(stdout, "zq8-Kp2vLm9xTr4wBn7Yc") || !strings.Contains(stdout, "[REDACTED:INVENTED_TOKEN]") {
		t.Fatalf("imported text was not redacted: %q", stdout)
	}
	code, stdout, _ = run("show", "claude-ai:conv-imported-1")
	if code != 0 || !strings.Contains(stdout, "source=claude-ai provider=claude-ai") || !strings.Contains(stdout, "assistant:\nStart with the beds.") {
		t.Fatalf("show: code=%d stdout=%q", code, stdout)
	}
}

func TestImportRejectsBadInput(t *testing.T) {
	installFixtures(t)
	dir := t.TempDir()
	notExport := filepath.Join(dir, "photos.zip")
	writeZip(t, notExport, map[string]string{"a.jpg": "x"})
	if code, _, stderr := run("import", notExport); code != 1 || !strings.Contains(stderr, "no ChatGPT, Claude.ai or Grok conversations") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if code, _, _ := run("import"); code != 2 {
		t.Fatalf("missing file should be a usage error, got %d", code)
	}
	if code, _, _ := run("import", "--nope", notExport); code != 2 {
		t.Fatalf("unknown flag should be a usage error, got %d", code)
	}
}

// A broken file under the imports folder is moved aside and skipped. It must
// not count as a failure, which would fail every push and block deletes.
func TestBrokenImportIsQuarantined(t *testing.T) {
	installFixtures(t)
	dir := t.TempDir()
	export := filepath.Join(dir, "export.zip")
	writeZip(t, export, map[string]string{
		"conversations.json": `[{"uuid": "conv-broken-1", "name": "x", "chat_messages": [{"sender": "human", "text": "teal-comet-905"}]}]`,
	})
	if code, _, stderr := run("import", export); code != 0 {
		t.Fatalf("import: %s", stderr)
	}
	if code, stdout, _ := run("search", "teal-comet-905"); code != 0 || !strings.Contains(stdout, "conv-broken-1") {
		t.Fatalf("search: %q", stdout)
	}
	stored := filepath.Join(os.Getenv("SESSIONS_IMPORTS_ROOT"), "claude-ai", "conv-broken-1.json")
	if err := os.WriteFile(stored, []byte("{not json, and a different size"), 0o600); err != nil {
		t.Fatal(err)
	}
	code, _, stderr := run("index")
	if code != 0 || !strings.Contains(stderr, "0 failed\n") || !strings.Contains(stderr, "skipped "+stored+": not a stored conversation, moved to conv-broken-1.json.bad") {
		t.Fatalf("index: code=%d stderr=%q", code, stderr)
	}
	if _, err := os.Stat(stored + ".bad"); err != nil {
		t.Fatal("broken file was not kept aside")
	}
	if code, stdout, _ := run("search", "teal-comet-905"); code != 0 || stdout != "no matches\n" {
		t.Fatalf("broken import still indexed: %q", stdout)
	}
	// The next run is clean: nothing left to trip on.
	if code, _, stderr := run("index"); code != 0 || strings.Contains(stderr, "skipped") {
		t.Fatalf("second index: code=%d stderr=%q", code, stderr)
	}
}
