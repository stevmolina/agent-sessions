package source

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/model"
)

func TestImportsDiscoverAndLoad(t *testing.T) {
	root := t.TempDir()
	t.Setenv("SESSIONS_IMPORTS_ROOT", root)
	conv := Imported{Source: SourceGrok, ID: "conv-1", StartedAt: "2026-05-01T00:00:00Z", Turns: []model.Turn{
		{Role: "user", Text: " first question "}, {Role: "tool", Text: "dropped"}, {Role: "assistant", Text: "answer"},
	}}
	if changed, err := WriteImported(root, conv); err != nil || !changed {
		t.Fatalf("changed=%v err=%v", changed, err)
	}
	// Stray files are ignored.
	if err := os.WriteFile(filepath.Join(root, SourceGrok, "notes.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	found, err := Imports{}.Discover()
	if err != nil || len(found) != 1 || found[0].Source != SourceGrok {
		t.Fatalf("found=%+v err=%v", found, err)
	}
	s, err := AdapterFor(SourceGrok).Load(found[0])
	if err != nil {
		t.Fatal(err)
	}
	if s.ID != "conv-1" || s.Provider != SourceGrok || s.Title != "first question" || s.CWD != nil {
		t.Fatalf("%+v", s)
	}
	if *s.StartedAt != "2026-05-01T00:00:00Z" || *s.UpdatedAt != "2026-05-01T00:00:00Z" {
		t.Fatalf("times %v %v", *s.StartedAt, *s.UpdatedAt)
	}
	if s.Body != "user: first question\n\nassistant: answer" {
		t.Fatalf("body %q", s.Body)
	}
	if err := CheckSnapshot([]string{found[0].Locator}); err != nil {
		t.Fatalf("an import should count as a known root: %v", err)
	}
}

func TestImportedPathNeverEscapes(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"../../evil", "a/b", ".hidden", "x..y", strings.Repeat("a", 200), `c:\x`} {
		path := ImportedPath(root, Imported{Source: SourceChatGPT, ID: id})
		if filepath.Dir(path) != filepath.Join(root, SourceChatGPT) || !strings.HasPrefix(filepath.Base(path), "sha256-") {
			t.Errorf("%q stored at %s", id, path)
		}
	}
	if got := filepath.Base(ImportedPath(root, Imported{Source: SourceChatGPT, ID: "6790a1b2-c3d4"})); got != "6790a1b2-c3d4.json" {
		t.Fatal(got)
	}
}

func TestLoadRefusesMismatchedSource(t *testing.T) {
	root := t.TempDir()
	if _, err := WriteImported(root, Imported{Source: SourceChatGPT, ID: "c1", Turns: []model.Turn{{Role: "user", Text: "x"}}}); err != nil {
		t.Fatal(err)
	}
	path := ImportedPath(root, Imported{Source: SourceChatGPT, ID: "c1"})
	if _, err := (Imports{}).Load(Candidate{Source: SourceGrok, OriginPath: path, Locator: path}); err == nil {
		t.Fatal("a file in the wrong folder must not load")
	}
	if _, err := WriteImported(root, Imported{Source: "cursor", ID: "c1"}); err == nil {
		t.Fatal("only import sources may be written")
	}
}
