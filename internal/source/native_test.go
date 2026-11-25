package source

import (
	"errors"
	"os"
	"strings"
	"testing"
)

func setRoots(t *testing.T) (antigravity, grok, muse string) {
	t.Helper()
	antigravity, grok, muse = t.TempDir(), t.TempDir(), t.TempDir()
	t.Setenv("SESSIONS_ANTIGRAVITY_ROOT", antigravity)
	t.Setenv("SESSIONS_GROK_ROOT", grok)
	t.Setenv("SESSIONS_MUSE_ROOT", muse)
	return
}

var chat = []NativeMsg{
	{"user", "<system-reminder>skills</system-reminder>Make the settings page work on iPad. Keep it simple."},
	{"assistant", "I'll check the layout at 820 px first."},
	{"user", "go"},
	{"assistant", "Done: the settings page now fits an iPad."},
}

func loadOne(t *testing.T, a SourceAdapter, wantSource string) (n int, text string, title string) {
	t.Helper()
	cands, err := a.Discover()
	if err != nil || len(cands) != 1 {
		t.Fatalf("discover: %v %v", cands, err)
	}
	if cands[0].Source != wantSource {
		t.Fatalf("source %q, want %q", cands[0].Source, wantSource)
	}
	s, err := a.Load(cands[0])
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Body, "must never be indexed") || strings.Contains(s.Body, "private reasoning") {
		t.Fatalf("tool or reasoning text leaked into the body:\n%s", s.Body)
	}
	if s.Source != wantSource || s.Revision == "" || s.StartedAt == nil || s.UpdatedAt == nil || s.CWD == nil {
		t.Fatalf("incomplete session: %+v", s)
	}
	return len(s.Turns), s.Body, s.Title
}

func TestGrokCLIDiscoverAndLoad(t *testing.T) {
	_, grok, _ := setRoots(t)
	if _, err := WriteGrokFixture(grok, "/Users/u/dev/resorsi/engagements/halorx", "01a13b45", "T3 Code browser", chat); err != nil {
		t.Fatal(err)
	}
	// A background job with no typed prompt, and a memory job folder: both left out.
	if _, err := WriteGrokFixture(grok, "/Users/u/dev/x", "empty-one", "memory job", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGrokFixture(grok, "/var/folders/xx/T/dusk-acp-memory-AbC", "dusk-one", "memory", chat); err != nil {
		t.Fatal(err)
	}
	n, body, title := loadOne(t, GrokCLI{}, SourceGrokCLI)
	if n != 4 || !strings.Contains(body, "Make the settings page") || strings.Contains(body, "<user_query>") {
		t.Fatalf("turns=%d body=%q", n, body)
	}
	if title != "Make the settings page work on iPad." {
		t.Fatalf("title %q", title)
	}
}

func TestMuseDiscoverAndLoad(t *testing.T) {
	_, _, muse := setRoots(t)
	if _, err := WriteMuseFixture(muse, "/Users/u/dev/projects/pokebots", "01a205b2", chat); err != nil {
		t.Fatal(err)
	}
	n, body, title := loadOne(t, Muse{}, SourceMuse)
	if n != 4 || !strings.Contains(body, "assistant: Done: the settings page now fits an iPad.") {
		t.Fatalf("turns=%d body=%q", n, body)
	}
	if title != "Make the settings page work on iPad." {
		t.Fatalf("title %q", title)
	}
}

func TestAntigravityDiscoverAndLoad(t *testing.T) {
	ag, _, _ := setRoots(t)
	if _, err := WriteAntigravityFixture(ag, "antigravity-cli", "/tmp/muse-test", "2a520e3d", chat); err != nil {
		t.Fatal(err)
	}
	n, body, title := loadOne(t, Antigravity{}, SourceAntigravity)
	if n != 4 || !strings.Contains(body, "Make the settings page") {
		t.Fatalf("turns=%d body=%q", n, body)
	}
	if title != "Make the settings page work on iPad." {
		t.Fatalf("title %q", title)
	}
	cands, _ := Antigravity{}.Discover()
	s, _ := Antigravity{}.Load(cands[0])
	if *s.CWD != "/tmp/muse-test" {
		t.Fatalf("cwd %q", *s.CWD)
	}
}

func TestNativeRootsMissingAreNotErrors(t *testing.T) {
	setRoots(t)
	for _, a := range []SourceAdapter{Antigravity{}, GrokCLI{}, Muse{}} {
		if c, err := a.Discover(); err != nil || len(c) != 0 {
			t.Fatalf("%T: %v %v", a, c, err)
		}
	}
}

func TestNativeRemovedFileIsSkippedNotFailed(t *testing.T) {
	_, grok, _ := setRoots(t)
	dir, _ := WriteGrokFixture(grok, "/Users/u/dev/x", "gone", "t", chat)
	cands, _ := GrokCLI{}.Discover()
	os.RemoveAll(dir)
	if _, err := (GrokCLI{}).Load(cands[0]); err == nil || !strings.Contains(err.Error(), "removed") {
		t.Fatalf("err=%v", err)
	}
}

func TestAdapterForRoutesNativeSources(t *testing.T) {
	for src, want := range map[string]SourceAdapter{SourceAntigravity: Antigravity{}, SourceGrokCLI: GrokCLI{}, SourceMuse: Muse{}} {
		if got := AdapterFor(src); got != want {
			t.Errorf("%s: %T", src, got)
		}
		if !ValidSource(src) {
			t.Errorf("%s is not a valid source", src)
		}
	}
	if _, ok := AdapterFor("grok").(Imports); !ok {
		t.Error("imported grok must keep its adapter")
	}
}

func TestEmptyConversationsAreQuietSkips(t *testing.T) {
	_, grok, muse := setRoots(t)
	_ = grok
	path, err := WriteMuseFixture(muse, "/Users/u/dev/x", "empty-muse", nil)
	if err != nil {
		t.Fatal(err)
	}
	cands, _ := Muse{}.Discover()
	if len(cands) != 1 || cands[0].OriginPath != path {
		t.Fatalf("cands=%v", cands)
	}
	_, err = Muse{}.Load(cands[0])
	var skip *SkipError
	if !errors.Is(err, ErrSkipped) || !errors.As(err, &skip) || !skip.Quiet {
		t.Fatalf("err=%v", err)
	}
}
