package repotag

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/usuario/sessions/internal/store"
)

func TestNormalizeRemote(t *testing.T) {
	cases := map[string]string{
		"git@github.com:StevMolina/Pokebots.git":        "stevmolina/pokebots",
		"https://github.com/stevmolina/pokebots":        "stevmolina/pokebots",
		"https://x-access-token:abc@github.com/o/n.git": "o/n",
		"ssh://git@github.com/o/n.git":                  "o/n",
		"https://github.com/o/some.repo.git":            "o/some.repo",
		"https://gitlab.com/o/n.git":                    "",
		"https://github.com.evil.example/o/n":           "",
		"https://github.com/orgs/acme/people":           "",
		"/local/path/repo":                              "",
		"https://api.github.com/repos/o/n/pulls":        "o/n",
		"git@github.com:o/n":                            "o/n",
	}
	for in, want := range cases {
		got, ok := NormalizeRemote(in)
		if got != want || ok != (want != "") {
			t.Errorf("%q: got %q,%v want %q", in, got, ok, want)
		}
	}
}

func TestFromTextFindsNamedRepos(t *testing.T) {
	text := strings.Join([]string{
		`gh pr list -R stevmolina/pokebots --state open`,
		`gh repo clone Acme/Widgets`,
		`git clone https://github.com/acme/other.git /tmp/x`,
		`git clone git@github.com:acme/third.git`,
		`gh api repos/acme/fourth/pulls`,
		`gh pr view 3 --repo=acme/fifth`,
		`curl https://github.com/orgs/acme`,
		`echo notgithub.com/a/b`,
	}, "\n")
	want := []string{"acme/other", "acme/third", "acme/fourth", "stevmolina/pokebots", "acme/fifth", "acme/widgets"}
	got := FromText(text)
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	have := map[string]bool{}
	for _, g := range got {
		have[g] = true
	}
	for _, w := range want {
		if !have[w] {
			t.Errorf("missing %s in %v", w, got)
		}
	}
}

func TestRepoFromConfigPrefersOrigin(t *testing.T) {
	cfg := "[core]\n\tbare = false\n[remote \"upstream\"]\n\turl = git@github.com:openbot/openbot.git\n[remote \"origin\"]\n\turl = https://github.com/stevmolina/pokebots.git\n"
	if got := RepoFromConfigText(cfg); got != "stevmolina/pokebots" {
		t.Fatal(got)
	}
	// origin off GitHub: a GitHub fork or upstream still tags the chat.
	cfg = "[remote \"origin\"]\n\turl = git@git.example.com:me/x.git\n[remote \"fork\"]\n\turl = git@github.com:me/x.git\n"
	if got := RepoFromConfigText(cfg); got != "me/x" {
		t.Fatal(got)
	}
	if got := RepoFromConfigText("[remote \"origin\"]\n\turl = /srv/git/x.git\n"); got != "" {
		t.Fatal(got)
	}
}

// checkout makes a folder that looks like a git checkout with one remote.
func checkout(t *testing.T, dir, url string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "[remote \"origin\"]\n\turl = " + url + "\n"
	if err := os.WriteFile(filepath.Join(dir, ".git", "config"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirResolvesSubfoldersAndWorktrees(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	main := filepath.Join(root, "dev", "pokebots")
	checkout(t, main, "git@github.com:stevmolina/pokebots.git")
	if err := os.MkdirAll(filepath.Join(main, "src", "deep"), 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree: .git is a file pointing into the main .git folder.
	gitdir := filepath.Join(main, ".git", "worktrees", "feat")
	if err := os.MkdirAll(gitdir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitdir, "commondir"), []byte("../..\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	wt := filepath.Join(root, "wt", "feat")
	if err := os.MkdirAll(wt, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wt, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	r := New()
	for _, dir := range []string{main, filepath.Join(main, "src", "deep"), filepath.Join(main, "gone", "folder"), wt} {
		if got := r.Dir(dir); got != "stevmolina/pokebots" {
			t.Errorf("%s: got %q", dir, got)
		}
	}
	if got := r.Dir(root); got != "" {
		t.Errorf("plain folder: got %q", got)
	}
	// Cached: remove the config, the answer stays.
	os.RemoveAll(filepath.Join(main, ".git"))
	if got := r.Dir(main); got != "stevmolina/pokebots" {
		t.Errorf("cache lost: %q", got)
	}
}

func TestHomeFolderRepoDoesNotTagEverything(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	checkout(t, home, "git@github.com:me/dotfiles.git")
	r := New()
	if got := r.Dir(filepath.Join(home, "Downloads", "x")); got != "" {
		t.Fatalf("got %q", got)
	}
}

func writeJSONL(t *testing.T, lines ...string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "chat.jsonl")
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFullReadsToolCallsOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	work := filepath.Join(root, "dev", "pokebots")
	checkout(t, work, "git@github.com:stevmolina/pokebots.git")
	skills := filepath.Join(root, "dev", "agents")
	checkout(t, skills, "git@github.com:stevmolina/agents.git")
	path := writeJSONL(t,
		// Claude: Bash names a repo, an edit and a bash cd resolve checkouts.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"gh pr list -R acme/widgets"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"`+work+`/src/a.ts","old_string":"github.com/prose/ignored","new_string":"x"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Bash","input":{"command":"cd `+work+` && git status"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Edit","input":{"file_path":"`+work+`/src/b.ts"}}]}}`,
		// One read of a skill file is not enough to tag that repo.
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"Read","input":{"file_path":"`+skills+`/skills/ship/SKILL.md"}}]}}`,
		// Prose is never scanned.
		`{"type":"user","message":{"content":"please look at github.com/prose/mentioned"}}`,
		// Codex: arguments is a JSON string.
		`{"type":"response_item","payload":{"type":"function_call","name":"exec_command","arguments":"{\"cmd\":\"gh repo clone acme/codex-repo\",\"workdir\":\"/nope\"}"}}`,
	)
	r := New()
	row := store.ExportRow{Path: path}
	got := r.Full(row)
	want := []string{"acme/widgets", "acme/codex-repo", "stevmolina/pokebots"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v want %v", got, want)
	}
}

func TestFullPutsFolderFirstAndCaps(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	work := filepath.Join(root, "pokebots")
	checkout(t, work, "git@github.com:stevmolina/pokebots.git")
	var lines []string
	for _, n := range []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l"} {
		lines = append(lines, `{"message":{"content":[{"type":"tool_use","input":{"command":"gh repo view o/`+n+`"}}]}}`)
	}
	r := New()
	cwd := work
	got := r.Full(store.ExportRow{CWD: &cwd, Path: writeJSONL(t, lines...)})
	if len(got) != MaxRepos || got[0] != "stevmolina/pokebots" {
		t.Fatalf("got %v", got)
	}
}

func TestMissingFileOrNonJSONLIsFolderOnly(t *testing.T) {
	root := t.TempDir()
	t.Setenv("HOME", filepath.Join(root, "home"))
	work := filepath.Join(root, "w")
	checkout(t, work, "git@github.com:o/w.git")
	r := New()
	got := r.Full(store.ExportRow{CWD: &work, Path: "t3code://env/thread"})
	if !reflect.DeepEqual(got, []string{"o/w"}) {
		t.Fatal(got)
	}
	if got := r.Full(store.ExportRow{Path: "/does/not/exist.jsonl"}); len(got) != 0 {
		t.Fatal(got)
	}
}
