//go:build unix

package cli

import (
	"os"
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

// A trigger within pushlock.TriggerInterval of the last push starts nothing and
// leaves its request for the next timer run.
func TestTriggerSoonAfterAPushIsDebounced(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SESSIONS_INDEX", filepath.Join(dir, "index.sqlite"))
	if err := pushlock.MarkRun(dir, time.Now().Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr := run("push", "--trigger")
	if code != 0 || stdout != "" || stderr != "" {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout, stderr)
	}
	if !pushlock.Pending(dir) {
		t.Fatal("debounced request not recorded")
	}
	if _, err := os.Stat(filepath.Join(dir, "push.log")); !os.IsNotExist(err) {
		t.Fatalf("a debounced trigger started a push (push.log: %v)", err)
	}
}

func TestFailureHeartbeatAtMostHourly(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if !failureHeartbeatDue(dir, now) {
		t.Fatal("first failure must be reported")
	}
	if err := writeStamp(filepath.Join(dir, failureHeartbeatName), now); err != nil {
		t.Fatal(err)
	}
	if failureHeartbeatDue(dir, now.Add(15*time.Minute)) {
		t.Fatal("failure reported again after 15 minutes")
	}
	if !failureHeartbeatDue(dir, now.Add(time.Hour)) {
		t.Fatal("failure not reported after an hour")
	}
}
