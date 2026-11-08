package brainmap

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const sample = `{
  "default": "someone/personal-brain",
  "rules": [
    {"brain": "someone/acme-knowledge", "match": ["engagements/acme", "worktrees/acme-*"]},
    {"brain": "someone/globex-knowledge", "match": ["engagements\\globex"]},
    {"brain": "someone/second-match", "match": ["engagements/acme/web"]}
  ]
}`

func TestBrainMatching(t *testing.T) {
	m, err := Parse([]byte(sample))
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]string{
		"/home/u/dev/engagements/acme":                "someone/acme-knowledge",
		"/home/u/dev/engagements/acme/repo":           "someone/acme-knowledge",
		"/home/u/dev/engagements/acme/web":            "someone/acme-knowledge", // first rule wins
		"/home/u/dev/engagements/acme2":               "someone/personal-brain",
		"/home/u/.t3/worktrees/acme-fix-login":        "someone/acme-knowledge",
		"/home/u/.t3/worktrees/acme-fix-login/sub":    "someone/acme-knowledge",
		"/home/u/.t3/worktrees/other-acme-fix":        "someone/personal-brain",
		`C:\Users\u\dev\engagements\globex\app`:       "someone/globex-knowledge",
		"/home/u/dev/engagements/globex":              "someone/globex-knowledge",
		"/home/u/dev/tooling/sessions":                "someone/personal-brain",
		"":                                            "someone/personal-brain",
		"   ":                                         "someone/personal-brain",
		"/home/u/dev/xengagements/acme":               "someone/personal-brain",
		"/home/u/dev/engagements/acme-knowledge/wiki": "someone/personal-brain",
	}
	for cwd, want := range cases {
		if got := m.Brain(cwd); got != want {
			t.Errorf("Brain(%q) = %q, want %q", cwd, got, want)
		}
	}
}

func TestEmptyAndNilMaps(t *testing.T) {
	var nilMap *Map
	if got := nilMap.Brain("/x/engagements/acme"); got != "" {
		t.Fatalf("nil map filed %q", got)
	}
	m, err := Parse([]byte(`{"rules":[{"brain":"someone/acme","match":["acme"]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Brain("/x/other"); got != "" {
		t.Fatalf("no default should mean no brain, got %q", got)
	}
	if got := m.Brain("/x/acme"); got != "someone/acme" {
		t.Fatalf("got %q", got)
	}
}

func TestParseRefusesBadMaps(t *testing.T) {
	bad := map[string]string{
		"not json":         `{"default":`,
		"absolute pattern": `{"rules":[{"brain":"a/b","match":["/home/u/acme"]}]}`,
		"drive pattern":    `{"rules":[{"brain":"a/b","match":["C:/acme"]}]}`,
		"empty pattern":    `{"rules":[{"brain":"a/b","match":[""]}]}`,
		"star only":        `{"rules":[{"brain":"a/b","match":["*"]}]}`,
		"no patterns":      `{"rules":[{"brain":"a/b","match":[]}]}`,
		"no brain":         `{"rules":[{"brain":"","match":["acme"]}]}`,
		"unknown field":    `{"defualt":"a/b"}`,
		"trailing data":    `{"default":"a/b"} {}`,
	}
	for name, raw := range bad {
		if _, err := Parse([]byte(raw)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	m, err := LoadFile(filepath.Join(dir, "missing.json"))
	if err != nil || m == nil || m.Brain("/x") != "" {
		t.Fatalf("missing file should be an empty map: %v %+v", err, m)
	}
	path := filepath.Join(dir, "brains.json")
	if err := os.WriteFile(path, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	m, err = LoadFile(path)
	if err != nil || m.Brain("/x/engagements/acme") != "someone/acme-knowledge" {
		t.Fatalf("load: %v", err)
	}
	if err := os.WriteFile(path, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil || !strings.Contains(err.Error(), "brain map") {
		t.Fatalf("malformed file should fail, got %v", err)
	}
}

func TestPathFollowsEnv(t *testing.T) {
	t.Setenv("SESSIONS_BRAINS_FILE", "/tmp/custom-brains.json")
	if Path() != "/tmp/custom-brains.json" {
		t.Fatal(Path())
	}
	t.Setenv("SESSIONS_BRAINS_FILE", "")
	if !strings.HasSuffix(filepath.ToSlash(Path()), ".config/sessions/brains.json") {
		t.Fatal(Path())
	}
}
