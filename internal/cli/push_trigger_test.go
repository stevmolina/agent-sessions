//go:build unix

package cli

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/usuario/sessions/internal/pushlock"
)

// While a push runs, a hook's trigger must return at once, start nothing, and
// leave a request the running push will see.
func TestTriggerWhileBusyQueuesAndReturns(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSIONS_INDEX", filepath.Join(dir, "index.sqlite"))
	release, err := pushlock.TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	start := time.Now()
	code, stdout, stderr := run("push", "--trigger")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if took := time.Since(start); took > time.Second {
		t.Fatalf("trigger blocked for %s", took)
	}
	if !pushlock.Pending(dir) {
		t.Fatal("request not recorded")
	}
}

func TestPushWhileBusyQueuesInsteadOfRunning(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSIONS_INDEX", filepath.Join(dir, "index.sqlite"))
	release, err := pushlock.TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	code, _, stderr := run("push")
	if code != 0 || !strings.Contains(stderr, "already running") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestPushRejectsUnknownFlag(t *testing.T) {
	if code, _, _ := run("push", "--nope"); code != 2 {
		t.Fatalf("code=%d", code)
	}
}
