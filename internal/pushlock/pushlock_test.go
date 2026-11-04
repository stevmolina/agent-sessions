//go:build unix

package pushlock

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunPushesOnceWhenIdle(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	code, ran, err := Run(dir, func() int { calls++; return 3 })
	if err != nil || !ran || code != 3 || calls != 1 {
		t.Fatalf("code=%d ran=%v err=%v calls=%d", code, ran, err, calls)
	}
	if Pending(dir) || Held(dir) {
		t.Fatal("left a marker or a lock behind")
	}
}

func TestBusyRunQueuesInsteadOfPushing(t *testing.T) {
	dir := t.TempDir()
	release, err := TryLock(dir)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	calls := 0
	_, ran, err := Run(dir, func() int { calls++; return 0 })
	if err != nil || ran || calls != 0 {
		t.Fatalf("ran=%v err=%v calls=%d", ran, err, calls)
	}
	if !Pending(dir) {
		t.Fatal("busy request was not recorded")
	}
	if !Held(dir) {
		t.Fatal("Held should see the lock")
	}
}

// A request that lands during a push makes the running push go once more.
func TestRequestDuringPushCoalescesIntoOneMoreRun(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	_, ran, err := Run(dir, func() int {
		calls++
		if calls == 1 {
			// Three turns end while the first push runs.
			for i := 0; i < 3; i++ {
				if _, ran, err := Run(dir, func() int { t.Fatal("second push ran concurrently"); return 0 }); ran || err != nil {
					t.Fatalf("nested run: ran=%v err=%v", ran, err)
				}
			}
		}
		return 0
	})
	if err != nil || !ran {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("want 2 pushes (first plus one coalesced), got %d", calls)
	}
	if Pending(dir) {
		t.Fatal("marker left behind")
	}
}

func TestRunStopsAfterMaxRuns(t *testing.T) {
	dir := t.TempDir()
	calls := 0
	_, _, err := Run(dir, func() int {
		calls++
		_ = MarkPending(dir) // a request arrives during every push
		return 0
	})
	if err != nil {
		t.Fatal(err)
	}
	if calls != MaxRuns {
		t.Fatalf("want %d runs, got %d", MaxRuns, calls)
	}
	if !Pending(dir) {
		t.Fatal("the request left over after the cap must stay for the next trigger")
	}
}

// Many triggers at once: never two pushes at the same time, and the last
// request is always served.
func TestConcurrentTriggersNeverOverlapAndNoneIsLost(t *testing.T) {
	dir := t.TempDir()
	var running, maxRunning, calls int32
	push := func() int {
		n := atomic.AddInt32(&running, 1)
		for {
			m := atomic.LoadInt32(&maxRunning)
			if n <= m || atomic.CompareAndSwapInt32(&maxRunning, m, n) {
				break
			}
		}
		atomic.AddInt32(&calls, 1)
		time.Sleep(5 * time.Millisecond)
		atomic.AddInt32(&running, -1)
		return 0
	}
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, _, err := Run(dir, push); err != nil {
				t.Error(err)
			}
		}()
		time.Sleep(time.Millisecond)
	}
	wg.Wait()
	if maxRunning != 1 {
		t.Fatalf("%d pushes overlapped", maxRunning)
	}
	if calls == 0 || calls >= 40 {
		t.Fatalf("expected coalescing, got %d pushes for 40 requests", calls)
	}
	if Pending(dir) {
		// Only possible if a single process hit MaxRuns; drain to prove it is served.
		if _, ran, _ := Run(dir, push); !ran {
			t.Fatal("leftover request could not be served")
		}
	}
}
