//go:build unix

package pushlock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestTriggerAllowedOncePerInterval(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if !TriggerAllowed(dir, now) {
		t.Fatal("no push ever ran: a trigger must be allowed")
	}
	if err := MarkRun(dir, now); err != nil {
		t.Fatal(err)
	}
	for _, d := range []time.Duration{0, time.Minute, TriggerInterval - time.Second} {
		if TriggerAllowed(dir, now.Add(d)) {
			t.Fatalf("trigger allowed %s after a push", d)
		}
	}
	if !TriggerAllowed(dir, now.Add(TriggerInterval)) {
		t.Fatal("trigger refused after the interval")
	}
}

// A broken stamp, or one from the future, must never stop pushes for good.
func TestTriggerAllowedFailsOpen(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if err := os.WriteFile(filepath.Join(dir, lastRunName), []byte("garbage"), 0o600); err != nil {
		t.Fatal(err)
	}
	if !TriggerAllowed(dir, now) {
		t.Fatal("unreadable stamp blocked the trigger")
	}
	if err := MarkRun(dir, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	if !TriggerAllowed(dir, now) {
		t.Fatal("stamp from the future blocked the trigger")
	}
}

// A debounced trigger leaves its marker, and the next allowed run serves it.
func TestDebouncedRequestIsServedByNextRun(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	if err := MarkRun(dir, now); err != nil {
		t.Fatal(err)
	}
	// A hook ends a turn two minutes later: too soon to push.
	if err := MarkPending(dir); err != nil {
		t.Fatal(err)
	}
	if TriggerAllowed(dir, now.Add(2*time.Minute)) {
		t.Fatal("trigger should be debounced")
	}
	if !Pending(dir) {
		t.Fatal("debounced request lost its marker")
	}
	// The timer's next run takes the request.
	calls := 0
	if _, ran, err := Run(dir, func() int { calls++; return 0 }); err != nil || !ran || calls != 1 {
		t.Fatalf("ran=%v err=%v calls=%d", ran, err, calls)
	}
	if Pending(dir) {
		t.Fatal("marker left after the run that served it")
	}
}

// One push per process: a request made during it waits on disk for the next run
// instead of starting a second push right away.
func TestRunAtMostOnceLeavesLateRequestsForTheNextRun(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	_, ran, err := RunAtMost(dir, 1, func() int {
		calls++
		_ = MarkPending(dir) // a turn ends during the push
		return 0
	})
	if err != nil || !ran || calls != 1 {
		t.Fatalf("ran=%v err=%v calls=%d", ran, err, calls)
	}
	if !Pending(dir) {
		t.Fatal("the late request was dropped")
	}
	if _, ran, _ := RunAtMost(dir, 1, func() int { calls++; return 0 }); !ran || calls != 2 || Pending(dir) {
		t.Fatalf("next run did not serve the request: ran=%v calls=%d pending=%v", ran, calls, Pending(dir))
	}
}
